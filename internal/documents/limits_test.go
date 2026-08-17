package documents

import (
	"context"
	"testing"
	"time"
)

func newLimitedService(heavy, title int) *Service {
	return NewService(nil, nil, nil, nil, nil, nil, nil, 4, 1<<20,
		Limits{Heavy: heavy, Title: title})
}

// Title extraction talks to a remote engine and can take minutes. Fingerprinting
// is what decides duplicate status, so it must never wait behind it.
func TestEngineWorkDoesNotBlockFingerprinting(t *testing.T) {
	s := newLimitedService(1, 1)
	ctx := context.Background()

	if !s.acquireTitle(ctx) {
		t.Fatal("could not take the title slot")
	}
	defer s.releaseTitle()

	done := make(chan bool, 1)
	go func() {
		gotHeavy := s.acquireHeavy(ctx)
		if gotHeavy {
			s.releaseHeavy()
		}
		done <- gotHeavy
	}()

	select {
	case ok := <-done:
		if !ok {
			t.Fatal("fingerprinting was refused")
		}
	case <-time.After(2 * time.Second):
		t.Fatal("fingerprinting blocked behind an engine call")
	}
}

func TestHeavyWorkRunsInParallelUpToTheLimit(t *testing.T) {
	s := newLimitedService(3, 1)
	ctx := context.Background()

	for i := 0; i < 3; i++ {
		if !s.acquireHeavy(ctx) {
			t.Fatalf("slot %d refused", i)
		}
	}

	// The fourth must wait, which is the memory guard doing its job.
	tight, cancel := context.WithTimeout(ctx, 150*time.Millisecond)
	defer cancel()
	if s.acquireHeavy(tight) {
		t.Fatal("heavy limit was not enforced")
	}
	for i := 0; i < 3; i++ {
		s.releaseHeavy()
	}
}

func TestLimitsFallBackToOne(t *testing.T) {
	s := newLimitedService(0, -4)
	if cap(s.heavy) != 1 || cap(s.titles) != 1 {
		t.Fatalf("heavy=%d titles=%d, want 1 and 1", cap(s.heavy), cap(s.titles))
	}
}

func TestAcquireHonoursCancellation(t *testing.T) {
	s := newLimitedService(1, 1)
	ctx, cancel := context.WithCancel(context.Background())
	// Both slots must be occupied first, otherwise the acquire can succeed
	// immediately and never consult the context.
	if !s.acquireHeavy(ctx) {
		t.Fatal("first heavy acquire failed")
	}
	defer s.releaseHeavy()
	if !s.acquireTitle(ctx) {
		t.Fatal("first title acquire failed")
	}
	defer s.releaseTitle()

	cancel()
	if s.acquireHeavy(ctx) {
		t.Fatal("heavy acquire ignored a cancelled context")
	}
	if s.acquireTitle(ctx) {
		t.Fatal("title acquire ignored a cancelled context")
	}
}

func TestTrimMemoryIsRateLimited(t *testing.T) {
	s := newLimitedService(1, 1)
	start := time.Now()
	for i := 0; i < 200; i++ {
		s.trimMemory()
	}
	// 200 stop-the-world collections would take far longer than this.
	if elapsed := time.Since(start); elapsed > 2*time.Second {
		t.Fatalf("trimMemory was not throttled: %s", elapsed)
	}
}
