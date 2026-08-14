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
	driver := strings.ToLower(strings.TrimSpace(opts.Driver))
	if r2Ready(opts.R2) && (driver == "" || driver == "local") {
		driver = "r2"
	}
	switch driver {
	case "r2", "s3":
		return NewR2(opts.R2)
	case "", "local":
		return NewLocal(opts.LocalDir)
	default:
		return nil, fmt.Errorf("unknown storage driver %q", opts.Driver)
	}
}

func r2Ready(opts R2Options) bool {
	return strings.TrimSpace(opts.AccountID) != "" &&
		strings.TrimSpace(opts.AccessKey) != "" &&
		strings.TrimSpace(opts.Secret) != "" &&
		strings.TrimSpace(opts.Bucket) != ""
}
