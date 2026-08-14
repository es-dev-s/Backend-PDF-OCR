package fingerprint

import (
	"hash/fnv"
)

func SimHash64(tokens []string) uint64 {
	if len(tokens) < MinTokens {
		return 0
	}
	var vec [64]int
	for _, tok := range tokens {
		if tok == "" {
			continue
		}
		h := fnv64(tok)
		for i := 0; i < 64; i++ {
			if h&(1<<uint(i)) != 0 {
				vec[i]++
			} else {
				vec[i]--
			}
		}
	}
	var out uint64
	for i := 0; i < 64; i++ {
		if vec[i] >= 0 {
			out |= 1 << uint(i)
		}
	}
	return out
}

func fnv64(s string) uint64 {
	h := fnv.New64a()
	_, _ = h.Write([]byte(s))
	return h.Sum64()
}
