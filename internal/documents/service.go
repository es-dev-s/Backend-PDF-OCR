package documents

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"mime/multipart"
	"net/http"
	"os"
	"path/filepath"
	"runtime/debug"
	"strings"
	"sync"
	"time"
	"unicode"

	"github.com/google/uuid"
	"golang.org/x/sync/errgroup"

	"ocr-backend/internal/auth"
	"ocr-backend/internal/blob"
	"ocr-backend/internal/engine"
	"ocr-backend/internal/fingerprint"
	"ocr-backend/internal/realtime"
	"ocr-backend/internal/retry"
	"ocr-backend/internal/worker"
)

type Notifier interface {
	Create(ctx context.Context, title, detail string) error
	NotifyUser(ctx context.Context, userID uuid.UUID, title, detail, kind string, docID *uuid.UUID) error
	NotifyAdmins(ctx context.Context, title, detail, kind string, docID *uuid.UUID) error
	HasUnreadKind(ctx context.Context, docID uuid.UUID, kind string) bool
}

type Service struct {
	repo    *Repo
	blob    blob.Store
	hub     *realtime.Hub
	pool    *worker.Pool
	notes   Notifier
	engine  *engine.Client
	log     *slog.Logger
	maxN    int
	maxB    int64
	hashing sync.Map
	heavy   chan struct{}
	put     chan struct{}
}

func NewService(repo *Repo, store blob.Store, hub *realtime.Hub, pool *worker.Pool, notes Notifier, eng *engine.Client, log *slog.Logger, maxSources int, maxBytes int64) *Service {
	if maxSources < 1 {
		maxSources = 4
	}
	return &Service{
		repo:   repo,
		blob:   store,
		hub:    hub,
		pool:   pool,
		notes:  notes,
		engine: eng,
		log:    log,
		maxN:   maxSources,
		maxB:   maxBytes,
		heavy:  make(chan struct{}, 1),
		put:    make(chan struct{}, 2),
	}
}

func (s *Service) List(ctx context.Context, pending bool) ([]Document, error) {
	user, err := auth.Must(ctx)
	if err != nil {
		return nil, err
	}
	filter := ListFilter{OwnerID: user.ID, Admin: user.Admin(), Pending: pending}
	if pending && !user.Admin() {
		return nil, ErrForbidden
	}
	return s.repo.List(ctx, filter)
}

func (s *Service) Get(ctx context.Context, id uuid.UUID) (Document, error) {
	doc, err := s.repo.Get(ctx, id)
	if err != nil {
		return Document{}, err
	}
	if err := s.canRead(ctx, doc); err != nil {
		return Document{}, err
	}
	return doc, nil
}

func (s *Service) NextERP(ctx context.Context) (string, error) {
	used, err := s.repo.ListERPs(ctx)
	if err != nil {
		return "", err
	}
	set := make(map[string]struct{}, len(used))
	for _, erp := range used {
		set[strings.ToLower(erp)] = struct{}{}
	}
	n := 10001
	for {
		code := fmt.Sprintf("ERP-%d", n)
		if _, ok := set[strings.ToLower(code)]; !ok {
			return code, nil
		}
		n++
	}
}

func (s *Service) Create(ctx context.Context, in CreateInput, files []*multipart.FileHeader) (Document, error) {
	user, err := auth.Must(ctx)
	if err != nil {
		return Document{}, err
	}
	in.Client = strings.TrimSpace(in.Client)
	in.ERP = strings.TrimSpace(in.ERP)
	in.ANZSCO = strings.TrimSpace(in.ANZSCO)
	in.Team = strings.TrimSpace(in.Team)
	in.Member = strings.TrimSpace(in.Member)
	if !user.Admin() || in.Member == "" {
		in.Member = strings.TrimSpace(user.Name)
	}
	if in.ERP == "" || in.Client == "" || in.Member == "" {
		return Document{}, ErrInvalid
	}
	if len(files) == 0 {
		return Document{}, ErrNoFiles
	}
	if len(files) > s.maxN {
		files = files[:s.maxN]
	}

	now := time.Now().UTC()
	owner := user.ID
	doc := Document{
		ID:       uuid.New(),
		Client:   in.Client,
		ERP:      in.ERP,
		ANZSCO:   in.ANZSCO,
		Team:     in.Team,
		Member:   in.Member,
		Uploader: in.Member,
		Status:   StatusProcessing,
		Uploaded: now,
		Sources:  make([]Source, 0, len(files)),
		OwnerID:  &owner,
	}

	written, err := s.storeFiles(ctx, doc.ID, files, in.Titles, now)
	if err != nil {
		s.cleanup(written)
		return Document{}, err
	}
	doc.Sources = written
	if err := s.repo.InsertDocument(ctx, doc); err != nil {
		s.cleanup(written)
		return Document{}, err
	}
	bg := s.jobContext()
	s.enqueueHash(bg, written)
	s.enqueueTitle(bg, written)
	out, err := s.repo.Get(ctx, doc.ID)
	if err != nil {
		return Document{}, err
	}
	s.hub.Publish(ctx, "document.created", out)
	return out, nil
}

func (s *Service) AddSources(ctx context.Context, id uuid.UUID, files []*multipart.FileHeader, titles []string) (Document, error) {
	doc, err := s.repo.Get(ctx, id)
	if err != nil {
		return Document{}, err
	}
	if err := s.canWrite(ctx, doc); err != nil {
		return Document{}, err
	}
	if len(files) == 0 {
		return Document{}, ErrNoFiles
	}
	if len(files) > s.maxN {
		files = files[:s.maxN]
	}
	now := time.Now().UTC()
	written, err := s.storeFiles(ctx, id, files, titles, now)
	if err != nil {
		s.cleanup(written)
		return Document{}, err
	}
	if err := s.repo.InsertSources(ctx, id, written, s.maxN); err != nil {
		s.cleanup(written)
		return Document{}, err
	}
	bg := s.jobContext()
	s.enqueueHash(bg, written)
	s.enqueueTitle(bg, written)
	out, err := s.repo.Get(ctx, id)
	if err != nil {
		return Document{}, err
	}
	s.hub.Publish(ctx, "document.updated", out)
	return out, nil
}

func (s *Service) Delete(ctx context.Context, id uuid.UUID) error {
	doc, err := s.repo.Get(ctx, id)
	if err != nil {
		return err
	}
	if err := s.canWrite(ctx, doc); err != nil {
		return err
	}
	keys, err := s.repo.Delete(ctx, id)
	if err != nil {
		return err
	}
	for _, key := range keys {
		_ = s.blob.Delete(ctx, key)
	}
	s.hub.Publish(ctx, "document.deleted", map[string]any{
		"id":       id.String(),
		"owner_id": doc.OwnerID,
	})
	return nil
}

func (s *Service) Approve(ctx context.Context, id uuid.UUID) (Document, error) {
	if err := s.requireAdmin(ctx); err != nil {
		return Document{}, err
	}
	if err := s.repo.Approve(ctx, id); err != nil {
		return Document{}, err
	}
	out, err := s.repo.Get(ctx, id)
	if err != nil {
		return Document{}, err
	}
	s.hub.Publish(ctx, "document.updated", out)
	if s.notes != nil && out.OwnerID != nil {
		_ = s.notes.NotifyUser(ctx, *out.OwnerID, "Duplicate approved", fmt.Sprintf("%s is now in your documents.", label(out)), "approved", &out.ID)
	}
	return out, nil
}

func (s *Service) Reject(ctx context.Context, id uuid.UUID) error {
	if err := s.requireAdmin(ctx); err != nil {
		return err
	}
	doc, err := s.repo.Get(ctx, id)
	if err != nil {
		return err
	}
	keys, deleted, err := s.repo.RejectPending(ctx, id)
	if err != nil {
		return err
	}
	for _, key := range keys {
		_ = s.blob.Delete(ctx, key)
	}
	if deleted {
		s.hub.Publish(ctx, "document.deleted", map[string]any{
			"id":       id.String(),
			"owner_id": doc.OwnerID,
		})
	} else {
		out, getErr := s.repo.Get(ctx, id)
		if getErr == nil {
			s.hub.Publish(ctx, "document.updated", out)
		}
	}
	if s.notes != nil && doc.OwnerID != nil {
		_ = s.notes.NotifyUser(ctx, *doc.OwnerID, "Duplicate declined", fmt.Sprintf("%s was not approved and the pending files were removed.", label(doc)), "rejected", &doc.ID)
	}
	return nil
}

func (s *Service) requireAdmin(ctx context.Context) error {
	user, err := auth.Must(ctx)
	if err != nil {
		return err
	}
	if !user.Admin() {
		return ErrForbidden
	}
	return nil
}

func (s *Service) canRead(ctx context.Context, doc Document) error {
	user, err := auth.Must(ctx)
	if err != nil {
		return err
	}
	if user.Admin() || Owns(doc.OwnerID, user.ID) {
		return nil
	}
	return ErrForbidden
}

func (s *Service) canWrite(ctx context.Context, doc Document) error {
	return s.canRead(ctx, doc)
}

func (s *Service) notifyReview(ctx context.Context, doc Document) {
	if s.notes.HasUnreadKind(ctx, doc.ID, "review") {
		return
	}
	name := label(doc)
	who := strings.TrimSpace(doc.Member)
	if who == "" {
		who = "A member"
	}
	_ = s.notes.NotifyAdmins(ctx, "Duplicate needs review", fmt.Sprintf("%s · %s submitted a duplicate. Open Review to approve or decline.", name, who), "review", &doc.ID)
	if doc.OwnerID != nil {
		_ = s.notes.NotifyUser(ctx, *doc.OwnerID, "Waiting for review", fmt.Sprintf("%s is on hold until an admin reviews this duplicate.", name), "review_pending", &doc.ID)
	}
}

func label(doc Document) string {
	title := strings.TrimSpace(doc.Title)
	if title != "" {
		return title
	}
	erp := strings.TrimSpace(doc.ERP)
	if erp != "" {
		return erp
	}
	return "Document"
}

func (s *Service) OpenFile(ctx context.Context, docID, sourceID uuid.UUID) (*os.File, int64, string, string, time.Time, error) {
	doc, err := s.repo.Get(ctx, docID)
	if err != nil {
		return nil, 0, "", "", time.Time{}, err
	}
	if err := s.canRead(ctx, doc); err != nil {
		return nil, 0, "", "", time.Time{}, err
	}
	src, err := s.repo.SourceMeta(ctx, docID, sourceID)
	if err != nil {
		return nil, 0, "", "", time.Time{}, err
	}
	path, err := s.blob.LocalPath(ctx, src.StorageKey)
	if err != nil {
		if errors.Is(err, blob.ErrNotFound) {
			return nil, 0, "", "", time.Time{}, ErrNotFound
		}
		return nil, 0, "", "", time.Time{}, err
	}
	f, err := os.Open(path)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return nil, 0, "", "", time.Time{}, ErrNotFound
		}
		return nil, 0, "", "", time.Time{}, err
	}
	info, err := f.Stat()
	if err != nil {
		_ = f.Close()
		return nil, 0, "", "", time.Time{}, err
	}
	if info.Size() <= 0 {
		_ = f.Close()
		return nil, 0, "", "", time.Time{}, ErrNotFound
	}
	if src.SizeBytes > 0 && info.Size() != src.SizeBytes && s.blob.Driver() == "r2" {
		_ = f.Close()
		_ = os.Remove(path)
		path, err = s.blob.LocalPath(ctx, src.StorageKey)
		if err != nil {
			if errors.Is(err, blob.ErrNotFound) {
				return nil, 0, "", "", time.Time{}, ErrNotFound
			}
			return nil, 0, "", "", time.Time{}, err
		}
		f, err = os.Open(path)
		if err != nil {
			return nil, 0, "", "", time.Time{}, err
		}
		info, err = f.Stat()
		if err != nil {
			_ = f.Close()
			return nil, 0, "", "", time.Time{}, err
		}
	}
	name := strings.TrimSpace(src.Title)
	if name == "" {
		name = filepath.Base(src.StorageKey)
	}
	ctype := src.ContentType
	if ctype == "" || ctype == "application/octet-stream" {
		if strings.EqualFold(filepath.Ext(src.StorageKey), ".pdf") || strings.EqualFold(filepath.Ext(name), ".pdf") {
			ctype = "application/pdf"
		}
	}
	return f, info.Size(), ctype, name, info.ModTime(), nil
}

func (s *Service) RecoverPending(ctx context.Context) {
	sources, err := s.repo.ListUnhashed(ctx, 1)
	if err != nil {
		if !errors.Is(err, ErrUnavailable) {
			s.log.Warn("recover pending failed", "err", err)
		}
		return
	}
	if len(sources) == 0 {
		return
	}
	s.log.Info("requeue unhashed sources", "count", len(sources))
	s.enqueueHash(ctx, sources)
}

func (s *Service) RunRecovery(ctx context.Context) {
	wait := time.NewTicker(400 * time.Millisecond)
	defer wait.Stop()
	for {
		if _, err := s.repo.ListUnhashed(ctx, 1); err == nil {
			break
		}
		select {
		case <-ctx.Done():
			return
		case <-wait.C:
		}
	}
	select {
	case <-ctx.Done():
		return
	case <-time.After(20 * time.Second):
	}
	s.RecoverPending(ctx)
	t := time.NewTicker(60 * time.Second)
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-t.C:
			s.RecoverPending(ctx)
		}
	}
}

func (s *Service) storeFiles(ctx context.Context, docID uuid.UUID, files []*multipart.FileHeader, titles []string, now time.Time) ([]Source, error) {
	out := make([]Source, len(files))
	g, ctx := errgroup.WithContext(ctx)
	g.SetLimit(2)
	var mu sync.Mutex
	for i, fh := range files {
		i, fh := i, fh
		g.Go(func() error {
			if fh.Size > s.maxB && s.maxB > 0 {
				return fmt.Errorf("file too large")
			}
			srcID := uuid.New()
			name := sanitizeName(fh.Filename)
			key := fmt.Sprintf("%s/%s/%s", docID.String(), srcID.String(), name)
			f, err := fh.Open()
			if err != nil {
				return err
			}
			defer f.Close()
			ctype := fh.Header.Get("Content-Type")
			if ctype == "" {
				ctype = "application/octet-stream"
			}
			head := make([]byte, 16)
			n, _ := io.ReadFull(f, head)
			if n > 0 {
				head = head[:n]
			} else {
				head = nil
			}
			sniffed := fingerprint.Sniff(head, name)
			if sniffed == "" && !strings.Contains(strings.ToLower(ctype), "pdf") && !strings.HasPrefix(strings.ToLower(ctype), "image/") {
				return ErrInvalid
			}
			if n > 0 {
				if detected := http.DetectContentType(head); detected != "application/octet-stream" {
					ctype = detected
				} else if sniffed == "pdf" {
					ctype = "application/pdf"
				}
			}
			var reader io.Reader = f
			if n > 0 {
				reader = io.MultiReader(bytes.NewReader(head), f)
			}
			limited := &limitErrReader{r: reader, max: s.maxB}
			if err := s.acquirePut(ctx); err != nil {
				return err
			}
			err = s.blob.Put(ctx, key, limited, fh.Size, ctype)
			s.releasePut()
			if err != nil {
				return err
			}
			provided := i < len(titles) && strings.TrimSpace(titles[i]) != ""
			title := name
			if provided {
				if cleaned := engine.SanitizeTitle(titles[i]); cleaned != "" {
					title = cleaned
				}
			}
			mu.Lock()
			out[i] = Source{
				ID:          srcID,
				DocumentID:  docID,
				Title:       title,
				StorageKey:  key,
				ContentType: ctype,
				SizeBytes:   fh.Size,
				Uniqueness:  Unique,
				Uploaded:    now.Add(time.Duration(i) * time.Millisecond),
				NeedsTitle:  sniffed == "pdf" && !provided,
			}
			mu.Unlock()
			return nil
		})
	}
	if err := g.Wait(); err != nil {
		return nonEmpty(out), err
	}
	return out, nil
}

func (s *Service) hashSources(ctx context.Context, sources []Source) {
	for _, src := range sources {
		if ctx.Err() != nil {
			return
		}
		s.hashSource(ctx, src)
	}
}

func (s *Service) enqueueHash(ctx context.Context, sources []Source) {
	if s.pool == nil {
		s.hashSources(ctx, sources)
		return
	}
	for _, src := range sources {
		src := src
		if _, loaded := s.hashing.Load(src.ID.String()); loaded {
			continue
		}
		if err := s.pool.Submit(ctx, func(jobCtx context.Context) {
			s.hashSource(jobCtx, src)
		}); err != nil {
			s.log.Warn("hash enqueue failed", "err", err, "source", src.ID)
		}
	}
}

func (s *Service) enqueueTitle(ctx context.Context, sources []Source) {
	if s.engine == nil || !s.engine.Configured() {
		return
	}
	if s.pool == nil {
		for _, src := range sources {
			if src.NeedsTitle {
				s.titleSource(ctx, src)
			}
		}
		return
	}
	for _, src := range sources {
		if !src.NeedsTitle {
			continue
		}
		src := src
		if err := s.pool.Submit(ctx, func(jobCtx context.Context) {
			s.titleSource(jobCtx, src)
		}); err != nil {
			s.log.Warn("title enqueue failed", "err", err, "source", src.ID)
		}
	}
}

func (s *Service) InspectFile(ctx context.Context, filename string, r io.Reader) PreviewResult {
	out := PreviewResult{
		OK:         true,
		Uniqueness: Unique,
		Filename:   sanitizeName(filename),
		Matches:    []PreviewMatch{},
	}
	select {
	case <-ctx.Done():
		out.OK = false
		return out
	default:
	}
	if !s.acquireHeavy(ctx) {
		out.OK = false
		return out
	}
	defer s.releaseHeavy()
	path, err := writeInspectTemp(r, filename, s.maxB)
	if err != nil {
		s.log.Warn("inspect temp failed", "err", err, "file", filename)
		out.OK = false
		return out
	}
	defer os.Remove(path)

	fp, err := fingerprint.Digest(path)
	if err != nil {
		s.log.Warn("inspect digest failed", "err", err, "file", filename)
		out.OK = false
		return out
	}
	out.Digest = fp.SHA256
	matches, err := s.repo.PreviewFingerprint(ctx, fp)
	if err != nil {
		s.log.Warn("inspect preview failed", "err", err, "file", filename)
		out.OK = false
		return out
	}
	if len(matches) > 0 {
		out.Uniqueness = Duplicate
		out.Matches = make([]PreviewMatch, 0, len(matches))
		for _, m := range matches {
			out.Matches = append(out.Matches, PreviewMatch{
				ID:         m.SourceID,
				Title:      m.Title,
				ERP:        m.ERP,
				Client:     m.Client,
				Member:     m.Member,
				Score:      m.Score,
				Uploaded:   m.Uploaded,
				Uniqueness: m.Uniqueness,
				Kind:       m.Kind,
			})
		}
	}
	return out
}

func writeInspectTemp(r io.Reader, filename string, max int64) (string, error) {
	ext := filepath.Ext(sanitizeName(filename))
	f, err := os.CreateTemp("", "ocr-inspect-*"+ext)
	if err != nil {
		return "", err
	}
	name := f.Name()
	limit := max
	if limit <= 0 {
		limit = 50 << 20
	}
	_, err = io.Copy(f, io.LimitReader(r, limit))
	closeErr := f.Close()
	if err != nil {
		_ = os.Remove(name)
		return "", err
	}
	if closeErr != nil {
		_ = os.Remove(name)
		return "", closeErr
	}
	return name, nil
}

func (s *Service) SuggestTitle(ctx context.Context, filename string, r io.Reader) engine.Result {
	var out engine.Result
	if s.engine == nil || !s.engine.Configured() {
		return out
	}
	if !s.acquireHeavy(ctx) {
		return out
	}
	defer s.releaseHeavy()
	head := make([]byte, 16)
	n, _ := io.ReadFull(r, head)
	if n > 0 {
		head = head[:n]
	}
	if fingerprint.Sniff(head, filename) != "pdf" {
		return out
	}
	var reader io.Reader = r
	if n > 0 {
		reader = io.MultiReader(bytes.NewReader(head), r)
	}
	res, err := s.engine.Title(ctx, filename, reader)
	if err != nil {
		s.log.Warn("engine title failed", "err", err, "file", filename)
		return out
	}
	return res
}

func (s *Service) titleSource(ctx context.Context, src Source) {
	if s.engine == nil || !s.engine.Configured() {
		return
	}
	if !s.acquireHeavy(ctx) {
		return
	}
	defer s.releaseHeavy()
	err := retry.Do(ctx, 4, func(ctx context.Context) error {
		path, err := s.blob.LocalPath(ctx, src.StorageKey)
		if err != nil {
			return err
		}
		f, err := os.Open(path)
		if err != nil {
			return err
		}
		defer f.Close()
		res, err := s.engine.Title(ctx, src.Title, f)
		if err != nil {
			return err
		}
		if !res.OK || res.Title == "" {
			return nil
		}
		if err := s.repo.SetSourceTitle(ctx, src.ID, res.Title); err != nil {
			return err
		}
		doc, err := s.repo.Get(ctx, src.DocumentID)
		if err != nil {
			if errors.Is(err, ErrNotFound) {
				return nil
			}
			return err
		}
		s.hub.Publish(ctx, "document.updated", doc)
		return nil
	})
	if err != nil && !errors.Is(err, ErrNotFound) && !errors.Is(err, context.Canceled) {
		s.log.Warn("title source failed", "err", err, "source", src.ID)
	}
}

func (s *Service) hashSource(ctx context.Context, src Source) {
	key := src.ID.String()
	if _, loaded := s.hashing.LoadOrStore(key, struct{}{}); loaded {
		return
	}
	defer s.hashing.Delete(key)

	if !s.acquireHeavy(ctx) {
		return
	}
	defer s.releaseHeavy()

	err := retry.Do(ctx, 8, func(ctx context.Context) error {
		meta, err := s.repo.SourceMeta(ctx, src.DocumentID, src.ID)
		if err != nil {
			return err
		}
		if meta.SHA256 != nil && *meta.SHA256 != "" {
			return nil
		}
		path, err := s.blob.LocalPath(ctx, src.StorageKey)
		if errors.Is(err, blob.ErrNotFound) {
			s.log.Warn("source blob missing; finishing uniqueness without file", "source", src.ID, "key", src.StorageKey)
			return s.publishFingerprint(ctx, src, fingerprint.Missing(src.ID.String()))
		}
		if err != nil {
			return err
		}
		fp, err := fingerprint.Analyze(path, filepath.Base(src.StorageKey), src.ContentType)
		if err != nil {
			if errors.Is(err, os.ErrNotExist) {
				s.log.Warn("source file gone; finishing uniqueness without file", "source", src.ID, "key", src.StorageKey)
				return s.publishFingerprint(ctx, src, fingerprint.Missing(src.ID.String()))
			}
			return err
		}
		return s.publishFingerprint(ctx, src, fp)
	})
	if err != nil && !errors.Is(err, ErrNotFound) && !errors.Is(err, blob.ErrNotFound) && !errors.Is(err, context.Canceled) {
		s.log.Error("hash source failed", "err", err, "source", src.ID)
	}
}

func (s *Service) publishFingerprint(ctx context.Context, src Source, fp fingerprint.Result) error {
	result, err := s.repo.FinalizeFingerprint(ctx, src, fp)
	if err != nil {
		return err
	}
	for _, id := range result.Touched {
		doc, err := s.repo.Get(ctx, id)
		if err != nil {
			if errors.Is(err, ErrNotFound) {
				continue
			}
			return err
		}
		s.hub.Publish(ctx, "document.updated", doc)
		if s.notes == nil {
			continue
		}
		if doc.Status == StatusPendingReview && doc.ID == src.DocumentID {
			s.notifyReview(ctx, doc)
			continue
		}
		notified := false
		for _, nid := range result.Notified {
			if nid == id {
				notified = true
				break
			}
		}
		if !notified {
			continue
		}
		switch doc.Status {
		case StatusDuplicate:
			if doc.OwnerID != nil {
				_ = s.notes.NotifyUser(ctx, *doc.OwnerID, "Duplicate saved", fmt.Sprintf("%s was saved as a duplicate.", label(doc)), "duplicate", &doc.ID)
			} else {
				_ = s.notes.NotifyAdmins(ctx, "Duplicate saved", fmt.Sprintf("%s has duplicate sources.", label(doc)), "duplicate", &doc.ID)
			}
		default:
			if doc.OwnerID != nil {
				_ = s.notes.NotifyUser(ctx, *doc.OwnerID, "Document processed", fmt.Sprintf("%s finished processing.", label(doc)), "processed", &doc.ID)
			} else {
				_ = s.notes.NotifyAdmins(ctx, "Document processed", fmt.Sprintf("%s finished processing.", label(doc)), "processed", &doc.ID)
			}
		}
	}
	return nil
}

func (s *Service) cleanup(sources []Source) {
	for _, src := range sources {
		if src.StorageKey == "" {
			continue
		}
		_ = s.blob.Delete(context.Background(), src.StorageKey)
	}
}

func nonEmpty(in []Source) []Source {
	out := make([]Source, 0, len(in))
	for _, s := range in {
		if s.StorageKey != "" {
			out = append(out, s)
		}
	}
	return out
}

type limitErrReader struct {
	r   io.Reader
	n   int64
	max int64
}

func (l *limitErrReader) Read(p []byte) (int, error) {
	if l.max > 0 && l.n >= l.max {
		return 0, fmt.Errorf("file too large")
	}
	n, err := l.r.Read(p)
	l.n += int64(n)
	if l.max > 0 && l.n > l.max {
		return n, fmt.Errorf("file too large")
	}
	return n, err
}

func (s *Service) acquireHeavy(ctx context.Context) bool {
	if s.heavy == nil {
		return true
	}
	select {
	case s.heavy <- struct{}{}:
		return true
	case <-ctx.Done():
		return false
	}
}

func (s *Service) releaseHeavy() {
	if s.heavy != nil {
		<-s.heavy
	}
	debug.FreeOSMemory()
}

func (s *Service) acquirePut(ctx context.Context) error {
	if s.put == nil {
		return nil
	}
	select {
	case s.put <- struct{}{}:
		return nil
	case <-ctx.Done():
		return ctx.Err()
	}
}

func (s *Service) releasePut() {
	if s.put == nil {
		return
	}
	select {
	case <-s.put:
	default:
	}
}

func (s *Service) jobContext() context.Context {
	if s.pool != nil {
		return s.pool.Context()
	}
	return context.Background()
}

func sanitizeName(name string) string {
	name = filepath.Base(strings.ReplaceAll(name, "\\", "/"))
	name = strings.TrimSpace(name)
	if name == "" || name == "." || name == ".." {
		return "upload.bin"
	}
	var b strings.Builder
	for _, r := range name {
		if r == 0 || r == '/' {
			continue
		}
		if unicode.IsControl(r) {
			continue
		}
		b.WriteRune(r)
	}
	out := strings.TrimSpace(b.String())
	if out == "" {
		return "upload.bin"
	}
	if len(out) > 180 {
		ext := filepath.Ext(out)
		out = out[:180-len(ext)] + ext
	}
	return out
}
