package httpapi

import (
	"encoding/json"
	"errors"
	"net/http"
	"strings"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/google/uuid"

	"ocr-backend/internal/auth"
	"ocr-backend/internal/documents"
)

func (s *Server) requireAuth(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		user, err := s.users.UserByToken(r.Context(), auth.TokenFromRequest(r))
		if err != nil {
			writeErrCode(w, http.StatusUnauthorized, "unauthorized", "sign in required")
			return
		}
		next.ServeHTTP(w, r.WithContext(auth.WithUser(r.Context(), user)))
	})
}

func (s *Server) requireAdmin(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		user, err := auth.Must(r.Context())
		if err != nil || !user.Admin() {
			writeErrCode(w, http.StatusForbidden, "forbidden", "admin only")
			return
		}
		next.ServeHTTP(w, r)
	})
}

func (s *Server) login(w http.ResponseWriter, r *http.Request) {
	ip := strings.TrimSpace(r.RemoteAddr)
	if fwd := strings.TrimSpace(r.Header.Get("X-Forwarded-For")); fwd != "" {
		ip = strings.TrimSpace(strings.Split(fwd, ",")[0])
	}
	var body struct {
		Email    string `json:"email"`
		Password string `json:"password"`
	}
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 1<<20)).Decode(&body); err != nil {
		writeErrCode(w, http.StatusBadRequest, "invalid", "invalid login")
		return
	}
	key := ip + "|" + auth.NormalizeEmail(body.Email)
	if !s.loginLimit.Allow(key, 8, 15*time.Minute) {
		writeErrCode(w, http.StatusTooManyRequests, "rate_limited", "too many attempts; try later")
		return
	}
	user, err := s.users.Authenticate(r.Context(), body.Email, body.Password)
	if err != nil {
		s.loginLimit.Fail(key)
		if errors.Is(err, auth.ErrDisabled) {
			writeErrCode(w, http.StatusForbidden, "disabled", "account is disabled")
			return
		}
		writeErrCode(w, http.StatusUnauthorized, "unauthorized", "invalid email or password")
		return
	}
	s.loginLimit.Clear(key)
	token, expires, err := s.users.CreateSession(r.Context(), user.ID)
	if err != nil {
		s.writeErr(w, err)
		return
	}
	s.setSessionCookie(w, r, token, expires)
	writeJSON(w, http.StatusOK, map[string]any{"user": user.Public()})
}

func (s *Server) logout(w http.ResponseWriter, r *http.Request) {
	_ = s.users.DeleteSession(r.Context(), auth.TokenFromRequest(r))
	s.clearSessionCookie(w, r)
	writeJSON(w, http.StatusOK, map[string]bool{"ok": true})
}

func (s *Server) me(w http.ResponseWriter, r *http.Request) {
	user, err := auth.Must(r.Context())
	if err != nil {
		writeErrCode(w, http.StatusUnauthorized, "unauthorized", "sign in required")
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"user": user.Public()})
}

func (s *Server) listUsers(w http.ResponseWriter, r *http.Request) {
	items, err := s.users.List(r.Context())
	if err != nil {
		s.writeErr(w, err)
		return
	}
	out := make([]auth.User, 0, len(items))
	for _, item := range items {
		out = append(out, item.Public())
	}
	writeJSON(w, http.StatusOK, map[string]any{"items": out})
}

func (s *Server) createUser(w http.ResponseWriter, r *http.Request) {
	var body struct {
		Email    string `json:"email"`
		Name     string `json:"name"`
		Password string `json:"password"`
		Role     string `json:"role"`
	}
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 1<<20)).Decode(&body); err != nil {
		writeErrCode(w, http.StatusBadRequest, "invalid", "invalid user")
		return
	}
	if body.Role == "" {
		body.Role = auth.RoleMember
	}
	user, err := s.users.CreateUser(r.Context(), body.Email, body.Name, body.Password, body.Role)
	if err != nil {
		s.writeErr(w, err)
		return
	}
	writeJSON(w, http.StatusCreated, map[string]any{"user": user.Public()})
}

func (s *Server) setUserDisabled(w http.ResponseWriter, r *http.Request) {
	id, err := parseID(chi.URLParam(r, "id"))
	if err != nil {
		writeErrCode(w, http.StatusBadRequest, "invalid", "invalid id")
		return
	}
	actor, err := auth.Must(r.Context())
	if err != nil {
		s.writeErr(w, err)
		return
	}
	if actor.ID == id {
		writeErrCode(w, http.StatusBadRequest, "invalid", "you cannot disable your own account")
		return
	}
	var body struct {
		Disabled bool `json:"disabled"`
	}
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 1<<20)).Decode(&body); err != nil {
		writeErrCode(w, http.StatusBadRequest, "invalid", "invalid user")
		return
	}
	if err := s.users.SetDisabled(r.Context(), id, body.Disabled); err != nil {
		s.writeErr(w, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]bool{"ok": true})
}

func (s *Server) listReviews(w http.ResponseWriter, r *http.Request) {
	items, err := s.docs.List(r.Context(), true)
	if err != nil {
		s.writeErr(w, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"items": items})
}

func (s *Server) approveReview(w http.ResponseWriter, r *http.Request) {
	id, err := parseID(chi.URLParam(r, "id"))
	if err != nil {
		writeErrCode(w, http.StatusBadRequest, "invalid", "invalid id")
		return
	}
	doc, err := s.docs.Approve(r.Context(), id)
	if err != nil {
		s.writeErr(w, err)
		return
	}
	writeJSON(w, http.StatusOK, doc)
}

func (s *Server) rejectReview(w http.ResponseWriter, r *http.Request) {
	id, err := parseID(chi.URLParam(r, "id"))
	if err != nil {
		writeErrCode(w, http.StatusBadRequest, "invalid", "invalid id")
		return
	}
	if err := s.docs.Reject(r.Context(), id); err != nil {
		s.writeErr(w, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]bool{"ok": true})
}

func (s *Server) setSessionCookie(w http.ResponseWriter, r *http.Request, token string, expires time.Time) {
	http.SetCookie(w, &http.Cookie{
		Name:     auth.CookieName,
		Value:    token,
		Path:     "/",
		Expires:  expires,
		MaxAge:   int(time.Until(expires).Seconds()),
		HttpOnly: true,
		Secure:   cookieSecure(r),
		SameSite: http.SameSiteLaxMode,
	})
}

func (s *Server) clearSessionCookie(w http.ResponseWriter, r *http.Request) {
	http.SetCookie(w, &http.Cookie{
		Name:     auth.CookieName,
		Value:    "",
		Path:     "/",
		MaxAge:   -1,
		HttpOnly: true,
		Secure:   cookieSecure(r),
		SameSite: http.SameSiteLaxMode,
	})
}

func cookieSecure(r *http.Request) bool {
	if r.TLS != nil {
		return true
	}
	return strings.EqualFold(strings.TrimSpace(r.Header.Get("X-Forwarded-Proto")), "https")
}

func eventVisible(user auth.User, raw []byte) bool {
	var event struct {
		Type string          `json:"type"`
		Data json.RawMessage `json:"data"`
	}
	if json.Unmarshal(raw, &event) != nil {
		return false
	}
	if user.Admin() {
		return true
	}
	switch event.Type {
	case "document.created", "document.updated":
		var doc struct {
			OwnerID *uuid.UUID `json:"owner_id"`
		}
		if json.Unmarshal(event.Data, &doc) != nil {
			return false
		}
		return documents.Owns(doc.OwnerID, user.ID)
	case "document.deleted":
		var payload struct {
			OwnerID *uuid.UUID `json:"owner_id"`
		}
		if json.Unmarshal(event.Data, &payload) != nil {
			return true
		}
		if payload.OwnerID == nil {
			return false
		}
		return documents.Owns(payload.OwnerID, user.ID)
	case "notification.created", "notification.updated":
		var note struct {
			UserID   *uuid.UUID `json:"user_id"`
			Audience string     `json:"audience"`
		}
		if json.Unmarshal(event.Data, &note) != nil {
			return false
		}
		if note.Audience == "admin" {
			return false
		}
		return note.UserID != nil && *note.UserID == user.ID
	case "notification.cleared":
		return true
	default:
		return false
	}
}
