package auth

import (
	"sync"
	"time"
)

// maxGateKeys bounds the throttle table. Without it a spray of unique client
// IPs would grow the map forever, since only the entries that log in again are
// ever revisited.
const maxGateKeys = 50_000

type LoginGate struct {
	mu       sync.Mutex
	fails    map[string][]time.Time
	sweptAt  time.Time
	lastSeen map[string]time.Time
}

func NewLoginGate() *LoginGate {
	return &LoginGate{
		fails:    map[string][]time.Time{},
		lastSeen: map[string]time.Time{},
	}
}

func (l *LoginGate) Allow(key string, max int, window time.Duration) bool {
	now := time.Now()
	l.mu.Lock()
	defer l.mu.Unlock()
	l.sweepLocked(now, window)
	hits := l.fails[key]
	kept := hits[:0]
	for _, at := range hits {
		if now.Sub(at) < window {
			kept = append(kept, at)
		}
	}
	l.store(key, kept, now)
	return len(kept) < max
}

func (l *LoginGate) Fail(key string) {
	now := time.Now()
	l.mu.Lock()
	l.store(key, append(l.fails[key], now), now)
	l.mu.Unlock()
}

func (l *LoginGate) Clear(key string) {
	l.mu.Lock()
	delete(l.fails, key)
	delete(l.lastSeen, key)
	l.mu.Unlock()
}

func (l *LoginGate) store(key string, hits []time.Time, now time.Time) {
	if len(hits) == 0 {
		delete(l.fails, key)
		delete(l.lastSeen, key)
		return
	}
	l.fails[key] = hits
	l.lastSeen[key] = now
}

// sweepLocked drops keys whose failures have all aged out. It runs at most once
// per window, or immediately once the table grows past its bound.
func (l *LoginGate) sweepLocked(now time.Time, window time.Duration) {
	if len(l.fails) < maxGateKeys && now.Sub(l.sweptAt) < window {
		return
	}
	l.sweptAt = now
	for key, seen := range l.lastSeen {
		if now.Sub(seen) >= window {
			delete(l.fails, key)
			delete(l.lastSeen, key)
		}
	}
}
