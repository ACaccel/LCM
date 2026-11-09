package hostsetup

import (
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"syscall"
)

// Info describes a file without following a final symbolic link.
type Info struct {
	Mode fs.FileMode // type bits and permissions, as in fs.FileMode
	UID  int
	GID  int
	Size int64
}

// IsDir, IsRegular and IsLink report the file type.
func (i Info) IsDir() bool     { return i.Mode.IsDir() }
func (i Info) IsRegular() bool { return i.Mode.IsRegular() }
func (i Info) IsLink() bool    { return i.Mode&fs.ModeSymlink != 0 }

// FS is the host filesystem as host setup sees it. Names are absolute,
// clean host paths; an implementation may place them under a test root.
// No method follows a symbolic link in the final path component.
type FS interface {
	Lstat(name string) (Info, error)
	// Open opens a regular file for reading.
	Open(name string) (io.ReadCloser, error)
	// ReadDir returns the sorted names in a directory.
	ReadDir(name string) ([]string, error)
	Readlink(name string) (string, error)
	Mkdir(name string, perm fs.FileMode, uid, gid int) error
	// WriteFile replaces name atomically with the content of r: readers see
	// either the old file or the complete new one with its final owner and
	// mode.
	WriteFile(name string, r io.Reader, perm fs.FileMode, uid, gid int) error
	Symlink(target, name string, uid, gid int) error
	// Chown and Chmod change a regular file or directory; they never
	// follow a symbolic link.
	Chown(name string, uid, gid int) error
	Chmod(name string, perm fs.FileMode) error
	Rename(from, to string) error
	Remove(name string) error
	RemoveAll(name string) error
	// MkdirTemp creates a new directory, mode 0700 and owned by the calling
	// process, in dir and returns its name.
	MkdirTemp(dir, pattern string) (string, error)
}

// OS is the real filesystem. A non-empty Root is prefixed to every name, so
// that tests can work on a directory tree instead of the host.
type OS struct {
	Root string
}

func (o OS) path(name string) (string, error) {
	if !filepath.IsAbs(name) || filepath.Clean(name) != name {
		return "", fmt.Errorf("host path %q is not absolute and clean", name)
	}
	if o.Root == "" {
		return name, nil
	}
	return filepath.Join(o.Root, name), nil
}

// Lstat implements FS.
func (o OS) Lstat(name string) (Info, error) {
	p, err := o.path(name)
	if err != nil {
		return Info{}, err
	}
	fi, err := os.Lstat(p)
	if err != nil {
		return Info{}, err
	}
	info := Info{Mode: fi.Mode(), Size: fi.Size(), UID: -1, GID: -1}
	if st, ok := fi.Sys().(*syscall.Stat_t); ok {
		info.UID, info.GID = int(st.Uid), int(st.Gid)
	}
	return info, nil
}

// Open implements FS.
func (o OS) Open(name string) (io.ReadCloser, error) {
	p, err := o.path(name)
	if err != nil {
		return nil, err
	}
	f, err := openNoFollow(p, 0)
	if err != nil {
		return nil, err
	}
	fi, err := f.Stat()
	if err == nil && !fi.Mode().IsRegular() {
		err = fmt.Errorf("%s is not a regular file", name)
	}
	if err != nil {
		_ = f.Close()
		return nil, err
	}
	return f, nil
}

// ReadDir implements FS.
func (o OS) ReadDir(name string) ([]string, error) {
	p, err := o.path(name)
	if err != nil {
		return nil, err
	}
	entries, err := os.ReadDir(p)
	if err != nil {
		return nil, err
	}
	names := make([]string, len(entries))
	for i, e := range entries {
		names[i] = e.Name()
	}
	sort.Strings(names)
	return names, nil
}

// Readlink implements FS.
func (o OS) Readlink(name string) (string, error) {
	p, err := o.path(name)
	if err != nil {
		return "", err
	}
	return os.Readlink(p)
}

// Mkdir implements FS. The directory is created private, whatever the
// umask, and gets its owner and final mode through a descriptor, so that a
// directory swapped in by whoever owns the parent is never changed.
func (o OS) Mkdir(name string, perm fs.FileMode, uid, gid int) error {
	p, err := o.path(name)
	if err != nil {
		return err
	}
	if err := os.Mkdir(p, 0o700); err != nil {
		return err
	}
	f, err := openNoFollow(p, syscall.O_DIRECTORY)
	if err != nil {
		return err
	}
	defer func() { _ = f.Close() }()
	fi, err := f.Stat()
	if err != nil {
		return err
	}
	if st, ok := fi.Sys().(*syscall.Stat_t); !ok || !fi.IsDir() || int(st.Uid) != os.Geteuid() {
		return fmt.Errorf("%s changed while it was created", name)
	}
	if err := f.Chown(uid, gid); err != nil {
		return err
	}
	return f.Chmod(perm)
}

// openNoFollow opens p for reading without following a symbolic link in
// its last component and without blocking on a FIFO; extra adds flags.
func openNoFollow(p string, extra int) (*os.File, error) {
	return os.OpenFile(p, os.O_RDONLY|syscall.O_NOFOLLOW|syscall.O_NONBLOCK|syscall.O_NOCTTY|extra, 0) //nolint:gosec // host paths chosen by host setup, opened without following a final link
}

// openForMetadata opens a regular file or directory to change its owner or
// mode through the descriptor: a link or another file type swapped in for
// it is refused rather than followed.
func openForMetadata(name, p string) (*os.File, error) {
	f, err := openNoFollow(p, 0)
	if err != nil {
		return nil, err
	}
	fi, err := f.Stat()
	if err == nil && !fi.IsDir() && !fi.Mode().IsRegular() {
		err = fmt.Errorf("%s is not a regular file or directory", name)
	}
	if err != nil {
		_ = f.Close()
		return nil, err
	}
	return f, nil
}

// WriteFile implements FS.
func (o OS) WriteFile(name string, r io.Reader, perm fs.FileMode, uid, gid int) error {
	p, err := o.path(name)
	if err != nil {
		return err
	}
	dir := filepath.Dir(p)
	f, err := os.CreateTemp(dir, "."+filepath.Base(p)+".lcm-*")
	if err != nil {
		return err
	}
	tmp := f.Name()
	defer func() {
		if tmp != "" {
			_ = os.Remove(tmp)
		}
	}()
	_, err = io.Copy(f, r)
	if err == nil {
		err = f.Chown(uid, gid)
	}
	if err == nil {
		err = f.Chmod(perm)
	}
	if err == nil {
		err = f.Sync()
	}
	if cerr := f.Close(); err == nil {
		err = cerr
	}
	if err != nil {
		return err
	}
	if err := os.Rename(tmp, p); err != nil {
		return err
	}
	tmp = ""
	return syncDir(dir)
}

func syncDir(dir string) error {
	d, err := os.Open(dir) //nolint:gosec // a directory of a path host setup manages
	if err != nil {
		return err
	}
	err = d.Sync()
	if cerr := d.Close(); err == nil {
		err = cerr
	}
	return err
}

// Symlink implements FS.
func (o OS) Symlink(target, name string, uid, gid int) error {
	p, err := o.path(name)
	if err != nil {
		return err
	}
	if err := os.Symlink(target, p); err != nil {
		return err
	}
	return os.Lchown(p, uid, gid)
}

// Chown implements FS for regular files and directories; it refuses
// symbolic links instead of following them.
func (o OS) Chown(name string, uid, gid int) error {
	p, err := o.path(name)
	if err != nil {
		return err
	}
	f, err := openForMetadata(name, p)
	if err != nil {
		return err
	}
	defer func() { _ = f.Close() }()
	return f.Chown(uid, gid)
}

// Chmod implements FS for regular files and directories; it refuses
// symbolic links instead of following them.
func (o OS) Chmod(name string, perm fs.FileMode) error {
	p, err := o.path(name)
	if err != nil {
		return err
	}
	f, err := openForMetadata(name, p)
	if err != nil {
		return err
	}
	defer func() { _ = f.Close() }()
	return f.Chmod(perm)
}

// Rename implements FS.
func (o OS) Rename(from, to string) error {
	pf, err := o.path(from)
	if err != nil {
		return err
	}
	pt, err := o.path(to)
	if err != nil {
		return err
	}
	return os.Rename(pf, pt)
}

// Remove implements FS.
func (o OS) Remove(name string) error {
	p, err := o.path(name)
	if err != nil {
		return err
	}
	return os.Remove(p)
}

// RemoveAll implements FS.
func (o OS) RemoveAll(name string) error {
	p, err := o.path(name)
	if err != nil {
		return err
	}
	if p == "/" || p == o.Root {
		return errors.New("refusing to remove the root directory")
	}
	return os.RemoveAll(p)
}

// MkdirTemp implements FS.
func (o OS) MkdirTemp(dir, pattern string) (string, error) {
	p, err := o.path(dir)
	if err != nil {
		return "", err
	}
	made, err := os.MkdirTemp(p, pattern)
	if err != nil {
		return "", err
	}
	return strings.TrimPrefix(made, o.Root), nil
}
