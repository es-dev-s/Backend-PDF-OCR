package documents

import (
	"slices"
	"testing"

	"ocr-backend/internal/fingerprint"
)

func TestFingerprintLockKeysOrderAndCoverage(t *testing.T) {
	fp := fingerprint.Result{
		SHA256:      "abc",
		TextNormSHA: "norm",
		SimHash:     0x1111222233334444,
		HasText:     true,
	}
	keys := fingerprintLockKeys(fp)
	if !slices.IsSorted(keys) {
		t.Fatalf("lock keys must be sorted to avoid deadlocks: %v", keys)
	}
	want := []string{
		"lsh:simhash:0:17476",
		"lsh:simhash:1:13107",
		"lsh:simhash:2:8738",
		"lsh:simhash:3:4369",
		"sha:abc",
		"txt:norm",
	}
	if !slices.Equal(keys, want) {
		t.Fatalf("keys=%v want=%v", keys, want)
	}

	near := fp
	near.SimHash ^= 1
	other := fingerprintLockKeys(near)
	shared := 0
	set := make(map[string]struct{}, len(keys))
	for _, k := range keys {
		set[k] = struct{}{}
	}
	for _, k := range other {
		if _, ok := set[k]; ok {
			shared++
		}
	}
	if shared < 4 {
		t.Fatalf("near-duplicate hashes must share sha/txt and most LSH locks, shared=%d", shared)
	}
}

func TestFingerprintLockKeysVisual(t *testing.T) {
	keys := fingerprintLockKeys(fingerprint.Result{
		SHA256:    "img",
		PHash:     99,
		HasVisual: true,
	})
	if len(keys) != 5 {
		t.Fatalf("expected sha + 4 phash bands, got %v", keys)
	}
	if keys[len(keys)-1] != "sha:img" && keys[0] != "sha:img" {
		if !slices.Contains(keys, "sha:img") {
			t.Fatalf("missing sha lock: %v", keys)
		}
	}
}
