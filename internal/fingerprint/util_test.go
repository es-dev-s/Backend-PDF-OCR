package fingerprint

import (
	"strings"
	"testing"
)

func TestNormalizeAndSimHash(t *testing.T) {
	base := strings.Repeat("visa application supporting statement for skilled migration ", 12)
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
	other := strings.Repeat("garden cooking recipe ingredients oven temperature ", 12)
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
	if !IsDuplicate(KindText, 99.5, 12, 3) {
		t.Fatal("identical extracted text is a duplicate")
	}
	if IsDuplicate(KindText, 90.6, 12, 12) {
		t.Fatal("loose simhash must not count as a duplicate")
	}
	if IsDuplicate(KindVisual, 84.0, 4, 4) {
		t.Fatal("loose phash must not count as a duplicate")
	}
	if IsDuplicate(KindVisual, 96, 4, 9) {
		t.Fatal("visual match with different page counts must not count")
	}
}

func TestBands(t *testing.T) {
	h := uint64(0x0123456789ABCDEF)
	b := Bands(h)
	if b[0] != 0xCDEF || b[1] != 0x89AB || b[2] != 0x4567 || b[3] != 0x0123 {
		t.Fatalf("bands=%v", b)
	}
}
