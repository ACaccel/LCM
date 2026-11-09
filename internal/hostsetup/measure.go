package hostsetup

import (
	"errors"
	"os"
	"path"

	"golang.org/x/sys/unix"
)

// Reasons MeasureTree stops before it has seen the whole tree. The usage
// returned with them covers what was seen.
var (
	ErrTreeDepth   = errors.New("the tree is nested too deeply")
	ErrTreeEntries = errors.New("the tree has too many entries")
	ErrTreeExpired = errors.New("measuring the tree took too long")
)

// MeasureLimits bound a measurement of a tree that someone else controls.
type MeasureLimits struct {
	// MaxDepth is the deepest directory level read; the root is level 0.
	MaxDepth int
	// MaxEntries is the number of entries (of any type) counted.
	MaxEntries int
	// Expired, when set, is polled when a directory is entered and every
	// few entries; the measurement stops once it reports true. Callers pass
	// a deadline on the injected clock, so tests never sleep.
	Expired func() bool
}

// TreeUsage is what MeasureTree saw: the sizes of the regular files and the
// number of entries.
type TreeUsage struct {
	Bytes   int64
	Entries int
}

const (
	// expiryStride is the number of entries between two polls of Expired.
	expiryStride = 64
	// readBatch bounds the names read from a directory at once, so a
	// directory with millions of names costs no memory beyond the entry
	// limit.
	readBatch = 1024
	// dirFlags open a directory without following a final link.
	dirFlags = unix.O_RDONLY | unix.O_DIRECTORY | unix.O_NOFOLLOW | unix.O_CLOEXEC
)

// MeasureTree sums the sizes of the regular files below dir and counts its
// entries. The tree may be changed by its owner while it is read: links are
// never followed, entries that vanish or change type meanwhile are skipped,
// and the walk is bounded in depth, entries and time. On the real
// filesystem every directory is opened relative to its parent's descriptor
// (O_NOFOLLOW|O_DIRECTORY), so the cost of an entry does not grow with its
// depth; other filesystems are read through OpenTree. It returns
// ErrTreeDepth, ErrTreeEntries or ErrTreeExpired when a bound is hit, and
// any error from opening dir itself.
func MeasureTree(fsys FS, dir string, lim MeasureLimits) (TreeUsage, error) {
	m := &measurer{lim: lim}
	if o, ok := fsys.(OS); ok {
		p, err := o.path(dir)
		if err != nil {
			return TreeUsage{}, err
		}
		fd, err := unix.Open(p, dirFlags, 0)
		if err != nil {
			return TreeUsage{}, &os.PathError{Op: "open", Path: dir, Err: err}
		}
		err = m.walkFD(fd, 0)
		return m.usage, err
	}
	t, err := fsys.OpenTree(dir)
	if err != nil {
		return TreeUsage{}, err
	}
	defer func() { _ = t.Close() }()
	err = m.walkTree(t, ".", 0)
	return m.usage, err
}

type measurer struct {
	lim   MeasureLimits
	usage TreeUsage
}

// count registers one entry and reports the bound it hits, if any.
func (m *measurer) count() error {
	if m.usage.Entries++; m.usage.Entries > m.lim.MaxEntries {
		return ErrTreeEntries
	}
	if m.lim.Expired != nil && m.usage.Entries%expiryStride == 0 && m.lim.Expired() {
		return ErrTreeExpired
	}
	return nil
}

// enter checks the bounds that apply when a directory is read.
func (m *measurer) enter(depth int) error {
	if depth > m.lim.MaxDepth {
		return ErrTreeDepth
	}
	if m.lim.Expired != nil && m.lim.Expired() {
		return ErrTreeExpired
	}
	return nil
}

// walkFD reads the directory fd and closes it.
func (m *measurer) walkFD(fd, depth int) error {
	f := os.NewFile(uintptr(fd), "") //nolint:gosec // descriptors fit in uintptr
	defer func() { _ = f.Close() }()
	if err := m.enter(depth); err != nil {
		return err
	}
	for {
		names, err := f.Readdirnames(readBatch)
		for _, name := range names {
			if err := m.count(); err != nil {
				return err
			}
			var st unix.Stat_t
			if unix.Fstatat(fd, name, &st, unix.AT_SYMLINK_NOFOLLOW) != nil {
				continue
			}
			switch st.Mode & unix.S_IFMT {
			case unix.S_IFREG:
				m.usage.Bytes += st.Size
			case unix.S_IFDIR:
				child, err := unix.Openat(fd, name, dirFlags, 0)
				if err != nil {
					// Removed or replaced since it was listed.
					continue
				}
				if err := m.walkFD(child, depth+1); err != nil {
					return err
				}
			}
		}
		if err != nil {
			// The end of the directory, or one that can no longer be read.
			return nil
		}
	}
}

// walkTree is the same walk over a Tree.
func (m *measurer) walkTree(t Tree, rel string, depth int) error {
	if err := m.enter(depth); err != nil {
		return err
	}
	names, _, err := t.ReadDir(rel)
	if err != nil {
		return nil //nolint:nilerr // a directory removed or replaced meanwhile is not counted
	}
	for _, name := range names {
		if err := m.count(); err != nil {
			return err
		}
		r := path.Join(rel, name)
		info, err := t.Lstat(r)
		switch {
		case err != nil:
		case info.IsDir():
			if err := m.walkTree(t, r, depth+1); err != nil {
				return err
			}
		case info.IsRegular():
			m.usage.Bytes += info.Size
		}
	}
	return nil
}
