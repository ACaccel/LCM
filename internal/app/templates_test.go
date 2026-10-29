package app_test

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"maps"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/ACaccel/lcm/internal/catalog"
	"github.com/ACaccel/lcm/internal/engine"
	"github.com/ACaccel/lcm/internal/execx"
	"github.com/ACaccel/lcm/internal/templates"
)

// TestEveryTemplateDeploys renders each template with its minimal
// parameters and deploys it through the real compiler.
func TestEveryTemplateDeploys(t *testing.T) {
	h := newHarness(t)
	for _, tpl := range templates.List() {
		params := map[string]any{"service": "web"}
		if tpl.ID == "auto-deploy" {
			params = map[string]any{"service": "web", "repo": "https://example.com/web.git", "requireCI": true, "ciRepo": "me/web", "ciWorkflows": []any{"ci.yml"}}
		}
		doc, err := tpl.Render("web-"+tpl.ID, params)
		if err != nil {
			t.Fatalf("%s: %v", tpl.ID, err)
		}
		if _, err := h.app.Engine.Deploy(h.ctx, doc); err != nil {
			t.Errorf("%s does not deploy: %v\n%s", tpl.ID, err, doc)
		}
		// The raw template document is for reading, not for deploying: its
		// trigger props still hold parameter references.
		if tpl.ID == "auto-deploy" {
			if _, err := h.app.Engine.Validate(h.ctx, []byte(tpl.Document())); err == nil {
				t.Errorf("%s: the raw template unexpectedly validates", tpl.ID)
			}
		}
	}
}

// discordRecorder behaves like a Discord webhook and records the messages.
type discordRecorder struct {
	*httptest.Server
	mu       sync.Mutex
	messages []string
	n        atomic.Int32
}

func newDiscord(t *testing.T) *discordRecorder {
	t.Helper()
	d := &discordRecorder{}
	d.Server = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		var m struct {
			Content string `json:"content"`
			// konata relay fields
			Summary string `json:"summary"`
			Status  string `json:"status"`
			Stages  []struct {
				Label, State string
			} `json:"stages"`
		}
		_ = json.Unmarshal(body, &m)
		msg := r.Method + " " + strings.ReplaceAll(m.Content, "\n", " | ")
		if r.Method == http.MethodPut {
			if r.Header.Get("Authorization") != "Bearer RELAY-KEY" {
				w.WriteHeader(http.StatusUnauthorized)
				return
			}
			msg = "PUT " + m.Status + " | " + m.Summary
			for _, st := range m.Stages {
				msg += " | " + st.Label + "=" + st.State
			}
		}
		d.mu.Lock()
		d.messages = append(d.messages, msg)
		d.mu.Unlock()
		w.Header().Set("Content-Type", "application/json")
		_, _ = fmt.Fprintf(w, `{"id":"msg%d","messageId":"msg%d","created":true}`, d.n.Add(1), d.n.Load())
	}))
	t.Cleanup(d.Close)
	return d
}

func (d *discordRecorder) all() []string {
	d.mu.Lock()
	defer d.mu.Unlock()
	return append([]string(nil), d.messages...)
}

// staticGit is a remote whose head tests move.
type staticGit struct {
	mu   sync.Mutex
	head string
}

func (g *staticGit) set(sha string) {
	g.mu.Lock()
	g.head = sha
	g.mu.Unlock()
}

func (g *staticGit) RemoteHead(context.Context, string, string) (string, error) {
	g.mu.Lock()
	defer g.mu.Unlock()
	return g.head, nil
}

const (
	shaOld = "1111111111111111111111111111111111111111"
	shaA   = "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"
	shaB   = "bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb"
)

// deployFixture is a service "web" under the auto-deploy template with a
// fake git remote and a Discord stub.
type deployFixture struct {
	*harness
	web     *switchable
	discord *discordRecorder
	git     *staticGit
	// served is the commit the fake checkout holds; the service is healthy
	// only while it equals goodCommit.
	served     atomic.Value
	goodCommit atomic.Value
	// badBuild is a commit whose build fails.
	badBuild atomic.Value
}

// newDeployFixture instantiates auto-deploy with Discord notifications;
// params override the template parameters.
func newDeployFixture(t *testing.T, params ...map[string]any) *deployFixture {
	t.Helper()
	git := &staticGit{head: shaOld}
	f := &deployFixture{git: git, discord: newDiscord(t), web: newSwitchable(t)}
	f.served.Store(shaOld)
	f.goodCommit.Store(shaA)
	f.badBuild.Store("")
	runner := &execx.Fake{}
	f.harness = newHarness(t, func(o *testOpts) { o.git, o.runner = git, runner })

	// The fake checkout: --detach moves it, restarting applies it to the service.
	runner.On(matchCmd("status --porcelain"), func(execx.Cmd) (execx.Result, error) { return execx.Result{}, nil })
	runner.On(matchCmd("rev-parse HEAD"), func(execx.Cmd) (execx.Result, error) {
		return execx.Result{Output: f.served.Load().(string) + "\n"}, nil
	})
	runner.On(matchCmd("rev-parse --verify"), func(c execx.Cmd) (execx.Result, error) {
		ref := c.Argv[len(c.Argv)-1]
		return execx.Result{Output: strings.TrimSuffix(ref, "^{commit}") + "\n"}, nil
	})
	runner.On(matchCmd("checkout --detach"), func(c execx.Cmd) (execx.Result, error) {
		f.served.Store(c.Argv[len(c.Argv)-1])
		return execx.Result{}, nil
	})
	runner.On(matchCmd("web-build"), func(execx.Cmd) (execx.Result, error) {
		if f.served.Load() == f.badBuild.Load() {
			return execx.Result{ExitCode: 2}, &execx.ExitError{Code: 2}
		}
		return execx.Result{}, nil
	})
	runner.On(matchCmd("web-restart"), func(execx.Cmd) (execx.Result, error) {
		f.web.healthy.Store(f.served.Load() == f.goodCommit.Load() || f.served.Load() == shaOld)
		return execx.Result{}, nil
	})

	secrets := filepath.Join(f.dir, "secrets")
	if err := os.MkdirAll(secrets, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(secrets, "ops-hook"), []byte(f.discord.URL+"/api/webhooks/1/TOKEN\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(secrets, "relay-key"), []byte("RELAY-KEY\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	f.putService(fmt.Sprintf(`
apiVersion: lcm/v1
kind: Service
metadata: { name: web }
spec:
  runtime: { type: command, start: web-start, stop: web-stop, restart: web-restart, status: web-status }
  source: { repo: /srv/web, branch: main, build: web-build, install: "true" }
  checks:
    - { name: http, type: http, interval: %s, timeout: 2s, props: { url: %q } }
`, checkInterval, f.web.srv.URL))
	tpl, _ := templates.Get("auto-deploy")
	p := map[string]any{"service": "web", "repo": "https://example.com/web.git", "requireCI": false, "notifySecret": "ops-hook"}
	for _, extra := range params {
		maps.Copy(p, extra)
	}
	// "relay" stands for the stub's konata route.
	if p["notifyUrl"] == "relay" {
		p["notifyUrl"] = f.discord.URL + "/notifications/deploys"
	}
	doc, err := tpl.Render("web-auto-deploy", p)
	if err != nil {
		t.Fatal(err)
	}
	f.deploy(string(doc))
	if _, err := f.app.Manager.Start(f.ctx, "web", catalog.ActionOptions{}); err != nil {
		t.Fatal(err)
	}
	// A rollback needs an earlier successful deployment to go back to.
	if _, err := f.app.Manager.Deploy(f.ctx, "web", catalog.ActionOptions{Version: shaOld}); err != nil {
		t.Fatal(err)
	}
	if err := f.app.SyncMonitor(f.ctx); err != nil {
		t.Fatal(err)
	}
	f.advance(checkInterval)
	// The git trigger records the current head with its first poll; the
	// push must come after that, which its armed timer shows.
	base := f.clk.Pending()
	f.syncTriggers()
	f.waitTimers(base + 1)
	return f
}

// push moves the remote head and lets the git trigger notice it.
func (f *deployFixture) push(sha string) string {
	f.t.Helper()
	f.git.set(sha)
	f.advance(time.Minute)
	ev := f.events.WaitFor(f.t, "deploy run of "+sha[:7], func(e engine.Event) bool {
		return e.Kind == engine.EventRunQueued && e.Policy == "web-auto-deploy"
	})
	return ev.RunID
}

// finish advances time in health-check steps until the run ends. Before each
// step it waits until the run is parked on its next poll, so that no step
// can overtake a timer that is not armed yet.
func (f *deployFixture) finish(runID string) engine.RunView {
	f.t.Helper()
	for range 60 {
		want := f.clk.Now().Add(10 * time.Second)
		f.events.WaitFor(f.t, "run to finish or park", func(e engine.Event) bool {
			if e.RunID != runID {
				return false
			}
			parked := e.Kind == engine.EventNodeSuspended && e.RetryAt != nil && e.RetryAt.Equal(want)
			return e.Kind == engine.EventRunFinished || parked
		})
		view, err := f.app.Engine.Run(f.ctx, runID)
		if err != nil {
			f.t.Fatal(err)
		}
		if view.Status.Terminal() {
			return view
		}
		f.advance(10 * time.Second)
	}
	f.t.Fatalf("run %s did not finish", runID)
	return engine.RunView{}
}

func TestAutoDeployTemplateDeploysHealthyCommit(t *testing.T) {
	f := newDeployFixture(t)
	f.goodCommit.Store(shaA)
	f.web.healthy.Store(true)

	run := f.push(shaA)
	view := f.finish(run)
	if view.Status != engine.RunSucceeded {
		t.Fatalf("run = %s (%s)", view.Status, view.Error)
	}
	for _, id := range []string{"announce", "deploy", "healthy", "done"} {
		if n, _ := view.Node(id); n.Status != engine.NodeCompleted {
			t.Errorf("node %s = %s", id, n.Status)
		}
	}
	if n, _ := view.Node("rollback"); n.Status == engine.NodeCompleted {
		t.Error("rolled back a healthy deployment")
	}
	if f.served.Load() != shaA {
		t.Errorf("checkout = %v, want %s", f.served.Load(), shaA)
	}
	// One message, posted at the start and edited at each stage.
	msgs := f.discord.all()
	if len(msgs) != 4 || !strings.HasPrefix(msgs[0], "POST ") || !strings.HasPrefix(msgs[3], "PATCH ") ||
		!strings.Contains(msgs[3], "Deployed aaaaaaa and the service is healthy. | Status: succeeded") || !strings.Contains(msgs[3], "✓ Health check") {
		t.Fatalf("discord = %q", msgs)
	}
}

func TestAutoDeployTemplateRollsBackUnhealthyCommit(t *testing.T) {
	f := newDeployFixture(t)
	f.goodCommit.Store(shaA) // commit B starts but never turns healthy
	run := f.push(shaB)
	view := f.finish(run)
	if view.Status != engine.RunSucceeded {
		t.Fatalf("run = %s (%s)", view.Status, view.Error)
	}
	if n, _ := view.Node("rolled_back"); n.Status != engine.NodeCompleted {
		t.Fatalf("rolled_back = %s", n.Status)
	}
	if n, _ := view.Node("done"); n.Status == engine.NodeCompleted {
		t.Error("reported success for an unhealthy commit")
	}
	if f.served.Load() != shaOld {
		t.Errorf("checkout after rollback = %v, want %s", f.served.Load(), shaOld)
	}
	msgs := f.discord.all()
	last := msgs[len(msgs)-1]
	if len(msgs) != 5 || !strings.Contains(last, "did not become healthy within 5m. Rolled back") ||
		!strings.Contains(last, "✓ Build and install | ✗ Health check | ✓ Rollback") {
		t.Fatalf("discord = %q", msgs)
	}
	// Each deployment starts a fresh message instead of editing the last one.
	f.git.set(shaA)
	f.advance(time.Minute)
	f.events.WaitFor(t, "second deploy run", func(e engine.Event) bool {
		return e.Kind == engine.EventRunQueued && e.Policy == "web-auto-deploy" && e.RunID != run
	})
}

// With notifyTarget konata the same stages go to the relay as structured
// updates of one thread; a failed build is reported as such.
func TestAutoDeployTemplateReportsToKonata(t *testing.T) {
	f := newDeployFixture(t, map[string]any{"notifyTarget": "konata", "notifySecret": "", "notifyUrl": "relay", "notifyTokenSecret": "relay-key"})
	f.badBuild.Store(shaB)
	run := f.push(shaB)
	view := f.finish(run)
	if view.Status != engine.RunSucceeded {
		t.Fatalf("run = %s (%s)", view.Status, view.Error)
	}
	msgs := f.discord.all()
	if len(msgs) != 4 {
		t.Fatalf("konata = %q", msgs)
	}
	want := "PUT failed | Deploying bbbbbbb failed. Rolled back to the previous version. | CI=skipped | Deploy window=done | Build and install=failed | Health check=skipped | Rollback=done"
	if msgs[3] != want {
		t.Fatalf("last update = %q, want %q", msgs[3], want)
	}
}

// TestBackupTemplateConfirmsWithHeartbeat runs the scheduled-backup template
// to its heartbeat confirmation, with and without the heartbeat arriving.
func TestBackupTemplateConfirmsWithHeartbeat(t *testing.T) {
	for _, tc := range []struct {
		name      string
		heartbeat bool
		wantNode  string
		wantNot   string
	}{
		{"heartbeat arrives", true, "heartbeat", "heartbeat_missing"},
		{"heartbeat missing", false, "heartbeat_missing", ""},
	} {
		t.Run(tc.name, func(t *testing.T) {
			discord := newDiscord(t)
			h := newHarness(t)
			secrets := filepath.Join(h.dir, "secrets")
			if err := os.MkdirAll(secrets, 0o700); err != nil {
				t.Fatal(err)
			}
			if err := os.WriteFile(filepath.Join(secrets, "ops-hook"), []byte(discord.URL+"/api/webhooks/1/TOKEN\n"), 0o600); err != nil {
				t.Fatal(err)
			}
			h.putService(`
apiVersion: lcm/v1
kind: Service
metadata: { name: db }
spec:
  runtime: { type: command, start: db-start, stop: db-stop, status: db-status }
  backup: { command: backup-db.sh }
  checks:
    - { name: dump, type: heartbeat, interval: 24h, critical: false }
`)
			tpl, _ := templates.Get("scheduled-backup")
			doc, err := tpl.Render("db-backup", map[string]any{
				"service": "db", "schedule": "0 15 * * *", "timezone": "UTC", "heartbeatCheck": "dump",
				"heartbeatTimeout": "10m", "notifySecret": "ops-hook",
			})
			if err != nil {
				t.Fatal(err)
			}
			h.deploy(string(doc))
			if err := h.app.SyncMonitor(h.ctx); err != nil {
				t.Fatal(err)
			}
			base := h.clk.Pending()
			h.syncTriggers()
			h.waitTimers(base + 1)

			// 12:00 now; the backup is due at 15:00.
			h.clk.Advance(3 * time.Hour)
			run := h.events.WaitFor(t, "backup run", func(e engine.Event) bool {
				return e.Kind == engine.EventRunQueued && e.Policy == "db-backup"
			}).RunID
			for i := range 12 {
				want := h.clk.Now().Add(time.Minute)
				ev := h.events.WaitFor(t, "heartbeat wait or the end of the run", func(e engine.Event) bool {
					if e.RunID != run {
						return false
					}
					parked := e.NodeID == "heartbeat" && e.Kind == engine.EventNodeSuspended && e.RetryAt != nil && e.RetryAt.Equal(want)
					return e.Kind == engine.EventRunFinished || parked
				})
				if ev.Kind == engine.EventRunFinished {
					break
				}
				if tc.heartbeat && i == 3 {
					if err := h.app.Monitor.Heartbeat(h.ctx, "db", "dump", []byte("done")); err != nil {
						t.Fatal(err)
					}
				}
				h.advance(time.Minute)
			}
			ctx, cancel := context.WithTimeout(context.Background(), waitTimeout)
			defer cancel()
			view, err := h.app.Engine.Wait(ctx, run)
			if err != nil || view.Status != engine.RunSucceeded {
				t.Fatalf("run = %v, %v", view.Status, err)
			}
			if n, _ := view.Node(tc.wantNode); n.Status != engine.NodeCompleted {
				t.Errorf("%s = %s", tc.wantNode, n.Status)
			}
			if tc.wantNot != "" {
				if n, _ := view.Node(tc.wantNot); n.Status == engine.NodeCompleted {
					t.Errorf("%s ran", tc.wantNot)
				}
			}
			msgs := discord.all()
			if tc.heartbeat != (len(msgs) == 0) {
				t.Errorf("notifications = %q", msgs)
			}
		})
	}
}
