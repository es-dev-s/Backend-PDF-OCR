package titlesim

import (
	"math"
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
		{"Design, and Finite Element Analysis of a Peanut.pdf", "design and finite element analysis of a peanut"},
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

func TestPeanutTitlesAreCompared(t *testing.T) {
	a := "Design, and Finite Element Analysis of a Peanut"
	b := "Design, and Finite Element Analysis of a Peanut System for Soils"
	score := Score(a, b)
	// design, finite, element, analysis, peanut vs those plus system, soils
	// Dice 2*5/(5+7) = 0.833
	if score < 0.82 || score > 0.85 {
		t.Fatalf("peanut score=%.4f want ~0.833", score)
	}
	if !Match(a, b) {
		t.Fatal("near-identical peanut titles must be stored as similar")
	}
	if got := int(math.Round(score * 100)); got != 83 {
		t.Fatalf("percent=%d want 83", got)
	}
}

func TestHometownSubsetIsCompared(t *testing.T) {
	a := "The home town of nepali"
	b := "The simulation of purely nature and home town of nepal"
	score := Score(a, b)
	// home, town, nepali≈nepal vs six content words → Dice 2*3/9 = 0.667
	if score < 0.65 || score > 0.68 {
		t.Fatalf("subset score=%.4f want ~0.667", score)
	}
	if score >= Threshold {
		t.Fatal("67% hometown overlap must stay below the 70% similar cap")
	}
	if got := int(math.Round(score * 100)); got != 67 {
		t.Fatalf("percent=%d want 67", got)
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
