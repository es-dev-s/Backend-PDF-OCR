package blob

import (
	"fmt"
	"strings"
)

type Options struct {
	Driver   string
	LocalDir string
	R2       R2Options
}

type R2Options struct {
	AccountID string
	AccessKey string
	Secret    string
	Bucket    string
	Endpoint  string
	Prefix    string
}

func New(opts Options) (Store, error) {
	switch strings.ToLower(strings.TrimSpace(opts.Driver)) {
	case "r2", "s3":
		return NewR2(opts.R2)
	case "", "local":
		return NewLocal(opts.LocalDir)
	default:
		return nil, fmt.Errorf("unknown storage driver %q", opts.Driver)
	}
}
