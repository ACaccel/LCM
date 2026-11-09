package hostsetup

import (
	"bytes"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"path"
	"strings"
)

// The toolchain copies are root-owned and not writable by group or others,
// and their links must stay inside the copy, so that the operator who owns
// the source cannot redirect what lcm executes. The source is read through
// a Tree, so that links or directories swapped in while it is read cannot
// make root copy anything from outside it, and every entry's mode and size
// come from the descriptor its content is read from. Nothing from the
// source is executed as root.

// Bounds of one toolchain tree; the Go and Node.js toolchains are far below
// them.
const (
	maxTreeEntries = 200_000
	maxTreeBytes   = 4 << 30
	maxLinkHops    = 40
)

// copyPerm is the mode of a copied toolchain entry: the source permissions
// without group/other write and without setuid, setgid or sticky bits.
func copyPerm(m fs.FileMode) fs.FileMode { return m.Perm() &^ 0o022 }

// budget bounds a walk over a tree.
type budget struct {
	entries int
	bytes   int64
}

func (b *budget) entry() error {
	if b.entries++; b.entries > maxTreeEntries {
		return fmt.Errorf("more than %d entries", maxTreeEntries)
	}
	return nil
}

func (b *budget) add(n int64) error {
	if b.bytes += n; b.bytes > maxTreeBytes {
		return fmt.Errorf("more than %d bytes", int64(maxTreeBytes))
	}
	return nil
}

func sameFile(a, b Info) bool { return a.Dev == b.Dev && a.Ino == b.Ino }

// openSource opens a toolchain source: src must be a directory, not a
// symbolic link, and the opened root must be that same directory.
func openSource(fsys FS, src string) (Tree, error) {
	before, err := fsys.Lstat(src)
	if err != nil {
		return nil, err
	}
	if !before.IsDir() {
		return nil, fmt.Errorf("%s is not a directory (name the target of a symbolic link)", src)
	}
	t, err := fsys.OpenTree(src)
	if err != nil {
		return nil, err
	}
	after, err := t.Root()
	if err == nil && !sameFile(before, after) {
		err = fmt.Errorf("%s changed while it was opened", src)
	}
	if err != nil {
		_ = t.Close()
		return nil, err
	}
	return t, nil
}

// treeDiff returns where dst differs from a copy of src owned by uid:gid,
// or "" when it does not.
func treeDiff(src Tree, fsys FS, dst string, uid, gid int) (string, error) {
	di, err := fsys.Lstat(dst)
	if errors.Is(err, fs.ErrNotExist) {
		return "missing", nil
	}
	if err != nil {
		return "", err
	}
	if !di.IsDir() {
		return dst + " is not a directory", nil
	}
	return dirDiff(src, fsys, dst, ".", uid, gid, &budget{})
}

func dirDiff(src Tree, fsys FS, dst, rel string, uid, gid int, b *budget) (string, error) {
	srcNames, _, err := src.ReadDir(rel)
	if err != nil {
		return "", err
	}
	dstNames, err := fsys.ReadDir(path.Join(dst, rel))
	if err != nil {
		return "", err
	}
	if !equalStrings(srcNames, dstNames) {
		return "entries of " + rel + " differ", nil
	}
	if d, err := entryDiff(src, fsys, dst, rel, uid, gid); d != "" || err != nil {
		return d, err
	}
	for _, name := range srcNames {
		if err := b.entry(); err != nil {
			return "", err
		}
		p := path.Join(rel, name)
		si, err := src.Lstat(p)
		if err != nil {
			return "", err
		}
		if si.IsDir() {
			if d, err := dirDiff(src, fsys, dst, p, uid, gid, b); d != "" || err != nil {
				return d, err
			}
			continue
		}
		if d, err := entryDiff(src, fsys, dst, p, uid, gid); d != "" || err != nil {
			return d, err
		}
	}
	return "", nil
}

// entryDiff compares one entry of the two trees, without descending.
func entryDiff(src Tree, fsys FS, dst, rel string, uid, gid int) (string, error) {
	d := path.Join(dst, rel)
	si, err := src.Lstat(rel)
	if err != nil {
		return "", err
	}
	di, err := fsys.Lstat(d)
	if err != nil {
		return "", err
	}
	if si.Mode.Type() != di.Mode.Type() {
		return rel + " changed type", nil
	}
	if di.UID != uid || di.GID != gid {
		return rel + " has another owner", nil
	}
	switch {
	case si.IsLink():
		st, err := src.Readlink(rel)
		if err != nil {
			return "", err
		}
		dt, err := fsys.Readlink(d)
		if err != nil {
			return "", err
		}
		if st != dt {
			return rel + " links elsewhere", nil
		}
		return "", nil
	case si.IsDir(), si.IsRegular():
	default:
		return "", fmt.Errorf("%s is not a file, directory or symbolic link", rel)
	}
	if di.Mode.Perm() != copyPerm(si.Mode) || di.Mode&(fs.ModeSetuid|fs.ModeSetgid|fs.ModeSticky) != 0 {
		return rel + " has another mode", nil
	}
	if si.IsRegular() {
		if si.Size != di.Size {
			return rel + " differs", nil
		}
		same, err := sameContent(src, rel, fsys, d)
		if err != nil || !same {
			return rel + " differs", err
		}
	}
	return "", nil
}

func sameContent(src Tree, rel string, fsys FS, d string) (bool, error) {
	fa, _, err := src.Open(rel)
	if err != nil {
		return false, err
	}
	defer func() { _ = fa.Close() }()
	fb, err := fsys.Open(d)
	if err != nil {
		return false, err
	}
	defer func() { _ = fb.Close() }()
	ba, bb := make([]byte, 64<<10), make([]byte, 64<<10)
	for {
		na, ea := io.ReadFull(fa, ba)
		nb, eb := io.ReadFull(fb, bb)
		if na != nb || !bytes.Equal(ba[:na], bb[:nb]) {
			return false, nil
		}
		doneA := errors.Is(ea, io.EOF) || errors.Is(ea, io.ErrUnexpectedEOF)
		doneB := errors.Is(eb, io.EOF) || errors.Is(eb, io.ErrUnexpectedEOF)
		switch {
		case ea != nil && !doneA:
			return false, ea
		case eb != nil && !doneB:
			return false, eb
		case doneA || doneB:
			return doneA && doneB, nil
		}
	}
}

func equalStrings(a, b []string) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}

// copier copies a source tree into new directories owned by uid:gid.
type copier struct {
	src  Tree
	fsys FS
	// owners are the accepted owners of source entries: root and the
	// operator, so that a hard link to another user's file is refused.
	owners   map[int]bool
	uid, gid int
	// perm, when set, replaces copyPerm.
	perm func(Info) fs.FileMode
	budget
	// maxBytes, when set, replaces the default byte bound of the tree, and
	// beforeFile, when set, runs with each file's size before it is
	// written; either can refuse the copy.
	maxBytes   int64
	beforeFile func(size int64) error
}

func (c *copier) modeOf(info Info) fs.FileMode {
	if c.perm != nil {
		return c.perm(info)
	}
	return copyPerm(info.Mode)
}

func (c *copier) owned(rel string, info Info) error {
	if !c.owners[info.UID] {
		return fmt.Errorf("%s is owned by user %d, not root or the operator", rel, info.UID)
	}
	return nil
}

// dir copies the directory rel, which must still be the directory want.
func (c *copier) dir(rel, dst string, want Info) error {
	names, info, err := c.src.ReadDir(rel)
	if err != nil {
		return err
	}
	if !sameFile(info, want) {
		return fmt.Errorf("%s changed while it was copied", rel)
	}
	if err := c.owned(rel, info); err != nil {
		return err
	}
	if err := c.fsys.Mkdir(dst, 0o700, c.uid, c.gid); err != nil {
		return err
	}
	for _, name := range names {
		if err := c.entry(); err != nil {
			return err
		}
		r, d := path.Join(rel, name), path.Join(dst, name)
		li, err := c.src.Lstat(r)
		if err != nil {
			return err
		}
		if err := c.owned(r, li); err != nil {
			return err
		}
		switch {
		case li.IsDir():
			err = c.dir(r, d, li)
		case li.IsLink():
			var target string
			if target, err = c.src.Readlink(r); err == nil {
				err = c.fsys.Symlink(target, d, c.uid, c.gid)
			}
		case li.IsRegular():
			err = c.file(r, d, li)
		default:
			err = fmt.Errorf("%s is not a file, directory or symbolic link", r)
		}
		if err != nil {
			return err
		}
	}
	// The final mode last, so that a read-only source directory can still
	// be filled.
	made, err := c.fsys.Lstat(dst)
	if err != nil {
		return err
	}
	return c.fsys.Chmod(dst, made, c.modeOf(info))
}

// file copies the regular file rel, which must still be the file want.
func (c *copier) file(rel, dst string, want Info) error {
	f, info, err := c.src.Open(rel)
	if err != nil {
		return err
	}
	defer func() { _ = f.Close() }()
	if !sameFile(info, want) {
		return fmt.Errorf("%s changed while it was copied", rel)
	}
	if err := c.owned(rel, info); err != nil {
		return err
	}
	if err := c.add(info.Size); err != nil {
		return err
	}
	if c.maxBytes > 0 && c.bytes > c.maxBytes {
		return fmt.Errorf("%s: the tree is larger than the %d bytes it was sized at", rel, c.maxBytes)
	}
	if c.beforeFile != nil {
		if err := c.beforeFile(info.Size); err != nil {
			return err
		}
	}
	return c.fsys.WriteFile(dst, io.LimitReader(f, info.Size), c.modeOf(info), c.uid, c.gid)
}

// copyToolchain replaces dst with a checked copy of src, owned by uid:gid.
// The copy is made next to dst; its links are checked and verify runs on
// it before it is swapped in. Any failure removes the partial copy.
func copyToolchain(fsys FS, src, dst string, owners map[int]bool, uid, gid int, verify func(dir string) error) error {
	return copySubtree(fsys, src, ".", dst, &copier{owners: owners, uid: uid, gid: gid}, verify)
}

// copySubtree is copyToolchain for the directory rel of the tree src, which
// c describes apart from its source and filesystem. Names below src resolve
// inside src only.
func copySubtree(fsys FS, src, rel, dst string, c *copier, verify func(dir string) error) error {
	t, err := openSource(fsys, src)
	if err != nil {
		return err
	}
	defer func() { _ = t.Close() }()
	root, err := t.Lstat(rel)
	if err != nil {
		return err
	}
	if !root.IsDir() {
		return fmt.Errorf("%s is not a directory", path.Join(src, rel))
	}
	next, old := dst+".new", dst+".old"
	for _, p := range []string{next, old} {
		if err := fsys.RemoveAll(p); err != nil {
			return err
		}
	}
	c.src, c.fsys = t, fsys
	err = c.dir(rel, next, root)
	if err == nil {
		err = checkLinks(fsTree{fs: fsys, root: next}, next)
	}
	if err == nil && verify != nil {
		err = verify(next)
	}
	if err != nil {
		return errors.Join(err, fsys.RemoveAll(next))
	}
	if _, err := fsys.Lstat(dst); err == nil {
		if err := fsys.Rename(dst, old); err != nil {
			return errors.Join(err, fsys.RemoveAll(next))
		}
	}
	if err := fsys.Rename(next, dst); err != nil {
		return err
	}
	return fsys.RemoveAll(old)
}

// checkLinks refuses a tree with a symbolic link that leads outside it,
// directly or through other links, or with an entry that is not a file,
// directory or link. name names the tree in errors.
func checkLinks(t Tree, name string) error {
	return walkLinks(t, name, nil, &budget{})
}

func relPath(parts []string) string {
	if len(parts) == 0 {
		return "."
	}
	return path.Join(parts...)
}

func walkLinks(t Tree, name string, rel []string, b *budget) error {
	names, _, err := t.ReadDir(relPath(rel))
	if err != nil {
		return err
	}
	for _, n := range names {
		if err := b.entry(); err != nil {
			return fmt.Errorf("%s: %w", name, err)
		}
		p := append(append([]string{}, rel...), n)
		info, err := t.Lstat(relPath(p))
		if err != nil {
			return err
		}
		switch {
		case info.IsDir():
			err = walkLinks(t, name, p, b)
		case info.IsLink():
			err = resolveInside(t, name, p)
		case !info.IsRegular():
			err = fmt.Errorf("%s/%s is not a file, directory or symbolic link", name, relPath(p))
		}
		if err != nil {
			return err
		}
	}
	return nil
}

// resolveInside follows the link at link component by component, the way
// the kernel would, and fails as soon as the path would leave the tree.
// Missing components are taken literally, like realpath -m.
func resolveInside(t Tree, name string, link []string) error {
	where := relPath(link)
	target, err := t.Readlink(where)
	if err != nil {
		return err
	}
	escape := func() error {
		return fmt.Errorf("link %s/%s -> %s points outside %s", name, where, target, name)
	}
	if path.IsAbs(target) {
		return escape()
	}
	cur := append([]string{}, link[:len(link)-1]...)
	pending := strings.Split(target, "/")
	for hops := 0; len(pending) > 0; {
		c := pending[0]
		pending = pending[1:]
		switch c {
		case "", ".":
			continue
		case "..":
			if len(cur) == 0 {
				return escape()
			}
			cur = cur[:len(cur)-1]
			continue
		}
		next := append(append([]string{}, cur...), c)
		info, err := t.Lstat(relPath(next))
		switch {
		case errors.Is(err, fs.ErrNotExist):
			cur = next
		case err != nil:
			return err
		case info.IsLink():
			if hops++; hops > maxLinkHops {
				return fmt.Errorf("link %s/%s: too many levels of links", name, where)
			}
			l, err := t.Readlink(relPath(next))
			if err != nil {
				return err
			}
			if path.IsAbs(l) {
				return escape()
			}
			pending = append(strings.Split(l, "/"), pending...)
		default:
			cur = next
		}
	}
	return nil
}
