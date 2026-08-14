package memlimit

import (
	"runtime/debug"
	"testing"
)

func TestApplyLocalDoesNotClamp(t *testing.T) {
	before := debug.SetMemoryLimit(-1)
	got := Apply(false)
	after := debug.SetMemoryLimit(before)
	if got != 0 {
		t.Fatalf("local apply should not set a limit, got %d", got)
	}
	_ = after
}
