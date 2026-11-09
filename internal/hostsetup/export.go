package hostsetup

import (
	"fmt"
	"io/fs"
	"path"
)

// The primitives below serve the deploy helper (internal/deployhelper),
// which acts as root on trees that other accounts own and must keep the
// same guarantees as host setup.

// CopyOptions describe a confined tree copy.
type CopyOptions struct {
	// Owners are the accepted owners of source entries; an entry of another
	// owner, such as a hard link to someone else's file, is refused.
	Owners map[int]bool
	// UID and GID own the copy.
	UID, GID int
	// Perm returns the mode of a copied file or directory; nil keeps the
	// source permissions without group/other write and special bits.
	Perm func(Info) fs.FileMode
	// Verify runs on the finished copy before it replaces dst.
	Verify func(dir string) error
	// Exclude, when set, leaves out the entries (and everything below them)
	// whose path relative to src it accepts.
	Exclude func(rel string) bool
}

// CopyTree replaces dst with a copy of the directory rel of the tree src.
// The source is read through a Tree confined to src, every entry is checked
// against what was opened, the copy is bounded in entries and bytes, and it
// is refused when one of its symbolic links leads outside it. Any failure
// removes the partial copy and keeps dst.
func CopyTree(fsys FS, src, rel, dst string, o CopyOptions) error {
	return copySubtree(fsys, src, rel, dst, &copier{owners: o.Owners, uid: o.UID, gid: o.GID, perm: o.Perm, exclude: o.Exclude}, o.Verify)
}

// HandOver gives the files and directories of a tree that only root can
// change, such as a fresh copy, to uid:gid. It works bottom-up, so that each
// directory stays root's until everything below it has been handed over:
// the new owner can never redirect a later change. Symbolic links keep
// their owner.
func HandOver(fsys FS, dir string, uid, gid int) error {
	info, err := fsys.Lstat(dir)
	if err != nil {
		return err
	}
	if !info.IsDir() {
		return fmt.Errorf("%s is not a directory", dir)
	}
	return handOver(fsys, dir, info, uid, gid, &budget{})
}

func handOver(fsys FS, dir string, want Info, uid, gid int, b *budget) error {
	names, err := fsys.ReadDir(dir)
	if err != nil {
		return err
	}
	for _, n := range names {
		if err := b.entry(); err != nil {
			return err
		}
		p := path.Join(dir, n)
		info, err := fsys.Lstat(p)
		if err != nil {
			return err
		}
		switch {
		case info.IsDir():
			err = handOver(fsys, p, info, uid, gid, b)
		case info.IsRegular():
			err = fsys.Chown(p, info, uid, gid)
		case info.IsLink():
		default:
			err = fmt.Errorf("%s is not a file, directory or symbolic link", p)
		}
		if err != nil {
			return err
		}
	}
	return fsys.Chown(dir, want, uid, gid)
}

// Printable replaces control and format characters and invalid UTF-8 in s
// by "?" and shortens it to at most max bytes, so that text another account
// chose can be shown on a terminal or recorded.
func Printable(s string, max int) string { return printable(s, max) }

// SystemAccountProblem describes why acc cannot be the dedicated system
// account with home directory home, the comment and no login shell that
// host setup creates, or returns "".
func SystemAccountProblem(fsys FS, acc Account, home, comment string) string {
	return systemAccountProblem(acc, home, comment, uidMin(fsys))
}
