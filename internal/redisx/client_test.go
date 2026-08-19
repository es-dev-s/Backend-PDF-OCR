package redisx

import (
	"context"
	"errors"
	"fmt"
	"io"
	"testing"
	"time"
)

func TestLastErrAcceptsDifferentConcreteTypes(t *testing.T) {
	c := New("", nil, 8)
	// Mixing fmt errors with context/network errors used to panic
	// atomic.Value: "store of inconsistently typed value".
	c.set(nil, fmt.Errorf("redis not connected"))
	c.set(nil, context.DeadlineExceeded)
	c.set(nil, errors.New("dial tcp: i/o timeout"))
	c.set(nil, io.EOF)
	c.set(nil, nil)
	if _, err := c.Status(); err == nil {
		t.Fatal("disconnected client must report down")
	}
	if d := time.Since(time.Now()); d > time.Second {
		t.Fatal("store must not block")
	}
}
