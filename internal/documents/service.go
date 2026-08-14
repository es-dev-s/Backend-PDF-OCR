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
	"strings"
	"sync"
	"time"
	"unicode"

	"github.com/google/uuid"
	"golang.org/x/sync/errgroup"

	"ocr-backend/internal/blob"
	"ocr-backend/internal/engine"
	"ocr-backend/internal/fingerprint"
	"ocr-backend/internal/realtime"
	"ocr-backend/internal/retry"
	"ocr-backend/internal/worker"
)

type Notifier interface {
	Create(ctx context.Context, title, detail string) error
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
	inspect chan struct{}
}

func NewService(repo *Repo, store blob.Store, hub *realtime.Hub, pool *worker.Pool, notes Notifier, eng *engine.Client, log *slog.Logger, maxSources int, maxBytes int64) *Service {
	if maxSources < 1 {
		maxSources = 4
	}
	return &Service{
		repo:    repo,
		blob:    store,
		hub:     hub,
		pool:    pool,
		notes:   notes,
		engine:  eng,
		log:     log,
		maxN:    maxSources,
		maxB:    maxBytes,
		inspect: make(chan struct{}, 2),
	}
}

func (s *Service) List(ctx context.Context) ([]Document, error) {
	return s.repo.List(ctx)
}

func (s *Service) Get(ctx context.Context, id uuid.UUID) (Document, error) {
	return s.repo.Get(ctx, id)
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
	in.Client = strings.TrimSpace(in.Client)
	in.ERP = strings.TrimSpace(in.ERP)
	in.ANZSCO = strings.TrimSpace(in.ANZSCO)
	in.Team = strings.TrimSpace(in.Team)
	in.Member = strings.TrimSpace(in.Member)
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
	bg := context.WithoutCancel(ctx)
	s.hashSources(bg, written)
	s.enqueueTitle(bg, written)
	out, err := s.repo.Get(ctx, doc.ID)
	if err != nil {
		return Document{}, err
	}
	s.hub.Publish(ctx, "document.created", out)
	return out, nil
}

func (s *Service) AddSources(ctx context.Context, id uuid.UUID, files []*multipart.FileHeader, titles []string) (Document, error) {
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
	bg := context.WithoutCancel(ctx)
	s.hashSources(bg, written)
	s.enqueueTitle(bg, written)
	out, err := s.repo.Get(ctx, id)
	if err != nil {
		return Document{}, err
	}
	s.hub.Publish(ctx, "document.updated", out)
	return out, nil
}

func (s *Service) Delete(ctx context.Context, id uuid.UUID) error {
	keys, err := s.repo.Delete(ctx, id)
	if err != nil {
		return err
	}
	for _, key := range keys {
		_ = s.blob.Delete(ctx, key)
	}
	s.hub.Publish(ctx, "document.deleted", map[string]string{"id": id.String()})
	return nil
}

func (s *Service) OpenFile(ctx context.Context, docID, sourceID uuid.UUID) (io.ReadCloser, int64, string, string, error) {
	src, err := s.repo.SourceMeta(ctx, docID, sourceID)
	if err != nil {
		return nil, 0, "", "", err
	}
	rc, size, ctype, err := s.blob.Open(ctx, src.StorageKey)
	if err != nil {
		return nil, 0, "", "", err
	}
	if src.ContentType != "" {
		ctype = src.ContentType
	}
	return rc, size, ctype, src.Title, nil
}

func (s *Service) RecoverPending(ctx context.Context) {
	sources, err := s.repo.ListUnhashed(ctx, 100)
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
	s.RecoverPending(ctx)
	t := time.NewTicker(30 * time.Second)
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
	g.SetLimit(4)
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
			if err := s.blob.Put(ctx, key, limited, fh.Size, ctype); err != nil {
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
	for _, src := range sources {
		src := src
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
	case s.inspect <- struct{}{}:
		defer func() { <-s.inspect }()
	case <-ctx.Done():
		out.OK = false
		return out
	}
	path, err := writeInspectTemp(r, filename, s.maxB)
	if err != nil {
		s.log.Warn("inspect temp failed", "err", err, "file", filename)
		out.OK = false
		return out
	}
	defer os.Remove(path)

	fp, err := fingerprint.Analyze(path, filename, "")
	if err != nil {
		s.log.Warn("inspect analyze failed", "err", err, "file", filename)
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
	err := retry.Do(ctx, 8, func(ctx context.Context) error {
		path, err := s.blob.LocalPath(ctx, src.StorageKey)
		if err != nil {
			return err
		}
		fp, err := fingerprint.Analyze(path, filepath.Base(src.StorageKey), src.ContentType)
		if err != nil {
			return err
		}
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
			notified := false
			for _, nid := range result.Notified {
				if nid == id {
					notified = true
					break
				}
			}
			if !notified || s.notes == nil {
				continue
			}
			title := "Document processed"
			detail := fmt.Sprintf("%s finished processing", doc.Title)
			if doc.Status == StatusDuplicate {
				title = "Duplicate found"
				detail = fmt.Sprintf("%s has duplicate sources", doc.Title)
			}
			_ = s.notes.Create(ctx, title, detail)
		}
		return nil
	})
	if err != nil && !errors.Is(err, ErrNotFound) && !errors.Is(err, context.Canceled) {
		s.log.Error("hash source failed", "err", err, "source", src.ID)
	}
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
