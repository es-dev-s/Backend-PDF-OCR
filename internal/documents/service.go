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
	"sort"
	"strings"
	"sync"
	"sync/atomic"
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
	"ocr-backend/internal/titlesim"
	"ocr-backend/internal/worker"
)

type Notifier interface {
	Create(ctx context.Context, title, detail string) error
	NotifyUser(ctx context.Context, userID uuid.UUID, title, detail, kind string, docID *uuid.UUID) error
	NotifyAdmins(ctx context.Context, title, detail, kind string, docID *uuid.UUID) error
	NotifyAdminsOnce(ctx context.Context, title, detail, kind string, docID uuid.UUID) error
	HasUnreadKind(ctx context.Context, docID uuid.UUID, kind string) bool
}

type Service struct {
	repo       *Repo
	blob       blob.Store
	hub        *realtime.Hub
	pool       *worker.Pool
	notes      Notifier
	engine     *engine.Client
	log        *slog.Logger
	maxN       int
	maxB       int64
	hashing    sync.Map
	titling    sync.Map
	similaring sync.Map
	heavy      chan struct{}
	titles     chan struct{}
	put        chan struct{}

	trimmedAt atomic.Int64
}

// Limits bounds the two kinds of background work. Fingerprinting is bounded by
// memory; engine calls are bounded by the engine itself. They get separate
// budgets so a slow engine can never stall duplicate detection.
type Limits struct {
	Heavy int
	Title int
}

func NewService(repo *Repo, store blob.Store, hub *realtime.Hub, pool *worker.Pool, notes Notifier, eng *engine.Client, log *slog.Logger, maxSources int, maxBytes int64, limits Limits) *Service {
	if maxSources < 1 {
		maxSources = 4
	}
	if limits.Heavy < 1 {
		limits.Heavy = 1
	}
	if limits.Title < 1 {
		limits.Title = 1
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
		heavy:  make(chan struct{}, limits.Heavy),
		titles: make(chan struct{}, limits.Title),
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

func (s *Service) UploadStats(ctx context.Context, from, to time.Time) (UploadStats, error) {
	if err := s.requireAdmin(ctx); err != nil {
		return UploadStats{}, err
	}
	return s.repo.UploadStats(ctx, from, to)
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

func (s *Service) PublicGet(ctx context.Context, id uuid.UUID) (Document, error) {
	doc, err := s.repo.Get(ctx, id)
	if err != nil {
		return Document{}, err
	}
	if err := PublicView(&doc); err != nil {
		return Document{}, err
	}
	return doc, nil
}

const firstERP = 10001

func (s *Service) NextERP(ctx context.Context) (string, error) {
	n, err := s.repo.NextERPNumber(ctx, firstERP)
	if err != nil {
		return "", err
	}
	return fmt.Sprintf("ERP-%d", n), nil
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
		return Document{}, ErrTooMany
	}

	now := time.Now().UTC()
	owner := user.ID
	doc := Document{
		ID:         uuid.New(),
		Client:     in.Client,
		ERP:        in.ERP,
		ANZSCO:     in.ANZSCO,
		Team:       in.Team,
		Member:     in.Member,
		Uploader:   in.Member,
		Status:     StatusProcessing,
		Uploaded:   now,
		Sources:    make([]Source, 0, len(files)),
		OwnerID:    &owner,
		ReviewNote: ClampNote(in.Note),
	}

	written, err := s.storeFiles(ctx, doc.ID, files, in.Titles, in.Notes, in.Note, now)
	if err != nil {
		s.cleanup(written)
		return Document{}, err
	}
	doc.Sources = written
	if joined := joinSourceNotes(written); joined != "" {
		doc.ReviewNote = joined
	}
	if err := s.repo.InsertDocument(ctx, doc); err != nil {
		s.cleanup(written)
		return Document{}, err
	}
	bg := s.jobContext()
	s.enqueueHash(bg, written)
	s.enqueueTitle(bg, written, false)
	s.enqueueSimilar(bg, written, false)
	out, err := s.repo.Get(ctx, doc.ID)
	if err != nil {
		return Document{}, err
	}
	s.hub.Publish(ctx, "document.created", out)
	return out, nil
}

func (s *Service) AddSources(ctx context.Context, id uuid.UUID, files []*multipart.FileHeader, titles, notes []string, note string) (Document, error) {
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
	if len(files) > s.maxN || len(doc.Sources)+len(files) > s.maxN {
		return Document{}, ErrTooMany
	}
	now := time.Now().UTC()
	written, err := s.storeFiles(ctx, id, files, titles, notes, note, now)
	if err != nil {
		s.cleanup(written)
		return Document{}, err
	}
	if err := s.repo.InsertSources(ctx, id, written, s.maxN, joinSourceNotes(written)); err != nil {
		s.cleanup(written)
		return Document{}, err
	}
	bg := s.jobContext()
	s.enqueueHash(bg, written)
	s.enqueueTitle(bg, written, false)
	s.enqueueSimilar(bg, written, false)
	out, err := s.repo.Get(ctx, id)
	if err != nil {
		return Document{}, err
	}
	s.hub.Publish(ctx, "document.updated", out)
	return out, nil
}

func (s *Service) Delete(ctx context.Context, id uuid.UUID) error {
	if err := s.requireAdmin(ctx); err != nil {
		return err
	}
	doc, err := s.repo.Get(ctx, id)
	if err != nil {
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
	if s.notes == nil {
		return
	}
	name := label(doc)
	who := strings.TrimSpace(doc.Member)
	if who == "" {
		who = "A member"
	}
	_ = s.notes.NotifyAdminsOnce(ctx, "Duplicate needs review", fmt.Sprintf("%s · %s submitted a duplicate. Open Review to approve or decline.", name, who), "review", doc.ID)
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
	return s.openStoredFile(ctx, docID, sourceID)
}

func (s *Service) PublicOpenFile(ctx context.Context, docID, sourceID uuid.UUID) (*os.File, int64, string, string, time.Time, error) {
	doc, err := s.repo.Get(ctx, docID)
	if err != nil {
		return nil, 0, "", "", time.Time{}, err
	}
	if err := PublicView(&doc); err != nil {
		return nil, 0, "", "", time.Time{}, err
	}
	return s.openStoredFile(ctx, docID, sourceID)
}

func (s *Service) openStoredFile(ctx context.Context, docID, sourceID uuid.UUID) (*os.File, int64, string, string, time.Time, error) {
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
	name := engine.PublicTitle(src.Title)
	if !engine.TitleSettled(name) {
		name = "document"
	}
	ctype := src.ContentType
	if ctype == "" || ctype == "application/octet-stream" {
		if strings.EqualFold(filepath.Ext(src.StorageKey), ".pdf") || strings.EqualFold(filepath.Ext(name), ".pdf") {
			ctype = "application/pdf"
		}
	}
	return f, info.Size(), ctype, name, info.ModTime(), nil
}

// recoverBatch is how many stalled rows one sweep claims. It is deliberately
// smaller than the worker queue so a backlog of millions drains steadily
// without the sweeper ever blocking on a full pool.
const recoverBatch = 100

// RecoverPending requeues rows that never finished hashing, title
// extraction, or similar-title matching. It reports whether the batch was
// full, so the caller can sweep again immediately instead of trickling
// through a large backlog.
func (s *Service) RecoverPending(ctx context.Context) bool {
	more := false
	if healed, err := s.repo.HealSettledTitles(ctx); err != nil {
		if !errors.Is(err, ErrUnavailable) {
			s.log.Warn("heal settled titles failed", "err", err)
		}
	} else if healed > 0 {
		s.log.Info("kept printed titles", "count", healed)
	}
	sources, err := s.repo.ListUnhashed(ctx, recoverBatch)
	if err != nil {
		if !errors.Is(err, ErrUnavailable) {
			s.log.Warn("recover pending failed", "err", err)
		}
		return false
	}
	if len(sources) > 0 {
		s.log.Info("requeue unhashed sources", "count", len(sources))
		s.enqueueHash(ctx, sources)
		more = len(sources) == recoverBatch
	}
	pendingTitles, err := s.repo.ListNeedingTitle(ctx, recoverBatch)
	if err != nil {
		if !errors.Is(err, ErrUnavailable) {
			s.log.Warn("recover titles failed", "err", err)
		}
		return more
	}
	if len(pendingTitles) > 0 {
		s.log.Info("requeue title extraction", "count", len(pendingTitles))
		s.enqueueTitle(ctx, pendingTitles, true)
		more = more || len(pendingTitles) == recoverBatch
	}
	pendingSimilar, err := s.repo.ListNeedingSimilar(ctx, recoverBatch)
	if err != nil {
		if !errors.Is(err, ErrUnavailable) {
			s.log.Warn("recover similar titles failed", "err", err)
		}
		return more
	}
	if len(pendingSimilar) == 0 {
		return more
	}
	s.log.Info("requeue title similarity", "count", len(pendingSimilar))
	s.enqueueSimilar(ctx, pendingSimilar, true)
	return more || len(pendingSimilar) == recoverBatch
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
	more := s.RecoverPending(ctx)
	t := time.NewTimer(recoverDelay(more))
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-t.C:
			more = s.RecoverPending(ctx)
			t.Reset(recoverDelay(more))
		}
	}
}

// recoverDelay keeps the sweeper idle-cheap, but drains a backlog quickly once
// it finds a full batch.
func recoverDelay(more bool) time.Duration {
	if more {
		return 5 * time.Second
	}
	return 15 * time.Second
}

func (s *Service) storeFiles(ctx context.Context, docID uuid.UUID, files []*multipart.FileHeader, titles, notes []string, fallback string, now time.Time) ([]Source, error) {
	out := make([]Source, len(files))
	fallback = ClampNote(fallback)
	g, ctx := errgroup.WithContext(ctx)
	g.SetLimit(2)
	var mu sync.Mutex
	for i, fh := range files {
		i, fh := i, fh
		g.Go(func() error {
			if fh.Size > s.maxB && s.maxB > 0 {
				return ErrTooLarge
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
			defer s.releasePut()
			err = s.blob.Put(ctx, key, limited, fh.Size, ctype)
			if err != nil {
				if errors.Is(err, ErrTooLarge) {
					return ErrTooLarge
				}
				return err
			}
			rawTitle := ""
			if i < len(titles) {
				rawTitle = strings.TrimSpace(titles[i])
			}
			printed := engine.PrintedTitle(rawTitle)
			unreadable := strings.EqualFold(rawTitle, engine.UnreadableTitle)
			isPDF := sniffed == "pdf"
			title := printed
			if unreadable {
				title = engine.UnreadableTitle
			}
			if title == "" && isPDF {
				title = engine.UntitledDocument
			}
			if title == "" {
				title = name
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
				NeedsTitle:  isPDF && printed == "" && !unreadable,
				Note:        noteForFile(notes, i, fallback),
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
		// A saturated pool must never stall an upload: the row stays unhashed
		// and the recovery sweeper picks it up.
		if err := s.pool.TrySubmit(func(jobCtx context.Context) {
			s.hashSource(jobCtx, src)
		}); err != nil && !errors.Is(err, worker.ErrBusy) {
			s.log.Warn("hash enqueue failed", "err", err, "source", src.ID)
		}
	}
}

func (s *Service) enqueueTitle(ctx context.Context, sources []Source, wait bool) {
	if s.engine == nil || !s.engine.Configured() {
		return
	}
	if s.pool == nil {
		for _, src := range sources {
			if engine.TitleSettled(src.Title) {
				_ = s.repo.SetSourceTitle(ctx, src.ID, src.Title)
				continue
			}
			if src.NeedsTitle {
				s.titleSource(ctx, src)
			}
		}
		return
	}
	for _, src := range sources {
		if engine.TitleSettled(src.Title) {
			_ = s.repo.SetSourceTitle(ctx, src.ID, src.Title)
			continue
		}
		if !src.NeedsTitle {
			continue
		}
		src := src
		key := src.ID.String()
		if _, loaded := s.titling.LoadOrStore(key, struct{}{}); loaded {
			continue
		}
		job := func(jobCtx context.Context) {
			defer s.titling.Delete(key)
			s.runTitleSource(jobCtx, src)
		}
		var err error
		if wait {
			err = s.pool.Submit(ctx, job)
		} else {
			err = s.pool.TrySubmit(job)
		}
		if err != nil {
			s.titling.Delete(key)
			if wait || !errors.Is(err, worker.ErrBusy) {
				if !errors.Is(err, context.Canceled) && !errors.Is(err, worker.ErrBusy) {
					s.log.Warn("title enqueue failed", "err", err, "source", src.ID)
				}
			}
		}
	}
}

func (s *Service) enqueueSimilar(ctx context.Context, sources []Source, wait bool) {
	if s.pool == nil {
		for _, src := range sources {
			if !src.NeedsTitle {
				s.similarSource(ctx, src)
			}
		}
		return
	}
	for _, src := range sources {
		if src.NeedsTitle {
			continue
		}
		src := src
		key := src.ID.String()
		if _, loaded := s.similaring.LoadOrStore(key, struct{}{}); loaded {
			continue
		}
		job := func(jobCtx context.Context) {
			defer s.similaring.Delete(key)
			s.similarSource(jobCtx, src)
		}
		var err error
		if wait {
			err = s.pool.Submit(ctx, job)
		} else {
			err = s.pool.TrySubmit(job)
		}
		if err != nil {
			s.similaring.Delete(key)
			if wait || !errors.Is(err, worker.ErrBusy) {
				if !errors.Is(err, context.Canceled) && !errors.Is(err, worker.ErrBusy) {
					s.log.Warn("similar enqueue failed", "err", err, "source", src.ID)
				}
			}
		}
	}
}

func (s *Service) similarSource(ctx context.Context, src Source) {
	key := src.ID.String()
	if s.pool == nil {
		if _, loaded := s.similaring.LoadOrStore(key, struct{}{}); loaded {
			return
		}
		defer s.similaring.Delete(key)
	}

	norm := titlesim.Normalize(src.Title)
	if err := s.repo.SetTitleNorm(ctx, src.ID, norm); err != nil {
		if !errors.Is(err, ErrNotFound) && !errors.Is(err, context.Canceled) {
			s.log.Warn("title norm write failed", "err", err, "source", src.ID)
		}
		return
	}
	hits := make([]similarHit, 0)
	touched := map[uuid.UUID]struct{}{src.DocumentID: {}}
	if norm != "" {
		candidates, err := s.repo.ListTitleCandidates(ctx, src.ID, src.DocumentID)
		if err != nil {
			if !errors.Is(err, ErrNotFound) && !errors.Is(err, context.Canceled) {
				s.log.Warn("title similar candidates failed", "err", err, "source", src.ID)
			}
			return
		}
		for _, c := range candidates {
			score := titlesim.ScoreNorm(norm, c.TitleNorm)
			if score < titlesim.Threshold {
				continue
			}
			hits = append(hits, similarHit{
				MatchedSourceID: c.ID,
				DocumentID:      c.DocumentID,
				Score:           score,
			})
			touched[c.DocumentID] = struct{}{}
		}
		sort.Slice(hits, func(i, j int) bool {
			if hits[i].Score == hits[j].Score {
				return hits[i].MatchedSourceID.String() < hits[j].MatchedSourceID.String()
			}
			return hits[i].Score > hits[j].Score
		})
		if len(hits) > maxSimilarKept {
			hits = hits[:maxSimilarKept]
		}
	}
	if err := s.repo.ReplaceSimilar(ctx, src.ID, norm, hits); err != nil {
		if !errors.Is(err, ErrNotFound) && !errors.Is(err, context.Canceled) {
			s.log.Warn("title similar write failed", "err", err, "source", src.ID)
		}
		return
	}
	published := 0
	for _, id := range sortedUUIDs(touched) {
		doc, err := s.repo.Get(ctx, id)
		if err != nil {
			continue
		}
		s.hub.Publish(ctx, "document.updated", doc)
		published++
		if published >= 24 {
			break
		}
	}
}

func titleRetryDelay(attempts int) time.Duration {
	delays := [...]time.Duration{
		20 * time.Second,
		45 * time.Second,
		2 * time.Minute,
		5 * time.Minute,
		15 * time.Minute,
		30 * time.Minute,
	}
	if attempts < 1 {
		attempts = 1
	}
	if attempts > len(delays) {
		attempts = len(delays)
	}
	return delays[attempts-1]
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
	// Spooling the upload to disk is bounded by maxB and waits on the client,
	// so it runs before the memory gate.
	path, err := writeInspectTemp(r, filename, s.maxB)
	if err != nil {
		s.log.Warn("inspect temp failed", "err", err, "file", filename)
		out.OK = false
		return out
	}
	defer os.Remove(path)

	if !s.acquireHeavy(ctx) {
		out.OK = false
		return out
	}
	defer s.releaseHeavy()
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
				DocumentID: m.DocumentID,
				FileURL:    fileURL(m.DocumentID, m.SourceID),
				Title:      m.Title,
				ERP:        m.ERP,
				Client:     m.Client,
				ANZSCO:     strings.TrimSpace(m.ANZSCO),
				Team:       strings.TrimSpace(m.Team),
				Member:     m.Member,
				Score:      m.Score,
				Uploaded:   m.Uploaded,
				Uniqueness: m.Uniqueness,
				Kind:       m.Kind,
				Note:       strings.TrimSpace(m.Note),
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

func (s *Service) SuggestTitle(ctx context.Context, filename string, r io.Reader) (engine.Result, error) {
	var out engine.Result
	if s.engine == nil || !s.engine.Configured() {
		return out, nil
	}
	head := make([]byte, 16)
	n, _ := io.ReadFull(r, head)
	if n > 0 {
		head = head[:n]
	}
	if fingerprint.Sniff(head, filename) != "pdf" {
		return out, nil
	}
	var reader io.Reader = r
	if n > 0 {
		reader = io.MultiReader(bytes.NewReader(head), r)
	}
	ctx, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()
	res, err := s.engine.TitleNow(ctx, filename, reader)
	if err != nil {
		if ctx.Err() != nil {
			return out, ctx.Err()
		}
		s.log.Warn("engine title failed", "err", err, "file", filename)
		return out, ErrEngineBusy
	}
	if res.Filename == "" {
		res.Filename = filename
	}
	res.Title = engine.DisplayName(res)
	return res, nil
}

func (s *Service) titleSource(ctx context.Context, src Source) {
	key := src.ID.String()
	if _, loaded := s.titling.LoadOrStore(key, struct{}{}); loaded {
		return
	}
	defer s.titling.Delete(key)
	s.runTitleSource(ctx, src)
}

func (s *Service) runTitleSource(ctx context.Context, src Source) {
	if s.engine == nil || !s.engine.Configured() {
		return
	}
	current, err := s.repo.GetSource(ctx, src.ID)
	if errors.Is(err, ErrNotFound) {
		return
	}
	if err == nil {
		src = current
		if engine.TitleSettled(src.Title) {
			_ = s.repo.SetSourceTitle(ctx, src.ID, src.Title)
			return
		}
	}
	if !s.acquireTitle(ctx) {
		_ = s.repo.DeferTitleRetry(ctx, src.ID, "")
		return
	}
	defer s.releaseTitle()
	err = retry.Do(ctx, 4, func(ctx context.Context) error {
		live, liveErr := s.repo.GetSource(ctx, src.ID)
		if liveErr == nil && engine.TitleSettled(live.Title) {
			return s.repo.SetSourceTitle(ctx, src.ID, live.Title)
		}
		path, err := s.blob.LocalPath(ctx, src.StorageKey)
		if errors.Is(err, blob.ErrNotFound) {
			return s.repo.ClearNeedsTitle(ctx, src.ID)
		}
		if err != nil {
			return err
		}
		f, err := os.Open(path)
		if errors.Is(err, os.ErrNotExist) {
			return s.repo.ClearNeedsTitle(ctx, src.ID)
		}
		if err != nil {
			return err
		}
		defer f.Close()
		res, err := s.engine.Title(ctx, filepath.Base(src.StorageKey), f)
		if err != nil {
			return err
		}
		name := engine.DisplayName(res)
		if !engine.TitleSettled(name) {
			if err := s.repo.DeferTitleRetry(ctx, src.ID, ""); err != nil {
				return err
			}
			if doc, getErr := s.repo.Get(ctx, src.DocumentID); getErr == nil {
				s.hub.Publish(ctx, "document.updated", doc)
			}
			return nil
		}
		if err := s.repo.SetSourceTitle(ctx, src.ID, name); err != nil {
			return err
		}
		s.enqueueSimilar(s.jobContext(), []Source{{
			ID:         src.ID,
			DocumentID: src.DocumentID,
			Title:      name,
		}}, false)
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
		_ = s.repo.DeferTitleRetry(ctx, src.ID, "")
	}
}

func (s *Service) hashSource(ctx context.Context, src Source) {
	key := src.ID.String()
	if _, loaded := s.hashing.LoadOrStore(key, struct{}{}); loaded {
		return
	}
	defer s.hashing.Delete(key)

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
		// Only the analysis holds a file in memory. Fetching the object is a
		// network wait, and gating it would throttle the whole pipeline.
		if !s.acquireHeavy(ctx) {
			return ctx.Err()
		}
		fp, err := fingerprint.Analyze(path, filepath.Base(src.StorageKey), src.ContentType)
		s.releaseHeavy()
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
		return 0, ErrTooLarge
	}
	n, err := l.r.Read(p)
	l.n += int64(n)
	if l.max > 0 && l.n > l.max {
		return n, ErrTooLarge
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
	s.trimMemory()
}

// acquireTitle gates calls to the extraction engine. It is deliberately
// separate from acquireHeavy so a slow engine cannot block fingerprinting,
// which is what decides whether a document is a duplicate.
func (s *Service) acquireTitle(ctx context.Context) bool {
	if s.titles == nil {
		return true
	}
	select {
	case s.titles <- struct{}{}:
		return true
	case <-ctx.Done():
		return false
	}
}

func (s *Service) releaseTitle() {
	if s.titles != nil {
		<-s.titles
	}
}

// trimMemory returns freed pages to the OS after large files are processed, but
// no more than once a second: FreeOSMemory stops the world, so calling it on
// every job would cost more than it saves once several run at a time.
func (s *Service) trimMemory() {
	now := time.Now().UnixNano()
	last := s.trimmedAt.Load()
	if now-last < int64(time.Second) {
		return
	}
	if !s.trimmedAt.CompareAndSwap(last, now) {
		return
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
