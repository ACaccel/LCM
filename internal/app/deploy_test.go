package app_test

import (
	"bytes"
	"errors"
	"io/fs"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"syscall"
	"testing"

	"github.com/ACaccel/lcm/internal/configsync"
	"github.com/ACaccel/lcm/internal/templates"
)

// deployDir holds the host deployment files (bootstrap, self-install, the
// unit and the documents seed.sh applies).
var deployDir = filepath.Join("..", "..", "deploy")

func readDeploy(t *testing.T, name string) []byte {
	t.Helper()
	b, err := fs.ReadFile(os.DirFS(deployDir), name)
	if err != nil {
		t.Fatal(err)
	}
	return b
}

// deployPolicyParams are the auto-deploy template parameters of
// deploy/lcm-auto-deploy.yaml.
var deployPolicyParams = map[string]any{ //nolint:gosec // notifyTokenSecret is a secret name, not a value
	"service": "lcm", "repo": "https://github.com/ACaccel/LCM.git", "branch": "main",
	"requireCI": true, "ciRepo": "ACaccel/LCM", "ciWorkflows": []any{"ci.yml"}, "healthTimeout": "3m",
	"notifyTarget": "konata", "notifyUrl": "http://127.0.0.1:4100/notifications/lcm-deploy", "notifyTokenSecret": "konata-notify-key",
}

func TestDeployPolicyIsTheTemplateRendering(t *testing.T) {
	tpl, ok := templates.Get("auto-deploy")
	if !ok {
		t.Fatal("no auto-deploy template")
	}
	want, err := tpl.Render("lcm-auto-deploy", deployPolicyParams)
	if err != nil {
		t.Fatal(err)
	}
	if got := readDeploy(t, "lcm-auto-deploy.yaml"); !bytes.Equal(got, want) {
		t.Fatalf("deploy/lcm-auto-deploy.yaml is not the rendering of the auto-deploy template with deployPolicyParams; replace it with:\n%s", want)
	}
}

// TestDeployDocumentsApply applies the documents seed.sh applies, as
// `lcm apply` would, and expects both to be created without findings.
func TestDeployDocumentsApply(t *testing.T) {
	h := newHarness(t)
	var docs []configsync.Document
	for _, name := range []string{"lcm-self.service.yaml", "lcm-auto-deploy.yaml"} {
		docs = append(docs, configsync.SplitDocuments(name, readDeploy(t, name))...)
	}
	results, err := h.app.ConfigSync.Apply(h.ctx, docs, configsync.Options{})
	if err != nil {
		t.Fatal(err)
	}
	if len(results) != 2 {
		t.Fatalf("results = %+v, want two", results)
	}
	for _, r := range results {
		if r.Action != configsync.ActionCreate || len(r.Issues) > 0 || len(r.Warnings) > 0 {
			t.Errorf("%s/%s: %s %s issues=%v warnings=%v", r.Kind, r.Name, r.Action, r.Reason, r.Issues, r.Warnings)
		}
	}
	rec, err := h.app.Catalog.Get(h.ctx, "lcm")
	if err != nil {
		t.Fatal(err)
	}
	src := rec.Spec.Spec.Source
	if src == nil || src.Install != "sudo -n /usr/local/libexec/lcm/self-install /var/lib/lcm/work/lcm/bin/lcm" {
		t.Errorf("install command = %+v, want exactly the argv the sudoers rule allows", src)
	}
	if rule := string(readDeploy(t, "sudoers")); !strings.Contains(rule, "NOPASSWD: /usr/local/libexec/lcm/self-install /var/lib/lcm/work/lcm/bin/lcm\n") {
		t.Errorf("deploy/sudoers does not allow the service's install command:\n%s", rule)
	}
}

// selfInstallEnv is a test-mode prefix for deploy/self-install.
type selfInstallEnv struct {
	t      *testing.T
	root   string
	script string
	src    string
	bin    string
}

func newSelfInstallEnv(t *testing.T) *selfInstallEnv {
	t.Helper()
	if os.Geteuid() == 0 {
		t.Skip("self-install test mode is refused as root")
	}
	for _, bin := range []string{"sh", "flock", "timeout", "stat"} {
		if _, err := exec.LookPath(bin); err != nil {
			t.Skipf("%s is not installed", bin)
		}
	}
	root := t.TempDir()
	e := &selfInstallEnv{t: t, root: root, src: filepath.Join(root, "var/lib/lcm/work/lcm/bin/lcm"),
		bin: filepath.Join(root, "usr/local/bin/lcm")}
	for _, d := range []string{"var/lib/lcm/work/lcm/bin", "usr/local/bin", "usr/local/libexec/lcm", "run"} {
		if err := os.MkdirAll(filepath.Join(root, d), 0o750); err != nil {
			t.Fatal(err)
		}
	}
	e.script = filepath.Join(root, "usr/local/libexec/lcm/self-install")
	e.write(e.script, string(readDeploy(t, "self-install")))
	e.write(e.bin, "#!/bin/sh\necho 'lcm v1 (c1) now'\n")
	return e
}

func (e *selfInstallEnv) write(path, content string) {
	e.t.Helper()
	if err := os.WriteFile(path, []byte(content), 0o700); err != nil { //nolint:gosec // executable test fixture
		e.t.Fatal(err)
	}
}

// run executes self-install with the given environment overrides and
// returns its exit code and output.
func (e *selfInstallEnv) run(env map[string]string, args ...string) (int, string) {
	e.t.Helper()
	cmd := exec.Command(e.script, args...) //nolint:gosec // the script under test
	base := map[string]string{
		"LCM_SELF_INSTALL_TEST_ROOT":   e.root,
		"LCM_SELF_INSTALL_TEST_UNIT":   "lcm-d1-unit.service",
		"LCM_SELF_INSTALL_TEST_HEALTH": "http://127.0.0.1:1/api/v1/health",
	}
	for k, v := range env {
		base[k] = v
	}
	for k, v := range base {
		if v != "" {
			cmd.Env = append(cmd.Env, k+"="+v)
		}
	}
	out, err := cmd.CombinedOutput()
	var ee *exec.ExitError
	switch {
	case err == nil:
		return 0, string(out)
	case errors.As(err, &ee):
		return ee.ExitCode(), string(out)
	}
	e.t.Fatalf("run self-install: %v", err)
	return 0, ""
}

func TestSelfInstallRefusesBadInvocations(t *testing.T) {
	e := newSelfInstallEnv(t)
	e.write(e.src, "#!/bin/sh\necho 'lcm v2 (c2) now'\n")
	cases := []struct {
		name string
		env  map[string]string
		args []string
		code int
		msg  string
	}{
		{"no argument", nil, nil, 2, "usage"},
		{"two arguments", nil, []string{e.src, e.src}, 2, "usage"},
		{"another path", nil, []string{filepath.Join(e.root, "tmp/lcm")}, 2, "only " + e.src + " is accepted"},
		{"an equivalent path", nil, []string{filepath.Join(e.root, "var/lib/lcm/work/lcm/bin") + "/../bin/lcm"}, 2, "is accepted"},
		{"an option", nil, []string{"--help"}, 2, "is accepted"},
		{"not root outside test mode", map[string]string{"LCM_SELF_INSTALL_TEST_ROOT": ""}, []string{"/var/lib/lcm/work/lcm/bin/lcm"}, 2, "must run as root"},
		{"relative test root", map[string]string{"LCM_SELF_INSTALL_TEST_ROOT": "relative"}, []string{e.src}, 2, "absolute"},
		{"foreign test unit", map[string]string{"LCM_SELF_INSTALL_TEST_UNIT": "lcm.service"}, []string{e.src}, 2, "lcm-d1-"},
		{"remote test health URL", map[string]string{"LCM_SELF_INSTALL_TEST_HEALTH": "http://example.com/api/v1/health"}, []string{e.src}, 2, "127.0.0.1"},
	}
	for _, c := range cases {
		code, out := e.run(c.env, c.args...)
		if code != c.code || !strings.Contains(out, c.msg) {
			t.Errorf("%s: exit %d %q, want %d containing %q", c.name, code, out, c.code, c.msg)
		}
	}
	e.expectUntouched()
}

func TestSelfInstallRefusesUnsafeSources(t *testing.T) {
	e := newSelfInstallEnv(t)
	secret := filepath.Join(e.root, "secret")
	e.write(secret, "#!/bin/sh\necho 'lcm v9 (c9) now'\n")

	check := func(name string, code int, msg string) {
		t.Helper()
		got, out := e.run(nil, e.src)
		if got != code || !strings.Contains(out, msg) {
			t.Errorf("%s: exit %d %q, want %d containing %q", name, got, out, code, msg)
		}
		e.expectUntouched()
	}

	check("missing source", 1, "not a regular file")
	if err := os.Symlink(secret, e.src); err != nil {
		t.Fatal(err)
	}
	check("symlinked source", 1, "not a regular file")
	if err := os.Remove(e.src); err != nil {
		t.Fatal(err)
	}
	if err := os.Mkdir(e.src, 0o750); err != nil {
		t.Fatal(err)
	}
	check("directory source", 1, "not a regular file")
	if err := os.Remove(e.src); err != nil {
		t.Fatal(err)
	}

	e.write(e.src, "#!/bin/sh\necho 'not a version'\n")
	check("source without a version", 1, "fails 'lcm version'")
	e.write(e.src, "#!/bin/sh\necho 'lcm v2;rm (c2) now'\n")
	check("unexpected version characters", 1, "fails 'lcm version'")

	e.write(e.src, "#!/bin/sh\necho 'lcm v2 (c2) now'\n")
	lock, err := os.OpenFile(filepath.Join(e.root, "run/lcm-self-install.lock"), os.O_CREATE|os.O_WRONLY, 0o600)
	if err != nil {
		t.Fatal(err)
	}
	if err := syscall.Flock(int(lock.Fd()), syscall.LOCK_EX|syscall.LOCK_NB); err != nil {
		t.Fatal(err)
	}
	check("lock held", 1, "another self-install is running")
	_ = lock.Close()

	pending := filepath.Join(e.root, "run/lcm-self-install.pending")
	e.write(pending, "9999999999\n")
	check("verification pending", 1, "still being verified")
}

// expectUntouched checks that the installed binary is still the original
// and that nothing was left behind.
func (e *selfInstallEnv) expectUntouched() {
	e.t.Helper()
	b, err := os.ReadFile(e.bin)
	if err != nil || !strings.Contains(string(b), "lcm v1 (c1)") {
		e.t.Fatalf("installed binary changed: %q, %v", b, err)
	}
	for _, leftover := range []string{e.bin + ".new", e.bin + ".prev"} {
		if _, err := os.Lstat(leftover); err == nil {
			e.t.Fatalf("%s was left behind", leftover)
		}
	}
}
