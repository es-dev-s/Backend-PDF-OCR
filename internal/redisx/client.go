package redisx

import (
	"context"
	"fmt"
	"log/slog"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/redis/go-redis/v9"

	"ocr-backend/internal/retry"
)

type Client struct {
	mu      sync.RWMutex
	rdb     *redis.Client
	url     string
	log     *slog.Logger
	ready   atomic.Bool
	lastErr atomic.Value
	poolN   int
}

func New(url string, log *slog.Logger, poolSize int) *Client {
	if poolSize < 4 {
		poolSize = 8
	}
	if poolSize > 32 {
		poolSize = 32
	}
	c := &Client{url: url, log: log, poolN: poolSize}
	c.lastErr.Store(fmt.Errorf("redis not connected"))
	return c
}

func (c *Client) Run(ctx context.Context) {
	if strings.TrimSpace(c.url) == "" {
		c.set(nil, fmt.Errorf("REDIS_URL is empty"))
		c.log.Error("redis disabled: REDIS_URL is empty")
		return
	}
	backoff := time.Second
	for {
		if ctx.Err() != nil {
			return
		}
		if err := c.connect(ctx); err != nil {
			c.set(nil, err)
			c.log.Warn("redis connect failed", "err", err, "retry_in", backoff.String())
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
		c.log.Info("redis connected")
		c.watch(ctx)
	}
}

func (c *Client) connect(ctx context.Context) error {
	opt, err := redis.ParseURL(c.url)
	if err != nil {
		return fmt.Errorf("parse REDIS_URL: %w", err)
	}
	opt.PoolSize = c.poolN
	opt.MinIdleConns = 2
	opt.MaxRetries = 3
	opt.MinRetryBackoff = 50 * time.Millisecond
	opt.MaxRetryBackoff = 2 * time.Second
	opt.DialTimeout = 5 * time.Second
	opt.ReadTimeout = 2 * time.Second
	opt.WriteTimeout = 2 * time.Second
	opt.PoolTimeout = 4 * time.Second
	opt.ConnMaxIdleTime = 5 * time.Minute
	opt.ConnMaxLifetime = 30 * time.Minute
	opt.ContextTimeoutEnabled = true
	rdb := redis.NewClient(opt)

	pingCtx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	if err := rdb.Ping(pingCtx).Err(); err != nil {
		_ = rdb.Close()
		return err
	}
	c.set(rdb, nil)
	return nil
}

func (c *Client) watch(ctx context.Context) {
	t := time.NewTicker(5 * time.Second)
	defer t.Stop()
	fails := 0
	for {
		select {
		case <-ctx.Done():
			return
		case <-t.C:
			rdb := c.Handle()
			if rdb == nil {
				return
			}
			pingCtx, cancel := context.WithTimeout(ctx, 5*time.Second)
			err := rdb.Ping(pingCtx).Err()
			if err == nil {
				_ = rdb.Set(pingCtx, "ocr:api:heartbeat", time.Now().UTC().Format(time.RFC3339Nano), 20*time.Second).Err()
			}
			cancel()
			if err != nil {
				fails++
				c.log.Warn("redis ping failed", "err", err, "fails", fails)
				if fails >= 8 {
					c.set(nil, err)
					return
				}
				continue
			}
			fails = 0
			c.ready.Store(true)
		}
	}
}

func (c *Client) set(rdb *redis.Client, err error) {
	c.mu.Lock()
	old := c.rdb
	c.rdb = rdb
	c.mu.Unlock()
	if err != nil {
		c.lastErr.Store(err)
		c.ready.Store(false)
	} else {
		c.lastErr.Store(fmt.Errorf(""))
		c.ready.Store(rdb != nil)
	}
	if old != nil && old != rdb {
		go func() {
			time.Sleep(2 * time.Second)
			_ = old.Close()
		}()
	}
}

func (c *Client) Handle() *redis.Client {
	c.mu.RLock()
	defer c.mu.RUnlock()
	return c.rdb
}

func (c *Client) Status() (string, error) {
	if c.ready.Load() && c.Handle() != nil {
		return "ok", nil
	}
	err, _ := c.lastErr.Load().(error)
	if err != nil && err.Error() != "" {
		return "down", err
	}
	return "down", fmt.Errorf("redis unavailable")
}

func (c *Client) Close() {
	c.mu.Lock()
	rdb := c.rdb
	c.rdb = nil
	c.mu.Unlock()
	c.ready.Store(false)
	if rdb != nil {
		_ = rdb.Close()
	}
}
