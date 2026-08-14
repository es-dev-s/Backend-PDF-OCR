package fingerprint

import (
	"strings"
	"testing"
)

func TestNormalizeAndSimHash(t *testing.T) {
	base := strings.Repeat("visa application supporting statement for skilled migration ", 16)
	a := Normalize("Hello,  WORLD!!\nThis is a test document about visas. " + base)
	b := Normalize("hello world this is a test document about visas " + base)
	if a != b {
		t.Fatalf("normalize mismatch:\n%s\n%s", a, b)
	}
	ha := SimHash64(Tokens(a))
	hb := SimHash64(Tokens(b))
	if ha == 0 || ha != hb {
		t.Fatalf("simhash mismatch %d %d", ha, hb)
	}
	if Hamming(ha, hb) != 0 {
		t.Fatal("expected identical hashes")
	}
	other := strings.Repeat("garden cooking recipe ingredients oven temperature ", 16)
	c := SimHash64(Tokens(Normalize("completely different content about cooking recipes and gardens " + other)))
	if c == 0 {
		t.Fatal("expected a simhash for long unrelated text")
	}
	if Hamming(ha, c) <= SimHashMaxDist {
		t.Fatalf("unrelated text too close: dist=%d", Hamming(ha, c))
	}
}

func TestIsDuplicateThresholds(t *testing.T) {
	if !IsDuplicate(KindExact, 100, 1, 99) {
		t.Fatal("exact bytes must match regardless of page count")
	}
	if IsDuplicate(KindText, 99.5, 12, 3) {
		t.Fatal("identical extracted text with different page counts must not count")
	}
	if !IsDuplicate(KindText, 99.5, 12, 12) {
		t.Fatal("identical extracted text with matching pages is a duplicate")
	}
	if !IsDuplicate(KindText, 98.4375, 8, 8) {
		t.Fatal("simhash distance 1 with matching pages is a duplicate")
	}
	if IsDuplicate(KindText, ScoreFromDistance(2, 64), 12, 12) {
		t.Fatal("simhash distance 2 must not count as a duplicate")
	}
	if IsDuplicate(KindVisual, 84.0, 4, 4) {
		t.Fatal("loose phash must not count as a duplicate")
	}
	if IsDuplicate(KindVisual, 96, 4, 4) {
		t.Fatal("visual distance above 1 must not count")
	}
	if IsDuplicate(KindVisual, 98.5, 4, 9) {
		t.Fatal("visual match with different page counts must not count")
	}
	if !IsDuplicate(KindVisual, 98.5, 1, 1) {
		t.Fatal("visual distance 1 with matching pages is a duplicate")
	}
}

func TestTextIsSubstantial(t *testing.T) {
	if textIsSubstantial(400, 64, 12) {
		t.Fatal("header-sized text on a long PDF must not fingerprint")
	}
	if !textIsSubstantial(800, 96, 1) {
		t.Fatal("substantial single-page text should fingerprint")
	}
	if !textIsSubstantial(12*80, 96, 12) {
		t.Fatal("enough text per page should fingerprint")
	}
}

func TestBands(t *testing.T) {
	h := uint64(0x0123456789ABCDEF)
	b := Bands(h)
	if b[0] != 0xCDEF || b[1] != 0x89AB || b[2] != 0x4567 || b[3] != 0x0123 {
		t.Fatalf("bands=%v", b)
	}
}

func TestMissingHashIsStableAndUnique(t *testing.T) {
	a := Missing("source-a")
	again := Missing("source-a")
	b := Missing("source-b")
	if a.SHA256 == "" || a.Kind != "missing" {
		t.Fatalf("missing fingerprint incomplete: %+v", a)
	}
	if a.SHA256 != again.SHA256 {
		t.Fatal("missing hash must be stable for the same source")
	}
	if a.SHA256 == b.SHA256 {
		t.Fatal("missing hashes must not collide across sources")
	}
}

func TestCapText(t *testing.T) {
	if got := capText("abc"); got != "abc" {
		t.Fatalf("short text changed: %q", got)
	}
	long := strings.Repeat("x", MaxTextBytes+80)
	got := capText(long)
	if len(got) != MaxTextBytes {
		t.Fatalf("len=%d want %d", len(got), MaxTextBytes)
	}
}
