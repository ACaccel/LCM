package hostsetup

import (
	"errors"
	"fmt"
	"io"
	"os"
	"path"
	"strings"

	"golang.org/x/sys/unix"
)

// MeasureTreeBelow measures rel, a clean relative path, below the directory
// root like MeasureTree, resolving rel inside root only: a link in any
// component makes it fail instead of being followed, so a tree that someone
// else controls cannot send the walk elsewhere. rel "." is root itself.
func MeasureTreeBelow(fsys FS, root, rel string, lim MeasureLimits) (TreeUsage, error) {
	m := &measurer{lim: lim}
	if o, ok := fsys.(OS); ok {
		p, err := o.path(root)
		if err != nil {
			return TreeUsage{}, err
		}
		fd, err := unix.Open(p, dirFlags, 0)
		if err != nil {
			return TreeUsage{}, &os.PathError{Op: "open", Path: root, Err: err}
		}
		if fd, err = openBelow(fd, rel); err != nil {
			return TreeUsage{}, &os.PathError{Op: "open", Path: path.Join(root, rel), Err: err}
		}
		err = m.walkFD(fd, 0)
		return m.usage, err
	}
	t, err := fsys.OpenTree(root)
	if err != nil {
		return TreeUsage{}, err
	}
	defer func() { _ = t.Close() }()
	err = m.walkTree(t, rel, 0)
	return m.usage, err
}

// openBelow opens the directories of rel one by one below the directory fd,
// without following links, and closes every descriptor but the last, which it
// returns. It closes fd on failure too.
func openBelow(fd int, rel string) (int, error) {
	if rel == "." || rel == "" {
		return fd, nil
	}
	for _, name := range strings.Split(rel, "/") {
		next, err := unix.Openat(fd, name, dirFlags, 0)
		_ = unix.Close(fd)
		if err != nil {
			return -1, err
		}
		fd = next
	}
	return fd, nil
}

// SecureTree gives the directory dir, which must be the one want describes,
// and every directory below it to uid:gid with mode 0700, so that its former
// owner can no longer add, remove or rename entries through a descriptor it
// still holds. A directory is secured before it is read, so nothing moves in
// or out of what has been read. Files are left alone. Links are never
// followed and every change goes through a descriptor of the directory that
// was looked at. The walk is bounded like MeasureTree; running into a bound
// is an error and leaves the rest of the tree unsecured.
func SecureTree(fsys FS, dir string, want Info, uid, gid int, lim MeasureLimits) error {
	m := &measurer{lim: lim}
	if o, ok := fsys.(OS); ok {
		p, err := o.path(dir)
		if err != nil {
			return err
		}
		fd, err := unix.Open(p, dirFlags, 0)
		if err != nil {
			return &os.PathError{Op: "open", Path: dir, Err: err}
		}
		var st unix.Stat_t
		if err := unix.Fstat(fd, &st); err != nil || uint64(st.Dev) != want.Dev || st.Ino != want.Ino { //nolint:gosec,unconvert // Dev is unsigned on Linux; the conversion keeps other layouts building
			_ = unix.Close(fd)
			return fmt.Errorf("%s changed since it was checked; nothing was changed", dir)
		}
		return m.secureFD(fd, 0, uid, gid)
	}
	if err := fsys.Chown(dir, want, uid, gid); err != nil {
		return err
	}
	if err := fsys.Chmod(dir, want, 0o700); err != nil {
		return err
	}
	t, err := fsys.OpenTree(dir)
	if err != nil {
		return err
	}
	defer func() { _ = t.Close() }()
	return m.secureTree(fsys, t, dir, ".", 0, uid, gid)
}

// secureFD secures the directory fd, then its subdirectories, and closes fd.
func (m *measurer) secureFD(fd, depth, uid, gid int) error {
	f := os.NewFile(uintptr(fd), "") //nolint:gosec // descriptors fit in uintptr
	defer func() { _ = f.Close() }()
	if err := m.enter(depth); err != nil {
		return err
	}
	if err := unix.Fchown(fd, uid, gid); err != nil {
		return err
	}
	if err := unix.Fchmod(fd, 0o700); err != nil {
		return err
	}
	for {
		names, err := f.Readdirnames(readBatch)
		for _, name := range names {
			if err := m.count(); err != nil {
				return err
			}
			var st unix.Stat_t
			if unix.Fstatat(fd, name, &st, unix.AT_SYMLINK_NOFOLLOW) != nil || st.Mode&unix.S_IFMT != unix.S_IFDIR {
				continue
			}
			child, err := unix.Openat(fd, name, dirFlags, 0)
			if err != nil {
				continue
			}
			// The directory opened must be the one looked at.
			var got unix.Stat_t
			if unix.Fstat(child, &got) != nil || got.Dev != st.Dev || got.Ino != st.Ino {
				_ = unix.Close(child)
				continue
			}
			if err := m.secureFD(child, depth+1, uid, gid); err != nil {
				return err
			}
		}
		if errors.Is(err, io.EOF) {
			return nil
		}
		if err != nil {
			return err
		}
	}
}

// secureTree is the same walk through a Tree, for filesystems that are not
// the real one: each change names the directory and is refused when another
// file has taken its place.
func (m *measurer) secureTree(fsys FS, t Tree, dir, rel string, depth, uid, gid int) error {
	if err := m.enter(depth); err != nil {
		return err
	}
	names, _, err := t.ReadDir(rel)
	if err != nil {
		return nil //nolint:nilerr // a directory removed or replaced meanwhile is not secured
	}
	for _, name := range names {
		if err := m.count(); err != nil {
			return err
		}
		r := path.Join(rel, name)
		info, err := t.Lstat(r)
		if err != nil || !info.IsDir() {
			continue
		}
		abs := path.Join(dir, r)
		if err := fsys.Chown(abs, info, uid, gid); err != nil {
			return err
		}
		if err := fsys.Chmod(abs, info, 0o700); err != nil {
			return err
		}
		if err := m.secureTree(fsys, t, dir, r, depth+1, uid, gid); err != nil {
			return err
		}
	}
	return nil
}
