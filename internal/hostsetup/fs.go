package hostsetup

import (
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"syscall"
)

// Info describes a file without following a final symbolic link.
type Info struct {
	Mode fs.FileMode // type bits and permissions, as in fs.FileMode
	UID  int
	GID  int
	Size int64
	// Dev and Ino identify the file, so that a file can be recognized
	// after it was opened.
	Dev uint64
	Ino uint64
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
	// Chown and Chmod change the regular file or directory that want, an
	// earlier Lstat, describes. They refuse when name now is another file
	// (a link, a hard link or a replacement), so that a decision taken on
	// one file is never applied to another.
	Chown(name string, want Info, uid, gid int) error
	Chmod(name string, want Info, perm fs.FileMode) error
	Rename(from, to string) error
	Remove(name string) error
	RemoveAll(name string) error
	// MkdirTemp creates a new directory, mode 0700 and owned by the calling
	// process, in dir and returns its name.
	MkdirTemp(dir, pattern string) (string, error)
	// OpenTree opens the directory root for reading a tree that someone
	// else may change meanwhile.
	OpenTree(root string) (Tree, error)
}

// Tree reads a directory tree confined to its root: no name resolves
// outside the root, whatever links the tree contains or gains while it is
// read. Names are slash-separated and relative to the root, "." being the
// root itself.
type Tree interface {
	// Root describes the opened root directory.
	Root() (Info, error)
	Lstat(rel string) (Info, error)
	Readlink(rel string) (string, error)
	// ReadDir returns the sorted names in a directory and the description
	// of the directory that was opened to read them.
	ReadDir(rel string) ([]string, Info, error)
	// Open opens a regular file. The description comes from the opened
	// file, so it belongs to the content read.
	Open(rel string) (io.ReadCloser, Info, error)
	Close() error
}

func infoOf(fi fs.FileInfo) Info {
	info := Info{Mode: fi.Mode(), Size: fi.Size(), UID: -1, GID: -1}
	if st, ok := fi.Sys().(*syscall.Stat_t); ok {
		info.UID, info.GID = int(st.Uid), int(st.Gid)
		info.Dev, info.Ino = uint64(st.Dev), st.Ino //nolint:gosec,unconvert // Dev is unsigned on Linux; the conversion keeps other layouts building
	}
	return info
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
	return infoOf(fi), nil
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
// umask, and gets its owner and final mode only if it is still the
// directory just created, so that one swapped in by whoever owns the parent
// is never changed.
func (o OS) Mkdir(name string, perm fs.FileMode, uid, gid int) error {
	p, err := o.path(name)
	if err != nil {
		return err
	}
	if err := os.Mkdir(p, 0o700); err != nil {
		return err
	}
	info, err := o.Lstat(name)
	if err != nil {
		return err
	}
	if !info.IsDir() || info.UID != os.Geteuid() {
		return fmt.Errorf("%s changed while it was created", name)
	}
	if err := o.Chown(name, info, uid, gid); err != nil {
		return err
	}
	return o.Chmod(name, info, perm)
}

// openNoFollow opens p for reading without following a symbolic link in
// its last component and without blocking on a FIFO; extra adds flags.
func openNoFollow(p string, extra int) (*os.File, error) {
	return os.OpenFile(p, os.O_RDONLY|syscall.O_NOFOLLOW|syscall.O_NONBLOCK|syscall.O_NOCTTY|extra, 0) //nolint:gosec // host paths chosen by host setup, opened without following a final link
}

// oPath is Linux's O_PATH (the same on every architecture Go supports),
// which package syscall does not define.
const oPath = 0x200000

// withExpected runs change on a /proc/self/fd path that names exactly the
// file want describes. The file is opened with O_PATH, which neither
// follows a final link nor opens a device, and must have want's type,
// device and inode; a link, a hard link or any other file swapped in after
// want was taken is refused instead of changed.
func withExpected(name, p string, want Info, change func(proc string) error) error {
	flags := oPath | syscall.O_NOFOLLOW | syscall.O_CLOEXEC
	switch {
	case want.IsDir():
		flags |= syscall.O_DIRECTORY
	case want.IsRegular():
	default:
		return fmt.Errorf("%s is not a regular file or directory", name)
	}
	fd, err := syscall.Open(p, flags, 0)
	if err != nil {
		return &fs.PathError{Op: "open", Path: name, Err: err}
	}
	defer func() { _ = syscall.Close(fd) }()
	var st syscall.Stat_t
	if err := syscall.Fstat(fd, &st); err != nil {
		return &fs.PathError{Op: "fstat", Path: name, Err: err}
	}
	got := Info{Mode: fileType(st.Mode), Dev: uint64(st.Dev), Ino: st.Ino} //nolint:gosec,unconvert // Dev is unsigned on Linux; the conversion keeps other layouts building
	if got.Mode.Type() != want.Mode.Type() || !sameFile(got, want) {
		return fmt.Errorf("%s changed since it was checked; nothing was changed", name)
	}
	return change("/proc/self/fd/" + strconv.Itoa(fd))
}

// fileType converts the type bits of a stat mode.
func fileType(m uint32) fs.FileMode {
	switch m & syscall.S_IFMT {
	case syscall.S_IFDIR:
		return fs.ModeDir
	case syscall.S_IFREG:
		return 0
	case syscall.S_IFLNK:
		return fs.ModeSymlink
	}
	return fs.ModeIrregular
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

// Chown implements FS.
func (o OS) Chown(name string, want Info, uid, gid int) error {
	p, err := o.path(name)
	if err != nil {
		return err
	}
	return withExpected(name, p, want, func(proc string) error { return os.Chown(proc, uid, gid) })
}

// Chmod implements FS.
func (o OS) Chmod(name string, want Info, perm fs.FileMode) error {
	p, err := o.path(name)
	if err != nil {
		return err
	}
	return withExpected(name, p, want, func(proc string) error { return os.Chmod(proc, perm) })
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

// OpenTree implements FS with os.Root, which resolves every name below the
// root one component at a time without leaving it.
func (o OS) OpenTree(root string) (Tree, error) {
	p, err := o.path(root)
	if err != nil {
		return nil, err
	}
	r, err := os.OpenRoot(p)
	if err != nil {
		return nil, err
	}
	return osTree{r: r}, nil
}

type osTree struct{ r *os.Root }

// treeOpenFlags never block on a FIFO and never acquire a terminal.
const treeOpenFlags = os.O_RDONLY | syscall.O_NONBLOCK | syscall.O_NOCTTY

func (t osTree) Root() (Info, error) {
	fi, err := t.r.Stat(".")
	if err != nil {
		return Info{}, err
	}
	return infoOf(fi), nil
}

func (t osTree) Lstat(rel string) (Info, error) {
	fi, err := t.r.Lstat(rel)
	if err != nil {
		return Info{}, err
	}
	return infoOf(fi), nil
}

func (t osTree) Readlink(rel string) (string, error) { return t.r.Readlink(rel) }

func (t osTree) ReadDir(rel string) ([]string, Info, error) {
	f, err := t.r.OpenFile(rel, treeOpenFlags|syscall.O_DIRECTORY, 0)
	if err != nil {
		return nil, Info{}, err
	}
	defer func() { _ = f.Close() }()
	fi, err := f.Stat()
	if err != nil {
		return nil, Info{}, err
	}
	if !fi.IsDir() {
		return nil, Info{}, fmt.Errorf("%s is not a directory", rel)
	}
	names, err := f.Readdirnames(-1)
	if err != nil {
		return nil, Info{}, err
	}
	sort.Strings(names)
	return names, infoOf(fi), nil
}

func (t osTree) Open(rel string) (io.ReadCloser, Info, error) {
	f, err := t.r.OpenFile(rel, treeOpenFlags, 0)
	if err != nil {
		return nil, Info{}, err
	}
	fi, err := f.Stat()
	if err == nil && !fi.Mode().IsRegular() {
		err = fmt.Errorf("%s is not a regular file", rel)
	}
	if err != nil {
		_ = f.Close()
		return nil, Info{}, err
	}
	return f, infoOf(fi), nil
}

func (t osTree) Close() error { return t.r.Close() }

// fsTree is a Tree over FS paths, for trees that only root can change, such
// as a fresh toolchain copy, and for filesystems without descriptors.
type fsTree struct {
	fs   FS
	root string
}

func (t fsTree) name(rel string) string { return path.Join(t.root, rel) }

func (t fsTree) Root() (Info, error) { return t.fs.Lstat(t.root) }

func (t fsTree) Lstat(rel string) (Info, error) { return t.fs.Lstat(t.name(rel)) }

func (t fsTree) Readlink(rel string) (string, error) { return t.fs.Readlink(t.name(rel)) }

func (t fsTree) ReadDir(rel string) ([]string, Info, error) {
	info, err := t.fs.Lstat(t.name(rel))
	if err != nil {
		return nil, Info{}, err
	}
	if !info.IsDir() {
		return nil, Info{}, fmt.Errorf("%s is not a directory", t.name(rel))
	}
	names, err := t.fs.ReadDir(t.name(rel))
	return names, info, err
}

func (t fsTree) Open(rel string) (io.ReadCloser, Info, error) {
	info, err := t.fs.Lstat(t.name(rel))
	if err != nil {
		return nil, Info{}, err
	}
	f, err := t.fs.Open(t.name(rel))
	return f, info, err
}

func (t fsTree) Close() error { return nil }
