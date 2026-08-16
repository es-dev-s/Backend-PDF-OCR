package auth

import (
	"sync"
	"time"
)

type LoginGate struct {
	mu    sync.Mutex
	fails map[string][]time.Time
}

func NewLoginGate() *LoginGate {
	return &LoginGate{fails: map[string][]time.Time{}}
}

func (l *LoginGate) Allow(key string, max int, window time.Duration) bool {
	now := time.Now()
	l.mu.Lock()
	defer l.mu.Unlock()
	hits := l.fails[key]
	kept := hits[:0]
	for _, at := range hits {
		if now.Sub(at) < window {
			kept = append(kept, at)
		}
	}
	if len(kept) >= max {
		l.fails[key] = kept
		return false
	}
	l.fails[key] = kept
	return true
}

func (l *LoginGate) Fail(key string) {
	l.mu.Lock()
	l.fails[key] = append(l.fails[key], time.Now())
	l.mu.Unlock()
}

func (l *LoginGate) Clear(key string) {
	l.mu.Lock()
	delete(l.fails, key)
	l.mu.Unlock()
}
