package hostsetup

import (
	"bytes"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func statusOf(t *testing.T, th *testHost) map[string]ComponentStatus {
	t.Helper()
	st, err := Status(th.host)
	if err != nil {
		t.Fatal(err)
	}
	out := map[string]ComponentStatus{}
	for _, c := range st {
		out[c.Name] = c
	}
	return out
}

func TestStatus(t *testing.T) {
	th := newTestHost(t)
	for name, c := range statusOf(t, th) {
		if c.State != StateNotInstalled {
			t.Errorf("fresh host: %s is %s", name, c.State)
		}
	}

	th = converged(t)
	th.cfg.Components.MachinePower = true
	th.cfg.Components.SecurityHost = &SecurityHost{MachineID: "m-1"}
	th.writeConfig(th.cfg)
	th.converge()
	th.fs.resetOps()
	got := statusOf(t, th)
	for name, c := range got {
		if c.State != StateUpToDate {
			t.Errorf("converged: %s is %s: %+v", name, c.State, c.Files)
		}
	}
	if w := th.fs.writes(); len(w) > 0 {
		t.Fatalf("status wrote %v", w)
	}
	if f := got[ComponentMachinePower].Files[1]; f.Path != PowerHelper || f.Installed != sha(exeData) || f.Embedded != sha(exeData) {
		t.Errorf("machine helper = %+v", f)
	}

	th.fs.file(SelfInstall, "old", 0o755, 0, 0)
	th.fs.get(Unit).mode = 0o664
	th.fs.denied[Sudoers] = true
	th.fs.remove(PowerSudoers)
	th.fs.file(SecurityHelper, string(asset("security-host/helper.py")), 0o644, aliceUID, aliceUID)
	got = statusOf(t, th)
	want := map[string]struct{ component, file, detail string }{
		SelfInstall:    {StateOutdated, StateOutdated, ""},
		Unit:           {StateOutdated, StateOutdated, "mode 0664, want 0644"},
		Sudoers:        {StateOutdated, StateUnknown, "not readable as this user"},
		PowerSudoers:   {StateMissing, StateMissing, ""},
		PowerWrapper:   {StateMissing, StateUpToDate, ""},
		SecurityHelper: {StateOutdated, StateOutdated, "owned by 1000:1000, not root"},
	}
	for _, c := range got {
		for _, f := range c.Files {
			w, ok := want[f.Path]
			if !ok {
				continue
			}
			if c.State != w.component || f.State != w.file || f.Detail != w.detail {
				t.Errorf("%s in %s: component %s, file %s %q; want %+v", f.Path, c.Name, c.State, f.State, f.Detail, w)
			}
		}
	}

	th.fs.file(SelfInstall, string(asset("self-install")), 0o755, 0, 0)
	th.fs.get(Unit).mode = 0o644
	if c := statusOf(t, th)[ComponentCore]; c.State != StateUnknown {
		t.Errorf("core with an unreadable file: %s", c.State)
	}
}

func TestOSFS(t *testing.T) {
	root := t.TempDir()
	fsys := OS{Root: root}
	uid, gid := os.Getuid(), os.Getgid()
	if err := fsys.Mkdir("/etc", 0o755, uid, gid); err != nil {
		t.Fatal(err)
	}
	if _, err := fsys.Lstat("etc"); err == nil {
		t.Fatal("a relative name was accepted")
	}
	if _, err := fsys.Lstat("/etc/../x"); err == nil {
		t.Fatal("an unclean name was accepted")
	}
	if err := fsys.WriteFile("/etc/a", strings.NewReader("one"), 0o640, uid, gid); err != nil {
		t.Fatal(err)
	}
	if err := fsys.WriteFile("/etc/a", strings.NewReader("two"), 0o600, uid, gid); err != nil {
		t.Fatal(err)
	}
	info, err := fsys.Lstat("/etc/a")
	if err != nil || !info.IsRegular() || info.Mode.Perm() != 0o600 || info.UID != uid || info.Size != 3 {
		t.Fatalf("lstat = %+v %v", info, err)
	}
	if sum, err := hashFile(fsys, "/etc/a"); err != nil || sum != sha("two") {
		t.Fatalf("content: %s %v", sum, err)
	}
	if err := fsys.Symlink("a", "/etc/l", uid, gid); err != nil {
		t.Fatal(err)
	}
	if _, err := fsys.Open("/etc/l"); err == nil {
		t.Fatal("open followed a link")
	}
	if err := fsys.Chmod("/etc/l", 0o777); err == nil {
		t.Fatal("chmod followed a link")
	}
	if target, err := fsys.Readlink("/etc/l"); err != nil || target != "a" {
		t.Fatalf("readlink = %q %v", target, err)
	}
	names, err := fsys.ReadDir("/etc")
	if err != nil || strings.Join(names, ",") != "a,l" {
		t.Fatalf("readdir = %v %v (temporary files left?)", names, err)
	}
	tmp, err := fsys.MkdirTemp("/etc", "x-")
	if err != nil || !strings.HasPrefix(tmp, "/etc/x-") {
		t.Fatalf("mkdirtemp = %q %v", tmp, err)
	}
	if err := fsys.Rename("/etc/a", tmp+"/b"); err != nil {
		t.Fatal(err)
	}
	if b, err := os.ReadFile(filepath.Join(root, tmp, "b")); err != nil || !bytes.Equal(b, []byte("two")) { //nolint:gosec // a file in the test's own directory
		t.Fatalf("renamed file: %q %v", b, err)
	}
	if err := fsys.RemoveAll(tmp); err != nil {
		t.Fatal(err)
	}
	if err := fsys.RemoveAll("/"); err == nil {
		t.Fatal("removed the root")
	}
	if err := fsys.Remove("/etc/l"); err != nil {
		t.Fatal(err)
	}
}
