package httpapi

import (
	"net/http"
	"net/http/httptest"
	"testing"
	"time"
)

func TestParseStatsRangeDefault(t *testing.T) {
	req := httptest.NewRequest(http.MethodGet, "/v1/stats/uploads", nil)
	from, to, err := parseStatsRange(req)
	if err != nil {
		t.Fatal(err)
	}
	if to.Sub(from) != 30*24*time.Hour {
		t.Fatalf("default window=%s want 30d", to.Sub(from))
	}
	if from.Location() != time.UTC || to.Location() != time.UTC {
		t.Fatal("range must be UTC")
	}
	if from.Hour() != 0 || to.Hour() != 0 {
		t.Fatal("range must be midnight")
	}
}

func TestParseStatsRangeInclusiveTo(t *testing.T) {
	req := httptest.NewRequest(http.MethodGet, "/v1/stats/uploads?from=2026-08-01&to=2026-08-17", nil)
	from, to, err := parseStatsRange(req)
	if err != nil {
		t.Fatal(err)
	}
	if from.Format("2006-01-02") != "2026-08-01" {
		t.Fatalf("from=%s", from.Format("2006-01-02"))
	}
	if to.Format("2006-01-02") != "2026-08-18" {
		t.Fatalf("exclusive to=%s", to.Format("2006-01-02"))
	}
}

func TestParseStatsRangeRejects(t *testing.T) {
	cases := []string{
		"/v1/stats/uploads?from=nope",
		"/v1/stats/uploads?to=2026-13-01",
		"/v1/stats/uploads?from=2026-08-17&to=2026-08-01",
		"/v1/stats/uploads?from=2024-01-01&to=2026-08-01",
	}
	for _, path := range cases {
		req := httptest.NewRequest(http.MethodGet, path, nil)
		if _, _, err := parseStatsRange(req); err == nil {
			t.Fatalf("expected error for %s", path)
		}
	}
}
