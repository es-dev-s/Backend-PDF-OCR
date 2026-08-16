package auth

import (
	"context"
	"errors"
	"net/http"
	"strings"
	"time"

	"github.com/google/uuid"
)

const (
	RoleAdmin  = "admin"
	RoleMember = "member"
	CookieName = "ocr_session"
	SessionTTL = 7 * 24 * time.Hour
)

var (
	ErrUnauthorized = errors.New("unauthorized")
	ErrForbidden    = errors.New("forbidden")
	ErrInvalidCreds = errors.New("invalid credentials")
	ErrInvalid      = errors.New("invalid")
	ErrDisabled     = errors.New("account disabled")
	ErrEmailTaken   = errors.New("email taken")
)

type ctxKey struct{}

type User struct {
	ID       uuid.UUID `json:"id"`
	Email    string    `json:"email"`
	Name     string    `json:"name"`
	Role     string    `json:"role"`
	Disabled bool      `json:"disabled,omitempty"`
	Created  time.Time `json:"created_at,omitempty"`
}

func (u User) Admin() bool {
	return u.Role == RoleAdmin
}

func (u User) Public() User {
	return User{ID: u.ID, Email: u.Email, Name: u.Name, Role: u.Role, Disabled: u.Disabled, Created: u.Created}
}

func WithUser(ctx context.Context, user User) context.Context {
	return context.WithValue(ctx, ctxKey{}, user)
}

func From(ctx context.Context) (User, bool) {
	user, ok := ctx.Value(ctxKey{}).(User)
	return user, ok && user.ID != uuid.Nil
}

func Must(ctx context.Context) (User, error) {
	user, ok := From(ctx)
	if !ok {
		return User{}, ErrUnauthorized
	}
	return user, nil
}

func TokenFromRequest(r *http.Request) string {
	if raw := strings.TrimSpace(r.Header.Get("Authorization")); strings.HasPrefix(strings.ToLower(raw), "bearer ") {
		return strings.TrimSpace(raw[7:])
	}
	if c, err := r.Cookie(CookieName); err == nil {
		return strings.TrimSpace(c.Value)
	}
	return strings.TrimSpace(r.URL.Query().Get("access_token"))
}

func NormalizeEmail(email string) string {
	return strings.ToLower(strings.TrimSpace(email))
}

func ValidRole(role string) bool {
	return role == RoleAdmin || role == RoleMember
}
