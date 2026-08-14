package postgres

import (
	"context"
	"embed"
	"fmt"
	"io/fs"
	"log/slog"
	"net"
	"sort"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"

	"ocr-backend/internal/retry"
)

//go:embed sql/*.sql
var sqlFS embed.FS

type Pool struct {
	mu      sync.RWMutex
	pool    *pgxpool.Pool
	url     string
	log     *slog.Logger
	ready   atomic.Bool
	lastErr atomic.Value // error
	maxConn int32
}

func New(url string, log *slog.Logger, maxConns int32) *Pool {
	if maxConns < 4 {
		maxConns = 8
	}
	if maxConns > 32 {
		maxConns = 32
	}
	p := &Pool{url: url, log: log, maxConn: maxConns}
	p.lastErr.Store(fmt.Errorf("postgres not connected"))
	return p
}

func (p *Pool) Run(ctx context.Context) {
	if strings.TrimSpace(p.url) == "" {
		p.set(nil, fmt.Errorf("DATABASE_URL is empty"))
		p.log.Error("postgres disabled: DATABASE_URL is empty")
		return
	}
	backoff := time.Second
	for {
		if ctx.Err() != nil {
			return
		}
		if err := p.connect(ctx); err != nil {
			p.set(nil, err)
			p.log.Warn("postgres connect failed", "err", err, "retry_in", backoff.String())
			select {
			case <-ctx.Done():
				return
			case <-time.After(retry.Jitter(backoff, 20*time.Second)):
			}
			if backoff < 15*time.Second {
				backoff *= 2
			}
			continue
		}
		backoff = time.Second
		p.log.Info("postgres connected")
		p.watch(ctx)
	}
}

func (p *Pool) connect(ctx context.Context) error {
	cfg, err := pgxpool.ParseConfig(p.url)
	if err != nil {
		return fmt.Errorf("parse DATABASE_URL: %w", err)
	}
	cfg.MaxConns = p.maxConn
	cfg.MinConns = 1
	if p.maxConn >= 8 {
		cfg.MinConns = 2
	}
	cfg.MaxConnLifetime = 10 * time.Minute
	cfg.MaxConnIdleTime = 2 * time.Minute
	cfg.HealthCheckPeriod = 30 * time.Second
	cfg.ConnConfig.ConnectTimeout = 8 * time.Second
	if cfg.ConnConfig.RuntimeParams == nil {
		cfg.ConnConfig.RuntimeParams = map[string]string{}
	}
	cfg.ConnConfig.RuntimeParams["application_name"] = "ocr-backend"
	cfg.ConnConfig.RuntimeParams["statement_timeout"] = "60000"
	cfg.ConnConfig.RuntimeParams["idle_in_transaction_session_timeout"] = "60000"
	cfg.ConnConfig.DialFunc = func(ctx context.Context, network, addr string) (net.Conn, error) {
		d := net.Dialer{Timeout: 8 * time.Second, KeepAlive: 30 * time.Second}
		return d.DialContext(ctx, network, addr)
	}

	dialCtx, cancel := context.WithTimeout(ctx, 10*time.Second)
	defer cancel()
	pool, err := pgxpool.NewWithConfig(dialCtx, cfg)
	if err != nil {
		return err
	}
	pingCtx, pingCancel := context.WithTimeout(ctx, 8*time.Second)
	defer pingCancel()
	if err := pool.Ping(pingCtx); err != nil {
		pool.Close()
		return err
	}
	migCtx, migCancel := context.WithTimeout(ctx, 30*time.Second)
	defer migCancel()
	if err := migrate(migCtx, pool); err != nil {
		pool.Close()
		return fmt.Errorf("migrate: %w", err)
	}
	p.set(pool, nil)
	return nil
}

func (p *Pool) watch(ctx context.Context) {
	t := time.NewTicker(5 * time.Second)
	defer t.Stop()
	fails := 0
	for {
		select {
		case <-ctx.Done():
			return
		case <-t.C:
			pool := p.Handle()
			if pool == nil {
				return
			}
			c, cancel := context.WithTimeout(ctx, 3*time.Second)
			err := pool.Ping(c)
			cancel()
			if err != nil {
				fails++
				p.log.Warn("postgres ping failed", "err", err, "fails", fails)
				if fails >= 3 {
					p.set(nil, err)
					return
				}
				continue
			}
			fails = 0
			p.ready.Store(true)
		}
	}
}

func (p *Pool) set(pool *pgxpool.Pool, err error) {
	p.mu.Lock()
	old := p.pool
	p.pool = pool
	p.mu.Unlock()
	if err != nil {
		p.lastErr.Store(err)
		p.ready.Store(false)
	} else {
		p.lastErr.Store(fmt.Errorf(""))
		p.ready.Store(pool != nil)
	}
	if old != nil && old != pool {
		go func() {
			time.Sleep(2 * time.Second)
			old.Close()
		}()
	}
}

func (p *Pool) Handle() *pgxpool.Pool {
	p.mu.RLock()
	defer p.mu.RUnlock()
	return p.pool
}

func (p *Pool) Status() (string, error) {
	if p.ready.Load() && p.Handle() != nil {
		return "ok", nil
	}
	err, _ := p.lastErr.Load().(error)
	if err != nil && err.Error() != "" {
		return "down", err
	}
	return "down", fmt.Errorf("postgres unavailable")
}

func (p *Pool) Close() {
	p.mu.Lock()
	pool := p.pool
	p.pool = nil
	p.mu.Unlock()
	p.ready.Store(false)
	if pool != nil {
		pool.Close()
	}
}

func migrate(ctx context.Context, pool *pgxpool.Pool) error {
	if _, err := pool.Exec(ctx, `
		CREATE TABLE IF NOT EXISTS schema_migrations (
			version TEXT PRIMARY KEY,
			applied_at TIMESTAMPTZ NOT NULL DEFAULT now()
		)`); err != nil {
		return err
	}

	entries, err := fs.ReadDir(sqlFS, "sql")
	if err != nil {
		return err
	}
	names := make([]string, 0, len(entries))
	for _, e := range entries {
		if !e.IsDir() && strings.HasSuffix(e.Name(), ".sql") {
			names = append(names, e.Name())
		}
	}
	sort.Strings(names)

	for _, name := range names {
		var applied bool
		if err := pool.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM schema_migrations WHERE version=$1)`, name).Scan(&applied); err != nil {
			return err
		}
		if applied {
			continue
		}
		body, err := sqlFS.ReadFile("sql/" + name)
		if err != nil {
			return err
		}
		tx, err := pool.Begin(ctx)
		if err != nil {
			return err
		}
		if _, err := tx.Exec(ctx, string(body)); err != nil {
			_ = tx.Rollback(ctx)
			return fmt.Errorf("%s: %w", name, err)
		}
		if _, err := tx.Exec(ctx, `INSERT INTO schema_migrations (version) VALUES ($1)`, name); err != nil {
			_ = tx.Rollback(ctx)
			return err
		}
		if err := tx.Commit(ctx); err != nil {
			return err
		}
	}
	return nil
}
