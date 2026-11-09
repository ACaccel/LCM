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
// the source cannot redirect what lcm executes. Nothing from the source is
// executed as root.

// copyPerm is the mode of a copied toolchain entry: the source permissions
// without group/other write and without setuid, setgid or sticky bits.
func copyPerm(m fs.FileMode) fs.FileMode { return m.Perm() &^ 0o022 }

// treeDiff returns where dst differs from a copy of src, or "" when it does
// not.
func treeDiff(fsys FS, src, dst string) (string, error) {
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
	return dirDiff(fsys, src, dst, ".")
}

func dirDiff(fsys FS, src, dst, rel string) (string, error) {
	srcNames, err := fsys.ReadDir(path.Join(src, rel))
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
	if d, err := entryDiff(fsys, src, dst, rel); d != "" || err != nil {
		return d, err
	}
	for _, name := range srcNames {
		p := path.Join(rel, name)
		si, err := fsys.Lstat(path.Join(src, p))
		if err != nil {
			return "", err
		}
		if si.IsDir() {
			if d, err := dirDiff(fsys, src, dst, p); d != "" || err != nil {
				return d, err
			}
			continue
		}
		if d, err := entryDiff(fsys, src, dst, p); d != "" || err != nil {
			return d, err
		}
	}
	return "", nil
}

// entryDiff compares one entry of the two trees, without descending.
func entryDiff(fsys FS, src, dst, rel string) (string, error) {
	s, d := path.Join(src, rel), path.Join(dst, rel)
	si, err := fsys.Lstat(s)
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
	if di.UID != 0 || di.GID != 0 {
		return rel + " is not owned by root", nil
	}
	switch {
	case si.IsLink():
		st, err := fsys.Readlink(s)
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
		return "", fmt.Errorf("%s is not a file, directory or symbolic link", s)
	}
	if di.Mode.Perm() != copyPerm(si.Mode) || di.Mode&(fs.ModeSetuid|fs.ModeSetgid|fs.ModeSticky) != 0 {
		return rel + " has another mode", nil
	}
	if si.IsRegular() {
		if si.Size != di.Size {
			return rel + " differs", nil
		}
		same, err := sameContent(fsys, s, d)
		if err != nil || !same {
			return rel + " differs", err
		}
	}
	return "", nil
}

func sameContent(fsys FS, a, b string) (bool, error) {
	fa, err := fsys.Open(a)
	if err != nil {
		return false, err
	}
	defer func() { _ = fa.Close() }()
	fb, err := fsys.Open(b)
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

// copyTree copies src to the new directory dst, owned by root.
func copyTree(fsys FS, src, dst string) error {
	si, err := fsys.Lstat(src)
	if err != nil {
		return err
	}
	if err := fsys.Mkdir(dst, 0o700, 0, 0); err != nil {
		return err
	}
	names, err := fsys.ReadDir(src)
	if err != nil {
		return err
	}
	for _, name := range names {
		s, d := path.Join(src, name), path.Join(dst, name)
		info, err := fsys.Lstat(s)
		if err != nil {
			return err
		}
		switch {
		case info.IsDir():
			err = copyTree(fsys, s, d)
		case info.IsLink():
			var target string
			if target, err = fsys.Readlink(s); err == nil {
				err = fsys.Symlink(target, d, 0, 0)
			}
		case info.IsRegular():
			err = copyFile(fsys, s, d, copyPerm(info.Mode))
		default:
			err = fmt.Errorf("%s is not a file, directory or symbolic link", s)
		}
		if err != nil {
			return err
		}
	}
	// The final mode last, so that a read-only source directory can still
	// be filled.
	return fsys.Chmod(dst, copyPerm(si.Mode))
}

func copyFile(fsys FS, src, dst string, perm fs.FileMode) error {
	f, err := fsys.Open(src)
	if err != nil {
		return err
	}
	defer func() { _ = f.Close() }()
	return fsys.WriteFile(dst, f, perm, 0, 0)
}

// maxLinkHops bounds the links followed while resolving one link.
const maxLinkHops = 40

// checkLinks refuses a tree with a symbolic link that leads outside it,
// directly or through other links, or with an entry that is not a file,
// directory or link.
func checkLinks(fsys FS, root string) error {
	return walkLinks(fsys, root, nil)
}

func walkLinks(fsys FS, root string, rel []string) error {
	dir := path.Join(append([]string{root}, rel...)...)
	names, err := fsys.ReadDir(dir)
	if err != nil {
		return err
	}
	for _, name := range names {
		p := append(append([]string{}, rel...), name)
		info, err := fsys.Lstat(path.Join(dir, name))
		if err != nil {
			return err
		}
		switch {
		case info.IsDir():
			err = walkLinks(fsys, root, p)
		case info.IsLink():
			err = resolveInside(fsys, root, p)
		case !info.IsRegular():
			err = fmt.Errorf("%s is not a file, directory or symbolic link", path.Join(dir, name))
		}
		if err != nil {
			return err
		}
	}
	return nil
}

// resolveInside follows the link at root/link component by component, the
// way the kernel would, and fails as soon as the path would leave root.
// Missing components are taken literally, like realpath -m.
func resolveInside(fsys FS, root string, link []string) error {
	where := path.Join(append([]string{root}, link...)...)
	escape := func(target string) error {
		return fmt.Errorf("link %s -> %s points outside %s", where, target, root)
	}
	target, err := fsys.Readlink(where)
	if err != nil {
		return err
	}
	if path.IsAbs(target) {
		return escape(target)
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
				return escape(target)
			}
			cur = cur[:len(cur)-1]
			continue
		}
		next := append(append([]string{}, cur...), c)
		info, err := fsys.Lstat(path.Join(append([]string{root}, next...)...))
		switch {
		case errors.Is(err, fs.ErrNotExist):
			cur = next
		case err != nil:
			return err
		case info.IsLink():
			if hops++; hops > maxLinkHops {
				return fmt.Errorf("link %s: too many levels of links", where)
			}
			t, err := fsys.Readlink(path.Join(append([]string{root}, next...)...))
			if err != nil {
				return err
			}
			if path.IsAbs(t) {
				return escape(target)
			}
			pending = append(strings.Split(t, "/"), pending...)
		default:
			cur = next
		}
	}
	return nil
}
