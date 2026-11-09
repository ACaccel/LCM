package hostsetup

import (
	"errors"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"syscall"
	"testing"
)

// realToolchain builds /src (a toolchain), /outside/secret and /other (a
// second directory) under a temporary root and returns the root.
func realToolchain(t *testing.T) string {
	t.Helper()
	root := t.TempDir()
	for name, data := range map[string]string{
		"src/bin/go": "go", "src/lib/a.txt": "a", "src/lib/deep/b.txt": "b",
		"outside/secret": "SECRET", "other/x": "x",
	} {
		p := filepath.Join(root, name)
		if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil { //nolint:gosec // a toolchain fixture with its usual modes
			t.Fatal(err)
		}
		if err := os.WriteFile(p, []byte(data), 0o644); err != nil { //nolint:gosec // a toolchain fixture with its usual modes
			t.Fatal(err)
		}
	}
	if err := os.Chmod(filepath.Join(root, "src/bin/go"), 0o775); err != nil { //nolint:gosec // an executable fixture
		t.Fatal(err)
	}
	if err := os.Symlink("lib", filepath.Join(root, "src/misc")); err != nil {
		t.Fatal(err)
	}
	return root
}

// hookFS runs a hook when the copy reads a directory or checks the source,
// so that tests can change the real tree at that moment.
type hookFS struct {
	OS
	afterLstat func(name string)
	onReadDir  func(rel string)
}

func (h hookFS) Lstat(name string) (Info, error) {
	info, err := h.OS.Lstat(name)
	if h.afterLstat != nil {
		h.afterLstat(name)
	}
	return info, err
}

func (h hookFS) OpenTree(root string) (Tree, error) {
	t, err := h.OS.OpenTree(root)
	if err != nil {
		return nil, err
	}
	return hookTree{Tree: t, onReadDir: h.onReadDir}, nil
}

type hookTree struct {
	Tree
	onReadDir func(rel string)
}

func (t hookTree) ReadDir(rel string) ([]string, Info, error) {
	if t.onReadDir != nil {
		t.onReadDir(rel)
	}
	return t.Tree.ReadDir(rel)
}

func copyReal(fsys FS, verify func(string) error) error {
	return copyToolchain(fsys, "/src", "/dst", map[int]bool{os.Getuid(): true}, os.Getuid(), os.Getgid(), verify)
}

// expectNoCopy checks that neither the copy nor its staging directory
// exists and that the secret was not copied anywhere.
func expectNoCopy(t *testing.T, root string) {
	t.Helper()
	for _, p := range []string{"dst", "dst.new"} {
		if _, err := os.Lstat(filepath.Join(root, p)); !errors.Is(err, fs.ErrNotExist) {
			t.Errorf("%s exists: %v", p, err)
		}
	}
	_ = filepath.WalkDir(root, func(p string, d fs.DirEntry, err error) error {
		if err == nil && d.Type().IsRegular() && !strings.HasPrefix(p, filepath.Join(root, "outside")) {
			if b, _ := os.ReadFile(p); string(b) == "SECRET" { //nolint:gosec // a file of the test's own tree
				t.Errorf("the secret was copied to %s", p)
			}
		}
		return nil
	})
}

func TestCopyToolchainOnRealFS(t *testing.T) {
	root := realToolchain(t)
	fsys := OS{Root: root}
	if err := copyReal(fsys, nil); err != nil {
		t.Fatal(err)
	}
	src, err := openSource(fsys, "/src")
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = src.Close() }()
	if d, err := treeDiff(src, fsys, "/dst", os.Getuid(), os.Getgid()); d != "" || err != nil {
		t.Fatalf("diff after copy: %q %v", d, err)
	}
	if info, _ := fsys.Lstat("/dst/bin/go"); info.Mode.Perm() != 0o755 {
		t.Errorf("bin/go mode %04o, want group write dropped", info.Mode.Perm())
	}
	if target, err := fsys.Readlink("/dst/misc"); err != nil || target != "lib" {
		t.Errorf("misc = %q %v", target, err)
	}
	// A second copy replaces the first.
	if err := os.WriteFile(filepath.Join(root, "src/lib/a.txt"), []byte("changed"), 0o644); err != nil { //nolint:gosec // fixture
		t.Fatal(err)
	}
	if err := copyReal(fsys, nil); err != nil {
		t.Fatal(err)
	}
	if b, _ := os.ReadFile(filepath.Join(root, "dst/lib/a.txt")); string(b) != "changed" { //nolint:gosec // fixture
		t.Errorf("a.txt = %q", b)
	}
}

func TestCopyToolchainRefusesSwaps(t *testing.T) {
	for name, tc := range map[string]struct {
		hook func(root string) hookFS
		want string
	}{
		"directory becomes an absolute link mid-copy": {func(root string) hookFS {
			return hookFS{OS: OS{Root: root}, onReadDir: func(rel string) {
				if rel == "lib" {
					swapForLink(t, filepath.Join(root, "src/lib"), filepath.Join(root, "outside"))
				}
			}}
		}, "escapes"},
		"directory becomes a relative link mid-copy": {func(root string) hookFS {
			return hookFS{OS: OS{Root: root}, onReadDir: func(rel string) {
				if rel == "lib" {
					swapForLink(t, filepath.Join(root, "src/lib"), "../outside")
				}
			}}
		}, "escapes"},
		"root becomes a link after its check": {func(root string) hookFS {
			done := false
			return hookFS{OS: OS{Root: root}, afterLstat: func(name string) {
				if name == "/src" && !done {
					done = true
					swapForLink(t, filepath.Join(root, "src"), filepath.Join(root, "other"))
				}
			}}
		}, "/src changed while it was opened"},
	} {
		t.Run(name, func(t *testing.T) {
			root := realToolchain(t)
			err := copyReal(tc.hook(root), nil)
			if err == nil || !strings.Contains(err.Error(), tc.want) {
				t.Fatalf("err = %v, want %q", err, tc.want)
			}
			expectNoCopy(t, root)
		})
	}
}

func swapForLink(t *testing.T, dir, target string) {
	t.Helper()
	if err := os.Rename(dir, dir+".away"); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(target, dir); err != nil {
		t.Fatal(err)
	}
}

func TestCopyToolchainRefusesUnsafeSources(t *testing.T) {
	t.Run("root replaced by a link", func(t *testing.T) {
		root := realToolchain(t)
		swapForLink(t, filepath.Join(root, "src"), filepath.Join(root, "other"))
		if err := copyReal(OS{Root: root}, nil); err == nil || !strings.Contains(err.Error(), "is not a directory") {
			t.Fatalf("err = %v", err)
		}
		expectNoCopy(t, root)
	})
	t.Run("link changed after the check", func(t *testing.T) {
		root := realToolchain(t)
		if err := os.Remove(filepath.Join(root, "src/misc")); err != nil {
			t.Fatal(err)
		}
		if err := os.Symlink("/etc", filepath.Join(root, "src/misc")); err != nil {
			t.Fatal(err)
		}
		if err := copyReal(OS{Root: root}, nil); err == nil || !strings.Contains(err.Error(), "points outside /dst.new") {
			t.Fatalf("err = %v", err)
		}
		expectNoCopy(t, root)
	})
	t.Run("FIFO", func(t *testing.T) {
		root := realToolchain(t)
		if err := syscall.Mkfifo(filepath.Join(root, "src/lib/pipe"), 0o644); err != nil {
			t.Skip("no FIFOs:", err)
		}
		if err := copyReal(OS{Root: root}, nil); err == nil || !strings.Contains(err.Error(), "not a file, directory or symbolic link") {
			t.Fatalf("err = %v", err)
		}
		expectNoCopy(t, root)
	})
	t.Run("another owner", func(t *testing.T) {
		root := realToolchain(t)
		err := copyToolchain(OS{Root: root}, "/src", "/dst", map[int]bool{0: true}, os.Getuid(), os.Getgid(), nil)
		if os.Getuid() != 0 && (err == nil || !strings.Contains(err.Error(), "not root or the operator")) {
			t.Fatalf("err = %v", err)
		}
		expectNoCopy(t, root)
	})
	t.Run("verification fails", func(t *testing.T) {
		root := realToolchain(t)
		if err := copyReal(OS{Root: root}, nil); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(root, "src/bin/go"), []byte("broken"), 0o755); err != nil { //nolint:gosec // fixture
			t.Fatal(err)
		}
		err := copyReal(OS{Root: root}, func(dir string) error {
			if dir != "/dst.new" {
				t.Errorf("verified %s", dir)
			}
			return errors.New("does not run")
		})
		if err == nil || !strings.Contains(err.Error(), "does not run") {
			t.Fatalf("err = %v", err)
		}
		if _, err := os.Lstat(filepath.Join(root, "dst.new")); !errors.Is(err, fs.ErrNotExist) {
			t.Errorf("partial copy kept: %v", err)
		}
		if b, _ := os.ReadFile(filepath.Join(root, "dst/bin/go")); string(b) != "go" { //nolint:gosec // fixture
			t.Errorf("the previous copy was replaced: %q", b)
		}
	})
}
