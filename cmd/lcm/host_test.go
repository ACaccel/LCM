package main

import (
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/ACaccel/lcm/deploy"
	"github.com/ACaccel/lcm/internal/hostsetup"
)

// testHostRoot returns an app whose host commands work on a directory tree
// with an lcm executable at /bin/lcm, as an unprivileged process.
func testHostRoot(t *testing.T) (*app, string, func(args ...string) result) {
	t.Helper()
	a, out, errb, _ := newApp(t, "")
	root := t.TempDir()
	writeUnder(t, root, "/bin/lcm", []byte("lcm executable"))
	a.hostSetup = func() (hostsetup.Host, error) {
		return hostsetup.Host{FS: hostsetup.OS{Root: root}, Runner: hostsetup.OSRunner{}, Executable: "/bin/lcm"}, nil
	}
	return a, root, func(args ...string) result { return runCLI(t, a, out, errb, args...) }
}

func writeUnder(t *testing.T, root, name string, data []byte) {
	t.Helper()
	p := filepath.Join(root, name)
	if err := os.MkdirAll(filepath.Dir(p), 0o750); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(p, data, 0o600); err != nil {
		t.Fatal(err)
	}
}

func TestHostUsage(t *testing.T) {
	_, _, cli := testHostRoot(t)
	for _, args := range [][]string{{"host"}, {"host", "bogus"}, {"host", "apply", "extra"}, {"host", "status", "x"},
		{"host", "init"}, {"host", "apply", "--bogus"}} {
		if r := cli(args...); r.code != 2 || !strings.Contains(r.stderr, "Usage:") {
			t.Errorf("%v: %+v", args, r)
		}
	}
	if r := cli("host", "apply", "-h"); r.code != 0 || !strings.Contains(r.stderr, "-check") {
		t.Errorf("apply -h: %+v", r)
	}
}

func TestHostCommandsNeedRoot(t *testing.T) {
	_, _, cli := testHostRoot(t)
	for _, args := range [][]string{{"host", "apply"}, {"host", "apply", "--check"}, {"host", "init", "--operator", "alice"}} {
		if r := cli(args...); r.code != 1 || !strings.Contains(r.stderr, "must run as root") {
			t.Errorf("%v: %+v", args, r)
		}
	}
}

func TestHostStatus(t *testing.T) {
	_, root, cli := testHostRoot(t)
	r := cli("host", "status")
	if r.code != 0 {
		t.Fatalf("%+v", r)
	}
	for _, c := range []string{"core", "machine-power", "security"} {
		if got := statusLine(r.stdout, c); got != c+" not installed" {
			t.Errorf("%s: %q\n%s", c, got, r.stdout)
		}
	}
	if strings.Contains(r.stdout, "sudo bin/lcm host apply") {
		t.Errorf("hint without anything to apply:\n%s", r.stdout)
	}

	unit, err := fs.ReadFile(deploy.Files, "lcm.service")
	if err != nil {
		t.Fatal(err)
	}
	writeUnder(t, root, hostsetup.Unit, unit)
	writeUnder(t, root, hostsetup.SelfInstall, []byte("older script"))
	r = cli("host", "status")
	for prefix, want := range map[string]string{
		"core":                   "core outdated",
		hostsetup.SelfInstall:    hostsetup.SelfInstall + " outdated installed",
		hostsetup.Sudoers:        hostsetup.Sudoers + " missing embedded",
		hostsetup.Unit:           hostsetup.Unit + " outdated installed",
		hostsetup.SecurityHelper: hostsetup.SecurityHelper + " missing embedded",
	} {
		if got := statusLine(r.stdout, prefix); !strings.HasPrefix(got, want) {
			t.Errorf("%s: %q, want %q\n%s", prefix, got, want, r.stdout)
		}
	}
	if r.code != 0 || !strings.Contains(r.stdout, "not root)") || !strings.Contains(r.stdout, "sudo bin/lcm host apply") {
		t.Errorf("outdated core:\n%s", r.stdout)
	}
	for _, line := range strings.Split(r.stdout, "\n") {
		if strings.HasSuffix(line, " ") {
			t.Errorf("trailing space: %q", line)
		}
	}
}

// statusLine returns the first line of out whose first field is name,
// with its fields joined by single spaces.
func statusLine(out, name string) string {
	for _, line := range strings.Split(out, "\n") {
		if f := strings.Fields(line); len(f) > 0 && f[0] == name {
			return strings.Join(f, " ")
		}
	}
	return ""
}
