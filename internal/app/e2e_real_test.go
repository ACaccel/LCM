package app_test

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"fmt"
	"net"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/ACaccel/lcm/internal/app"
	"github.com/ACaccel/lcm/internal/auth"
	"github.com/ACaccel/lcm/internal/catalog"
	"github.com/ACaccel/lcm/internal/config"
	"github.com/ACaccel/lcm/internal/engine"
	"github.com/ACaccel/lcm/internal/templates"
)

// TestRealScenarios runs the auto-deploy and scheduled-backup scenarios of
// the design against real git repositories, a throwaway user-scope systemd
// unit and the real clock. It is opt-in because it starts a process under
// `systemctl --user`:
//
//	LCM_IT_E2E=1 go test ./internal/app -run TestRealScenarios -v -count=1
//
// Everything it creates lives in a temp directory and in one unit named
// lcm-e2e-<random>, which is stopped and removed at the end.
func TestRealScenarios(t *testing.T) {
	if os.Getenv("LCM_IT_E2E") != "1" {
		t.Skip("set LCM_IT_E2E=1 to run the end-to-end scenarios")
	}
	for _, bin := range []string{"git", "python3", "systemctl", "curl"} {
		if _, err := exec.LookPath(bin); err != nil {
			t.Skipf("%s is not installed", bin)
		}
	}
	if out, err := exec.Command("systemctl", "--user", "show", "-p", "Version").CombinedOutput(); err != nil {
		t.Skipf("systemctl --user is not usable: %v: %s", err, out)
	}

	e := newRealEnv(t)
	t.Run("auto deploy", e.autoDeploy)
	t.Run("scheduled backup", e.scheduledBackup)
}

// realEnv is a running LCM with the fixtures of both scenarios.
type realEnv struct {
	t       *testing.T
	ctx     context.Context
	app     *app.App
	dir     string
	unit    string
	port    int
	discord *discordRecorder
	// origin is the bare repository LCM polls; dev is where commits are made;
	// work is the checkout LCM deploys from.
	origin, dev, work string
	baseline          string
	httpc             *http.Client
}

func newRealEnv(t *testing.T) *realEnv {
	t.Helper()
	e := &realEnv{t: t, ctx: auth.WithActor(context.Background(), auth.CLI), dir: t.TempDir()}
	suffix := make([]byte, 4)
	if _, err := rand.Read(suffix); err != nil {
		t.Fatal(err)
	}
	e.unit = "lcm-e2e-" + hex.EncodeToString(suffix)
	e.origin, e.dev, e.work = filepath.Join(e.dir, "origin.git"), filepath.Join(e.dir, "dev"), filepath.Join(e.dir, "work")
	e.httpc = &http.Client{Timeout: 5 * time.Second, Transport: &http.Transport{DisableKeepAlives: true}}
	e.discord = newDiscord(t)

	e.port = freePort(t)
	e.writeUnit()
	e.initRepos()
	e.startApp()
	e.createDeployFixtures()
	return e
}

func freePort(t *testing.T) int {
	t.Helper()
	l, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = l.Close() }()
	return l.Addr().(*net.TCPAddr).Port
}

func (e *realEnv) sh(dir string, name string, args ...string) string {
	e.t.Helper()
	cmd := exec.Command(name, args...) //nolint:gosec // fixed git calls on temp repositories
	cmd.Dir = dir
	out, err := cmd.CombinedOutput()
	if err != nil {
		e.t.Fatalf("%s %v: %v\n%s", name, args, err, out)
	}
	return strings.TrimSpace(string(out))
}

func (e *realEnv) systemctl(args ...string) {
	e.t.Helper()
	_ = exec.Command("systemctl", append([]string{"--user"}, args...)...).Run() //nolint:gosec // systemctl on this test's own unit
}

// writeUnit installs the throwaway unit: a static file server on the deploy
// directory. It is removed at cleanup, even when the test fails.
func (e *realEnv) writeUnit() {
	home, err := os.UserHomeDir()
	if err != nil {
		e.t.Fatal(err)
	}
	unitDir := filepath.Join(home, ".config", "systemd", "user")
	if err := os.MkdirAll(unitDir, 0o750); err != nil {
		e.t.Fatal(err)
	}
	deploy := filepath.Join(e.dir, "deploy")
	if err := os.MkdirAll(deploy, 0o750); err != nil {
		e.t.Fatal(err)
	}
	path := filepath.Join(unitDir, e.unit+".service")
	body := fmt.Sprintf("[Unit]\nDescription=LCM end-to-end test service\n\n[Service]\nExecStart=%s -m http.server %d --bind 127.0.0.1 --directory %s\n",
		mustLook("python3"), e.port, deploy)
	if err := os.WriteFile(path, []byte(body), 0o600); err != nil {
		e.t.Fatal(err)
	}
	e.t.Cleanup(func() {
		e.systemctl("stop", e.unit+".service")
		if err := os.Remove(path); err != nil && !os.IsNotExist(err) {
			e.t.Errorf("remove unit file: %v", err)
		}
		e.systemctl("daemon-reload")
		e.systemctl("reset-failed", e.unit+".service")
	})
	e.systemctl("daemon-reload")
}

func mustLook(bin string) string {
	p, err := exec.LookPath(bin)
	if err != nil {
		panic(err)
	}
	return p
}

// commit writes the files of a version into the dev clone, commits, pushes
// and returns the commit id. site/health makes the server healthy and
// site/version tells which version is served; a build.sh that fails stands
// for a broken build.
func (e *realEnv) commit(version string, buildOK, healthy bool) string {
	e.t.Helper()
	site := filepath.Join(e.dev, "site")
	_ = os.RemoveAll(site)
	if err := os.MkdirAll(site, 0o750); err != nil {
		e.t.Fatal(err)
	}
	files := map[string]string{"site/version": version + "\n", "build.sh": "exit 0\n"}
	if !buildOK {
		files["build.sh"] = "echo build is broken >&2; exit 1\n"
	}
	if healthy {
		files["site/health"] = "ok\n"
	}
	for name, content := range files {
		if err := os.WriteFile(filepath.Join(e.dev, name), []byte(content), 0o600); err != nil {
			e.t.Fatal(err)
		}
	}
	e.sh(e.dev, "git", "add", "-A")
	e.sh(e.dev, "git", "-c", "user.name=e2e", "-c", "user.email=e2e@example.invalid", "commit", "-q", "-m", "version "+version)
	e.sh(e.dev, "git", "push", "-q", "origin", "main")
	return e.sh(e.dev, "git", "rev-parse", "HEAD")
}

func (e *realEnv) initRepos() {
	e.t.Helper()
	e.sh(e.dir, "git", "init", "-q", "--bare", "--initial-branch=main", e.origin)
	e.sh(e.dir, "git", "clone", "-q", e.origin, e.dev)
	e.baseline = e.commit("0", true, true)
	e.sh(e.dir, "git", "clone", "-q", e.origin, e.work)
}

func (e *realEnv) startApp() {
	e.t.Helper()
	cfg := config.Default()
	cfg.DataDir = filepath.Join(e.dir, "data")
	cfg.Listen = "127.0.0.1:0"
	cfg.SystemctlPrivilege = config.NoPrivilege
	client := &http.Client{Transport: &http.Transport{DisableKeepAlives: true}}
	a, err := app.New(e.ctx, cfg, app.WithHTTPClient(client), app.WithLogger(testLogger(e.t)))
	if err != nil {
		e.t.Fatalf("app.New: %v", err)
	}
	e.app = a
	runCtx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { done <- a.Run(runCtx) }()
	e.t.Cleanup(func() {
		cancel()
		select {
		case err := <-done:
			if err != nil {
				e.t.Errorf("Run: %v", err)
			}
		case <-time.After(30 * time.Second):
			e.t.Error("Run did not return")
		}
		ctx, stop := context.WithTimeout(context.Background(), 30*time.Second)
		defer stop()
		if err := a.Close(ctx); err != nil {
			e.t.Errorf("Close: %v", err)
		}
		client.CloseIdleConnections()
		e.httpc.CloseIdleConnections()
	})
}

func (e *realEnv) createDeployFixtures() {
	e.t.Helper()
	secrets := e.app.Config.SecretsPath()
	if err := os.MkdirAll(secrets, 0o700); err != nil {
		e.t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(secrets, "e2e-hook"), []byte(e.discord.URL+"/api/webhooks/1/TOKEN\n"), 0o600); err != nil {
		e.t.Fatal(err)
	}
	install := fmt.Sprintf("rm -rf %[1]s/* && cp -r site/. %[1]s/", filepath.Join(e.dir, "deploy"))
	doc := fmt.Sprintf(`
apiVersion: lcm/v1
kind: Service
metadata: { name: e2e-web }
spec:
  runtime: { type: systemd, unit: %s.service, scope: user }
  source: { repo: %s, branch: main, build: "sh ./build.sh", install: %q }
  checks:
    - { name: http, type: http, interval: 2s, timeout: 1s, props: { url: "http://127.0.0.1:%d/health" } }
`, e.unit, e.work, install, e.port)
	if _, err := e.app.Catalog.Put(e.ctx, []byte(doc), 0); err != nil {
		e.t.Fatal(err)
	}
}

func (e *realEnv) served() string {
	resp, err := e.httpc.Get(fmt.Sprintf("http://127.0.0.1:%d/version", e.port))
	if err != nil {
		return "unreachable"
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode != http.StatusOK {
		return fmt.Sprintf("status %d", resp.StatusCode)
	}
	b := make([]byte, 64)
	n, _ := resp.Body.Read(b)
	return strings.TrimSpace(string(b[:n]))
}

// eventually polls cond until it holds or the deadline passes.
func (e *realEnv) eventually(what string, timeout time.Duration, cond func() bool) {
	e.t.Helper()
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		if cond() {
			return
		}
		time.Sleep(200 * time.Millisecond)
	}
	e.t.Fatalf("timed out after %s waiting for %s", timeout, what)
}

// runs returns the runs of a policy, newest first.
func (e *realEnv) runs(policy string) []engine.RunSummary {
	page, err := e.app.Engine.ListRuns(e.ctx, engine.RunQuery{Policy: policy, Limit: 50})
	if err != nil {
		e.t.Fatal(err)
	}
	return page.Runs
}

// push commits a version and tells the git trigger about it, as a webhook
// would, then waits for the policy run that follows and returns it.
func (e *realEnv) push(policy, version string, buildOK, healthy bool) engine.RunView {
	e.t.Helper()
	before := len(e.runs(policy))
	sha := e.commit(version, buildOK, healthy)
	n, err := e.app.Triggers.NotifyPush(e.ctx, e.origin, "main", sha)
	if err != nil || n == 0 {
		e.t.Fatalf("NotifyPush matched %d triggers: %v", n, err)
	}
	e.eventually("a run for version "+version, 30*time.Second, func() bool { return len(e.runs(policy)) > before })
	id := e.runs(policy)[0].ID
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Minute)
	defer cancel()
	view, err := e.app.Engine.Wait(ctx, id)
	if err != nil {
		e.t.Fatalf("waiting for run %s: %v", id, err)
	}
	return view
}

func completed(v engine.RunView, ids ...string) []string {
	var bad []string
	for _, id := range ids {
		if n, _ := v.Node(id); n.Status != engine.NodeCompleted {
			bad = append(bad, id+"="+string(n.Status))
		}
	}
	return bad
}

func (e *realEnv) autoDeploy(t *testing.T) {
	// The first deployment gives rollbacks a version to return to.
	if _, err := e.app.Manager.Deploy(e.ctx, "e2e-web", catalog.ActionOptions{Version: e.baseline}); err != nil {
		t.Fatalf("baseline deploy: %v", err)
	}
	e.expectHealthyOn("0")
	tpl, _ := templates.Get("auto-deploy")
	doc, err := tpl.Render("e2e-auto-deploy", map[string]any{
		"service": "e2e-web", "repo": e.origin, "requireCI": false, "healthTimeout": "30s", "notifySecret": "e2e-hook",
	})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := e.app.Engine.Deploy(e.ctx, doc); err != nil {
		t.Fatalf("deploy policy: %v\n%s", err, doc)
	}
	// The trigger only records the first head it sees; show it the baseline
	// so that the pushes below start runs.
	if n, err := e.app.Triggers.NotifyPush(e.ctx, e.origin, "main", e.baseline); err != nil || n != 1 {
		t.Fatalf("priming the git trigger: %d triggers, %v", n, err)
	}

	t.Log("push A: a good commit")
	view := e.push("e2e-auto-deploy", "A", true, true)
	if bad := completed(view, "announce", "deploy", "healthy", "done"); view.Status != engine.RunSucceeded || len(bad) > 0 {
		t.Fatalf("run for A = %s (%s), nodes not completed: %v", view.Status, view.Error, bad)
	}
	if got := e.served(); got != "A" {
		t.Fatalf("after A the server shows %q", got)
	}
	msgs := e.discord.all()
	if len(msgs) != 2 || !strings.HasPrefix(msgs[0], "POST ") || !strings.HasPrefix(msgs[1], "PATCH ") || !strings.Contains(msgs[1], "Deployed") {
		t.Fatalf("notifications for A = %q", msgs)
	}

	t.Log("push B: the build fails")
	view = e.push("e2e-auto-deploy", "B", false, true)
	e.expectRolledBack(view, "B")
	if n, _ := view.Node("deploy"); !strings.Contains(fmt.Sprint(n.Output), "build") {
		t.Errorf("deploy for B reported %v, want the failed build step", n.Output)
	}
	if n, _ := view.Node("healthy"); n.Status == engine.NodeCompleted {
		t.Error("waited for health after a failed build")
	}
	e.expectHealthyOn("A")

	t.Log("push C: the server comes up unhealthy")
	view = e.push("e2e-auto-deploy", "C", true, false)
	e.expectRolledBack(view, "C")
	if n, _ := view.Node("healthy"); n.Status != engine.NodeCompleted || n.Port != "timeout" {
		t.Errorf("health wait for C = %s on port %q, want the timeout port", n.Status, n.Port)
	}
	e.expectHealthyOn("A")
	if msgs := e.discord.all(); len(msgs) != 6 {
		t.Fatalf("notifications = %q, want a POST and one edit per stage for each of three runs", msgs)
	}
	if st, ok := e.app.Monitor.Service("e2e-web"); !ok || st.Health != "healthy" {
		t.Errorf("health at the end = %+v", st)
	}
	rec, err := e.app.Catalog.Get(e.ctx, "e2e-web")
	if err != nil || rec.State != catalog.StateRunning {
		t.Errorf("service state at the end = %v, %v", rec, err)
	}
}

// expectRolledBack checks that a run ended on the rollback branch.
func (e *realEnv) expectRolledBack(v engine.RunView, version string) {
	e.t.Helper()
	if v.Status != engine.RunSucceeded {
		e.t.Fatalf("run for %s = %s (%s)", version, v.Status, v.Error)
	}
	if bad := completed(v, "announce", "rollback", "rolled_back"); len(bad) > 0 {
		e.t.Fatalf("run for %s: nodes not completed: %v", version, bad)
	}
	if n, _ := v.Node("done"); n.Status == engine.NodeCompleted {
		e.t.Fatalf("run for %s reported success", version)
	}
}

func (e *realEnv) expectHealthyOn(version string) {
	e.t.Helper()
	e.eventually("version "+version+" to be served", 15*time.Second, func() bool { return e.served() == version })
	e.eventually("the service to report healthy", 20*time.Second, func() bool {
		st, ok := e.app.Monitor.Service("e2e-web")
		return ok && st.Health == "healthy"
	})
}

func (e *realEnv) scheduledBackup(t *testing.T) {
	_, tok, err := e.app.Auth.CreateToken(e.ctx, "e2e-heartbeat", auth.ScopeIngest)
	if err != nil {
		t.Fatal(err)
	}
	out := filepath.Join(e.dir, "backup.out")
	beat := fmt.Sprintf("curl -fsS -X POST -H 'Authorization: Bearer %s' http://%s/api/v1/heartbeats/e2e-db/dump", tok, e.app.Addr())
	doc := fmt.Sprintf(`
apiVersion: lcm/v1
kind: Service
metadata: { name: e2e-db }
spec:
  runtime: { type: command, start: "true", stop: "true", status: "true" }
  backup: { command: "echo dump > %s && %s" }
  checks:
    - { name: dump, type: heartbeat, interval: 1h, critical: false }
`, out, beat)
	if _, err := e.app.Catalog.Put(e.ctx, []byte(doc), 0); err != nil {
		t.Fatal(err)
	}
	if _, err := e.app.Manager.Start(e.ctx, "e2e-db", catalog.ActionOptions{}); err != nil {
		t.Fatal(err)
	}
	if err := e.app.SyncMonitor(e.ctx); err != nil {
		t.Fatal(err)
	}

	tpl, _ := templates.Get("scheduled-backup")
	body, err := tpl.Render("e2e-backup", map[string]any{
		"service": "e2e-db", "schedule": "* * * * *", "timezone": "UTC", "heartbeatCheck": "dump", "notifySecret": "e2e-hook",
	})
	if err != nil {
		t.Fatal(err)
	}
	notifications := len(e.discord.all())
	if _, err := e.app.Engine.Deploy(e.ctx, body); err != nil {
		t.Fatalf("deploy policy: %v\n%s", err, body)
	}

	// The next minute boundary fires the cron trigger.
	t.Log("waiting for the next cron minute")
	e.eventually("a backup run", 90*time.Second, func() bool { return len(e.runs("e2e-backup")) > 0 })
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
	defer cancel()
	view, err := e.app.Engine.Wait(ctx, e.runs("e2e-backup")[0].ID)
	if err != nil || view.Status != engine.RunSucceeded {
		t.Fatalf("backup run = %v, %v", view.Status, err)
	}
	if bad := completed(view, "backup", "heartbeat"); len(bad) > 0 {
		t.Fatalf("nodes %v", bad)
	}
	b, err := os.ReadFile(out) //nolint:gosec // path inside the test temp dir
	if err != nil || strings.TrimSpace(string(b)) != "dump" {
		t.Fatalf("backup file = %q, %v", b, err)
	}
	if n, _ := view.Node("heartbeat_missing"); n.Status == engine.NodeCompleted {
		t.Error("reported a missing heartbeat")
	}
	if got := len(e.discord.all()); got != notifications {
		t.Errorf("a successful backup sent notifications: %q", e.discord.all())
	}
	// Stop the schedule so no further run starts during cleanup.
	if err := e.app.Engine.Undeploy(e.ctx, "e2e-backup"); err != nil {
		t.Fatal(err)
	}
}
