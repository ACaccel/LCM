package catalog_test

import (
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/ACaccel/lcm/internal/auth"
	"github.com/ACaccel/lcm/internal/catalog"
	"github.com/ACaccel/lcm/internal/execx"
	rt "github.com/ACaccel/lcm/internal/runtime"
)

// gitFixture is a bare remote, a developer clone that pushes to it and the
// checkout LCM deploys from.
type gitFixture struct {
	t                 *testing.T
	remote, dev, repo string
}

func (g *gitFixture) git(dir string, args ...string) string {
	g.t.Helper()
	full := append([]string{"-c", "user.name=Test", "-c", "user.email=test@example.com", "-c", "commit.gpgsign=false", "-c", "protocol.file.allow=always"}, args...)
	cmd := exec.Command("git", full...) //nolint:gosec // test fixture with constant arguments
	cmd.Dir = dir
	cmd.Env = append(os.Environ(), "GIT_CONFIG_GLOBAL=/dev/null", "GIT_CONFIG_SYSTEM=/dev/null")
	out, err := cmd.CombinedOutput()
	if err != nil {
		g.t.Fatalf("git %s: %v\n%s", strings.Join(args, " "), err, out)
	}
	return strings.TrimSpace(string(out))
}

func newGitFixture(t *testing.T) *gitFixture {
	t.Helper()
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git is not installed")
	}
	base := t.TempDir()
	g := &gitFixture{t: t, remote: filepath.Join(base, "remote.git"), dev: filepath.Join(base, "dev"), repo: filepath.Join(base, "checkout")}
	g.git(base, "init", "--bare", "-b", "main", g.remote)
	g.git(base, "init", "-b", "main", g.dev)
	g.git(g.dev, "remote", "add", "origin", g.remote)
	g.commit("v1")
	g.git(base, "clone", g.remote, g.repo)
	return g
}

// commit writes content to app.txt, pushes the commit and returns its sha.
func (g *gitFixture) commit(content string) string {
	g.t.Helper()
	if err := os.WriteFile(filepath.Join(g.dev, "app.txt"), []byte(content+"\n"), 0o600); err != nil {
		g.t.Fatal(err)
	}
	g.git(g.dev, "add", "app.txt")
	g.git(g.dev, "commit", "-m", content)
	g.git(g.dev, "push", "origin", "main")
	return g.git(g.dev, "rev-parse", "HEAD")
}

func (g *gitFixture) head() string { return g.git(g.repo, "rev-parse", "HEAD") }

func (g *gitFixture) installed() string {
	g.t.Helper()
	b, err := os.ReadFile(filepath.Join(g.repo, "installed.txt"))
	if err != nil {
		g.t.Fatal(err)
	}
	return strings.TrimSpace(string(b))
}

const (
	okBuild   = "cat app.txt > built.txt"
	okInstall = "cp built.txt installed.txt"
)

func (g *gitFixture) doc(name, build, install string) string {
	return svcDoc(name, fmt.Sprintf("  source:\n    repo: %s\n    branch: main\n    build: %q\n    install: %q\n", g.repo, build, install))
}

func realGit() envOption { return withRunner(execx.New(execx.WithEnvAllow("PATH", "HOME"))) }

func deployments(t *testing.T, e *env, name string) []catalog.Deployment {
	t.Helper()
	deps, err := e.cat.Deployments(context.Background(), name, 0, 0)
	if err != nil {
		t.Fatal(err)
	}
	return deps
}

func lifecycleSummaries(t *testing.T, e *env, name string) []string {
	t.Helper()
	page, err := e.cat.Timeline(context.Background(), catalog.TimelineQuery{Service: name, Kinds: []string{catalog.KindLifecycle}})
	if err != nil {
		t.Fatal(err)
	}
	var out []string
	for i := len(page.Entries) - 1; i >= 0; i-- {
		out = append(out, page.Entries[i].Summary)
	}
	return out
}

func TestDeploySuccessFirstAndUpdate(t *testing.T) {
	g := newGitFixture(t)
	e := newEnv(t, realGit())
	e.put(g.doc("app", okBuild, okInstall), 0)
	ad := e.runtime.adapter("app")
	v1 := g.head()

	res, err := e.mgr.Deploy(e.ctx, "app", catalog.ActionOptions{RunID: "run1"})
	if err != nil {
		t.Fatal(err)
	}
	if res.Version != v1 || res.From != catalog.StateRegistered || res.To != catalog.StateRunning || res.DeploymentID == 0 {
		t.Fatalf("first deploy = %+v", res)
	}
	if g.head() != v1 || g.installed() != "v1" {
		t.Fatalf("checkout at %s, installed %q", g.head(), g.installed())
	}
	if got := ad.Calls(); len(got) != 1 || got[0] != "start" {
		t.Fatalf("inactive service must be started: %v", got)
	}

	v2 := g.commit("v2")
	e.clk.Advance(60_000_000_000)
	res, err = e.mgr.Deploy(e.ctx, "app", catalog.ActionOptions{})
	if err != nil || res.Version != v2 || res.PreviousVersion != v1 || res.From != catalog.StateRunning {
		t.Fatalf("update = %+v %v", res, err)
	}
	if g.head() != v2 || g.installed() != "v2" {
		t.Fatalf("checkout at %s, installed %q", g.head(), g.installed())
	}
	if got := ad.Calls(); len(got) != 2 || got[1] != "restart" {
		t.Fatalf("active service must be restarted: %v", got)
	}

	if got, want := strings.Join(lifecycleSummaries(t, e, "app"), "; "),
		"registered -> deploying (deploy); deploying -> running (deploy); running -> updating (deploy); updating -> running (deploy)"; got != want {
		t.Fatalf("lifecycle = %s", got)
	}

	deps := deployments(t, e, "app")
	if len(deps) != 2 {
		t.Fatalf("deployments = %+v", deps)
	}
	d := deps[0]
	if d.Outcome != catalog.OutcomeSucceeded || d.Version != v2 || d.PreviousVersion != v1 || d.Action != "deploy" || d.Actor != alice ||
		d.FinishedAt.IsZero() || !strings.Contains(d.LogExcerpt, "git fetch") || !strings.Contains(d.LogExcerpt, "restarting service") {
		t.Fatalf("deployment = %+v", d)
	}
	if deps[1].RunID != "run1" || deps[1].Version != v1 {
		t.Fatalf("first deployment = %+v", deps[1])
	}

	page, _ := e.cat.Timeline(context.Background(), catalog.TimelineQuery{Service: "app", Kinds: []string{catalog.KindDeployment}})
	if len(page.Entries) != 2 || !strings.Contains(page.Entries[0].Summary, "deploy succeeded at "+v2[:12]) {
		t.Fatalf("deployment timeline = %+v", page.Entries)
	}
}

func TestDeployRefusesDirtyRepository(t *testing.T) {
	g := newGitFixture(t)
	e := newEnv(t, realGit())
	e.put(g.doc("app", okBuild, okInstall), 0)
	e.setState("app", catalog.StateRunning)
	ad := e.runtime.adapter("app")

	if err := os.WriteFile(filepath.Join(g.repo, "app.txt"), []byte("hand edit\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	_, err := e.mgr.Deploy(e.ctx, "app", catalog.ActionOptions{})
	if !errors.Is(err, catalog.ErrDirtyRepo) || !errors.Is(err, catalog.ErrPrecondition) {
		t.Fatalf("err = %v", err)
	}
	if e.state("app") != catalog.StateRunning || len(ad.Calls()) != 0 || len(deployments(t, e, "app")) != 0 {
		t.Fatalf("a refused deploy changed things: state %s, calls %v", e.state("app"), ad.Calls())
	}
	if e.locker.isHeld(catalog.LockName("app")) {
		t.Fatal("lock leaked")
	}

	// Untracked files cannot be lost by a checkout and do not block a deploy.
	g.git(g.repo, "checkout", "--", "app.txt")
	if err := os.WriteFile(filepath.Join(g.repo, "scratch.log"), []byte("x"), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := e.mgr.Deploy(e.ctx, "app", catalog.ActionOptions{}); err != nil {
		t.Fatalf("untracked file blocked the deploy: %v", err)
	}
}

func TestDeployBuildFailureLeavesFailed(t *testing.T) {
	g := newGitFixture(t)
	e := newEnv(t, realGit())
	e.put(g.doc("app", "echo compiling; echo 'syntax error on line 3' >&2; exit 3", okInstall), 0)
	e.setState("app", catalog.StateRunning)
	ad := e.runtime.adapter("app")
	v1 := g.head()

	res, err := e.mgr.Deploy(e.ctx, "app", catalog.ActionOptions{})
	var se *catalog.StepError
	if !errors.Is(err, catalog.ErrDeployFailed) || !errors.As(err, &se) || se.Step != "build" || execx.ExitCode(err) != 3 {
		t.Fatalf("err = %v", err)
	}
	if res.To != catalog.StateFailed || e.state("app") != catalog.StateFailed || res.Version != v1 {
		t.Fatalf("result %+v, state %s", res, e.state("app"))
	}
	if len(ad.Calls()) != 0 {
		t.Fatalf("a failed build must not restart the service: %v", ad.Calls())
	}
	d := deployments(t, e, "app")[0]
	if d.Outcome != catalog.OutcomeFailed || d.Version != v1 || !strings.Contains(d.Error, "build") ||
		!strings.Contains(d.LogExcerpt, "syntax error on line 3") || d.FinishedAt.IsZero() {
		t.Fatalf("deployment = %+v", d)
	}
	if got := lifecycleSummaries(t, e, "app"); got[len(got)-1] != "updating -> failed (deploy)" {
		t.Fatalf("lifecycle = %v", got)
	}
	entries, _ := e.audit.List(context.Background(), 1, 0)
	if entries[0].Action != "service.deploy" || entries[0].Outcome != "error" {
		t.Fatalf("audit = %+v", entries[0])
	}
}

func TestDeployOtherFailures(t *testing.T) {
	t.Run("restart fails", func(t *testing.T) {
		g := newGitFixture(t)
		e := newEnv(t, realGit())
		e.put(g.doc("app", okBuild, okInstall), 0)
		e.setState("app", catalog.StateRunning)
		e.runtime.adapter("app").set(func(a *fakeAdapter) { a.restartErr = errors.New("unit failed"); a.state = rt.StateActive })
		_, err := e.mgr.Deploy(e.ctx, "app", catalog.ActionOptions{})
		var se *catalog.StepError
		if !errors.As(err, &se) || se.Step != "restart" || e.state("app") != catalog.StateFailed {
			t.Fatalf("err = %v, state %s", err, e.state("app"))
		}
	})
	t.Run("fetch fails", func(t *testing.T) {
		g := newGitFixture(t)
		e := newEnv(t, realGit())
		e.put(g.doc("app", okBuild, okInstall), 0)
		if err := os.Rename(g.remote, g.remote+".gone"); err != nil {
			t.Fatal(err)
		}
		_, err := e.mgr.Deploy(e.ctx, "app", catalog.ActionOptions{})
		var se *catalog.StepError
		if !errors.As(err, &se) || se.Step != "fetch" || e.state("app") != catalog.StateFailed {
			t.Fatalf("err = %v, state %s", err, e.state("app"))
		}
	})
	t.Run("unknown version", func(t *testing.T) {
		g := newGitFixture(t)
		e := newEnv(t, realGit())
		e.put(g.doc("app", okBuild, okInstall), 0)
		_, err := e.mgr.Deploy(e.ctx, "app", catalog.ActionOptions{Version: "deadbeefdeadbeef"})
		var se *catalog.StepError
		if !errors.As(err, &se) || se.Step != "resolve" || e.state("app") != catalog.StateFailed {
			t.Fatalf("err = %v, state %s", err, e.state("app"))
		}
	})
	t.Run("invalid version", func(t *testing.T) {
		g := newGitFixture(t)
		e := newEnv(t, realGit())
		e.put(g.doc("app", okBuild, okInstall), 0)
		for _, v := range []string{"--upload-pack=x", "main", "abc", "zzzzzzzz", "HEAD~1"} {
			if _, err := e.mgr.Deploy(e.ctx, "app", catalog.ActionOptions{Version: v}); !errors.Is(err, catalog.ErrInvalidVersion) {
				t.Errorf("version %q: %v", v, err)
			}
		}
		if e.state("app") != catalog.StateRegistered || e.runner.count() != 0 {
			t.Fatal("an invalid version must be refused before any command runs")
		}
	})
	t.Run("no source", func(t *testing.T) {
		e := newEnv(t)
		e.put(svcDoc("app", ""), 0)
		if _, err := e.mgr.Deploy(e.ctx, "app", catalog.ActionOptions{}); !errors.Is(err, catalog.ErrNoSource) {
			t.Fatalf("err = %v", err)
		}
	})
}

func TestDeploySpecificVersionByShortSHA(t *testing.T) {
	g := newGitFixture(t)
	e := newEnv(t, realGit())
	e.put(g.doc("app", okBuild, okInstall), 0)
	v1 := g.head()
	g.commit("v2")

	res, err := e.mgr.Deploy(e.ctx, "app", catalog.ActionOptions{Version: v1[:8]})
	if err != nil || res.Version != v1 || g.installed() != "v1" {
		t.Fatalf("deploy %s = %+v %v (installed %q)", v1[:8], res, err, g.installed())
	}
}

func TestContextCancelledDuringDeployStillFinalizes(t *testing.T) {
	g := newGitFixture(t)
	ctx, cancel := context.WithCancel(context.Background())
	hook := &cancelRunner{inner: execx.New(execx.WithEnvAllow("PATH", "HOME")), match: "cat app.txt", cancel: cancel}
	e := newEnv(t, withRunner(hook))
	e.put(g.doc("app", okBuild, okInstall), 0)
	e.setState("app", catalog.StateRunning)

	_, err := e.mgr.Deploy(auth.WithActor(ctx, alice), "app", catalog.ActionOptions{})
	if !errors.Is(err, context.Canceled) || !errors.Is(err, catalog.ErrDeployFailed) {
		t.Fatalf("err = %v", err)
	}
	if e.state("app") != catalog.StateFailed {
		t.Fatalf("state = %s: bookkeeping must survive cancellation", e.state("app"))
	}
	if d := deployments(t, e, "app")[0]; d.Outcome != catalog.OutcomeFailed || d.FinishedAt.IsZero() {
		t.Fatalf("deployment = %+v", d)
	}
	if e.locker.isHeld(catalog.LockName("app")) {
		t.Fatal("lock leaked after cancellation")
	}
}

type cancelRunner struct {
	inner  execx.Runner
	match  string
	cancel context.CancelFunc
}

func (c *cancelRunner) Run(ctx context.Context, cmd execx.Cmd) (execx.Result, error) {
	if strings.Contains(cmd.Display(), c.match) {
		c.cancel()
	}
	return c.inner.Run(ctx, cmd)
}

func TestRollback(t *testing.T) {
	g := newGitFixture(t)
	e := newEnv(t, realGit())
	e.put(g.doc("app", okBuild, okInstall), 0)
	v1 := g.head()
	if _, err := e.mgr.Deploy(e.ctx, "app", catalog.ActionOptions{}); err != nil {
		t.Fatal(err)
	}
	if _, err := e.mgr.Rollback(e.ctx, "app", catalog.ActionOptions{}); !errors.Is(err, catalog.ErrNoRollbackTarget) || !errors.Is(err, catalog.ErrPrecondition) {
		t.Fatalf("rollback with a single deployment: %v", err)
	}
	if e.state("app") != catalog.StateRunning {
		t.Fatalf("refused rollback changed state to %s", e.state("app"))
	}

	v2 := g.commit("v2")
	if _, err := e.mgr.Deploy(e.ctx, "app", catalog.ActionOptions{}); err != nil {
		t.Fatal(err)
	}

	// After a successful deploy, rollback returns to the version it replaced.
	g.commit("v3-unused") // the remote moves on; rollback must not fetch or use it
	res, err := e.mgr.Rollback(e.ctx, "app", catalog.ActionOptions{})
	if err != nil || res.Version != v1 || res.PreviousVersion != v2 || res.To != catalog.StateRunning {
		t.Fatalf("rollback = %+v %v", res, err)
	}
	if g.head() != v1 || g.installed() != "v1" {
		t.Fatalf("checkout at %s, installed %q", g.head(), g.installed())
	}
	deps := deployments(t, e, "app")
	if deps[0].Action != "rollback" || deps[0].Outcome != catalog.OutcomeSucceeded || deps[0].Version != v1 {
		t.Fatalf("rollback deployment = %+v", deps[0])
	}
	for _, c := range e.runner.cmds[len(e.runner.cmds)-8:] {
		if strings.Contains(c, "fetch") {
			t.Fatalf("rollback must work offline, ran %q", c)
		}
	}
	if got := lifecycleSummaries(t, e, "app"); got[len(got)-2] != "running -> rolling_back (rollback)" {
		t.Fatalf("lifecycle = %v", got)
	}
}

func TestRollbackAfterFailedDeployReturnsToLastGood(t *testing.T) {
	g := newGitFixture(t)
	e := newEnv(t, realGit())
	// Version v3 cannot be built.
	build := "if grep -q v3 app.txt; then echo 'cannot build v3' >&2; exit 1; fi; cat app.txt > built.txt"
	e.put(g.doc("app", build, okInstall), 0)
	v1 := g.head()
	if _, err := e.mgr.Deploy(e.ctx, "app", catalog.ActionOptions{}); err != nil {
		t.Fatal(err)
	}
	v2 := g.commit("v2")
	if _, err := e.mgr.Deploy(e.ctx, "app", catalog.ActionOptions{}); err != nil {
		t.Fatal(err)
	}
	g.commit("v3")
	if _, err := e.mgr.Deploy(e.ctx, "app", catalog.ActionOptions{}); !errors.Is(err, catalog.ErrDeployFailed) {
		t.Fatalf("v3 deploy: %v", err)
	}
	if e.state("app") != catalog.StateFailed {
		t.Fatalf("state = %s", e.state("app"))
	}

	res, err := e.mgr.Rollback(e.ctx, "app", catalog.ActionOptions{})
	if err != nil || res.Version != v2 || res.From != catalog.StateFailed || res.To != catalog.StateRunning {
		t.Fatalf("rollback = %+v %v (want v2 %s, not v1 %s)", res, err, v2, v1)
	}
	if g.head() != v2 || g.installed() != "v2" {
		t.Fatalf("checkout at %s, installed %q", g.head(), g.installed())
	}
	if got := lifecycleSummaries(t, e, "app"); got[len(got)-2] != "failed -> rolling_back (rollback)" {
		t.Fatalf("lifecycle = %v", got)
	}
}

func TestDeployIsIdempotentPerKey(t *testing.T) {
	g := newGitFixture(t)
	e := newEnv(t, realGit())
	e.put(g.doc("app", okBuild, okInstall), 0)
	opts := catalog.ActionOptions{RunID: "run1", IdempotencyKey: "run1/deploy"}

	first, err := e.mgr.Deploy(e.ctx, "app", opts)
	if err != nil {
		t.Fatal(err)
	}
	cmds, calls := e.runner.count(), len(e.runtime.adapter("app").Calls())
	g.commit("v2") // a repeated deploy would pick this up

	again, err := e.mgr.Deploy(e.ctx, "app", opts)
	if err != nil || !again.Replayed || again.Version != first.Version || again.DeploymentID != first.DeploymentID {
		t.Fatalf("replay = %+v %v", again, err)
	}
	if e.runner.count() != cmds || len(e.runtime.adapter("app").Calls()) != calls || len(deployments(t, e, "app")) != 1 {
		t.Fatal("a replayed deploy executed again")
	}
}
