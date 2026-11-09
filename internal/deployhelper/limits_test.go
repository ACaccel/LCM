package deployhelper

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/ACaccel/lcm/internal/clock"
	"github.com/ACaccel/lcm/internal/hostsetup"
)

// tickClock advances a fake clock by step on every reading, so that a
// measurement runs out of time without sleeping.
type tickClock struct {
	*clock.Fake
	step time.Duration
}

func (c tickClock) Now() time.Time {
	c.Advance(c.step)
	return c.Fake.Now()
}

// deepChain makes n nested directories below the real path p.
func deepChain(t *testing.T, p string, n int) {
	t.Helper()
	if err := os.MkdirAll(filepath.Join(p, strings.Repeat("d/", n)), 0o700); err != nil {
		t.Fatal(err)
	}
}

func TestUsageDeepChain(t *testing.T) {
	e := newEnv(t)
	lim := Limits{}.withDefaults()
	deepChain(t, e.real("/var/cache/lcm-build/web/npm"), 300)

	start := time.Now()
	m := e.h.usage(lim, "/var/cache/lcm-build/web")
	if !strings.Contains(m.stopped, "nested deeper than 256 levels") {
		t.Errorf("measured %+v", m)
	}
	if d := time.Since(start); d > 5*time.Second {
		t.Errorf("the measurement took %s", d)
	}
	if why := e.h.overLimit([]string{"/var/cache/lcm-build/web"}, nil, lim); !strings.Contains(why, "nested deeper") {
		t.Errorf("a deep chain is not over the limit: %q", why)
	}

	// The same chain is fine within a larger depth limit.
	lim.MaxDepth = 400
	if m := e.h.usage(lim, "/var/cache/lcm-build/web"); m.stopped != "" || m.entries != 301 {
		t.Errorf("measured %+v", m)
	}
}

func TestUsageDeadline(t *testing.T) {
	e := newEnv(t)
	e.h.Clock = tickClock{Fake: e.clock, step: 400 * time.Millisecond}
	for _, d := range []string{"a", "b", "c", "d"} {
		e.write("/var/cache/lcm-build/web/"+d+"/f", "x", 0o644)
	}
	lim := Limits{MeasureTimeout: time.Second}.withDefaults()
	if m := e.h.usage(lim, "/var/cache/lcm-build/web"); !strings.Contains(m.stopped, "took longer than 1s") {
		t.Errorf("measured %+v", m)
	}
	if why := e.h.overLimit([]string{"/var/cache/lcm-build/web"}, nil, lim); !strings.Contains(why, "took longer") {
		t.Errorf("a slow measurement is not over the limit: %q", why)
	}
	lim.MeasureTimeout = time.Hour
	if m := e.h.usage(lim, "/var/cache/lcm-build/web"); m.stopped != "" || m.bytes != 4 || m.entries != 8 {
		t.Errorf("measured %+v", m)
	}
}

func TestBuildStopsOnDeepTree(t *testing.T) {
	e := newEnv(t)
	e.h.Limits = Limits{Interval: time.Second}

	// A cache that is too deep is emptied before the build.
	deepChain(t, e.real("/var/cache/lcm-build/web/npm"), 300)
	e.ok(buildReq(commit1))
	if e.exists("/var/cache/lcm-build/web/npm") {
		t.Error("the too deep cache was kept")
	}

	// So a build that nests directories deeper than the limit is stopped.
	e.build = func(src string) (string, int) {
		deepChain(t, filepath.Join(src, "deep"), 300)
		e.clock.Advance(time.Second)
		return "killed\n", 143
	}
	e.refused(buildReq(commit1), CodeFailed, "the build was stopped: its directories are nested deeper than 256 levels")
	if got := e.systemctlCalls(); len(got) == 0 || !strings.HasPrefix(got[len(got)-1], "stop lcm-build-web-") {
		t.Errorf("systemctl %q", got)
	}
}

// devFS reports another device for one path, as if it were mounted apart.
type devFS struct {
	hostsetup.OS
	path string
}

func (f devFS) Lstat(name string) (hostsetup.Info, error) {
	info, err := f.OS.Lstat(name)
	if name == f.path {
		info.Dev++
	}
	return info, err
}

func TestBuildChecksCacheFileSystem(t *testing.T) {
	e := newEnv(t)
	var measured []string
	cacheFree := uint64(10 << 30)
	e.h.Free = func(p string) (uint64, uint64, error) {
		measured = append(measured, p)
		if p == "/var/cache/lcm-build/web" {
			return cacheFree, 1 << 20, nil
		}
		return 10 << 30, 1 << 20, nil
	}
	e.h.Limits = Limits{Interval: time.Second}

	// One file system: only the builds are measured.
	e.ok(buildReq(commit1))
	for _, p := range measured {
		if p != "/var/lib/lcm-build/web" {
			t.Errorf("measured %s", p)
		}
	}

	// Mounted apart, the cache needs room too, before and during a build.
	e.h.FS = devFS{OS: hostsetup.OS{Root: e.root}, path: "/var/cache/lcm-build/web"}
	measured = nil
	e.ok(buildReq(commit1))
	if len(measured) < 2 || measured[0] != "/var/lib/lcm-build/web" || measured[1] != "/var/cache/lcm-build/web" {
		t.Errorf("measured %q", measured)
	}
	e.build = func(string) (string, int) {
		cacheFree = 1 << 20
		e.clock.Advance(time.Second)
		return "", 143
	}
	e.refused(buildReq(commit1), CodeFailed, "the build was stopped: the file system of /var/cache/lcm-build/web has only 1048576 bytes")
	e.refused(buildReq(commit1), CodeRefused, "/var/cache/lcm-build/web has 1048576 bytes free")
}

// A build is removed only while no install of the service can be copying it.
func TestPruneWaitsForInstall(t *testing.T) {
	e := newEnv(t)
	var ids []string
	build := func() {
		e.clock.Advance(time.Second)
		ids = append(ids, e.ok(buildReq(commit1)).Build.BuildID)
	}
	for range keepBuilds {
		build()
	}
	unlock, err := e.h.Ledger.Lock("web")
	if err != nil {
		t.Fatal(err)
	}
	build()
	for _, id := range ids {
		if !e.exists("/var/lib/lcm-build/web/" + id) {
			t.Errorf("build %s was removed while the service lock was held", id)
		}
	}
	unlock()
	build()
	for i, id := range ids {
		if got := e.exists("/var/lib/lcm-build/web/" + id); got != (i >= len(ids)-keepBuilds) {
			t.Errorf("build %d kept: %v", i, got)
		}
	}
}
