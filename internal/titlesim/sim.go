package titlesim

import (
	"strings"
	"unicode"
)

const (
	Threshold = 0.90
	wordMin   = 0.85
	minRunes  = 8
	shortWord = 4
)

var stop = map[string]struct{}{
	"a": {}, "an": {}, "and": {}, "as": {}, "at": {}, "by": {},
	"for": {}, "from": {}, "in": {}, "into": {}, "of": {}, "on": {},
	"over": {}, "per": {}, "the": {}, "to": {}, "using": {}, "via": {},
	"with": {},
}

// Normalize folds a printed title into comparable tokens. Placeholder
// titles, filenames, and unreadable scans become empty so they never match.
func Normalize(s string) string {
	s = strings.TrimSpace(strings.ToLower(s))
	if s == "" || s == "untitled document" || s == "title not readable (scanned pdf)" {
		return ""
	}
	if strings.HasSuffix(s, ".pdf") {
		return ""
	}
	var b strings.Builder
	b.Grow(len(s))
	space := true
	for _, r := range s {
		if unicode.IsLetter(r) || unicode.IsNumber(r) {
			b.WriteRune(unicode.ToLower(r))
			space = false
			continue
		}
		if !space {
			b.WriteByte(' ')
			space = true
		}
	}
	return strings.TrimSpace(b.String())
}

// Tokens are the content words used for similarity. Stopwords are dropped.
func Tokens(norm string) []string {
	parts := strings.Fields(norm)
	out := make([]string, 0, len(parts))
	for _, p := range parts {
		if _, skip := stop[p]; skip {
			continue
		}
		out = append(out, p)
	}
	return out
}

// Score is 0..1 from how many content words align, using the longer title
// as the denominator so a short phrase cannot match a long unrelated one.
func Score(a, b string) float64 {
	return ScoreNorm(Normalize(a), Normalize(b))
}

func ScoreNorm(left, right string) float64 {
	if left == "" || right == "" {
		return 0
	}
	if left == right {
		return 1
	}
	a := Tokens(left)
	b := Tokens(right)
	if len(a) == 0 || len(b) == 0 {
		return 0
	}
	if (runeLen(left) < minRunes || runeLen(right) < minRunes) && left != right {
		return 0
	}
	matched, _, _ := Align(a, b)
	den := len(a)
	if len(b) > den {
		den = len(b)
	}
	if den == 0 {
		return 0
	}
	return float64(matched) / float64(den)
}

func Match(a, b string) bool {
	return Score(a, b) >= Threshold
}

// Align pairs content words greedily. A word counts as the same when it is
// identical, or (if long enough) when its spelling is at least wordMin.
func Align(a, b []string) (matched int, aHit, bHit []bool) {
	aHit = make([]bool, len(a))
	bHit = make([]bool, len(b))
	for i, aw := range a {
		best := -1
		bestR := 0.0
		for j, bw := range b {
			if bHit[j] {
				continue
			}
			r := wordScore(aw, bw)
			if r >= wordMin && r > bestR {
				bestR = r
				best = j
			}
		}
		if best >= 0 {
			aHit[i] = true
			bHit[best] = true
			matched++
		}
	}
	return matched, aHit, bHit
}

func wordScore(a, b string) float64 {
	if a == b {
		return 1
	}
	if runeLen(a) < shortWord || runeLen(b) < shortWord {
		return 0
	}
	return ratio(a, b)
}

func runeLen(s string) int {
	n := 0
	for range s {
		n++
	}
	return n
}

func ratio(a, b string) float64 {
	if a == b {
		return 1
	}
	if a == "" || b == "" {
		return 0
	}
	ar := []rune(a)
	br := []rune(b)
	d := levenshtein(ar, br)
	max := len(ar)
	if len(br) > max {
		max = len(br)
	}
	if max == 0 {
		return 1
	}
	return 1 - float64(d)/float64(max)
}

func levenshtein(a, b []rune) int {
	if len(a) == 0 {
		return len(b)
	}
	if len(b) == 0 {
		return len(a)
	}
	prev := make([]int, len(b)+1)
	cur := make([]int, len(b)+1)
	for j := 0; j <= len(b); j++ {
		prev[j] = j
	}
	for i := 1; i <= len(a); i++ {
		cur[0] = i
		for j := 1; j <= len(b); j++ {
			cost := 1
			if a[i-1] == b[j-1] {
				cost = 0
			}
			del := prev[j] + 1
			ins := cur[j-1] + 1
			sub := prev[j-1] + cost
			cur[j] = del
			if ins < cur[j] {
				cur[j] = ins
			}
			if sub < cur[j] {
				cur[j] = sub
			}
		}
		prev, cur = cur, prev
	}
	return prev[len(b)]
}
