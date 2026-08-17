package worker

import (
	"context"
	"errors"
	"io"
	"log/slog"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

func discardLog() *slog.Logger {
	return slog.New(slog.NewTextHandler(io.Discard, nil))
}

func TestPoolStartRacesWithSubmit(t *testing.T) {
	pool := New(4, discardLog())
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	var done atomic.Int64
	var wg sync.WaitGroup
	for i := 0; i < 16; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			_ = pool.TrySubmit(func(context.Context) { done.Add(1) })
			_ = pool.Context()
		}()
	}
	pool.Start(ctx, 4)
	wg.Wait()
	pool.Stop(context.Background())
}

func TestPoolStartIsIdempotent(t *testing.T) {
	pool := New(2, discardLog())
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	pool.Start(ctx, 2)
	first := pool.Context()
	pool.Start(ctx, 2)
	if pool.Context() != first {
		t.Fatal("second Start replaced the running context")
	}
	pool.Stop(context.Background())
}

func TestTrySubmitReportsBusyInsteadOfBlocking(t *testing.T) {
	pool := New(1, discardLog())
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	pool.Start(ctx, 1)
	defer pool.Stop(context.Background())

	release := make(chan struct{})
	defer close(release)
	started := make(chan struct{})
	if err := pool.Submit(ctx, func(context.Context) {
		close(started)
		<-release
	}); err != nil {
		t.Fatal(err)
	}
	<-started

	// Fill the buffer, then confirm the next attempt fails fast.
	for i := 0; i < cap(pool.jobs); i++ {
		if err := pool.TrySubmit(func(context.Context) {}); err != nil {
			t.Fatalf("slot %d: %v", i, err)
		}
	}
	done := make(chan error, 1)
	go func() { done <- pool.TrySubmit(func(context.Context) {}) }()
	select {
	case err := <-done:
		if !errors.Is(err, ErrBusy) {
			t.Fatalf("want ErrBusy, got %v", err)
		}
	case <-time.After(time.Second):
		t.Fatal("TrySubmit blocked on a full queue")
	}
}

func TestPoolStopIsSafeUnderConcurrentSubmit(t *testing.T) {
	pool := New(4, discardLog())
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	pool.Start(ctx, 4)

	var wg sync.WaitGroup
	for i := 0; i < 8; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for j := 0; j < 50; j++ {
				_ = pool.TrySubmit(func(context.Context) {})
			}
		}()
	}
	wg.Add(1)
	go func() {
		defer wg.Done()
		pool.Stop(context.Background())
	}()
	wg.Wait()
	pool.Stop(context.Background())
}
