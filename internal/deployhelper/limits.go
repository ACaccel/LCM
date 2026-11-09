package deployhelper

import (
	"context"
	"errors"
	"fmt"
	"math"
	"sync"
	"syscall"
	"time"

	"github.com/ACaccel/lcm/internal/clock"
	"github.com/ACaccel/lcm/internal/hostsetup"
)

// Limits bound the disk use of builds. A zero field selects its default.
type Limits struct {
	// MaxBytes and MaxEntries bound a running build's source tree, home and
	// cache together; the build is stopped beyond them.
	MaxBytes   int64
	MaxEntries int
	// CacheBytes: a cache larger than this when a build starts is emptied
	// first.
	CacheBytes int64
	// MinFreeBytes and MinFreeInodes must be free on the build file system
	// for a build to start; below StopFreeBytes or StopFreeInodes a running
	// build is stopped.
	MinFreeBytes, StopFreeBytes   uint64
	MinFreeInodes, StopFreeInodes uint64
	// Interval is how often a running build is measured.
	Interval time.Duration
	// MaxDepth is the deepest directory nesting measured: a tree nested
	// deeper counts as over the limit. MeasureTimeout bounds one
	// measurement on the helper's clock; running out counts as over the
	// limit too.
	MaxDepth       int
	MeasureTimeout time.Duration
}

var defaultLimits = Limits{
	MaxBytes: 16 << 30, MaxEntries: 2_000_000, CacheBytes: 4 << 30,
	MinFreeBytes: 2 << 30, StopFreeBytes: 512 << 20, MinFreeInodes: 100_000, StopFreeInodes: 20_000,
	Interval: 10 * time.Second, MaxDepth: 256, MeasureTimeout: 30 * time.Second,
}

func (l Limits) withDefaults() Limits {
	d := defaultLimits
	if l.MaxBytes > 0 {
		d.MaxBytes = l.MaxBytes
	}
	if l.MaxEntries > 0 {
		d.MaxEntries = l.MaxEntries
	}
	if l.CacheBytes > 0 {
		d.CacheBytes = l.CacheBytes
	}
	if l.MinFreeBytes > 0 {
		d.MinFreeBytes = l.MinFreeBytes
	}
	if l.StopFreeBytes > 0 {
		d.StopFreeBytes = l.StopFreeBytes
	}
	if l.MinFreeInodes > 0 {
		d.MinFreeInodes = l.MinFreeInodes
	}
	if l.StopFreeInodes > 0 {
		d.StopFreeInodes = l.StopFreeInodes
	}
	if l.Interval > 0 {
		d.Interval = l.Interval
	}
	if l.MaxDepth > 0 {
		d.MaxDepth = l.MaxDepth
	}
	if l.MeasureTimeout > 0 {
		d.MeasureTimeout = l.MeasureTimeout
	}
	return d
}

// statfsFree reports the bytes available to unprivileged users and the free
// inodes of the file system holding p. A file system without inode
// accounting reports unlimited inodes.
func statfsFree(p string) (uint64, uint64, error) {
	var st syscall.Statfs_t
	if err := syscall.Statfs(p, &st); err != nil {
		return 0, 0, err
	}
	inodes := st.Ffree
	if st.Files == 0 {
		inodes = math.MaxUint64
	}
	return st.Bavail * uint64(st.Bsize), inodes, nil //nolint:gosec // block sizes are positive
}

// checkFree refuses when the file system holding p has less free space or
// inodes than asked; without a Free function nothing is checked.
func (h *Helper) checkFree(p string, bytes, inodes uint64) error {
	if h.Free == nil {
		return nil
	}
	b, n, err := h.Free(p)
	switch {
	case err != nil:
		return fmt.Errorf("measure the free space of %s: %w", p, err)
	case b < bytes:
		return fmt.Errorf("%s has %d bytes free, less than %d", p, b, bytes)
	case n < inodes:
		return fmt.Errorf("%s has %d inodes free, less than %d", p, n, inodes)
	}
	return nil
}

// measured is what usage saw of some directories: their sizes and entries,
// and why the measurement stopped early, or "". A tree that stops it counts
// as over the limit.
type measured struct {
	bytes   int64
	entries int
	stopped string
}

// usage measures the directories together, bounded by the limits and by one
// deadline on the helper's clock. The trees belong to the build account:
// they are read through descriptors, so links its owner plants never make
// root walk anything else and a deep chain of directories costs no more per
// entry than a flat one. An unreadable directory is not counted.
func (h *Helper) usage(lim Limits, dirs ...string) measured {
	deadline := h.Clock.Now().Add(lim.MeasureTimeout)
	var m measured
	for _, d := range dirs {
		u, err := hostsetup.MeasureTree(h.FS, d, hostsetup.MeasureLimits{
			MaxDepth: lim.MaxDepth, MaxEntries: lim.MaxEntries - m.entries,
			Expired: func() bool { return !h.Clock.Now().Before(deadline) },
		})
		m.bytes, m.entries = m.bytes+u.Bytes, m.entries+u.Entries
		switch {
		case errors.Is(err, hostsetup.ErrTreeDepth):
			m.stopped = fmt.Sprintf("its directories are nested deeper than %d levels", lim.MaxDepth)
		case errors.Is(err, hostsetup.ErrTreeExpired):
			m.stopped = fmt.Sprintf("measuring its files took longer than %s", lim.MeasureTimeout)
		}
		if m.stopped != "" {
			break
		}
	}
	return m
}

// overLimit describes why a running build must stop, or returns "".
func (h *Helper) overLimit(dirs, fsPaths []string, lim Limits) string {
	if h.Free != nil {
		for _, p := range fsPaths {
			if b, n, err := h.Free(p); err == nil && (b < lim.StopFreeBytes || n < lim.StopFreeInodes) {
				return fmt.Sprintf("the file system of %s has only %d bytes and %d inodes free", p, b, n)
			}
		}
	}
	m := h.usage(lim, dirs...)
	switch {
	case m.stopped != "":
		return m.stopped
	case m.bytes > lim.MaxBytes:
		return fmt.Sprintf("its files exceed %d bytes", lim.MaxBytes)
	case m.entries > lim.MaxEntries:
		return fmt.Sprintf("it has more than %d files", lim.MaxEntries)
	}
	return ""
}

// watchBuild measures a running build every Interval and stops its unit
// when it exceeds the limits or the file system runs short. stop ends the
// watch and returns why the build was stopped, or "".
func (h *Helper) watchBuild(ctx context.Context, unit string, dirs, fsPaths []string, lim Limits) (stop func() string) {
	var (
		mu     sync.Mutex
		timer  clock.Timer
		done   bool
		reason string
		tick   func()
	)
	tick = func() {
		why := h.overLimit(dirs, fsPaths, lim)
		mu.Lock()
		defer mu.Unlock()
		if done {
			return
		}
		if why == "" {
			timer = h.Clock.AfterFunc(lim.Interval, tick)
			return
		}
		reason = why
		_, _ = h.run(context.WithoutCancel(ctx), systemctlTimeout, systemctl, "stop", unit+".service")
	}
	mu.Lock()
	timer = h.Clock.AfterFunc(lim.Interval, tick)
	mu.Unlock()
	return func() string {
		mu.Lock()
		defer mu.Unlock()
		done = true
		timer.Stop()
		return reason
	}
}

// emptyDir removes everything in a directory that only its owner's builds
// use; no build of the service runs meanwhile.
func (h *Helper) emptyDir(dir string) error {
	names, err := h.FS.ReadDir(dir)
	if err != nil {
		return err
	}
	var errs []error
	for _, n := range names {
		errs = append(errs, h.FS.RemoveAll(dir+"/"+n))
	}
	return errors.Join(errs...)
}
