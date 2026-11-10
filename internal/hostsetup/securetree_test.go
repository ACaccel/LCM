package hostsetup

import (
	"errors"
	"os"
	"path/filepath"
	"testing"
)

func mkdirs(t *testing.T, mode os.FileMode, paths ...string) {
	t.Helper()
	for _, p := range paths {
		if err := os.MkdirAll(p, 0o700); err != nil {
			t.Fatal(err)
		}
		if err := os.Chmod(p, mode); err != nil { //nolint:gosec // a loose mode, so that securing it shows
			t.Fatal(err)
		}
	}
}

func modeOf(t *testing.T, p string) os.FileMode {
	t.Helper()
	fi, err := os.Lstat(p)
	if err != nil {
		t.Fatal(err)
	}
	return fi.Mode().Perm()
}

func TestSecureTreeSecuresEveryDirectoryAndFollowsNoLink(t *testing.T) {
	base := t.TempDir()
	root, outside := filepath.Join(base, "root"), filepath.Join(base, "outside")
	mkdirs(t, 0o777, root, filepath.Join(root, "a/b/c"), filepath.Join(root, "d"), outside)
	if err := os.WriteFile(filepath.Join(root, "a/file"), []byte("x"), 0o666); err != nil { //nolint:gosec // a loose mode, to show files are left alone
		t.Fatal(err)
	}
	if err := os.Symlink(outside, filepath.Join(root, "a/link")); err != nil {
		t.Fatal(err)
	}
	fileMode := modeOf(t, filepath.Join(root, "a/file"))
	fsys := OS{}
	want, err := fsys.Lstat(root)
	if err != nil {
		t.Fatal(err)
	}
	lim := MeasureLimits{MaxDepth: 10, MaxEntries: 100}
	if err := SecureTree(fsys, root, want, os.Getuid(), os.Getgid(), lim); err != nil {
		t.Fatal(err)
	}
	for _, d := range []string{"", "a", "a/b", "a/b/c", "d"} {
		if m := modeOf(t, filepath.Join(root, d)); m != 0o700 {
			t.Errorf("%q has mode %o", d, m)
		}
	}
	if m := modeOf(t, filepath.Join(root, "a/file")); m != fileMode {
		t.Errorf("a file was changed to %o", m)
	}
	if m := modeOf(t, outside); m != 0o777 {
		t.Errorf("a link was followed: %o", m)
	}

	// Another directory in place of the one that was looked at is refused.
	other := filepath.Join(base, "other")
	mkdirs(t, 0o777, other)
	if err := SecureTree(fsys, other, want, os.Getuid(), os.Getgid(), lim); err == nil {
		t.Error("a directory other than the expected one was secured")
	}
	if m := modeOf(t, other); m != 0o777 {
		t.Errorf("mode %o", m)
	}
}

func TestSecureTreeIsBounded(t *testing.T) {
	root := filepath.Join(t.TempDir(), "root")
	mkdirs(t, 0o777, root, filepath.Join(root, "a/b/c/d"))
	fsys := OS{}
	want, _ := fsys.Lstat(root)
	if err := SecureTree(fsys, root, want, os.Getuid(), os.Getgid(), MeasureLimits{MaxDepth: 2, MaxEntries: 100}); !errors.Is(err, ErrTreeDepth) {
		t.Errorf("depth: %v", err)
	}
	mkdirs(t, 0o777, filepath.Join(root, "x1"), filepath.Join(root, "x2"), filepath.Join(root, "x3"))
	if err := SecureTree(fsys, root, want, os.Getuid(), os.Getgid(), MeasureLimits{MaxDepth: 10, MaxEntries: 2}); !errors.Is(err, ErrTreeEntries) {
		t.Errorf("entries: %v", err)
	}
	if err := SecureTree(fsys, root, want, os.Getuid(), os.Getgid(), MeasureLimits{MaxDepth: 10, MaxEntries: 100, Expired: func() bool { return true }}); !errors.Is(err, ErrTreeExpired) {
		t.Errorf("time: %v", err)
	}
}

func TestMeasureTreeBelowResolvesInsideTheRoot(t *testing.T) {
	base := t.TempDir()
	root, outside := filepath.Join(base, "root"), filepath.Join(base, "outside")
	mkdirs(t, 0o755, filepath.Join(root, "real/sub"), filepath.Join(outside, "x"))
	if err := os.WriteFile(filepath.Join(root, "real/sub/f"), []byte("12345"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(outside, "x/big"), []byte("0123456789"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(outside, filepath.Join(root, "escape")); err != nil {
		t.Fatal(err)
	}
	lim := MeasureLimits{MaxDepth: 10, MaxEntries: 100}
	u, err := MeasureTreeBelow(OS{}, root, "real", lim)
	if err != nil || u.Bytes != 5 {
		t.Errorf("real: %+v %v", u, err)
	}
	// A link in an intermediate component is not followed.
	if u, err := MeasureTreeBelow(OS{}, root, "escape/x", lim); err == nil || u.Bytes != 0 {
		t.Errorf("escape: %+v %v", u, err)
	}
	if _, err := MeasureTreeBelow(OS{}, root, "missing", lim); !errors.Is(err, os.ErrNotExist) {
		t.Errorf("missing: %v", err)
	}
}

func TestSecureTreeFailsOnADirectoryItCannotOpen(t *testing.T) {
	if os.Geteuid() == 0 {
		t.Skip("root opens a directory of mode 0000")
	}
	root := filepath.Join(t.TempDir(), "root")
	locked := filepath.Join(root, "locked")
	mkdirs(t, 0o700, root, locked)
	if err := os.Chmod(locked, 0); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.Chmod(locked, 0o700) }) //nolint:gosec // a directory needs its search bit for t.TempDir to remove it
	fsys := OS{}
	want, err := fsys.Lstat(root)
	if err != nil {
		t.Fatal(err)
	}
	// A directory left unsecured must not look like a secured tree.
	if err := SecureTree(fsys, root, want, os.Getuid(), os.Getgid(), MeasureLimits{MaxDepth: 10, MaxEntries: 100}); err == nil {
		t.Fatal("SecureTree reported success with a directory it could not open")
	}
}
