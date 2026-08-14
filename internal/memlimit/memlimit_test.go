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

func TestLimitForUsesCgroupHeadroom(t *testing.T) {
	if LimitFor(0) != 0 {
		t.Fatal("unknown cgroup must not invent a cap")
	}
	const plan int64 = 24 << 30
	got := LimitFor(plan)
	want := plan * 70 / 100
	if got != want {
		t.Fatalf("got %d want %d", got, want)
	}
	if got == 384<<20 {
		t.Fatal("must not fall back to 384MiB on a 24GiB plan")
	}
}

func TestFormat(t *testing.T) {
	if Format(0) != "unlimited" {
		t.Fatal(Format(0))
	}
	if Format(16<<30) != "16GiB" {
		t.Fatal(Format(16 << 30))
	}
}
