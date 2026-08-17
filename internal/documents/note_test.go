package documents

import (
	"strings"
	"testing"
)

func TestClampNote(t *testing.T) {
	if got := ClampNote("  hello  "); got != "hello" {
		t.Fatalf("trim: %q", got)
	}
	if got := ClampNote(""); got != "" {
		t.Fatalf("empty: %q", got)
	}
	got := ClampNote(strings.Repeat("a", 600))
	if len([]rune(got)) != 500 {
		t.Fatalf("max runes: %d", len([]rune(got)))
	}
}
