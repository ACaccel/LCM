package hostsetup

import (
	"os"
	"path/filepath"
	"testing"
)

func TestCopyTreeExcludes(t *testing.T) {
	root := realToolchain(t)
	err := CopyTree(OS{Root: root}, "/src", "lib", "/dst", CopyOptions{
		Owners: map[int]bool{os.Getuid(): true}, UID: os.Getuid(), GID: os.Getgid(),
		Exclude: func(rel string) bool { return rel == "lib/deep" },
	})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := os.Lstat(filepath.Join(root, "dst/a.txt")); err != nil {
		t.Errorf("a.txt was not copied: %v", err)
	}
	if _, err := os.Lstat(filepath.Join(root, "dst/deep")); err == nil {
		t.Error("the excluded directory was copied")
	}
}
