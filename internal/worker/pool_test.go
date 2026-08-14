package worker

import (
	"context"
	"io"
	"log/slog"
	"sync"
	"testing"
	"time"
)

func TestPoolQueuesJobsWithoutDropping(t *testing.T) {
	log := slog.New(slog.NewTextHandler(io.Discard, nil))
	pool := New(1, log)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	pool.Start(ctx, 1)
	defer pool.Stop(context.Background())

	gate := make(chan struct{})
	var mu sync.Mutex
	got := make([]int, 0, 8)
	first := make(chan struct{})

	if err := pool.Submit(ctx, func(context.Context) {
		close(first)
		<-gate
		mu.Lock()
		got = append(got, 0)
		mu.Unlock()
	}); err != nil {
		t.Fatal(err)
	}
	<-first
	for i := 1; i <= 5; i++ {
		n := i
		if err := pool.Submit(ctx, func(context.Context) {
			mu.Lock()
			got = append(got, n)
			mu.Unlock()
		}); err != nil {
			t.Fatalf("queued job %d: %v", n, err)
		}
	}
	close(gate)

	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		mu.Lock()
		n := len(got)
		mu.Unlock()
		if n == 6 {
			break
		}
		time.Sleep(10 * time.Millisecond)
	}
	mu.Lock()
	defer mu.Unlock()
	if len(got) != 6 {
		t.Fatalf("jobs dropped under load: got %v", got)
	}
}
