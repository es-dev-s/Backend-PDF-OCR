package blob

import (
	"context"
	"errors"
	"io"
)

var ErrNotFound = errors.New("object not found")

// Store is the object layer. Local disk today, object storage later.
type Store interface {
	Put(ctx context.Context, key string, r io.Reader, size int64, contentType string) error
	Open(ctx context.Context, key string) (rc io.ReadCloser, size int64, contentType string, err error)
	Delete(ctx context.Context, key string) error
	Ready(ctx context.Context) error
	Driver() string
	LocalPath(ctx context.Context, key string) (string, error)
}
