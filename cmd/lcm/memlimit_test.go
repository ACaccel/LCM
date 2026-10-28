package main

import (
	"math"
	"runtime/debug"
	"testing"

	"github.com/ACaccel/lcm/internal/config"
)

func TestApplyMemoryLimit(t *testing.T) {
	prev := debug.SetMemoryLimit(-1)
	t.Cleanup(func() { debug.SetMemoryLimit(prev) })
	current := func() int64 { return debug.SetMemoryLimit(-1) }
	reset := func() { debug.SetMemoryLimit(math.MaxInt64) }

	cfg := config.Default()
	reset()
	if err := applyMemoryLimit(cfg, false, false); err != nil || current() != 40<<20 {
		t.Fatalf("default: limit %d, %v", current(), err)
	}

	// GOMEMLIMIT from the environment beats the built-in default.
	reset()
	if err := applyMemoryLimit(cfg, false, true); err != nil || current() != math.MaxInt64 {
		t.Fatalf("GOMEMLIMIT set: limit %d, %v", current(), err)
	}
	// An explicit LCM setting beats GOMEMLIMIT.
	cfg.MemoryLimit = "64MiB"
	if err := applyMemoryLimit(cfg, true, true); err != nil || current() != 64<<20 {
		t.Fatalf("explicit: limit %d, %v", current(), err)
	}
	// "off" disables a limit that was set before.
	cfg.MemoryLimit = "off"
	if err := applyMemoryLimit(cfg, true, false); err != nil || current() != math.MaxInt64 {
		t.Fatalf("off: limit %d, %v", current(), err)
	}
	cfg.MemoryLimit = "lots"
	if err := applyMemoryLimit(cfg, true, false); err == nil {
		t.Fatal("invalid limit accepted")
	}
}
