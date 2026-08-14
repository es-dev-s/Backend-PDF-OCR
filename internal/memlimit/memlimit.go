package memlimit

import (
	"os"
	"runtime/debug"
	"strconv"
	"strings"
)

const (
	unlimitedSentinel = 1 << 62
	headroomNum       = 70
	headroomDen       = 100
)

// Apply sets a GC memory limit from GOMEMLIMIT or ~70% of the cgroup cap.
// It never invents a 384 MiB clamp on a large Railway plan.
func Apply(hosted bool) int64 {
	if !hosted {
		return 0
	}
	debug.SetGCPercent(50)
	if strings.TrimSpace(os.Getenv("GOMEMLIMIT")) != "" {
		return Current()
	}
	n := LimitFor(CgroupLimit())
	if n <= 0 {
		return 0
	}
	debug.SetMemoryLimit(n)
	return n
}

func Current() int64 {
	return debug.SetMemoryLimit(-1)
}

func CgroupLimit() int64 {
	for _, path := range []string{
		"/sys/fs/cgroup/memory.max",
		"/sys/fs/cgroup/memory/memory.limit_in_bytes",
	} {
		raw, err := os.ReadFile(path)
		if err != nil {
			continue
		}
		s := strings.TrimSpace(string(raw))
		if s == "" || strings.EqualFold(s, "max") {
			return 0
		}
		n, err := strconv.ParseInt(s, 10, 64)
		if err != nil || n <= 0 || n >= unlimitedSentinel {
			continue
		}
		return n
	}
	return 0
}

func LimitFor(cgroup int64) int64 {
	if cgroup <= 0 {
		return 0
	}
	return cgroup * headroomNum / headroomDen
}

func Format(n int64) string {
	if n <= 0 {
		return "unlimited"
	}
	const (
		mib = 1 << 20
		gib = 1 << 30
	)
	if n%gib == 0 {
		return strconv.FormatInt(n/gib, 10) + "GiB"
	}
	if n%mib == 0 {
		return strconv.FormatInt(n/mib, 10) + "MiB"
	}
	return strconv.FormatInt(n, 10)
}
