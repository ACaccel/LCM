package hostsetup

import (
	"bytes"
	"os"
	"path/filepath"
	"strings"
	"syscall"
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
	if f := got[ComponentMachinePower].Files[1]; f.Path != PowerHelper || f.State != StateUpToDate || f.Installed != sha(exeData) || f.Embedded != "" || f.Detail != "pinned" {
		t.Errorf("machine helper = %+v", f)
	}
	// A self-deploy changes the running binary, not the pin.
	th.fs.file(testExe, "a newer self-deployed build", 0o755, aliceUID, aliceUID)
	th.host.Executable = "/nonexistent"
	if c := statusOf(t, th)[ComponentMachinePower]; c.State != StateUpToDate {
		t.Errorf("machine power after a new build: %+v", c)
	}
	th.fs.get(PowerHelper).mode = 0o775
	if f := statusOf(t, th)[ComponentMachinePower].Files[1]; f.State != StateOutdated || f.Detail != "mode 0775, want 0755" {
		t.Errorf("writable pin: %+v", f)
	}
	th.fs.get(PowerHelper).mode = 0o755

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
	if li, err := fsys.Lstat("/etc/l"); err != nil || fsys.Chmod("/etc/l", li, 0o777) == nil {
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

// TestOSFSMetadataChangesOnlyTheCheckedFile swaps the checked file for a
// link, a hard link or a FIFO, the races a service account that owns the
// parent could attempt, and expects every change to be refused.
func TestOSFSMetadataChangesOnlyTheCheckedFile(t *testing.T) {
	root := t.TempDir()
	fsys := OS{Root: root}
	uid, gid := os.Getuid(), os.Getgid()
	if err := fsys.Mkdir("/data", 0o775, uid, gid); err != nil {
		t.Fatal(err)
	}
	if info, err := fsys.Lstat("/data"); err != nil || info.Mode.Perm() != 0o775 {
		t.Fatalf("mkdir mode: %+v %v (the umask must not apply)", info, err)
	}
	if err := fsys.WriteFile("/secret", strings.NewReader("s"), 0o600, uid, gid); err != nil {
		t.Fatal(err)
	}
	if err := fsys.WriteFile("/file", strings.NewReader("f"), 0o600, uid, gid); err != nil {
		t.Fatal(err)
	}
	checked := map[string]Info{}
	for _, name := range []string{"/data", "/file"} {
		info, err := fsys.Lstat(name)
		if err != nil {
			t.Fatal(err)
		}
		checked[name] = info
	}
	// The checked files themselves can be changed.
	if err := fsys.Chmod("/file", checked["/file"], 0o640); err != nil {
		t.Fatal(err)
	}
	if err := fsys.Chown("/data", checked["/data"], uid, gid); err != nil {
		t.Fatal(err)
	}
	secret := filepath.Join(root, "secret")
	swaps := map[string]func(p string) error{
		"symbolic link": func(p string) error { return os.Symlink("secret", p) },
		"hard link":     func(p string) error { return os.Link(secret, p) },
		"FIFO":          func(p string) error { return syscall.Mkfifo(p, 0o600) },
	}
	for kind, swap := range swaps {
		for name, info := range checked {
			p := filepath.Join(root, name)
			if err := os.RemoveAll(p); err != nil {
				t.Fatal(err)
			}
			if err := swap(p); err != nil {
				t.Fatalf("%s: %v", kind, err)
			}
			if err := fsys.Chmod(name, info, 0o755); err == nil {
				t.Errorf("chmod of %s accepted a %s", name, kind)
			}
			if err := fsys.Chown(name, info, uid, gid); err == nil {
				t.Errorf("chown of %s accepted a %s", name, kind)
			}
			if err := os.Remove(p); err != nil {
				t.Fatal(err)
			}
		}
	}
	if info, _ := fsys.Lstat("/secret"); info.Mode.Perm() != 0o600 {
		t.Errorf("the swapped-in file changed to %04o", info.Mode.Perm())
	}
}

func TestCommandsAsAnotherAccountLeaveTheTerminal(t *testing.T) {
	if sysProcAttr(Command{Path: "true"}) != nil {
		t.Error("a command as root changes its process attributes")
	}
	attr := sysProcAttr(Command{Path: "lcm", As: &Account{Name: "lcm", UID: 990, GID: 991}})
	if attr == nil || !attr.Setsid || attr.Credential == nil || attr.Credential.Uid != 990 || attr.Credential.Gid != 991 ||
		attr.Credential.Groups == nil || len(attr.Credential.Groups) != 0 {
		t.Fatalf("attributes %+v", attr)
	}
}

func TestOSRunner(t *testing.T) {
	r := OSRunner{}
	if _, err := r.LookPath("sh"); err != nil {
		t.Skip("no sh:", err)
	}
	out, err := r.Run(t.Context(), Command{Path: "sh", Args: []string{"-c", "echo $PATH; echo oops >&2; exit 3"}})
	if exitCode(err) != 3 || strings.TrimSpace(string(out)) != SafePath || !strings.Contains(err.Error(), "oops") {
		t.Fatalf("out %q err %v", out, err)
	}
	if _, err := r.LookPath("a/b"); err == nil {
		t.Error("a name with a slash was looked up")
	}
}
