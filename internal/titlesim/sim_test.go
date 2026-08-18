package titlesim

import (
	"strings"
	"testing"
)

func TestNormalize(t *testing.T) {
	cases := []struct {
		in, want string
	}{
		{"The Journey of Man", "the journey of man"},
		{"  The, Journey! of  Man. ", "the journey of man"},
		{"notes.pdf", ""},
		{"Untitled document", ""},
		{"Title not readable (scanned PDF)", ""},
		{"", ""},
	}
	for _, c := range cases {
		if got := Normalize(c.in); got != c.want {
			t.Errorf("Normalize(%q)=%q want %q", c.in, got, c.want)
		}
	}
}

func TestJourneyTypoIsAboveThreshold(t *testing.T) {
	score := Score("The journey of man", "The jorney of man")
	if score < Threshold {
		t.Fatalf("typo score=%.4f want >= %.2f", score, Threshold)
	}
	if !Match("The journey of man", "The jorney of man") {
		t.Fatal("expected match")
	}
}

func TestIdenticalTitlesScoreOne(t *testing.T) {
	if Score("River Cleaning Machine", "river cleaning machine") != 1 {
		t.Fatal("identical folded titles must score 1")
	}
}

func TestUnrelatedTitlesStayBelow(t *testing.T) {
	score := Score("Egyptian Journal of Chemistry", "River Cleaning Machine")
	if score >= Threshold {
		t.Fatalf("unrelated score=%.4f", score)
	}
}

func TestDifferentPapersDoNotMatch(t *testing.T) {
	a := "Kinetic Study of Esterification of Acetic Acid with nbutanol and isobutanol Catalyzed by Ion Exchange Resin"
	b := "Investigation of Packing Effect on Mass Transfer Coefficient in a Single Drop Liquid Extraction Column"
	score := Score(a, b)
	if score >= Threshold {
		t.Fatalf("distinct papers scored %.4f", score)
	}
}

func TestWordCoverageRejectsShortOverlap(t *testing.T) {
	long := "Kinetic Study of Esterification of Acetic Acid with nbutanol and isobutanol Catalyzed by Ion Exchange Resin"
	if Score("Study of Acid", long) >= Threshold {
		t.Fatal("a few shared words must not match a long title")
	}
}

func TestOneTypoInLongTitleStillMatches(t *testing.T) {
	a := "Kinetic Study of Esterification of Acetic Acid with nbutanol and isobutanol Catalyzed by Ion Exchange Resin"
	b := strings.Replace(a, "Esterification", "Esterificaton", 1)
	if Score(a, b) < Threshold {
		t.Fatalf("single long-word typo scored %.4f", Score(a, b))
	}
}

func TestShortTitlesOnlyMatchWhenExact(t *testing.T) {
	if Score("Report", "Reporx") != 0 {
		t.Fatal("short fuzzy matches must be rejected")
	}
	if Score("Report I", "Report I") != 1 {
		t.Fatal("short exact match must score 1")
	}
}

func TestEmptyNeverMatches(t *testing.T) {
	if Score("", "anything at all") != 0 {
		t.Fatal("empty must not match")
	}
	if Score("notes.pdf", "notes.pdf") != 0 {
		t.Fatal("filenames must not match")
	}
}

func TestLevenshteinKnownPairs(t *testing.T) {
	if d := levenshtein([]rune("kitten"), []rune("sitting")); d != 3 {
		t.Fatalf("kitten/sitting=%d", d)
	}
	if d := levenshtein([]rune("jorney"), []rune("journey")); d != 1 {
		t.Fatalf("jorney/journey=%d", d)
	}
}
