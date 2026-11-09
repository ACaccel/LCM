package hostsetup

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// plainFS hides the real filesystem's type, so MeasureTree reads through
// OpenTree instead of descriptors.
type plainFS struct{ FS }

func TestMeasureTree(t *testing.T) {
	dir := t.TempDir()
	write := func(rel, data string) {
		t.Helper()
		p := filepath.Join(dir, rel)
		if err := os.MkdirAll(filepath.Dir(p), 0o700); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(p, []byte(data), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	write("tree/a", "12345")
	write("tree/sub/b", "123")
	write("outside/big", strings.Repeat("x", 1000))
	// Links count as entries but are never followed.
	if err := os.Symlink(filepath.Join(dir, "outside"), filepath.Join(dir, "tree/dirlink")); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(filepath.Join(dir, "outside/big"), filepath.Join(dir, "tree/filelink")); err != nil {
		t.Fatal(err)
	}
	for name, fsys := range map[string]FS{"descriptors": OS{Root: dir}, "tree": plainFS{OS{Root: dir}}} {
		t.Run(name, func(t *testing.T) {
			u, err := MeasureTree(fsys, "/tree", MeasureLimits{MaxDepth: 8, MaxEntries: 100})
			if err != nil || u.Bytes != 8 || u.Entries != 5 {
				t.Errorf("usage %+v, %v", u, err)
			}
			if _, err := MeasureTree(fsys, "/tree", MeasureLimits{MaxDepth: 8, MaxEntries: 3}); !errors.Is(err, ErrTreeEntries) {
				t.Errorf("too many entries: %v", err)
			}
			if _, err := MeasureTree(fsys, "/tree", MeasureLimits{MaxDepth: 0, MaxEntries: 100}); !errors.Is(err, ErrTreeDepth) {
				t.Errorf("too deep: %v", err)
			}
			if _, err := MeasureTree(fsys, "/tree", MeasureLimits{MaxDepth: 8, MaxEntries: 100, Expired: func() bool { return true }}); !errors.Is(err, ErrTreeExpired) {
				t.Errorf("expired: %v", err)
			}
			if _, err := MeasureTree(fsys, "/missing", MeasureLimits{MaxDepth: 8, MaxEntries: 100}); err == nil {
				t.Error("a missing root was measured")
			}
		})
	}
}

func TestMeasureTreeRefusesLinkedRoot(t *testing.T) {
	dir := t.TempDir()
	if err := os.Mkdir(filepath.Join(dir, "real"), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(filepath.Join(dir, "real"), filepath.Join(dir, "link")); err != nil {
		t.Fatal(err)
	}
	if _, err := MeasureTree(OS{Root: dir}, "/link", MeasureLimits{MaxDepth: 8, MaxEntries: 100}); err == nil {
		t.Error("a root that is a link was measured")
	}
}

// A chain of directories far deeper than the limit is refused quickly, on
// descriptors and through a tree alike.
func TestMeasureTreeDeepChain(t *testing.T) {
	dir := t.TempDir()
	if err := os.MkdirAll(filepath.Join(dir, "deep", strings.Repeat("d/", 300)), 0o700); err != nil {
		t.Fatal(err)
	}
	for name, fsys := range map[string]FS{"descriptors": OS{Root: dir}, "tree": plainFS{OS{Root: dir}}} {
		start := time.Now()
		u, err := MeasureTree(fsys, "/deep", MeasureLimits{MaxDepth: 256, MaxEntries: 1_000_000})
		if !errors.Is(err, ErrTreeDepth) {
			t.Errorf("%s: %+v, %v", name, u, err)
		}
		if d := time.Since(start); d > 5*time.Second {
			t.Errorf("%s: took %s", name, d)
		}
		if u, err := MeasureTree(fsys, "/deep", MeasureLimits{MaxDepth: 400, MaxEntries: 1_000_000}); err != nil || u.Entries != 300 {
			t.Errorf("%s within the depth limit: %+v, %v", name, u, err)
		}
	}
}
