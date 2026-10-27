package configsync_test

import (
	"context"
	"log/slog"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"go.uber.org/goleak"

	"github.com/ACaccel/lcm/internal/audit"
	"github.com/ACaccel/lcm/internal/auth"
	"github.com/ACaccel/lcm/internal/catalog"
	"github.com/ACaccel/lcm/internal/clock"
	"github.com/ACaccel/lcm/internal/configsync"
	"github.com/ACaccel/lcm/internal/engine"
	"github.com/ACaccel/lcm/internal/execx"
	"github.com/ACaccel/lcm/internal/modules"
	"github.com/ACaccel/lcm/internal/node"
	"github.com/ACaccel/lcm/internal/nodes/core"
	"github.com/ACaccel/lcm/internal/signal"
	"github.com/ACaccel/lcm/internal/store"
	"github.com/ACaccel/lcm/internal/store/storetest"
)

func TestMain(m *testing.M) {
	goleak.VerifyTestMain(m)
}

var epoch = time.Date(2026, 3, 1, 12, 0, 0, 0, time.UTC)

// env wires a Syncer to real components on a temp SQLite file.
type env struct {
	t     *testing.T
	db    *store.DB
	clk   *clock.Fake
	bus   *signal.Bus
	cat   *catalog.Catalog
	mods  *modules.Store
	eng   *engine.Engine
	audit *audit.Log
	sync  *configsync.Syncer
	// ctx carries a human operator; syncCtx is what a manual caller uses.
	ctx context.Context
}

func newEnv(t *testing.T) *env {
	t.Helper()
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git is not installed")
	}
	e := &env{t: t, db: storetest.Open(t), clk: clock.NewFake(epoch)}
	e.ctx = auth.WithActor(context.Background(), auth.Actor{Kind: auth.KindUser, ID: "1", Name: "alice"})
	e.audit = audit.New(e.db, audit.WithClock(e.clk.Now))
	e.bus = signal.NewBus(discardLogger(), e.clk.Now)
	var err error
	if e.cat, err = catalog.New(catalog.Config{DB: e.db, Audit: e.audit, Bus: e.bus, Clock: e.clk}); err != nil {
		t.Fatal(err)
	}
	if e.mods, err = modules.New(e.db, e.audit, e.clk); err != nil {
		t.Fatal(err)
	}
	reg := node.NewRegistry()
	core.Register(reg)
	if e.eng, err = engine.New(engine.Config{DB: e.db, Registry: reg, Modules: e.mods, Workers: 2, Clock: e.clk, Audit: e.audit}); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
		defer cancel()
		if err := e.eng.Close(ctx); err != nil {
			t.Errorf("engine close: %v", err)
		}
	})
	e.sync, err = configsync.New(configsync.Config{
		DB: e.db, Audit: e.audit, Catalog: e.cat, Modules: e.mods, Engine: e.eng, Bus: e.bus,
		Runner: execx.New(), Dir: filepath.Join(t.TempDir(), "sources"), Clock: e.clk,
	})
	if err != nil {
		t.Fatal(err)
	}
	return e
}

func serviceYAML(name, extra string) string {
	return "apiVersion: lcm/v1\nkind: Service\nmetadata: { name: " + name + " }\nspec:\n" +
		"  runtime: { type: command, start: " + name + "-start, stop: " + name + "-stop, restart: " + name + "-restart, status: " + name + "-status }\n" + extra
}

const echoNodes = `    - { id: start, type: trigger.manual, next: { out: [echo] } }
    - { id: echo, type: data.set, props: { values: { got: "x" } } }
`

func policyYAML(name, nodes string) string {
	return "apiVersion: lcm/v1\nkind: Policy\nmetadata: { name: " + name + " }\nspec:\n  nodes:\n" + nodes
}

func moduleYAML(name, value string) string {
	return "apiVersion: lcm/v1\nkind: Module\nmetadata: { name: " + name + " }\nspec:\n  nodes:\n" +
		"    - { id: in, type: module.input, next: { out: [set] } }\n" +
		"    - { id: set, type: data.set, props: { values: { v: " + value + " } }, next: { out: [out] } }\n" +
		"    - { id: out, type: module.output, props: { port: out } }\n"
}

func docs(path string, texts ...string) []configsync.Document {
	out := make([]configsync.Document, len(texts))
	for i, t := range texts {
		out[i] = configsync.Document{Path: path, Data: []byte(t)}
	}
	return out
}

// apply runs a manual apply and fails the test on a call error.
func (e *env) apply(opts configsync.Options, texts ...string) []configsync.Result {
	e.t.Helper()
	res, err := e.sync.Apply(e.ctx, docs("test.yaml", texts...), opts)
	if err != nil {
		e.t.Fatalf("Apply: %v", err)
	}
	return res
}

func byRef(res []configsync.Result) map[string]configsync.Result {
	m := map[string]configsync.Result{}
	for _, r := range res {
		if _, dup := m[r.Ref().String()]; !dup {
			m[r.Ref().String()] = r
		}
	}
	return m
}

func wantAction(t *testing.T, res []configsync.Result, ref string, want configsync.Action) configsync.Result {
	t.Helper()
	r, ok := byRef(res)[ref]
	if !ok {
		t.Fatalf("no result for %s in %+v", ref, res)
	}
	if r.Action != want {
		t.Fatalf("%s: action = %s (%s %v), want %s", ref, r.Action, r.Reason, r.Issues, want)
	}
	return r
}

// gitRepo is a bare "remote" with a working clone to commit from.
type gitRepo struct {
	t      *testing.T
	remote string
	work   string
}

func git(t *testing.T, dir string, args ...string) string {
	t.Helper()
	//nolint:gosec // test helper; the arguments are literals of the tests
	cmd := exec.Command("git", append([]string{"-c", "user.name=test", "-c", "user.email=test@example.invalid", "-c", "commit.gpgsign=false"}, args...)...)
	cmd.Dir = dir
	cmd.Env = append(os.Environ(), "GIT_TERMINAL_PROMPT=0")
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("git %s: %v\n%s", strings.Join(args, " "), err, out)
	}
	return strings.TrimSpace(string(out))
}

func newRepo(t *testing.T) *gitRepo {
	t.Helper()
	root := t.TempDir()
	r := &gitRepo{t: t, remote: filepath.Join(root, "remote.git"), work: filepath.Join(root, "work")}
	git(t, root, "init", "--bare", "--quiet", "-b", "main", r.remote)
	git(t, root, "clone", "--quiet", r.remote, r.work)
	git(t, r.work, "checkout", "-q", "-B", "main")
	return r
}

// commit writes files (an empty content removes the file), commits and pushes.
func (r *gitRepo) commit(msg string, files map[string]string) string {
	r.t.Helper()
	for name, content := range files {
		p := filepath.Join(r.work, name)
		if content == "" {
			if err := os.Remove(p); err != nil {
				r.t.Fatal(err)
			}
			continue
		}
		if err := os.MkdirAll(filepath.Dir(p), 0o750); err != nil {
			r.t.Fatal(err)
		}
		if err := os.WriteFile(p, []byte(content), 0o600); err != nil {
			r.t.Fatal(err)
		}
	}
	git(r.t, r.work, "add", "-A")
	git(r.t, r.work, "commit", "--quiet", "-m", msg)
	git(r.t, r.work, "push", "--quiet", "origin", "main")
	return git(r.t, r.work, "rev-parse", "HEAD")
}

// addSource registers a source on the repo's remote.
func (e *env) addSource(name string, r *gitRepo, path string) configsync.Source {
	e.t.Helper()
	src, err := e.sync.CreateSource(e.ctx, configsync.Source{Name: name, Repo: r.remote, Branch: "main", Path: path, Enabled: true})
	if err != nil {
		e.t.Fatalf("CreateSource: %v", err)
	}
	return src
}

func (e *env) syncNow(name string) configsync.Report {
	e.t.Helper()
	rep, err := e.sync.SyncNow(e.ctx, name)
	if err != nil {
		e.t.Fatalf("SyncNow: %v", err)
	}
	if rep.Error != "" {
		e.t.Fatalf("sync error: %s", rep.Error)
	}
	return rep
}

func (e *env) owner(kind, name string) *configsync.Ownership {
	e.t.Helper()
	k, err := configsync.ParseKind(kind)
	if err != nil {
		e.t.Fatal(err)
	}
	o, err := e.sync.Owner(context.Background(), k, name)
	if err != nil {
		e.t.Fatal(err)
	}
	return o
}

// editService changes a service the way the web UI does: a plain save
// based on the current version.
func (e *env) editService(name, doc string) {
	e.t.Helper()
	rec, err := e.cat.Get(e.ctx, name)
	if err != nil {
		e.t.Fatal(err)
	}
	if _, err := e.cat.Put(e.ctx, []byte(doc), rec.Version); err != nil {
		e.t.Fatalf("edit service: %v", err)
	}
}

func discardLogger() *slog.Logger { return slog.New(slog.DiscardHandler) }
