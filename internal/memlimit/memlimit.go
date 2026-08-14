package memlimit

import (
	"os"
	"runtime/debug"
	"strings"
)

const defaultHosted = 384 << 20 // 384 MiB — leaves headroom on a 512 MiB Railway box

// Apply makes the GC reclaim memory before the platform OOM-kills the process.
func Apply(hosted bool) int64 {
	if !hosted {
		return 0
	}
	debug.SetGCPercent(40)
	if strings.TrimSpace(os.Getenv("GOMEMLIMIT")) != "" {
		return debug.SetMemoryLimit(-1)
	}
	debug.SetMemoryLimit(defaultHosted)
	return defaultHosted
}
