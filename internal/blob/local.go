package blob

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
)

type Local struct {
	root string
}

func NewLocal(root string) (*Local, error) {
	abs, err := filepath.Abs(root)
	if err != nil {
		return nil, fmt.Errorf("storage dir: %w", err)
	}
	if err := os.MkdirAll(abs, 0o755); err != nil {
		return nil, fmt.Errorf("create storage dir: %w", err)
	}
	return &Local{root: abs}, nil
}

func (l *Local) Driver() string { return "local" }

func (l *Local) Ready(_ context.Context) error {
	info, err := os.Stat(l.root)
	if err != nil {
		return err
	}
	if !info.IsDir() {
		return fmt.Errorf("storage path is not a directory")
	}
	return nil
}

func (l *Local) Put(ctx context.Context, key string, r io.Reader, _ int64, _ string) error {
	path, err := l.resolve(key)
	if err != nil {
		return err
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return err
	}
	tmp, err := os.CreateTemp(filepath.Dir(path), ".upload-*")
	if err != nil {
		return err
	}
	tmpName := tmp.Name()
	ok := false
	defer func() {
		_ = tmp.Close()
		if !ok {
			_ = os.Remove(tmpName)
		}
	}()

	buf := make([]byte, 256<<10)
	for {
		if err := ctx.Err(); err != nil {
			return err
		}
		n, readErr := r.Read(buf)
		if n > 0 {
			if _, err := tmp.Write(buf[:n]); err != nil {
				return err
			}
		}
		if readErr == io.EOF {
			break
		}
		if readErr != nil {
			return readErr
		}
	}
	if err := tmp.Sync(); err != nil {
		return err
	}
	if err := tmp.Close(); err != nil {
		return err
	}
	if err := os.Rename(tmpName, path); err != nil {
		return err
	}
	ok = true
	return nil
}

func (l *Local) LocalPath(_ context.Context, key string) (string, error) {
	return l.resolve(key)
}

func (l *Local) Open(_ context.Context, key string) (io.ReadCloser, int64, string, error) {
	path, err := l.resolve(key)
	if err != nil {
		return nil, 0, "", err
	}
	f, err := os.Open(path)
	if err != nil {
		return nil, 0, "", err
	}
	info, err := f.Stat()
	if err != nil {
		_ = f.Close()
		return nil, 0, "", err
	}
	return f, info.Size(), contentTypeFor(path), nil
}

func (l *Local) Delete(_ context.Context, key string) error {
	path, err := l.resolve(key)
	if err != nil {
		return err
	}
	err = os.Remove(path)
	if errors.Is(err, os.ErrNotExist) {
		return nil
	}
	return err
}

func (l *Local) resolve(key string) (string, error) {
	clean := filepath.Clean("/" + strings.ReplaceAll(key, "\\", "/"))
	clean = strings.TrimPrefix(clean, "/")
	if clean == "" || strings.Contains(clean, "..") {
		return "", fmt.Errorf("invalid storage key")
	}
	full := filepath.Join(l.root, filepath.FromSlash(clean))
	rel, err := filepath.Rel(l.root, full)
	if err != nil || strings.HasPrefix(rel, "..") {
		return "", fmt.Errorf("invalid storage key")
	}
	return full, nil
}

func contentTypeFor(path string) string {
	switch strings.ToLower(filepath.Ext(path)) {
	case ".pdf":
		return "application/pdf"
	case ".png":
		return "image/png"
	case ".jpg", ".jpeg":
		return "image/jpeg"
	case ".webp":
		return "image/webp"
	case ".tif", ".tiff":
		return "image/tiff"
	default:
		return "application/octet-stream"
	}
}
