package auth

import (
	"net/http"
	"net/http/httptest"
	"testing"
)

func TestTokenFromRequestPrefersBearerThenCookie(t *testing.T) {
	req := httptest.NewRequest(http.MethodGet, "/v1/documents?access_token=query-token", nil)
	req.Header.Set("Authorization", "Bearer header-token")
	if got := TokenFromRequest(req); got != "header-token" {
		t.Fatalf("bearer: got %q", got)
	}

	req = httptest.NewRequest(http.MethodGet, "/v1/documents?access_token=query-token", nil)
	req.AddCookie(&http.Cookie{Name: CookieName, Value: "cookie-token"})
	if got := TokenFromRequest(req); got != "cookie-token" {
		t.Fatalf("cookie: got %q", got)
	}

	req = httptest.NewRequest(http.MethodGet, "/v1/documents?access_token=query-token", nil)
	if got := TokenFromRequest(req); got != "" {
		t.Fatalf("query token must be ignored, got %q", got)
	}
}
