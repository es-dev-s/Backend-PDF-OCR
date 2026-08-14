package retry

import (
	"context"
	"errors"
	"math/rand"
	"net"
	"time"

	"github.com/jackc/pgx/v5/pgconn"
	"github.com/redis/go-redis/v9"
)

var ErrUnavailable = errors.New("database unavailable")

func Transient(err error) bool {
	if err == nil {
		return false
	}
	if errors.Is(err, context.Canceled) {
		return false
	}
	if errors.Is(err, ErrUnavailable) {
		return true
	}
	if errors.Is(err, redis.ErrClosed) || errors.Is(err, redis.Nil) {
		return true
	}
	var netErr net.Error
	if errors.As(err, &netErr) {
		return true
	}
	var pgErr *pgconn.PgError
	if errors.As(err, &pgErr) {
		switch pgErr.Code {
		case "40001", // serialization_failure
			"40P01", // deadlock_detected
			"55P03", // lock_not_available
			"53300", // too_many_connections
			"57P01", // admin_shutdown
			"57P02", // crash_shutdown
			"57P03", // cannot_connect_now
			"08000", "08001", "08003", "08006", "08004":
			return true
		}
	}
	return false
}

func Do(ctx context.Context, attempts int, fn func(context.Context) error) error {
	if attempts < 1 {
		attempts = 1
	}
	var err error
	backoff := 50 * time.Millisecond
	for i := 0; i < attempts; i++ {
		if ctx.Err() != nil {
			return ctx.Err()
		}
		err = fn(ctx)
		if err == nil || !Transient(err) {
			return err
		}
		if i == attempts-1 {
			break
		}
		delay := backoff + time.Duration(rand.Int63n(int64(backoff/2)+1))
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-time.After(delay):
		}
		if backoff < 2*time.Second {
			backoff *= 2
		}
	}
	return err
}

func Jitter(base, max time.Duration) time.Duration {
	if base < time.Millisecond {
		base = time.Millisecond
	}
	j := base + time.Duration(rand.Int63n(int64(base)+1))
	if max > 0 && j > max {
		return max
	}
	return j
}
