package documents

import (
	"strings"
	"testing"

	"github.com/google/uuid"
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
	if got := ClampNote("one\ntwo"); got != "one two" {
		t.Fatalf("flatten: %q", got)
	}
}

func TestJoinSourceNotes(t *testing.T) {
	got := joinSourceNotes([]Source{
		{Note: " first "},
		{Note: "first"},
		{Note: "second"},
		{Note: ""},
	})
	if got != "first\nsecond" {
		t.Fatalf("join: %q", got)
	}
}

func TestNoteForFile(t *testing.T) {
	if got := noteForFile([]string{"a", ""}, 1, "fallback"); got != "" {
		t.Fatalf("empty slot must not inherit fallback: %q", got)
	}
	if got := noteForFile(nil, 0, "fallback"); got != "fallback" {
		t.Fatalf("legacy shared note: %q", got)
	}
	if got := noteForFile([]string{"keep"}, 0, "fallback"); got != "keep" {
		t.Fatalf("per-file: %q", got)
	}
}

func TestNoteForFileKeepsPerDuplicateReasons(t *testing.T) {
	notes := []string{"", "keep scanned copy", "client asked for both"}
	if got := noteForFile(notes, 0, ""); got != "" {
		t.Fatalf("unique file must not inherit a note: %q", got)
	}
	if got := noteForFile(notes, 1, ""); got != "keep scanned copy" {
		t.Fatalf("first duplicate: %q", got)
	}
	if got := noteForFile(notes, 2, ""); got != "client asked for both" {
		t.Fatalf("second duplicate: %q", got)
	}
}

func TestMergeReviewNote(t *testing.T) {
	if got := mergeReviewNote("old", ""); got != "old" {
		t.Fatalf("keep existing: %q", got)
	}
	if got := mergeReviewNote("", "new"); got != "new" {
		t.Fatalf("set new: %q", got)
	}
	if got := mergeReviewNote("old", "new"); got != "old\nnew" {
		t.Fatalf("append: %q", got)
	}
	if got := mergeReviewNote("same", "same"); got != "same" {
		t.Fatalf("dedupe: %q", got)
	}
}

func TestMergeNoteLogKeepsHistory(t *testing.T) {
	got := mergeNoteLog("first", "second\nfirst", "third")
	if got != "first\nsecond\nthird" {
		t.Fatalf("history: %q", got)
	}
}

func TestFoldClusterNotesKeepsOwnReason(t *testing.T) {
	incoming := uuid.New()
	matchID := uuid.New()
	src := &Source{
		ID:         incoming,
		Uniqueness: Duplicate,
		Note:       "keep this scan",
		Duplicates: []DuplicateMatch{{
			ID:   matchID,
			Note: "earlier copy",
		}},
	}
	foldClusterNotes(map[uuid.UUID]*Source{incoming: src})
	if src.Note != "keep this scan" {
		t.Fatalf("own note overwritten: %q", src.Note)
	}
	if src.Duplicates[0].Note != "earlier copy" {
		t.Fatalf("match note overwritten: %q", src.Duplicates[0].Note)
	}
	if src.NoteLog != "keep this scan\nearlier copy" {
		t.Fatalf("note log: %q", src.NoteLog)
	}
}
