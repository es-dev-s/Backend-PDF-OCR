package worker

import (
	"context"
	"log/slog"
	"sync"
	"time"
)

type Job func(ctx context.Context)

type Pool struct {
	log    *slog.Logger
	jobs   chan Job
	wg     sync.WaitGroup
	once   sync.Once
	cancel context.CancelFunc
	ctx    context.Context
}

func New(n int, log *slog.Logger) *Pool {
	if n < 1 {
		n = 1
	}
	return &Pool{
		log:  log,
		jobs: make(chan Job, n*32),
	}
}

func (p *Pool) Start(parent context.Context, n int) {
	if n < 1 {
		n = 1
	}
	p.ctx, p.cancel = context.WithCancel(context.WithoutCancel(parent))
	for i := 0; i < n; i++ {
		p.wg.Add(1)
		go p.loop()
	}
}

func (p *Pool) loop() {
	defer p.wg.Done()
	for {
		select {
		case <-p.ctx.Done():
			return
		case job := <-p.jobs:
			func(job Job) {
				defer func() {
					if rec := recover(); rec != nil {
						p.log.Error("worker panic", "recover", rec)
					}
				}()
				ctx, cancel := context.WithTimeout(p.ctx, 2*time.Minute)
				defer cancel()
				job(ctx)
			}(job)
		}
	}
}

func (p *Pool) Submit(ctx context.Context, job Job) error {
	if p.ctx == nil {
		return context.Canceled
	}
	select {
	case <-p.ctx.Done():
		return p.ctx.Err()
	case <-ctx.Done():
		return ctx.Err()
	case p.jobs <- job:
		return nil
	}
}

func (p *Pool) Stop(ctx context.Context) {
	p.once.Do(func() {
		if p.cancel != nil {
			p.cancel()
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
