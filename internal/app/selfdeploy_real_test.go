package app_test

import (
	"bytes"
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
	"time"

	"github.com/ACaccel/lcm/internal/engine"
)

// TestRealSelfDeploy runs LCM deploying itself with the committed deploy
// files: a real lcm process under a throwaway user-scope unit (derived from
// deploy/lcm.service), deploy/self-install in test mode on a temp install
// prefix, and deploy/lcm-self.service.yaml and deploy/lcm-auto-deploy.yaml
// with that prefix's paths. The build step copies prebuilt binaries so that
// a run takes minutes, not a console build. It is opt-in:
//
//	LCM_IT_SELFDEPLOY=1 go test ./internal/app -run TestRealSelfDeploy -v -count=1 -timeout 20m
//
// Everything lives in a temp directory and in user units named
// lcm-d1-<random>*, which are stopped and removed at the end.
func TestRealSelfDeploy(t *testing.T) {
	if os.Getenv("LCM_IT_SELFDEPLOY") != "1" {
		t.Skip("set LCM_IT_SELFDEPLOY=1 to run the self-deploy scenario")
	}
	if os.Geteuid() == 0 {
		t.Skip("self-install test mode is refused as root")
	}
	for _, bin := range []string{"go", "git", "systemctl", "systemd-run", "curl", "flock"} {
		if _, err := exec.LookPath(bin); err != nil {
			t.Skipf("%s is not installed", bin)
		}
	}
	if out, err := exec.Command("systemctl", "--user", "show", "-p", "Version").CombinedOutput(); err != nil {
		t.Skipf("systemctl --user is not usable: %v: %s", err, out)
	}

	e := newSelfEnv(t)
	e.firstDeploy()
	t.Run("good update", e.goodUpdate)
	t.Run("update that never becomes healthy", e.brokenUpdate)
}

type selfEnv struct {
	t    *testing.T
	dir  string
	root string
	unit string
	port int
	// bins are the prebuilt binaries a commit can name: vA, vB and a broken
	// one that reports a version but fails to serve.
	bins              map[string]string
	origin, dev, work string
	tokenFile         string
	token             string
	sha               map[string]string
	httpc             *http.Client
}

func newSelfEnv(t *testing.T) *selfEnv {
	t.Helper()
	suffix := make([]byte, 4)
	if _, err := rand.Read(suffix); err != nil {
		t.Fatal(err)
	}
	dir := t.TempDir()
	e := &selfEnv{
		t: t, dir: dir, root: filepath.Join(dir, "root"), unit: "lcm-d1-" + hex.EncodeToString(suffix) + ".service",
		port: freePort(t), bins: map[string]string{}, sha: map[string]string{},
		httpc: &http.Client{Timeout: 5 * time.Second, Transport: &http.Transport{DisableKeepAlives: true}},
	}
	t.Cleanup(e.httpc.CloseIdleConnections)
	for _, d := range []string{"usr/local/bin", "usr/local/libexec/lcm", "var/lib/lcm/work", "etc/lcm", "run"} {
		if err := os.MkdirAll(filepath.Join(e.root, d), 0o750); err != nil {
			t.Fatal(err)
		}
	}
	e.buildBinaries()
	e.write(filepath.Join(e.root, "usr/local/libexec/lcm/self-install"), readDeploy(t, "self-install"), 0o700)
	e.initRepos()
	e.write(filepath.Join(e.root, "usr/local/bin/lcm"), e.read(e.bins["A"]), 0o700)
	e.writeUnit()
	e.start()
	e.waitHealthy("vA", time.Minute)
	e.createToken()
	return e
}

func (e *selfEnv) write(path string, data []byte, mode os.FileMode) {
	e.t.Helper()
	if err := os.WriteFile(path, data, mode); err != nil { //nolint:gosec // test fixtures, some executable
		e.t.Fatal(err)
	}
}

func (e *selfEnv) read(path string) []byte {
	e.t.Helper()
	b, err := os.ReadFile(path) //nolint:gosec // paths of this test's temp directory
	if err != nil {
		e.t.Fatal(err)
	}
	return b
}

func (e *selfEnv) cmd(dir, name string, args ...string) string {
	e.t.Helper()
	c := exec.Command(name, args...) //nolint:gosec // fixed commands on this test's fixtures
	c.Dir = dir
	out, err := c.CombinedOutput()
	if err != nil {
		e.t.Fatalf("%s %v: %v\n%s", name, args, err, out)
	}
	return strings.TrimSpace(string(out))
}

func (e *selfEnv) systemctl(args ...string) string {
	out, _ := exec.Command("systemctl", append([]string{"--user"}, args...)...).CombinedOutput() //nolint:gosec // this test's own units
	return strings.TrimSpace(string(out))
}

func (e *selfEnv) buildBinaries() {
	e.t.Helper()
	bins := filepath.Join(e.dir, "bins")
	for _, v := range []string{"A", "B"} {
		out := filepath.Join(bins, "lcm-v"+v)
		c := exec.Command("go", "build", "-trimpath", "-o", out, //nolint:gosec // builds this module
			"-ldflags", "-X github.com/ACaccel/lcm/internal/buildinfo.Version=v"+v, "./cmd/lcm")
		c.Dir = filepath.Join("..", "..")
		c.Env = append(os.Environ(), "CGO_ENABLED=0")
		if b, err := c.CombinedOutput(); err != nil {
			e.t.Fatalf("go build: %v\n%s", err, b)
		}
		e.bins[v] = out
	}
	e.bins["bad"] = filepath.Join(bins, "lcm-bad")
	e.write(e.bins["bad"], []byte("#!/bin/sh\nif [ \"${1:-}\" = version ]; then echo 'lcm vbad (bad) now'; exit 0; fi\necho 'this build is broken' >&2\nexit 1\n"), 0o700)
}

// commit makes the next commit select a prebuilt binary and pushes it.
func (e *selfEnv) commit(which string) string {
	e.t.Helper()
	e.write(filepath.Join(e.dev, "binary"), []byte(e.bins[which]+"\n"), 0o600)
	e.cmd(e.dev, "git", "add", "-A")
	e.cmd(e.dev, "git", "-c", "user.name=e2e", "-c", "user.email=e2e@example.invalid", "commit", "-q", "-m", "binary "+which)
	e.cmd(e.dev, "git", "push", "-q", "origin", "main")
	e.sha[which] = e.cmd(e.dev, "git", "rev-parse", "HEAD")
	return e.sha[which]
}

func (e *selfEnv) initRepos() {
	e.t.Helper()
	e.origin, e.dev = filepath.Join(e.dir, "origin.git"), filepath.Join(e.dir, "dev")
	e.work = filepath.Join(e.root, "var/lib/lcm/work/lcm")
	e.cmd(e.dir, "git", "init", "-q", "--bare", "--initial-branch=main", e.origin)
	e.cmd(e.dir, "git", "clone", "-q", e.origin, e.dev)
	e.write(filepath.Join(e.dev, ".gitignore"), []byte("/bin/\n"), 0o600)
	e.commit("A")
	e.cmd(e.dir, "git", "clone", "-q", e.origin, e.work)
}

// replace applies old -> new pairs and fails when one does not occur, so
// that a change to a deploy file cannot silently bypass this test.
func (e *selfEnv) replace(name, s string, pairs ...string) string {
	e.t.Helper()
	for i := 0; i+1 < len(pairs); i += 2 {
		if !strings.Contains(s, pairs[i]) {
			e.t.Fatalf("%s no longer contains %q", name, pairs[i])
		}
		s = strings.ReplaceAll(s, pairs[i], pairs[i+1])
	}
	return s
}

func (e *selfEnv) writeUnit() {
	e.t.Helper()
	unit := e.replace("deploy/lcm.service", string(readDeploy(e.t, "lcm.service")),
		"EnvironmentFile=/etc/lcm/lcm.env", "EnvironmentFile="+e.root+"/etc/lcm/lcm.env",
		"ExecStart=/usr/local/bin/lcm serve", "ExecStart="+e.root+"/usr/local/bin/lcm serve",
		// A user manager cannot switch users or set up a private /tmp.
		"User=lcm\n", "", "Group=lcm\n", "", "PrivateTmp=yes\n", "")
	env := e.replace("deploy/lcm.env", string(readDeploy(e.t, "lcm.env")),
		"LCM_DATA_DIR=/var/lib/lcm", "LCM_DATA_DIR="+e.root+"/var/lib/lcm",
		"LCM_LISTEN=127.0.0.1:7800", fmt.Sprintf("LCM_LISTEN=127.0.0.1:%d", e.port),
		"LCM_SYSTEMCTL_PRIVILEGE=sudo -n", "LCM_SYSTEMCTL_PRIVILEGE=none")
	e.write(filepath.Join(e.root, "etc/lcm/lcm.env"), []byte(env), 0o600)

	home, err := os.UserHomeDir()
	if err != nil {
		e.t.Fatal(err)
	}
	unitDir := filepath.Join(home, ".config", "systemd", "user")
	if err := os.MkdirAll(unitDir, 0o750); err != nil {
		e.t.Fatal(err)
	}
	path := filepath.Join(unitDir, e.unit)
	e.write(path, []byte(unit), 0o600)
	prefix := strings.TrimSuffix(e.unit, ".service")
	e.t.Cleanup(func() {
		e.systemctl("stop", prefix+"-update-*.timer", prefix+"-update-*.service", e.unit)
		if err := os.Remove(path); err != nil && !os.IsNotExist(err) {
			e.t.Errorf("remove unit file: %v", err)
		}
		e.systemctl("daemon-reload")
		e.systemctl("reset-failed", e.unit, prefix+"-update-*")
	})
	e.systemctl("daemon-reload")
}

func (e *selfEnv) start() {
	e.t.Helper()
	if out, err := exec.Command("systemctl", "--user", "start", e.unit).CombinedOutput(); err != nil { //nolint:gosec // this test's unit
		e.t.Fatalf("start %s: %v: %s", e.unit, err, out)
	}
}

func (e *selfEnv) mainPID() string { return e.systemctl("show", "-p", "MainPID", "--value", e.unit) }

// started returns when the current LCM process was started.
func (e *selfEnv) started() time.Time {
	e.t.Helper()
	v := e.systemctl("show", "-p", "ExecMainStartTimestamp", "--value", "--timestamp=unix", e.unit)
	var sec int64
	if _, err := fmt.Sscanf(v, "@%d", &sec); err != nil {
		e.t.Fatalf("ExecMainStartTimestamp %q: %v", v, err)
	}
	return time.Unix(sec, 0)
}

// expectRestartBetween checks that LCM restarted after node first finished
// and before node last finished: the deployment was recorded before the
// restart, and the run carried on in the new process.
func (e *selfEnv) expectRestartBetween(v engine.RunView, first, last string) {
	e.t.Helper()
	a, b := node(v, first).FinishedAt, node(v, last).FinishedAt
	at := e.started()
	if a == nil || b == nil || !a.Truncate(time.Second).Before(at.Add(time.Second)) || !b.After(at) {
		e.t.Errorf("restart at %s is not between %s finishing (%v) and %s finishing (%v)", at, first, a, last, b)
	}
}

func (e *selfEnv) url(path string) string {
	return fmt.Sprintf("http://127.0.0.1:%d/api/v1%s", e.port, path)
}

// health returns the version the server reports, or "" when it is down.
func (e *selfEnv) health() string {
	resp, err := e.httpc.Get(e.url("/health"))
	if err != nil {
		return ""
	}
	defer func() { _ = resp.Body.Close() }()
	var body struct{ Status, Version string }
	if resp.StatusCode != http.StatusOK || json.NewDecoder(resp.Body).Decode(&body) != nil || body.Status != "ok" {
		return ""
	}
	return body.Version
}

func (e *selfEnv) eventually(what string, timeout time.Duration, cond func() bool) {
	e.t.Helper()
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		if cond() {
			return
		}
		time.Sleep(500 * time.Millisecond)
	}
	e.t.Fatalf("timed out after %s waiting for %s; journal:\n%s", timeout, what, e.journal())
}

func (e *selfEnv) journal() string {
	prefix := strings.TrimSuffix(e.unit, ".service")
	out, _ := exec.Command("journalctl", "--user", "--no-pager", "-n", "60", "-o", "cat", //nolint:gosec // this test's units
		"-u", e.unit, "-u", prefix+"-update-*").CombinedOutput()
	return string(out)
}

func (e *selfEnv) waitHealthy(version string, timeout time.Duration) {
	e.t.Helper()
	e.eventually("version "+version+" to serve", timeout, func() bool { return e.health() == version })
}

func (e *selfEnv) createToken() {
	e.t.Helper()
	out, err := exec.Command(e.bins["A"], "token", "create", "--name", "e2e", "--data-dir", filepath.Join(e.root, "var/lib/lcm")).Output() //nolint:gosec // the binary built by this test
	if err != nil {
		e.t.Fatalf("token create: %v", err)
	}
	e.token = strings.TrimSpace(string(out))
	e.tokenFile = filepath.Join(e.dir, "token")
	e.write(e.tokenFile, []byte(e.token+"\n"), 0o600)
}

// lcm runs a client command of the vA binary against the server.
func (e *selfEnv) lcm(args ...string) string {
	e.t.Helper()
	c := exec.Command(e.bins["A"], args...) //nolint:gosec // the binary built by this test
	c.Env = []string{fmt.Sprintf("LCM_SERVER=http://127.0.0.1:%d", e.port), "LCM_TOKEN_FILE=" + e.tokenFile, "PATH=" + os.Getenv("PATH")}
	out, err := c.CombinedOutput()
	if err != nil {
		e.t.Fatalf("lcm %v: %v\n%s", args, err, out)
	}
	return string(out)
}

// api calls the API with the token; ok is false while the server restarts.
func (e *selfEnv) api(method, path string, body any, into any) (ok bool) {
	var rd *bytes.Reader
	if body != nil {
		b, err := json.Marshal(body)
		if err != nil {
			e.t.Fatal(err)
		}
		rd = bytes.NewReader(b)
	} else {
		rd = bytes.NewReader(nil)
	}
	req, err := http.NewRequestWithContext(context.Background(), method, e.url(path), rd)
	if err != nil {
		e.t.Fatal(err)
	}
	req.Header.Set("Authorization", "Bearer "+e.token)
	req.Header.Set("X-GitHub-Event", "push")
	resp, err := e.httpc.Do(req)
	if err != nil {
		return false
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode >= 300 {
		return false
	}
	return into == nil || json.NewDecoder(resp.Body).Decode(into) == nil
}

func (e *selfEnv) writeDoc(name, content string) string {
	e.t.Helper()
	path := filepath.Join(e.dir, name)
	e.write(path, []byte(content), 0o600)
	return path
}

// serviceDoc is deploy/lcm-self.service.yaml on this test's prefix, unit
// and port, with a build step that copies the binary a commit names.
func (e *selfEnv) serviceDoc() string {
	doc := string(readDeploy(e.t, "lcm-self.service.yaml"))
	build := regexp.MustCompile(`(?s)    build: >-\n.*?      make build\n`)
	if !build.MatchString(doc) {
		e.t.Fatal("deploy/lcm-self.service.yaml no longer has the expected build step")
	}
	doc = build.ReplaceAllLiteralString(doc, "    build: mkdir -p bin && cp \"$(cat binary)\" bin/lcm\n")
	testMode := fmt.Sprintf("LCM_SELF_INSTALL_TEST_ROOT=%s LCM_SELF_INSTALL_TEST_UNIT=%s LCM_SELF_INSTALL_TEST_HEALTH=%s LCM_SELF_INSTALL_TEST_WAIT=20 %s",
		e.root, e.unit, e.url("/health"), e.root+"/usr/local/libexec/lcm/self-install")
	return e.replace("deploy/lcm-self.service.yaml", doc,
		"/var/lib/lcm", e.root+"/var/lib/lcm",
		"sudo -n /usr/local/libexec/lcm/self-install", testMode,
		"systemctl is-active --quiet lcm.service", "systemctl --user is-active --quiet "+e.unit,
		"http://127.0.0.1:7800", fmt.Sprintf("http://127.0.0.1:%d", e.port))
}

type serviceView struct {
	State           string `json:"state"`
	Health          string `json:"health"`
	DeployedVersion string `json:"deployedVersion"`
}

func (e *selfEnv) service() (serviceView, bool) {
	var v serviceView
	return v, e.api("GET", "/services/lcm", nil, &v)
}

func (e *selfEnv) waitServiceHealthy(timeout time.Duration) {
	e.t.Helper()
	e.eventually("service lcm to be healthy", timeout, func() bool {
		v, ok := e.service()
		return ok && v.Health == "healthy" && v.State == "running"
	})
}

func (e *selfEnv) pendingGone() bool {
	_, err := os.Stat(filepath.Join(e.root, "run/lcm-self-install.pending"))
	return errors.Is(err, os.ErrNotExist)
}

// firstDeploy applies the service and records a known-good deployment, as
// seed.sh does, then deploys the policy.
func (e *selfEnv) firstDeploy() {
	e.t.Helper()
	e.lcm("apply", "-f", e.writeDoc("service.yaml", e.serviceDoc()))
	pid := e.mainPID()
	e.t.Log(strings.TrimSpace(e.lcm("action", "lcm", "deploy", "--version", e.sha["A"], "--wait")))
	e.eventually("the self-update restart", time.Minute, func() bool { return e.mainPID() != pid && e.pendingGone() })
	e.waitHealthy("vA", time.Minute)
	e.waitServiceHealthy(time.Minute)

	policy := e.replace("deploy/lcm-auto-deploy.yaml", string(readDeploy(e.t, "lcm-auto-deploy.yaml")),
		"repo: https://github.com/ACaccel/LCM.git", "repo: "+e.origin,
		"requireCI: true", "requireCI: false",
		"healthTimeout: 3m", "healthTimeout: 90s")
	e.lcm("apply", "-f", e.writeDoc("policy.yaml", policy))
	// The git trigger records the first head it sees without starting a run.
	e.push(e.sha["A"])
}

// push tells the git trigger about a commit through the GitHub webhook.
func (e *selfEnv) push(sha string) {
	e.t.Helper()
	payload := map[string]any{"ref": "refs/heads/main", "after": sha, "repository": map[string]string{"clone_url": e.origin}}
	var res struct{ Matched int }
	if !e.api("POST", "/hooks/github", payload, &res) || res.Matched != 1 {
		e.t.Fatalf("push webhook for %s matched %d triggers", sha, res.Matched)
	}
}

func (e *selfEnv) runIDs() []string {
	var page struct {
		Items []struct{ ID string } `json:"items"`
	}
	if !e.api("GET", "/runs?policy=lcm-auto-deploy&limit=20", nil, &page) {
		return nil
	}
	ids := make([]string, len(page.Items))
	for i, r := range page.Items {
		ids[i] = r.ID
	}
	return ids
}

// release commits a binary, pushes it and returns the finished run.
func (e *selfEnv) release(which string, timeout time.Duration) engine.RunView {
	e.t.Helper()
	before := len(e.runIDs())
	e.push(e.commit(which))
	var id string
	e.eventually("a run for "+which, 30*time.Second, func() bool {
		ids := e.runIDs()
		if len(ids) > before {
			id = ids[0]
		}
		return id != ""
	})
	var view engine.RunView
	e.eventually("run "+id+" to finish", timeout, func() bool {
		return e.api("GET", "/runs/"+id, nil, &view) &&
			view.Status != engine.RunRunning && view.Status != engine.RunSuspended && view.Status != engine.RunQueued
	})
	return view
}

func node(v engine.RunView, id string) engine.NodeView {
	for _, n := range v.Nodes {
		if n.ID == id {
			return n
		}
	}
	return engine.NodeView{}
}

func (e *selfEnv) expectNodes(v engine.RunView, done []string, notDone []string) {
	e.t.Helper()
	if v.Status != engine.RunSucceeded {
		e.t.Fatalf("run %s = %s (%s)", v.ID, v.Status, v.Error)
	}
	for _, id := range done {
		if n := node(v, id); n.Status != engine.NodeCompleted || n.Error != "" {
			e.t.Errorf("node %s = %s %q, want completed", id, n.Status, n.Error)
		}
	}
	for _, id := range notDone {
		if n := node(v, id); n.Status == engine.NodeCompleted {
			e.t.Errorf("node %s completed", id)
		}
	}
}

func (e *selfEnv) installed(suffix string) []byte {
	return e.read(filepath.Join(e.root, "usr/local/bin/lcm"+suffix))
}

func (e *selfEnv) goodUpdate(t *testing.T) {
	pid := e.mainPID()
	view := e.release("B", 4*time.Minute)
	e.expectNodes(view, []string{"deploy", "healthy", "done"}, []string{"rollback"})
	if n := node(view, "healthy"); n.Port != "out" {
		t.Errorf("health wait ended on port %q", n.Port)
	}
	if e.mainPID() == pid {
		t.Error("LCM was not restarted during the run")
	}
	e.expectRestartBetween(view, "deploy", "healthy")
	if got := e.health(); got != "vB" {
		t.Errorf("serving %q, want vB", got)
	}
	if !bytes.Equal(e.installed(".prev"), e.read(e.bins["A"])) {
		t.Error("lcm.prev is not the vA binary")
	}
	e.eventually("the verification to finish", time.Minute, e.pendingGone)
	if v, ok := e.service(); !ok || v.DeployedVersion != e.sha["B"] {
		t.Errorf("service = %+v, want deployed %s", v, e.sha["B"])
	}
}

func (e *selfEnv) brokenUpdate(t *testing.T) {
	view := e.release("bad", 8*time.Minute)
	e.expectNodes(view, []string{"deploy", "healthy", "rollback", "rolled_back"}, []string{"done", "rollback_failed"})
	if n := node(view, "healthy"); n.Port != "timeout" {
		t.Errorf("health wait ended on port %q, want timeout", n.Port)
	}
	e.eventually("the rollback's verification to finish", time.Minute, e.pendingGone)
	// The rollback's own restart comes after the rollback was recorded.
	if done, at := node(view, "rollback").FinishedAt, e.started(); done == nil || at.Before(done.Truncate(time.Second)) {
		t.Errorf("restart at %s, rollback finished at %v", at, done)
	}
	e.waitHealthy("vB", time.Minute)
	e.waitServiceHealthy(time.Minute)
	if !bytes.Equal(e.installed(""), e.read(e.bins["B"])) {
		t.Error("the installed binary is not vB after the rollback")
	}
	if !bytes.Equal(e.installed(".failed"), e.read(e.bins["bad"])) {
		t.Error("lcm.failed is not the broken binary")
	}
	if v, _ := e.service(); v.DeployedVersion != e.sha["B"] {
		t.Errorf("deployed version = %s, want %s after the rollback", v.DeployedVersion, e.sha["B"])
	}
	prefix := strings.TrimSuffix(e.unit, ".service")
	if failed := e.systemctl("list-units", "--failed", "--plain", "--no-legend", prefix+"-update-*"); failed == "" {
		t.Error("the failed verification left no failed unit behind")
	} else {
		t.Logf("failed verification unit: %s", failed)
	}
}
