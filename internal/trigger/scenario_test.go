package trigger_test

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/ACaccel/lcm/internal/engine"
	"github.com/ACaccel/lcm/internal/engine/enginetest"
	"github.com/ACaccel/lcm/internal/lock"
	"github.com/ACaccel/lcm/internal/node"
	"github.com/ACaccel/lcm/internal/nodes/flow"
	"github.com/ACaccel/lcm/internal/nodes/guard"
	"github.com/ACaccel/lcm/internal/nodes/network"
	"github.com/ACaccel/lcm/internal/nodes/notify"
	"github.com/ACaccel/lcm/internal/nodes/stateful"
	"github.com/ACaccel/lcm/internal/state"
)

// These tests run the reference scenarios of the design (3.13) end to end:
// real engine, trigger manager and node packages against httptest stubs and
// fake action nodes (the lifecycle nodes belong to another phase).

type recorded struct {
	Method, Path, Query, Body string
}

// recorder is an HTTP stub that logs requests and answers through handle.
type recorder struct {
	*httptest.Server
	mu   sync.Mutex
	reqs []recorded
}

func newRecorder(t *testing.T, handle func(w http.ResponseWriter, r *http.Request)) *recorder {
	t.Helper()
	rec := &recorder{}
	rec.Server = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		b, _ := io.ReadAll(r.Body)
		rec.mu.Lock()
		rec.reqs = append(rec.reqs, recorded{r.Method, r.URL.Path, r.URL.RawQuery, string(b)})
		rec.mu.Unlock()
		handle(w, r)
	}))
	t.Cleanup(rec.Close)
	return rec
}

func (r *recorder) all() []recorded {
	r.mu.Lock()
	defer r.mu.Unlock()
	return append([]recorded(nil), r.reqs...)
}

// contents returns the message text of every Discord request, in order,
// prefixed with the method.
func (r *recorder) contents() []string {
	var out []string
	for _, q := range r.all() {
		var body map[string]any
		if json.Unmarshal([]byte(q.Body), &body) == nil {
			out = append(out, q.Method+" "+fmt.Sprint(body["content"]))
		}
	}
	return out
}

// discordStub behaves like a Discord webhook: POST with wait=true returns a
// message id, PATCH edits it.
func discordStub(t *testing.T) *recorder {
	t.Helper()
	var n atomic.Int32
	return newRecorder(t, func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		if r.Method == http.MethodPatch {
			_, _ = io.WriteString(w, `{"id":"edited"}`)
			return
		}
		_ = json.NewEncoder(w).Encode(map[string]string{"id": fmt.Sprintf("msg%d", n.Add(1))})
	})
}

// actions counts the fake lifecycle nodes.
type actions struct {
	deploys, rollbacks, backups atomic.Int32
	deployedVersion             atomic.Value
}

func registerFakes(h *enginetest.Harness, a *actions) {
	type deployProps struct {
		Version string `json:"version"`
	}
	node.Register(h.Registry, node.Spec[deployProps]{
		Type: "fake.deploy", Category: "lifecycle", Effect: node.EffectWrite,
		Run: func(_ context.Context, _ *node.RunContext, p deployProps) (node.Result, error) {
			a.deploys.Add(1)
			a.deployedVersion.Store(p.Version)
			return node.Result{Output: map[string]any{"version": p.Version}}, nil
		},
	})
	node.Register(h.Registry, node.Spec[struct{}]{
		Type: "fake.rollback", Category: "lifecycle", Effect: node.EffectWrite,
		Run: func(context.Context, *node.RunContext, struct{}) (node.Result, error) {
			a.rollbacks.Add(1)
			return node.Result{Output: map[string]any{"rolledBack": true}}, nil
		},
	})
	node.Register(h.Registry, node.Spec[struct{}]{
		Type: "fake.backup", Category: "lifecycle", Effect: node.EffectWrite,
		Run: func(_ context.Context, rc *node.RunContext, _ struct{}) (node.Result, error) {
			a.backups.Add(1)
			return node.Result{Output: map[string]any{"startedMs": rc.Now().UnixMilli()}}, nil
		},
	})
}

type scenario struct {
	*env
	a       *actions
	discord *recorder
	state   *state.Store
	gh      *recorder
	health  *recorder
	// ciRuns is the check-runs JSON the GitHub stub returns; healthVersion
	// is the version the health endpoint reports (empty: 503).
	ciRuns        atomic.Value
	healthVersion atomic.Value
}

func newScenario(t *testing.T) *scenario {
	t.Helper()
	s := &scenario{a: &actions{}, discord: discordStub(t)}
	s.ciRuns.Store(`{"total_count":0,"check_runs":[]}`)
	s.healthVersion.Store("")
	s.gh = newRecorder(t, func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = io.WriteString(w, s.ciRuns.Load().(string))
	})
	s.health = newRecorder(t, func(w http.ResponseWriter, r *http.Request) {
		v := s.healthVersion.Load().(string)
		if v == "" {
			w.WriteHeader(http.StatusServiceUnavailable)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]string{"version": v})
	})
	secrets := fixedSecrets{
		"discord-hook": s.discord.URL + "/api/webhooks/9/TOKEN",
		"gh-token":     "ghp_test",
	}
	s.env = newEnv(t, secrets, "", func(h *enginetest.Harness, cfg *engine.Config) {
		st := state.New(h.DB, state.WithClock(h.Clock.Now))
		s.state = st
		cfg.Vars = append(cfg.Vars, st.Var())
		lm := lock.New(h.DB, lock.WithClock(h.Clock))
		flow.Register(h.Registry, flow.Deps{Secrets: secrets})
		guard.Register(h.Registry)
		network.Register(h.Registry, network.Deps{Secrets: secrets, GitHubAPI: s.gh.URL})
		notify.Register(h.Registry, notify.Deps{Secrets: secrets, State: st})
		stateful.Register(h.Registry, stateful.Deps{State: st, Locks: lm})
		registerFakes(h, s.a)
	})
	return s
}

func ciRuns(statuses ...string) string {
	type run struct {
		Name       string `json:"name"`
		Status     string `json:"status"`
		Conclusion string `json:"conclusion"`
	}
	var runs []run
	for i, st := range statuses {
		r := run{Name: fmt.Sprintf("job%d", i), Status: "completed", Conclusion: st}
		if st == "pending" {
			r.Status, r.Conclusion = "in_progress", ""
		}
		runs = append(runs, r)
	}
	b, _ := json.Marshal(map[string]any{"total_count": len(runs), "check_runs": runs})
	return string(b)
}

// parked waits until the node of the run is suspended with its timer armed
// for epoch+at. extraTimers counts the timers other components hold.
func (x *env) parked(t *testing.T, runID, nodeID string, at time.Duration, extraTimers int) {
	t.Helper()
	want := enginetest.Epoch.Add(at)
	x.h.Events.WaitFor(t, fmt.Sprintf("%s suspended until +%s", nodeID, at), func(e engine.Event) bool {
		return e.Kind == engine.EventNodeSuspended && e.RunID == runID && e.NodeID == nodeID && e.RetryAt != nil && e.RetryAt.Equal(want)
	})
	x.h.WaitTimers(1 + extraTimers)
}

func (x *env) advance(d time.Duration) { x.h.Clock.Advance(d) }

func nodeStatus(v engine.RunView, id string) engine.NodeStatus {
	n, _ := v.Node(id)
	return n.Status
}

// Scenario 1: auto deploy. git -> ci.status -> guard.window -> deploy ->
// wait.until health -> notify (edited in place); a failed health wait rolls
// back instead.
func (s *scenario) deployPolicy() string {
	msg := func(id, text, next string) string {
		return fmt.Sprintf(`    - { id: %s, type: notify, props: { target: discord, urlSecret: discord-hook, stateKey: deploy-msg, message: %q }%s }
`, id, text, next)
	}
	return policy("auto-deploy", `    - { id: push, type: trigger.git, props: { repo: "https://github.com/acme/app.git", branch: main, interval: 1m }, next: { out: [announce] } }
`+msg("announce", "started", ", next: { out: [ci] }")+`    - id: ci
      type: ci.status
      props: { repo: acme/app, commit: "{{ trigger.commit }}", tokenSecret: gh-token, waitFor: 30m, interval: 1m }
      next: { success: [window], failure: [cifail], pending: [cifail] }
    - { id: window, type: guard.window, props: { windows: [{ start: "02:00", end: "05:00" }], wait: true }, next: { out: [deploy], blocked: [cifail] } }
    - { id: deploy, type: fake.deploy, props: { version: "{{ trigger.commit }}" }, next: { out: [health], error: [rollback] } }
    - id: health
      type: wait.until
      props:
        interval: 30s
        timeout: 5m
        probe: { url: "`+s.health.URL+`/health" }
        condition: "probe.body.version == inputs.want"
        inputs: { want: "{{ trigger.commit }}" }
      next: { out: [done], timeout: [rollback] }
`+msg("done", "deployed", "")+`    - { id: rollback, type: fake.rollback, next: { out: [rolledback] } }
`+msg("rolledback", "rolled back", "")+msg("cifail", "ci failed", ""))
}

func (s *scenario) startDeploy(t *testing.T) (runID string, seen map[string]bool) {
	t.Helper()
	s.git.set("https://github.com/acme/app.git", "main", "aaaaaaa")
	s.h.Deploy(s.e, s.deployPolicy())
	s.sync(t)
	s.timers(1) // first poll recorded aaa without a run
	s.git.set("https://github.com/acme/app.git", "main", "bbbbbbb")
	s.advance(time.Minute)
	seen = map[string]bool{}
	ev := s.h.Events.WaitFor(t, "deploy run", func(e engine.Event) bool {
		return e.Kind == engine.EventRunQueued && e.Policy == "auto-deploy"
	})
	seen[ev.RunID] = true
	return ev.RunID, seen
}

func TestScenarioAutoDeploy(t *testing.T) {
	s := newScenario(t)
	s.ciRuns.Store(ciRuns("success", "pending"))
	id, _ := s.startDeploy(t)

	// CI is pending for two polls (the git watcher holds one more timer).
	s.parked(t, id, "ci", 2*time.Minute, 1)
	s.advance(time.Minute)
	s.parked(t, id, "ci", 3*time.Minute, 1)
	s.ciRuns.Store(ciRuns("success", "success"))
	s.advance(time.Minute)

	// 00:04 UTC is outside the 02:00-05:00 window: the run waits for it.
	s.parked(t, id, "window", 2*time.Hour, 1)
	if s.a.deploys.Load() != 0 {
		t.Fatal("deployed outside the window")
	}
	s.advance(2*time.Hour - 3*time.Minute)

	// The service reports the old version for two probes, then the new one.
	s.parked(t, id, "health", 2*time.Hour+30*time.Second, 1)
	s.advance(30 * time.Second)
	s.parked(t, id, "health", 2*time.Hour+time.Minute, 1)
	s.healthVersion.Store("bbbbbbb")
	s.advance(30 * time.Second)

	v := s.h.Wait(s.e, id)
	if v.Status != engine.RunSucceeded {
		t.Fatalf("status = %s (%s)", v.Status, v.Error)
	}
	if s.a.deploys.Load() != 1 || s.a.deployedVersion.Load() != "bbbbbbb" || s.a.rollbacks.Load() != 0 {
		t.Fatalf("deploys=%d version=%v rollbacks=%d", s.a.deploys.Load(), s.a.deployedVersion.Load(), s.a.rollbacks.Load())
	}
	for _, n := range []string{"announce", "ci", "window", "deploy", "health", "done"} {
		if nodeStatus(v, n) != engine.NodeCompleted {
			t.Errorf("node %s: %s", n, nodeStatus(v, n))
		}
	}
	for _, n := range []string{"rollback", "rolledback", "cifail"} {
		if nodeStatus(v, n) == engine.NodeCompleted {
			t.Errorf("node %s ran", n)
		}
	}
	// One Discord message, created at the start and edited when done.
	got := s.discord.contents()
	if len(got) != 2 || got[0] != "POST started" || got[1] != "PATCH deployed" {
		t.Fatalf("discord = %v", got)
	}
	if reqs := s.discord.all(); reqs[1].Path != "/api/webhooks/9/TOKEN/messages/msg1" {
		t.Fatalf("edit went to %s", reqs[1].Path)
	}
	if ev := v.Event.(map[string]any); ev["commit"] != "bbbbbbb" || ev["previous"] != "aaaaaaa" {
		t.Fatalf("trigger event = %v", ev)
	}
}

func TestScenarioAutoDeployRollsBackWhenUnhealthy(t *testing.T) {
	s := newScenario(t)
	s.ciRuns.Store(ciRuns("success"))
	id, _ := s.startDeploy(t)

	// CI is green and the run reaches the window wait.
	s.parked(t, id, "window", 2*time.Hour, 1)
	s.advance(2*time.Hour - time.Minute)
	// Health never reports the new version: poll until the 5 minute timeout.
	base := 2 * time.Hour
	for step := 1; step <= 10; step++ {
		s.parked(t, id, "health", base+time.Duration(step)*30*time.Second, 1)
		s.advance(30 * time.Second)
	}
	v := s.h.Wait(s.e, id)
	if v.Status != engine.RunSucceeded {
		t.Fatalf("status = %s (%s)", v.Status, v.Error)
	}
	if s.a.deploys.Load() != 1 || s.a.rollbacks.Load() != 1 {
		t.Fatalf("deploys=%d rollbacks=%d", s.a.deploys.Load(), s.a.rollbacks.Load())
	}
	if nodeStatus(v, "done") == engine.NodeCompleted || nodeStatus(v, "rolledback") != engine.NodeCompleted {
		t.Fatal("wrong branch after the health timeout")
	}
	got := s.discord.contents()
	if len(got) != 2 || got[1] != "PATCH rolled back" {
		t.Fatalf("discord = %v", got)
	}
}

func TestScenarioAutoDeployStopsOnFailedCI(t *testing.T) {
	s := newScenario(t)
	s.ciRuns.Store(ciRuns("success", "failure"))
	id, _ := s.startDeploy(t)
	v := s.h.Wait(s.e, id)
	if v.Status != engine.RunSucceeded {
		t.Fatalf("status = %s (%s)", v.Status, v.Error)
	}
	if s.a.deploys.Load() != 0 || nodeStatus(v, "cifail") != engine.NodeCompleted {
		t.Fatalf("deploys=%d cifail=%s", s.a.deploys.Load(), nodeStatus(v, "cifail"))
	}
	if got := s.discord.contents(); len(got) != 2 || got[1] != "PATCH ci failed" {
		t.Fatalf("discord = %v", got)
	}
}

// Scenario 3: scheduled backup. cron -> backup -> wait for a heartbeat that
// arrives through policy state -> notify.
func (s *scenario) backupPolicy(hook string) string {
	return policy("nightly-backup", `    - { id: tick, type: trigger.cron, props: { schedule: "0 3 * * *", timezone: Asia/Taipei }, next: { out: [backup] } }
    - { id: backup, type: fake.backup, next: { out: [heartbeat] } }
    - id: heartbeat
      type: wait.until
      props:
        interval: 10m
        timeout: 30m
        condition: "inputs.beat != nil && inputs.beat >= inputs.since"
        inputs: { beat: "{{ state.backup_heartbeat }}", since: "{{ nodes.backup.startedMs }}" }
      next: { out: [ok], timeout: [missed] }
    - { id: ok, type: notify, props: { target: webhook, url: "`+hook+`", title: backup, message: "completed {{ nodes.heartbeat.polls }}" } }
    - { id: missed, type: notify, props: { target: webhook, url: "`+hook+`", title: backup, message: "no heartbeat" } }
`)
}

func TestScenarioScheduledBackupWithHeartbeat(t *testing.T) {
	for _, tc := range []struct {
		name      string
		heartbeat bool
		wantMsg   string
		wantNode  string
	}{
		{"heartbeat arrives", true, "completed 3", "ok"},
		{"heartbeat missing", false, "no heartbeat", "missed"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			s := newScenario(t)
			hook := newRecorder(t, func(w http.ResponseWriter, r *http.Request) { w.WriteHeader(http.StatusNoContent) })
			s.h.Deploy(s.e, s.backupPolicy(hook.URL+"/notify"))
			s.sync(t)
			s.timers(1)

			// 03:00 in Taipei is 19:00 UTC of the first day.
			s.advance(19 * time.Hour)
			ev := s.h.Events.WaitFor(t, "backup run", func(e engine.Event) bool {
				return e.Kind == engine.EventRunQueued && e.Policy == "nightly-backup"
			})
			id := ev.RunID
			fired := 19 * time.Hour

			s.parked(t, id, "heartbeat", fired+10*time.Minute, 1)
			if tc.heartbeat {
				s.advance(10 * time.Minute)
				s.parked(t, id, "heartbeat", fired+20*time.Minute, 1)
				// The job finishes and reports in (what P4's heartbeat check does).
				if _, err := s.state.Set(context.Background(), "nightly-backup", "backup_heartbeat", s.h.Clock.Now().UnixMilli()); err != nil {
					t.Fatal(err)
				}
				s.advance(10 * time.Minute)
			} else {
				s.advance(10 * time.Minute)
				s.parked(t, id, "heartbeat", fired+20*time.Minute, 1)
				s.advance(10 * time.Minute)
				s.parked(t, id, "heartbeat", fired+30*time.Minute, 1)
				s.advance(10 * time.Minute)
			}
			v := s.h.Wait(s.e, id)
			if v.Status != engine.RunSucceeded {
				t.Fatalf("status = %s (%s)", v.Status, v.Error)
			}
			if s.a.backups.Load() != 1 || nodeStatus(v, tc.wantNode) != engine.NodeCompleted {
				t.Fatalf("backups=%d %s=%s", s.a.backups.Load(), tc.wantNode, nodeStatus(v, tc.wantNode))
			}
			reqs := hook.all()
			if len(reqs) != 1 || !strings.Contains(reqs[0].Body, `"message":"`+tc.wantMsg+`"`) {
				t.Fatalf("webhook requests = %+v", reqs)
			}
			if ev := v.Event.(map[string]any); ev["scheduled"] != "2026-01-02T03:00:00+08:00" || ev["catchUp"] != false {
				t.Fatalf("event = %v", ev)
			}
		})
	}
}
