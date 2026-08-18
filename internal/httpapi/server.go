package httpapi

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"mime/multipart"
	"net/http"
	"path/filepath"
	"strings"
	"time"
	"unicode"

	"github.com/go-chi/chi/v5"
	"github.com/go-chi/chi/v5/middleware"
	"github.com/go-chi/cors"
	"github.com/google/uuid"

	"ocr-backend/internal/auth"
	"ocr-backend/internal/blob"
	"ocr-backend/internal/config"
	"ocr-backend/internal/documents"
	"ocr-backend/internal/engine"
	"ocr-backend/internal/memlimit"
	"ocr-backend/internal/notifications"
	"ocr-backend/internal/postgres"
	"ocr-backend/internal/realtime"
	"ocr-backend/internal/redisx"
)

const serviceName = "ocr-backend"

type Server struct {
	cfg        config.Config
	log        *slog.Logger
	pg         *postgres.Pool
	rdb        *redisx.Client
	blob       blob.Store
	hub        *realtime.Hub
	docs       *documents.Service
	notes      *notifications.Repo
	engine     *engine.Client
	users      *auth.Repo
	loginLimit *auth.LoginGate
	start      time.Time
	uploads    chan struct{}
}

func New(
	cfg config.Config,
	log *slog.Logger,
	pg *postgres.Pool,
	rdb *redisx.Client,
	store blob.Store,
	hub *realtime.Hub,
	docs *documents.Service,
	notes *notifications.Repo,
	eng *engine.Client,
	users *auth.Repo,
) *Server {
	n := cfg.MaxInflightUploads
	if n < 1 {
		n = 8
	}
	return &Server{
		cfg:        cfg,
		log:        log,
		pg:         pg,
		rdb:        rdb,
		blob:       store,
		hub:        hub,
		docs:       docs,
		notes:      notes,
		engine:     eng,
		users:      users,
		loginLimit: auth.NewLoginGate(),
		start:      time.Now(),
		uploads:    make(chan struct{}, n),
	}
}

func (s *Server) Handler() http.Handler {
	r := chi.NewRouter()
	r.Use(middleware.RequestID)
	r.Use(middleware.RealIP)
	r.Use(middleware.Recoverer)
	r.Use(s.logRequest)
	r.Use(cors.Handler(cors.Options{
		AllowedOrigins:   s.cfg.CORSOrigins,
		AllowedMethods:   []string{"GET", "POST", "PATCH", "DELETE", "HEAD", "OPTIONS"},
		AllowedHeaders:   []string{"Accept", "Content-Type", "Authorization", "X-Request-ID"},
		ExposedHeaders:   []string{"X-Request-ID"},
		AllowCredentials: true,
		MaxAge:           300,
	}))

	r.Get("/", s.root)
	r.Get("/health", s.health)
	r.Head("/health", s.health)
	r.Get("/ready", s.ready)
	r.Get("/health/stream", s.healthStream)

	r.Route("/v1", func(r chi.Router) {
		r.Post("/auth/login", s.login)
		r.Post("/auth/logout", s.logout)
		r.Get("/public/documents/{id}", s.getPublicDocument)
		r.Get("/public/documents/{id}/sources/{sid}/file", s.getPublicFile)
		r.Head("/public/documents/{id}/sources/{sid}/file", s.getPublicFile)
		r.Group(func(r chi.Router) {
			r.Use(s.requireAuth)
			r.Get("/auth/me", s.me)
			r.Get("/documents", s.listDocuments)
			r.Post("/documents", s.createDocument)
			r.Get("/documents/next-erp", s.nextERP)
			r.Post("/documents/inspect", s.inspectFile)
			r.Delete("/documents/{id}", s.deleteDocument)
			r.Post("/documents/{id}/sources", s.addSources)
			r.Get("/documents/{id}/sources/{sid}/file", s.getFile)
			r.Head("/documents/{id}/sources/{sid}/file", s.getFile)
			r.Post("/engine/title", s.suggestTitle)
			r.Get("/notifications", s.listNotifications)
			r.Patch("/notifications/{id}/read", s.markRead)
			r.Post("/notifications/read-all", s.markAllRead)
			r.Get("/events/stream", s.eventsStream)
			r.Group(func(r chi.Router) {
				r.Use(s.requireAdmin)
				r.Get("/users", s.listUsers)
				r.Post("/users", s.createUser)
				r.Patch("/users/{id}", s.setUserDisabled)
				r.Get("/reviews", s.listReviews)
				r.Post("/reviews/{id}/approve", s.approveReview)
				r.Post("/reviews/{id}/reject", s.rejectReview)
				r.Get("/stats/uploads", s.uploadStats)
			})
		})
	})
	return r
}

func (s *Server) logRequest(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if strings.HasSuffix(r.URL.Path, "/stream") {
			next.ServeHTTP(w, r)
			return
		}
		start := time.Now()
		ww := middleware.NewWrapResponseWriter(w, r.ProtoMajor)
		next.ServeHTTP(ww, r)
		s.log.Info("http",
			"method", r.Method,
			"path", r.URL.Path,
			"status", ww.Status(),
			"bytes", ww.BytesWritten(),
			"dur_ms", time.Since(start).Milliseconds(),
			"req_id", middleware.GetReqID(r.Context()),
		)
	})
}

type healthPayload struct {
	OK            bool              `json:"ok"`
	Status        string            `json:"status"`
	Ready         bool              `json:"ready"`
	Service       string            `json:"service"`
	UptimeSeconds float64           `json:"uptime_seconds"`
	Timestamp     string            `json:"timestamp"`
	Checks        map[string]string `json:"checks"`
}

func (s *Server) snapshot(_ context.Context) healthPayload {
	checks := map[string]string{}
	pg, _ := s.pg.Status()
	checks["postgres"] = pg
	rd, _ := s.rdb.Status()
	checks["redis"] = rd
	checks["storage"] = "ok"
	if s.blob != nil {
		checks["storage_driver"] = s.blob.Driver()
	}
	if s.engine != nil && s.engine.Configured() {
		checks["engine"] = "ok"
	} else {
		checks["engine"] = "off"
	}
	checks["gomemlimit"] = memlimit.Format(memlimit.Current())
	if cg := memlimit.CgroupLimit(); cg > 0 {
		checks["cgroup_memory"] = memlimit.Format(cg)
	} else {
		checks["cgroup_memory"] = "unlimited"
	}

	ready := pg == "ok" && rd == "ok"
	status := "ok"
	if !ready {
		status = "starting"
		if time.Since(s.start) > 20*time.Second {
			status = "degraded"
		}
	}
	return healthPayload{
		OK:            true,
		Status:        status,
		Ready:         ready,
		Service:       serviceName,
		UptimeSeconds: time.Since(s.start).Seconds(),
		Timestamp:     time.Now().UTC().Format(time.RFC3339Nano),
		Checks:        checks,
	}
}

func (s *Server) root(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, http.StatusOK, map[string]any{
		"service": serviceName,
		"health":  "/health",
		"stream":  "/health/stream",
		"events":  "/v1/events/stream",
	})
}

func (s *Server) health(w http.ResponseWriter, r *http.Request) {
	payload := s.snapshot(r.Context())
	writeJSON(w, http.StatusOK, payload)
}

func (s *Server) ready(w http.ResponseWriter, r *http.Request) {
	payload := s.snapshot(r.Context())
	if !payload.Ready {
		writeJSON(w, http.StatusServiceUnavailable, payload)
		return
	}
	writeJSON(w, http.StatusOK, payload)
}

func (s *Server) healthStream(w http.ResponseWriter, r *http.Request) {
	flusher, ok := w.(http.Flusher)
	if !ok {
		http.Error(w, "stream unsupported", http.StatusInternalServerError)
		return
	}
	w.Header().Set("Content-Type", "text/event-stream")
	w.Header().Set("Cache-Control", "no-cache, no-transform")
	w.Header().Set("Connection", "keep-alive")
	w.Header().Set("X-Accel-Buffering", "no")
	w.Header().Set("Content-Encoding", "identity")
	tick := time.NewTicker(s.cfg.Heartbeat)
	defer tick.Stop()
	writeSSE(w, flusher, "status", s.snapshot(r.Context()))
	for {
		select {
		case <-r.Context().Done():
			return
		case <-tick.C:
			writeSSE(w, flusher, "status", s.snapshot(r.Context()))
		}
	}
}

func (s *Server) eventsStream(w http.ResponseWriter, r *http.Request) {
	flusher, ok := w.(http.Flusher)
	if !ok {
		http.Error(w, "stream unsupported", http.StatusInternalServerError)
		return
	}
	w.Header().Set("Content-Type", "text/event-stream")
	w.Header().Set("Cache-Control", "no-cache, no-transform")
	w.Header().Set("Connection", "keep-alive")
	w.Header().Set("X-Accel-Buffering", "no")
	w.Header().Set("Content-Encoding", "identity")
	ch, cancel := s.hub.Subscribe(256)
	defer cancel()
	writeSSE(w, flusher, "hello", map[string]string{"ok": "true"})
	keep := time.NewTicker(15 * time.Second)
	defer keep.Stop()
	for {
		select {
		case <-r.Context().Done():
			return
		case <-keep.C:
			fmt.Fprintf(w, ": keepalive\n\n")
			flusher.Flush()
		case raw, ok := <-ch:
			if !ok {
				return
			}
			user, _ := auth.From(r.Context())
			if !eventVisible(user, raw) {
				continue
			}
			fmt.Fprintf(w, "event: message\ndata: %s\n\n", raw)
			flusher.Flush()
		}
	}
}

func (s *Server) listDocuments(w http.ResponseWriter, r *http.Request) {
	items, err := s.docs.List(r.Context(), false)
	if err != nil {
		s.writeErr(w, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"items": items})
}

func (s *Server) nextERP(w http.ResponseWriter, r *http.Request) {
	erp, err := s.docs.NextERP(r.Context())
	if err != nil {
		s.writeErr(w, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]string{"erp": erp})
}

func (s *Server) acquireUpload(ctx context.Context) bool {
	ctx, cancel := context.WithTimeout(ctx, 120*time.Second)
	defer cancel()
	select {
	case s.uploads <- struct{}{}:
		return true
	case <-ctx.Done():
		return false
	}
}

func (s *Server) releaseUpload() {
	select {
	case <-s.uploads:
	default:
	}
}

func (s *Server) writeBusy(w http.ResponseWriter) {
	w.Header().Set("Retry-After", "2")
	writeErrCode(w, http.StatusServiceUnavailable, "busy", "server is busy; retry shortly")
}

func (s *Server) createDocument(w http.ResponseWriter, r *http.Request) {
	if !s.acquireUpload(r.Context()) {
		s.writeBusy(w)
		return
	}
	defer s.releaseUpload()
	in, files, err := parseUpload(w, r, s.cfg)
	if err != nil {
		writeErrCode(w, http.StatusBadRequest, "invalid", err.Error())
		return
	}
	doc, err := s.docs.Create(r.Context(), in, files)
	if err != nil {
		s.writeErr(w, err)
		return
	}
	writeJSON(w, http.StatusCreated, doc)
}

func (s *Server) addSources(w http.ResponseWriter, r *http.Request) {
	id, err := parseID(chi.URLParam(r, "id"))
	if err != nil {
		writeErrCode(w, http.StatusBadRequest, "invalid", "invalid id")
		return
	}
	if !s.acquireUpload(r.Context()) {
		s.writeBusy(w)
		return
	}
	defer s.releaseUpload()
	in, files, err := parseUpload(w, r, s.cfg)
	if err != nil {
		writeErrCode(w, http.StatusBadRequest, "invalid", err.Error())
		return
	}
	doc, err := s.docs.AddSources(r.Context(), id, files, in.Titles, in.Notes, in.Note)
	if err != nil {
		s.writeErr(w, err)
		return
	}
	writeJSON(w, http.StatusOK, doc)
}

func (s *Server) deleteDocument(w http.ResponseWriter, r *http.Request) {
	id, err := parseID(chi.URLParam(r, "id"))
	if err != nil {
		writeErrCode(w, http.StatusBadRequest, "invalid", "invalid id")
		return
	}
	if err := s.docs.Delete(r.Context(), id); err != nil {
		s.writeErr(w, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]bool{"ok": true})
}

func (s *Server) getPublicDocument(w http.ResponseWriter, r *http.Request) {
	id, err := parseID(chi.URLParam(r, "id"))
	if err != nil {
		writeErrCode(w, http.StatusBadRequest, "invalid", "invalid id")
		return
	}
	doc, err := s.docs.PublicGet(r.Context(), id)
	if err != nil {
		s.writeErr(w, err)
		return
	}
	writeJSON(w, http.StatusOK, doc)
}

func (s *Server) getFile(w http.ResponseWriter, r *http.Request) {
	s.serveSourceFile(w, r, false)
}

func (s *Server) getPublicFile(w http.ResponseWriter, r *http.Request) {
	s.serveSourceFile(w, r, true)
}

func (s *Server) serveSourceFile(w http.ResponseWriter, r *http.Request, public bool) {
	docID, err := parseID(chi.URLParam(r, "id"))
	if err != nil {
		writeErrCode(w, http.StatusBadRequest, "invalid", "invalid id")
		return
	}
	sid, err := parseID(chi.URLParam(r, "sid"))
	if err != nil {
		writeErrCode(w, http.StatusBadRequest, "invalid", "invalid id")
		return
	}
	open := s.docs.OpenFile
	if public {
		open = s.docs.PublicOpenFile
	}
	f, _, ctype, name, mod, err := open(r.Context(), docID, sid)
	if err != nil {
		s.writeErr(w, err)
		return
	}
	defer f.Close()
	if ctype != "" {
		w.Header().Set("Content-Type", ctype)
	}
	w.Header().Set("Content-Disposition", fmt.Sprintf(`inline; filename=%q`, safeDownloadName(name, ctype)))
	w.Header().Set("Cache-Control", "private, max-age=120")
	w.Header().Set("X-Content-Type-Options", "nosniff")
	http.ServeContent(w, r, name, mod, f)
}

func (s *Server) listNotifications(w http.ResponseWriter, r *http.Request) {
	items, err := s.notes.List(r.Context())
	if err != nil {
		s.writeErr(w, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"items": items})
}

func (s *Server) markRead(w http.ResponseWriter, r *http.Request) {
	id, err := parseID(chi.URLParam(r, "id"))
	if err != nil {
		writeErrCode(w, http.StatusBadRequest, "invalid", "invalid id")
		return
	}
	item, err := s.notes.MarkRead(r.Context(), id)
	if err != nil {
		s.writeErr(w, err)
		return
	}
	writeJSON(w, http.StatusOK, item)
}

func (s *Server) markAllRead(w http.ResponseWriter, r *http.Request) {
	if err := s.notes.MarkAllRead(r.Context()); err != nil {
		s.writeErr(w, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]bool{"ok": true})
}

func (s *Server) inspectFile(w http.ResponseWriter, r *http.Request) {
	if !s.acquireUpload(r.Context()) {
		s.writeBusy(w)
		return
	}
	defer s.releaseUpload()
	fh, f, ok := openOneUpload(w, r, s.cfg.MaxUploadBytes)
	if !ok {
		return
	}
	defer f.Close()
	writeJSON(w, http.StatusOK, s.docs.InspectFile(r.Context(), fh.Filename, f))
}

func (s *Server) suggestTitle(w http.ResponseWriter, r *http.Request) {
	fh, f, ok := openOneUpload(w, r, s.cfg.MaxUploadBytes)
	if !ok {
		return
	}
	defer f.Close()
	res, err := s.docs.SuggestTitle(r.Context(), fh.Filename, f)
	if err != nil {
		s.writeErr(w, err)
		return
	}
	writeJSON(w, http.StatusOK, res)
}

func openOneUpload(w http.ResponseWriter, r *http.Request, max int64) (*multipart.FileHeader, multipart.File, bool) {
	r.Body = http.MaxBytesReader(w, r.Body, max+1<<20)
	if err := r.ParseMultipartForm(1 << 20); err != nil {
		writeErrCode(w, http.StatusBadRequest, "invalid", "invalid upload")
		return nil, nil, false
	}
	var fh *multipart.FileHeader
	if r.MultipartForm != nil {
		if files := r.MultipartForm.File["file"]; len(files) > 0 {
			fh = files[0]
		} else if files := r.MultipartForm.File["files"]; len(files) > 0 {
			fh = files[0]
		}
	}
	if fh == nil {
		writeErrCode(w, http.StatusBadRequest, "no_files", "file is required")
		return nil, nil, false
	}
	f, err := fh.Open()
	if err != nil {
		writeErrCode(w, http.StatusBadRequest, "invalid", "cannot read file")
		return nil, nil, false
	}
	return fh, f, true
}

func (s *Server) writeErr(w http.ResponseWriter, err error) {
	switch {
	case errors.Is(err, context.Canceled):
		w.WriteHeader(499)
		return
	case errors.Is(err, context.DeadlineExceeded):
		writeErrCode(w, http.StatusGatewayTimeout, "timeout", "request timed out")
		return
	case errors.Is(err, documents.ErrEngineBusy):
		s.writeBusy(w)
		return
	case errors.Is(err, documents.ErrUnavailable):
		writeErrCode(w, http.StatusServiceUnavailable, "unavailable", "postgres is unavailable")
	case errors.Is(err, documents.ErrNotFound), errors.Is(err, blob.ErrNotFound):
		writeErrCode(w, http.StatusNotFound, "not_found", "not found")
	case errors.Is(err, documents.ErrERPTaken):
		writeErrCode(w, http.StatusConflict, "erp_taken", "ERP code is already in use")
	case errors.Is(err, documents.ErrInvalid):
		writeErrCode(w, http.StatusBadRequest, "invalid", "invalid document")
	case errors.Is(err, documents.ErrNoFiles):
		writeErrCode(w, http.StatusBadRequest, "no_files", "at least one file is required")
	case errors.Is(err, documents.ErrTooMany):
		writeErrCode(w, http.StatusBadRequest, "too_many", "source limit reached")
	case errors.Is(err, documents.ErrTooLarge):
		writeErrCode(w, http.StatusBadRequest, "too_large", "file too large")
	case errors.Is(err, auth.ErrUnauthorized), errors.Is(err, auth.ErrInvalidCreds):
		writeErrCode(w, http.StatusUnauthorized, "unauthorized", "sign in required")
	case errors.Is(err, auth.ErrForbidden), errors.Is(err, documents.ErrForbidden):
		writeErrCode(w, http.StatusForbidden, "forbidden", "not allowed")
	case errors.Is(err, auth.ErrEmailTaken):
		writeErrCode(w, http.StatusConflict, "email_taken", "email is already in use")
	case errors.Is(err, auth.ErrInvalid):
		writeErrCode(w, http.StatusBadRequest, "invalid", "invalid user")
	case errors.Is(err, auth.ErrDisabled):
		writeErrCode(w, http.StatusForbidden, "disabled", "account is disabled")
	default:
		s.log.Error("request failed", "err", err)
		writeErrCode(w, http.StatusInternalServerError, "internal", "internal error")
	}
}

func parseUpload(w http.ResponseWriter, r *http.Request, cfg config.Config) (documents.CreateInput, []*multipart.FileHeader, error) {
	max := cfg.MaxUploadBytes*int64(cfg.MaxSources) + 1<<20
	r.Body = http.MaxBytesReader(w, r.Body, max)
	if err := r.ParseMultipartForm(1 << 20); err != nil {
		return documents.CreateInput{}, nil, err
	}
	in := documents.CreateInput{
		Client: r.FormValue("client"),
		ERP:    r.FormValue("erp"),
		ANZSCO: r.FormValue("anzsco"),
		Team:   r.FormValue("team"),
		Member: r.FormValue("member"),
		Note:   r.FormValue("note"),
	}
	var files []*multipart.FileHeader
	if r.MultipartForm != nil {
		files = append(files, r.MultipartForm.File["files"]...)
		files = append(files, r.MultipartForm.File["file"]...)
		files = append(files, r.MultipartForm.File["files[]"]...)
		in.Titles = append(in.Titles, r.MultipartForm.Value["titles"]...)
		in.Titles = append(in.Titles, r.MultipartForm.Value["title"]...)
		in.Notes = append(in.Notes, r.MultipartForm.Value["notes"]...)
	}
	return in, files, nil
}

func parseID(raw string) (uuid.UUID, error) {
	return uuid.Parse(strings.TrimSpace(raw))
}

func safeDownloadName(name, ctype string) string {
	raw := strings.ReplaceAll(strings.TrimSpace(name), "\\", "/")
	raw = strings.ReplaceAll(raw, "/", "-")
	base := filepath.Base(raw)
	var b strings.Builder
	for _, r := range base {
		if r < 32 || r == '"' || unicode.IsControl(r) {
			continue
		}
		b.WriteRune(r)
	}
	out := strings.TrimSpace(b.String())
	if out == "" || out == "." || out == ".." {
		out = "document"
	}
	if filepath.Ext(out) == "" && strings.Contains(strings.ToLower(ctype), "pdf") {
		out += ".pdf"
	}
	return out
}

func writeJSON(w http.ResponseWriter, code int, v any) {
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.WriteHeader(code)
	_ = json.NewEncoder(w).Encode(v)
}

func writeErrCode(w http.ResponseWriter, code int, kind, msg string) {
	writeJSON(w, code, map[string]string{"error": msg, "code": kind})
}

func writeSSE(w http.ResponseWriter, flusher http.Flusher, event string, v any) {
	raw, err := json.Marshal(v)
	if err != nil {
		return
	}
	fmt.Fprintf(w, "retry: 2000\n: keepalive\nevent: %s\ndata: %s\n\n", event, raw)
	flusher.Flush()
}
