package worker

import (
	"context"
	"errors"
	"log/slog"
	"sync"
	"time"
)

// ErrBusy is returned by TrySubmit when the queue has no free slot. Callers
// that can retry later (the recovery sweeper) use it to back off instead of
// blocking on a saturated pool.
var ErrBusy = errors.New("worker pool busy")

type Job func(ctx context.Context)

type Pool struct {
	log  *slog.Logger
	jobs chan Job
	wg   sync.WaitGroup
	once sync.Once

	// ctx is published by Start and read by every Submit/Context caller, so it
	// has to be guarded rather than written in place.
	mu     sync.RWMutex
	ctx    context.Context
	cancel context.CancelFunc
}

func New(n int, log *slog.Logger) *Pool {
	if n < 1 {
		n = 1
	}
	return &Pool{
		log:  log,
		jobs: make(chan Job, 512),
	}
}

func (p *Pool) Start(parent context.Context, n int) {
	if n < 1 {
		n = 1
	}
	ctx, cancel := context.WithCancel(context.WithoutCancel(parent))
	p.mu.Lock()
	if p.ctx != nil {
		p.mu.Unlock()
		cancel()
		return
	}
	p.ctx, p.cancel = ctx, cancel
	p.mu.Unlock()
	for i := 0; i < n; i++ {
		p.wg.Add(1)
		go p.loop(ctx)
	}
}

func (p *Pool) Context() context.Context {
	if p == nil {
		return context.Background()
	}
	p.mu.RLock()
	ctx := p.ctx
	p.mu.RUnlock()
	if ctx == nil {
		return context.Background()
	}
	return ctx
}

func (p *Pool) running() context.Context {
	if p == nil {
		return nil
	}
	p.mu.RLock()
	defer p.mu.RUnlock()
	return p.ctx
}

func (p *Pool) loop(root context.Context) {
	defer p.wg.Done()
	for {
		select {
		case <-root.Done():
			return
		case job := <-p.jobs:
			p.run(root, job)
		}
	}
}

func (p *Pool) run(root context.Context, job Job) {
	defer func() {
		if rec := recover(); rec != nil {
			p.log.Error("worker panic", "recover", rec)
		}
	}()
	ctx, cancel := context.WithTimeout(root, 4*time.Minute)
	defer cancel()
	job(ctx)
}

func (p *Pool) Submit(ctx context.Context, job Job) error {
	root := p.running()
	if root == nil {
		return context.Canceled
	}
	if ctx == nil {
		ctx = root
	}
	select {
	case <-root.Done():
		return root.Err()
	case <-ctx.Done():
		return ctx.Err()
	case p.jobs <- job:
		return nil
	}
}

// TrySubmit queues a job only when the pool has spare capacity.
func (p *Pool) TrySubmit(job Job) error {
	root := p.running()
	if root == nil {
		return context.Canceled
	}
	select {
	case <-root.Done():
		return root.Err()
	case p.jobs <- job:
		return nil
	default:
		return ErrBusy
	}
}

func (p *Pool) Stop(ctx context.Context) {
	p.once.Do(func() {
		p.mu.RLock()
		cancel := p.cancel
		p.mu.RUnlock()
		if cancel != nil {
			cancel()
		}
	})
	done := make(chan struct{})
	go func() {
		p.wg.Wait()
		close(done)
	}()
	select {
	case <-done:
	case <-ctx.Done():
	}
}
