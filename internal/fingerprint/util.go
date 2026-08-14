package fingerprint

import (
	"bytes"
	"path/filepath"
	"strings"
	"unicode"
)

const (
	KindExact  = "exact"
	KindText   = "text"
	KindVisual = "visual"

	MinTextRunes     = 800
	MinTokens        = 96
	MinRunesPerPage  = 80
	SimHashMaxDist   = 1
	PHashMaxDist     = 1
	MaxTextPages     = 8
	MaxTextBytes     = 128 << 10
	MaxVisualPages   = 1
	MaxImagePixels   = 2_000_000
	LSHBands         = 4
	MinExactScore    = 99.9
	MinTextNearScore = 98.0
	MinVisualScore   = 98.0
)

func Sniff(head []byte, filename string) string {
	if len(head) >= 5 && bytes.HasPrefix(head, []byte("%PDF-")) {
		return "pdf"
	}
	if len(head) >= 3 && bytes.Equal(head[:3], []byte{0xff, 0xd8, 0xff}) {
		return "image"
	}
	if len(head) >= 8 && bytes.Equal(head[:8], []byte{0x89, 'P', 'N', 'G', 0x0d, 0x0a, 0x1a, 0x0a}) {
		return "image"
	}
	if len(head) >= 6 && (bytes.HasPrefix(head, []byte("GIF87a")) || bytes.HasPrefix(head, []byte("GIF89a"))) {
		return "image"
	}
	if len(head) >= 12 && bytes.Equal(head[4:8], []byte("ftyp")) {
		return "image"
	}
	if len(head) >= 4 && (bytes.Equal(head[:4], []byte("II*\x00")) || bytes.Equal(head[:4], []byte("MM\x00*"))) {
		return "image"
	}
	if len(head) >= 4 && bytes.Equal(head[:4], []byte{0x52, 0x49, 0x46, 0x46}) {
		return "image"
	}
	ext := strings.ToLower(filepath.Ext(filename))
	switch ext {
	case ".pdf":
		return "pdf"
	case ".png", ".jpg", ".jpeg", ".webp", ".gif", ".tif", ".tiff":
		return "image"
	}
	return ""
}

func Normalize(s string) string {
	var b strings.Builder
	b.Grow(len(s))
	prevSpace := true
	for _, r := range s {
		switch {
		case unicode.IsLetter(r) || unicode.IsDigit(r):
			b.WriteRune(unicode.ToLower(r))
			prevSpace = false
		case unicode.IsSpace(r) || unicode.IsPunct(r) || unicode.IsSymbol(r):
			if !prevSpace {
				b.WriteByte(' ')
				prevSpace = true
			}
		}
	}
	return strings.TrimSpace(b.String())
}

func Tokens(norm string) []string {
	if norm == "" {
		return nil
	}
	return strings.Fields(norm)
}

func Hamming(a, b uint64) int {
	x := a ^ b
	n := 0
	for x != 0 {
		x &= x - 1
		n++
	}
	return n
}

func Bands(h uint64) [LSHBands]uint16 {
	return [LSHBands]uint16{
		uint16(h),
		uint16(h >> 16),
		uint16(h >> 32),
		uint16(h >> 48),
	}
}

func ScoreFromDistance(dist, bits int) float64 {
	if bits <= 0 {
		return 0
	}
	if dist < 0 {
		dist = 0
	}
	if dist > bits {
		return 0
	}
	return 100.0 * (1.0 - float64(dist)/float64(bits))
}

func PagesCompatible(a, b int) bool {
	if a <= 0 || b <= 0 {
		return false
	}
	d := a - b
	if d < 0 {
		d = -d
	}
	if d == 0 {
		return true
	}
	return d == 1 && a >= 8 && b >= 8
}

func IsDuplicate(kind string, score float64, pagesA, pagesB int) bool {
	switch kind {
	case KindExact:
		return score >= MinExactScore
	case KindText:
		return score >= MinTextNearScore && PagesCompatible(pagesA, pagesB)
	case KindVisual:
		return score >= MinVisualScore && PagesCompatible(pagesA, pagesB)
	default:
		return false
	}
}
