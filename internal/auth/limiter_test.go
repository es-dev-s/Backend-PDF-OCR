package auth

import (
	"fmt"
	"sync"
	"testing"
	"time"
)

func TestLoginGateBlocksAfterMaxFailures(t *testing.T) {
	gate := NewLoginGate()
	const max = 3
	window := time.Minute
	for i := 0; i < max; i++ {
		if !gate.Allow("1.2.3.4", max, window) {
			t.Fatalf("blocked early at attempt %d", i)
		}
		gate.Fail("1.2.3.4")
	}
	if gate.Allow("1.2.3.4", max, window) {
		t.Fatal("gate did not block after the limit")
	}
	gate.Clear("1.2.3.4")
	if !gate.Allow("1.2.3.4", max, window) {
		t.Fatal("gate stayed closed after a successful login")
	}
}

func TestLoginGateForgetsExpiredKeys(t *testing.T) {
	gate := NewLoginGate()
	window := 10 * time.Millisecond
	for i := 0; i < 200; i++ {
		gate.Fail(fmt.Sprintf("ip-%d", i))
	}
	time.Sleep(2 * window)
	// Any call sweeps the table once the window has elapsed.
	gate.Allow("fresh", 3, window)

	gate.mu.Lock()
	n := len(gate.fails)
	seen := len(gate.lastSeen)
	gate.mu.Unlock()
	if n != 0 || seen != 0 {
		t.Fatalf("stale keys retained: fails=%d lastSeen=%d", n, seen)
	}
}

func TestLoginGateIsConcurrencySafe(t *testing.T) {
	gate := NewLoginGate()
	var wg sync.WaitGroup
	for i := 0; i < 16; i++ {
		wg.Add(1)
		go func(n int) {
			defer wg.Done()
			key := fmt.Sprintf("ip-%d", n%4)
			for j := 0; j < 200; j++ {
				gate.Allow(key, 5, time.Second)
				gate.Fail(key)
				if j%10 == 0 {
					gate.Clear(key)
				}
			}
		}(i)
	}
	wg.Wait()
}
