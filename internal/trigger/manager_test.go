package trigger_test

import (
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/ACaccel/lcm/internal/engine"
	"github.com/ACaccel/lcm/internal/engine/enginetest"
	"github.com/ACaccel/lcm/internal/secret"
	"github.com/ACaccel/lcm/internal/signal"
	"github.com/ACaccel/lcm/internal/trigger"
)

// env is one engine with a trigger manager on the harness store.
type env struct {
	h   *enginetest.Harness
	e   *engine.Engine
	m   *trigger.Manager
	bus *signal.Bus
	git *fakeGit
}

func policy(name, nodes string) string {
	return "apiVersion: lcm/v1\nkind: Policy\nmetadata: { name: " + name + " }\nspec:\n  concurrency: { mode: parallel, limit: 50 }\n  nodes:\n" + nodes
}

// echo is the node every test policy runs after its trigger.
const echo = "    - { id: work, type: test.echo, props: { value: \"{{ trigger }}\" } }\n"

func trig(typ, props string) string {
	return "    - { id: t, type: " + typ + ", props: " + props + ", next: { out: [work] } }\n" + echo
}

type fakeGit struct {
	mu    sync.Mutex
	heads map[string]string
	err   error
	calls int
}

func (f *fakeGit) set(repo, branch, sha string) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.heads[repo+"#"+branch] = sha
}

func (f *fakeGit) RemoteHead(_ context.Context, repo, branch string) (string, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.calls++
	if f.err != nil {
		return "", f.err
	}
	sha, ok := f.heads[repo+"#"+branch]
	if !ok {
		return "", trigger.ErrBranchNotFound
	}
	return sha, nil
}

// newEnv builds the harness, registers the trigger types and any extra
// node types through pre, and opens the engine.
func newEnv(t *testing.T, secrets secret.Source, calendarDir string, pre ...func(*enginetest.Harness, *engine.Config)) *env {
	t.Helper()
	h := enginetest.New(t)
	trigger.Register(h.Registry)
	e := h.Open(func(cfg *engine.Config) {
		for _, f := range pre {
			f(h, cfg)
		}
	})
	x := &env{h: h, e: e, bus: signal.NewBus(nopLogger(), h.Clock.Now), git: &fakeGit{heads: map[string]string{}}}
	x.m = x.newManager(t, secrets, calendarDir)
	return x
}

// newManager starts a manager on the shared engine and store; tests call it
// again to simulate a restart.
func (x *env) newManager(t *testing.T, secrets secret.Source, calendarDir string) *trigger.Manager {
	t.Helper()
	m, err := trigger.NewManager(trigger.Config{
		Engine: x.e, DB: x.h.DB, Bus: x.bus, Clock: x.h.Clock, Git: x.git, Secrets: secrets,
		CalendarDir: calendarDir, Logger: nopLogger(),
	})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(m.Close)
	return m
}

func (x *env) sync(t *testing.T) {
	t.Helper()
	if err := x.m.Sync(context.Background()); err != nil {
		t.Fatal(err)
	}
}

// runs waits for n runs of policy beyond those already counted in seen and
// returns their views once finished.
func (x *env) runs(t *testing.T, policy string, seen map[string]bool, n int) []engine.RunView {
	t.Helper()
	var out []engine.RunView
	for len(out) < n {
		ev := x.h.Events.WaitFor(t, "run of "+policy, func(e engine.Event) bool {
			return e.Kind == engine.EventRunQueued && e.Policy == policy && !seen[e.RunID]
		})
		seen[ev.RunID] = true
		out = append(out, x.h.Wait(x.e, ev.RunID))
	}
	return out
}

func (x *env) noRuns(t *testing.T, policy string, seen map[string]bool) {
	t.Helper()
	for _, ev := range x.h.Events.Events() {
		if ev.Kind == engine.EventRunQueued && ev.Policy == policy && !seen[ev.RunID] {
			t.Fatalf("unexpected run %s of %s", ev.RunID, policy)
		}
	}
}

func (x *env) timers(n int) { x.h.T.Helper(); x.h.WaitTimers(n) }

func event(t *testing.T, v engine.RunView) map[string]any {
	t.Helper()
	m, ok := v.Event.(map[string]any)
	if !ok {
		t.Fatalf("event = %#v", v.Event)
	}
	return m
}

func TestSignalTriggerFiltersAndStartsRuns(t *testing.T) {
	x := newEnv(t, nil, "")
	x.h.Deploy(x.e, policy("restart", trig("trigger.signal",
		`{ kinds: [health.changed], services: [api], severities: [critical], statuses: [down], labels: { env: prod } }`)))
	x.sync(t)
	seen := map[string]bool{}

	pub := func(s signal.Signal) { x.bus.Publish(s) }
	base := signal.Signal{Kind: signal.KindHealth, Service: "api", Severity: "critical",
		Labels: map[string]string{"env": "prod", "team": "x"}, Data: map[string]any{"health": "down", "previous": "healthy"}}
	// Each variation breaks exactly one filter.
	for _, mod := range []func(*signal.Signal){
		func(s *signal.Signal) { s.Kind = signal.KindCheck },
		func(s *signal.Signal) { s.Service = "web" },
		func(s *signal.Signal) { s.Severity = "warning" },
		func(s *signal.Signal) { s.Labels = map[string]string{"env": "dev"} },
		func(s *signal.Signal) { s.Data = map[string]any{"health": "healthy"} },
	} {
		s := base
		mod(&s)
		pub(s)
	}
	pub(base)

	got := x.runs(t, "restart", seen, 1)
	ev := event(t, got[0])
	if ev["service"] != "api" || ev["kind"] != "health.changed" || ev["data"].(map[string]any)["health"] != "down" {
		t.Fatalf("event = %v", ev)
	}
	x.noRuns(t, "restart", seen)
	if got[0].Status != engine.RunSucceeded {
		t.Fatalf("status = %s", got[0].Status)
	}
}

func TestSignalTriggerStopsOnUndeploy(t *testing.T) {
	x := newEnv(t, nil, "")
	x.h.Deploy(x.e, policy("p", trig("trigger.signal", `{ kinds: [alert] }`)))
	x.sync(t)
	if x.m.Watching() != 1 {
		t.Fatalf("watching = %d", x.m.Watching())
	}
	if err := x.e.Undeploy(x.h.Ctx, "p"); err != nil {
		t.Fatal(err)
	}
	x.sync(t)
	if x.m.Watching() != 0 {
		t.Fatalf("watching after undeploy = %d", x.m.Watching())
	}
	x.bus.Publish(signal.Signal{Kind: signal.KindAlert})
	x.noRuns(t, "p", map[string]bool{})
}

func TestNewVersionReplacesWatcher(t *testing.T) {
	x := newEnv(t, nil, "")
	x.h.Deploy(x.e, policy("p", trig("trigger.signal", `{ services: [a] }`)))
	x.sync(t)
	x.h.Deploy(x.e, policy("p", trig("trigger.signal", `{ services: [b] }`)))
	x.sync(t)
	if x.m.Watching() != 1 {
		t.Fatalf("watching = %d", x.m.Watching())
	}
	seen := map[string]bool{}
	x.bus.Publish(signal.Signal{Kind: signal.KindAlert, Service: "a"})
	x.bus.Publish(signal.Signal{Kind: signal.KindAlert, Service: "b"})
	got := x.runs(t, "p", seen, 1)
	if event(t, got[0])["service"] != "b" {
		t.Fatalf("old version still triggers: %v", got[0].Event)
	}
	x.noRuns(t, "p", seen)
}

func TestObserveKicksRunLoop(t *testing.T) {
	x := newEnv(t, nil, "")
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() { x.m.Run(ctx); close(done) }()
	x.timers(1) // initial sync done, periodic timer armed

	x.h.Deploy(x.e, policy("p", trig("trigger.signal", `{ kinds: [alert] }`)))
	x.m.Observe(engine.Event{Kind: engine.EventPolicyDeployed, Policy: "p"})
	deadline := time.After(enginetest.Timeout)
	for x.m.Watching() != 1 {
		select {
		case <-deadline:
			t.Fatal("deploy event did not start the watcher")
		case <-time.After(time.Millisecond):
		}
	}
	cancel()
	<-done
	// Run closed the manager, which waited for every watcher to stop.
	x.bus.Publish(signal.Signal{Kind: signal.KindAlert})
	x.noRuns(t, "p", map[string]bool{})
}

func TestGitFirstSightRecordsWithoutFiring(t *testing.T) {
	x := newEnv(t, nil, "")
	x.git.set("https://example.com/o/r.git", "main", "aaa")
	x.h.Deploy(x.e, policy("deploy", trig("trigger.git", `{ repo: "https://example.com/o/r.git", branch: main, interval: 1m }`)))
	x.sync(t)
	x.timers(1) // first poll done
	seen := map[string]bool{}
	x.noRuns(t, "deploy", seen)

	// Unchanged head: still nothing.
	x.h.Clock.Advance(time.Minute)
	x.timers(1)
	x.noRuns(t, "deploy", seen)

	x.git.set("https://example.com/o/r.git", "main", "bbb")
	x.h.Clock.Advance(time.Minute)
	got := x.runs(t, "deploy", seen, 1)
	ev := event(t, got[0])
	if ev["commit"] != "bbb" || ev["previous"] != "aaa" || ev["branch"] != "main" || ev["repo"] != "https://example.com/o/r.git" {
		t.Fatalf("event = %v", ev)
	}
	x.timers(1)

	// Same head again does not start another run.
	x.h.Clock.Advance(time.Minute)
	x.timers(1)
	x.noRuns(t, "deploy", seen)
}

func TestGitRestartDoesNotRefire(t *testing.T) {
	x := newEnv(t, nil, "")
	repo := "/srv/repo"
	x.git.set(repo, "main", "aaa")
	x.h.Deploy(x.e, policy("deploy", trig("trigger.git", `{ repo: "/srv/repo" }`)))
	x.sync(t)
	x.timers(1)
	x.git.set(repo, "main", "bbb")
	x.h.Clock.Advance(time.Minute)
	seen := map[string]bool{}
	x.runs(t, "deploy", seen, 1)
	x.timers(1)

	x.m.Close()
	x.m = x.newManager(t, nil, "")
	x.sync(t)
	x.timers(1)
	x.noRuns(t, "deploy", seen)

	// A commit made while LCM was down is picked up after the restart.
	x.m.Close()
	x.git.set(repo, "main", "ccc")
	x.m = x.newManager(t, nil, "")
	x.sync(t)
	got := x.runs(t, "deploy", seen, 1)
	if ev := event(t, got[0]); ev["commit"] != "ccc" || ev["previous"] != "bbb" {
		t.Fatalf("event = %v", ev)
	}
}

func TestGitFireOnFirst(t *testing.T) {
	x := newEnv(t, nil, "")
	x.git.set("/r", "dev", "aaa")
	x.h.Deploy(x.e, policy("p", trig("trigger.git", `{ repo: /r, branch: dev, fireOnFirst: true }`)))
	x.sync(t)
	got := x.runs(t, "p", map[string]bool{}, 1)
	if ev := event(t, got[0]); ev["commit"] != "aaa" || ev["previous"] != "" {
		t.Fatalf("event = %v", ev)
	}
}

func TestGitErrorsAreRetriedNotFatal(t *testing.T) {
	x := newEnv(t, nil, "")
	x.git.set("/r", "main", "aaa")
	x.git.err = errors.New("network down")
	x.h.Deploy(x.e, policy("p", trig("trigger.git", `{ repo: /r }`)))
	x.sync(t)
	x.timers(1)
	x.git.mu.Lock()
	x.git.err = nil
	x.git.mu.Unlock()
	x.h.Clock.Advance(time.Minute)
	x.timers(1)
	x.git.set("/r", "main", "bbb")
	x.h.Clock.Advance(time.Minute)
	got := x.runs(t, "p", map[string]bool{}, 1)
	if ev := event(t, got[0]); ev["commit"] != "bbb" || ev["previous"] != "aaa" {
		t.Fatalf("event = %v", ev)
	}
}

func TestGitNotifyPush(t *testing.T) {
	x := newEnv(t, nil, "")
	x.git.set("https://github.com/o/r.git", "main", "aaa")
	x.h.Deploy(x.e, policy("p", trig("trigger.git", `{ repo: "https://github.com/o/r.git", interval: 1h }`)))
	x.sync(t)
	x.timers(1)
	seen := map[string]bool{}

	n, err := x.m.NotifyPush(context.Background(), "git@github.com:O/R.git", "main", "bbb")
	if err != nil || n != 1 {
		t.Fatalf("NotifyPush = %d, %v", n, err)
	}
	got := x.runs(t, "p", seen, 1)
	if ev := event(t, got[0]); ev["commit"] != "bbb" || ev["previous"] != "aaa" {
		t.Fatalf("event = %v", ev)
	}
	// A repeated notification and the later poll of the same commit are no-ops.
	_, _ = x.m.NotifyPush(context.Background(), "https://github.com/o/r", "main", "bbb")
	x.git.set("https://github.com/o/r.git", "main", "bbb")
	x.h.Clock.Advance(time.Hour)
	x.timers(1)
	x.noRuns(t, "p", seen)

	if n, _ := x.m.NotifyPush(context.Background(), "https://github.com/o/other", "main", "ccc"); n != 0 {
		t.Fatalf("matched an unrelated repo: %d", n)
	}
	if n, _ := x.m.NotifyPush(context.Background(), "https://github.com/o/r", "dev", "ccc"); n != 0 {
		t.Fatalf("matched an unrelated branch: %d", n)
	}
}

func TestCronFiresOnScheduleInTimezone(t *testing.T) {
	x := newEnv(t, nil, "")
	// 09:00 in Taipei (UTC+8) is 01:00 UTC; the harness clock starts at 2026-01-01 00:00 UTC.
	x.h.Deploy(x.e, policy("backup", trig("trigger.cron", `{ schedule: "0 9 * * *", timezone: Asia/Taipei }`)))
	x.sync(t)
	x.timers(1)
	seen := map[string]bool{}

	x.h.Clock.Advance(59 * time.Minute)
	x.timers(1)
	x.noRuns(t, "backup", seen)

	x.h.Clock.Advance(time.Minute)
	got := x.runs(t, "backup", seen, 1)
	ev := event(t, got[0])
	if ev["scheduled"] != "2026-01-01T09:00:00+08:00" || ev["fired"] != "2026-01-01T01:00:00Z" || ev["catchUp"] != false {
		t.Fatalf("event = %v", ev)
	}
	x.timers(1)
	x.h.Clock.Advance(24 * time.Hour)
	got = x.runs(t, "backup", seen, 1)
	if event(t, got[0])["scheduled"] != "2026-01-02T09:00:00+08:00" {
		t.Fatalf("second event = %v", got[0].Event)
	}
}

func writeCalendar(t *testing.T, dir, name, content string) {
	t.Helper()
	if err := os.WriteFile(filepath.Join(dir, name), []byte(content), 0o600); err != nil {
		t.Fatal(err)
	}
}

func TestCronCalendarSkipAndOnly(t *testing.T) {
	dir := t.TempDir()
	writeCalendar(t, dir, "holidays.txt", "# 2026 holidays\n2026-01-02 Bridge day\n")
	x := newEnv(t, nil, dir)
	x.h.Deploy(x.e, policy("skipper", trig("trigger.cron", `{ schedule: "0 12 * * *", calendar: holidays.txt, calendarMode: skip }`)))
	x.sync(t)
	x.timers(1)
	seen := map[string]bool{}

	x.h.Clock.Advance(12 * time.Hour) // Jan 1 12:00
	got := x.runs(t, "skipper", seen, 1)
	if event(t, got[0])["scheduled"] != "2026-01-01T12:00:00Z" {
		t.Fatalf("event = %v", got[0].Event)
	}
	x.timers(1)
	x.h.Clock.Advance(24 * time.Hour) // Jan 2 is listed: next fire is Jan 3
	x.h.Clock.Advance(0)
	x.timers(1)
	x.noRuns(t, "skipper", seen)
	x.h.Clock.Advance(24 * time.Hour)
	got = x.runs(t, "skipper", seen, 1)
	if event(t, got[0])["scheduled"] != "2026-01-03T12:00:00Z" {
		t.Fatalf("event = %v", got[0].Event)
	}
}

func TestCronCalendarOnly(t *testing.T) {
	dir := t.TempDir()
	writeCalendar(t, dir, "trading.txt", "2026-01-05\n2026-01-07\n")
	x := newEnv(t, nil, dir)
	x.h.Deploy(x.e, policy("open", trig("trigger.cron", `{ schedule: "30 8 * * *", calendar: trading.txt, calendarMode: only }`)))
	x.sync(t)
	x.timers(1)
	seen := map[string]bool{}
	for _, want := range []string{"2026-01-05T08:30:00Z", "2026-01-07T08:30:00Z"} {
		next, _ := time.Parse(time.RFC3339, want)
		x.h.Clock.Advance(next.Sub(x.h.Clock.Now()))
		got := x.runs(t, "open", seen, 1)
		if event(t, got[0])["scheduled"] != want {
			t.Fatalf("event = %v, want %s", got[0].Event, want)
		}
		x.timers(1)
	}
}

func TestCronCalendarErrors(t *testing.T) {
	dir := t.TempDir()
	x := newEnv(t, nil, dir)
	// A missing calendar keeps the trigger from starting.
	x.h.Deploy(x.e, policy("p", trig("trigger.cron", `{ schedule: "* * * * *", calendar: missing.txt }`)))
	x.sync(t)
	if x.m.Watching() != 0 {
		t.Fatal("trigger started without its calendar")
	}
	// Once the file exists, the next sync starts it.
	writeCalendar(t, dir, "missing.txt", "2026-01-01\n")
	x.sync(t)
	if x.m.Watching() != 1 {
		t.Fatal("trigger did not start after the calendar appeared")
	}
	// Escapes from the calendar directory are rejected.
	x.h.Deploy(x.e, policy("q", trig("trigger.cron", `{ schedule: "* * * * *", calendar: "../x" }`)))
	x.sync(t)
	if x.m.Watching() != 1 {
		t.Fatal("calendar path outside the directory was accepted")
	}
	// A malformed file does not leak its content.
	writeCalendar(t, dir, "bad.txt", "not-a-date secret-ish\n")
	x.h.Deploy(x.e, policy("r", trig("trigger.cron", `{ schedule: "* * * * *", calendar: bad.txt }`)))
	x.sync(t)
	if x.m.Watching() != 1 {
		t.Fatal("malformed calendar accepted")
	}
}

// cronRestart simulates downtime: a manager runs, is closed, the clock moves
// on and a new manager starts with the persisted state.
func cronRestart(t *testing.T, catchUp string) (*env, []engine.RunView) {
	t.Helper()
	x := newEnv(t, nil, "")
	x.h.Deploy(x.e, policy("job", trig("trigger.cron", `{ schedule: "0 * * * *", catchUp: `+catchUp+` }`)))
	x.sync(t)
	x.timers(1) // lastFire = 00:00 persisted
	x.m.Close()

	// Down from 00:00 to 03:30: the 01:00, 02:00 and 03:00 fires are missed.
	x.h.Clock.Advance(3*time.Hour + 30*time.Minute)
	x.m = x.newManager(t, nil, "")
	x.sync(t)
	x.timers(1)
	var views []engine.RunView
	for _, ev := range x.h.Events.Events() {
		if ev.Kind == engine.EventRunQueued && ev.Policy == "job" {
			views = append(views, x.h.Wait(x.e, ev.RunID))
		}
	}
	return x, views
}

func TestCronCatchUpSkip(t *testing.T) {
	_, runs := cronRestart(t, "skip")
	if len(runs) != 0 {
		t.Fatalf("catchUp skip started %d runs", len(runs))
	}
}

func TestCronCatchUpOnce(t *testing.T) {
	_, runs := cronRestart(t, "once")
	if len(runs) != 1 {
		t.Fatalf("catchUp once started %d runs", len(runs))
	}
	ev := event(t, runs[0])
	if ev["scheduled"] != "2026-01-01T03:00:00Z" || ev["catchUp"] != true {
		t.Fatalf("event = %v", ev)
	}
}

func TestCronCatchUpAll(t *testing.T) {
	_, runs := cronRestart(t, "all")
	if len(runs) != 3 {
		t.Fatalf("catchUp all started %d runs", len(runs))
	}
	var scheduled []string
	for _, r := range runs {
		scheduled = append(scheduled, event(t, r)["scheduled"].(string))
		if event(t, r)["catchUp"] != true {
			t.Fatalf("event = %v", r.Event)
		}
	}
	want := "2026-01-01T01:00:00Z,2026-01-01T02:00:00Z,2026-01-01T03:00:00Z"
	if strings.Join(scheduled, ",") != want {
		t.Fatalf("scheduled = %v", scheduled)
	}
}

func TestCronCatchUpAllIsCapped(t *testing.T) {
	x := newEnv(t, nil, "")
	x.h.Deploy(x.e, policy("job", trig("trigger.cron", `{ schedule: "* * * * *", catchUp: all }`)))
	x.sync(t)
	x.timers(1)
	x.m.Close()
	x.h.Clock.Advance(3 * time.Hour) // 180 missed minutes
	x.m = x.newManager(t, nil, "")
	x.sync(t)
	x.timers(1)
	n := 0
	for _, ev := range x.h.Events.Events() {
		if ev.Kind == engine.EventRunQueued && ev.Policy == "job" {
			n++
		}
	}
	if n != trigger.MaxCatchUp {
		t.Fatalf("started %d runs, want the cap %d", n, trigger.MaxCatchUp)
	}
}

func TestCronFirstSightDoesNotCatchUp(t *testing.T) {
	x := newEnv(t, nil, "")
	x.h.Clock.Advance(10 * time.Hour)
	x.h.Deploy(x.e, policy("job", trig("trigger.cron", `{ schedule: "0 * * * *", catchUp: all }`)))
	x.sync(t)
	x.timers(1)
	x.noRuns(t, "job", map[string]bool{})
}

func TestCronStateForgottenOnUndeploy(t *testing.T) {
	x := newEnv(t, nil, "")
	x.h.Deploy(x.e, policy("job", trig("trigger.cron", `{ schedule: "0 * * * *", catchUp: all }`)))
	x.sync(t)
	x.timers(1)
	if err := x.e.Undeploy(x.h.Ctx, "job"); err != nil {
		t.Fatal(err)
	}
	x.sync(t)
	x.h.Clock.Advance(5 * time.Hour)
	x.h.Deploy(x.e, policy("job", trig("trigger.cron", `{ schedule: "0 * * * *", catchUp: all }`)))
	x.sync(t)
	x.timers(1)
	x.noRuns(t, "job", map[string]bool{})
}

func sign(secretValue string, body []byte) string {
	mac := hmac.New(sha256.New, []byte(secretValue))
	mac.Write(body)
	return hex.EncodeToString(mac.Sum(nil))
}

type fixedSecrets map[string]string

func (f fixedSecrets) Get(name string) (secret.Value, error) {
	if v, ok := f[name]; ok {
		return secret.NewValue(v), nil
	}
	return secret.Value{}, secret.ErrNotFound
}

func TestWebhookHMAC(t *testing.T) {
	x := newEnv(t, fixedSecrets{"hook-key": "s3cret"}, "")
	x.h.Deploy(x.e, policy("signed", trig("trigger.webhook", `{ name: github, hmacSecret: hook-key }`)))
	x.h.Deploy(x.e, policy("open", trig("trigger.webhook", `{ name: other }`)))
	x.sync(t)
	ctx := context.Background()
	body := []byte(`{"ref":"refs/heads/main"}`)

	hdr := func(sig string) http.Header {
		h := http.Header{}
		h.Set("X-Hub-Signature-256", sig)
		h.Set("Authorization", "Bearer should-not-be-stored")
		h.Set("X-Github-Event", "push")
		return h
	}

	if _, err := x.m.Deliver(ctx, "github", http.Header{}, body); !errors.Is(err, trigger.ErrBadSignature) {
		t.Fatalf("missing signature: %v", err)
	}
	if _, err := x.m.Deliver(ctx, "github", hdr("sha256="+sign("wrong", body)), body); !errors.Is(err, trigger.ErrBadSignature) {
		t.Fatalf("bad signature: %v", err)
	}
	if _, err := x.m.Deliver(ctx, "github", hdr("sha256=zz"), body); !errors.Is(err, trigger.ErrBadSignature) {
		t.Fatalf("malformed signature: %v", err)
	}
	if _, err := x.m.Deliver(ctx, "github", hdr("sha256="+sign("s3cret", []byte("other body"))), body); !errors.Is(err, trigger.ErrBadSignature) {
		t.Fatalf("signature of another body: %v", err)
	}
	x.noRuns(t, "signed", map[string]bool{})

	ids, err := x.m.Deliver(ctx, "github", hdr("sha256="+sign("s3cret", body)), body)
	if err != nil || len(ids) != 1 {
		t.Fatalf("valid delivery: %v %v", ids, err)
	}
	v := x.h.Wait(x.e, ids[0])
	ev := event(t, v)
	headers := ev["headers"].(map[string]any)
	if ev["name"] != "github" || ev["body"].(map[string]any)["ref"] != "refs/heads/main" || headers["x-github-event"] != "push" {
		t.Fatalf("event = %v", ev)
	}
	if _, leaked := headers["authorization"]; leaked {
		t.Fatal("authorization header stored in the event")
	}
	// The bare hex form works too.
	if ids, err := x.m.Deliver(ctx, "github", hdr(sign("s3cret", body)), body); err != nil || len(ids) != 1 {
		t.Fatalf("bare hex signature: %v %v", ids, err)
	}

	// Unsigned listeners accept anything; non-JSON bodies arrive as strings.
	ids, err = x.m.Deliver(ctx, "other", nil, []byte("plain text"))
	if err != nil || len(ids) != 1 {
		t.Fatalf("open delivery: %v %v", ids, err)
	}
	if ev := event(t, x.h.Wait(x.e, ids[0])); ev["body"] != "plain text" {
		t.Fatalf("event = %v", ev)
	}

	if _, err := x.m.Deliver(ctx, "nobody", nil, nil); !errors.Is(err, trigger.ErrNoListener) {
		t.Fatalf("unknown hook: %v", err)
	}
	if _, err := x.m.Deliver(ctx, "other", nil, make([]byte, trigger.MaxWebhookBody+1)); !errors.Is(err, trigger.ErrBodyTooLarge) {
		t.Fatalf("large body: %v", err)
	}
}

func TestWebhookMissingSecretFailsClosed(t *testing.T) {
	x := newEnv(t, fixedSecrets{}, "")
	x.h.Deploy(x.e, policy("signed", trig("trigger.webhook", `{ name: hook, hmacSecret: nope }`)))
	x.sync(t)
	ids, err := x.m.Deliver(context.Background(), "hook", http.Header{}, []byte("x"))
	if err == nil || len(ids) != 0 || errors.Is(err, trigger.ErrBadSignature) {
		t.Fatalf("unresolvable secret: ids=%v err=%v", ids, err)
	}
	x.noRuns(t, "signed", map[string]bool{})
}

func TestTriggerPropsValidation(t *testing.T) {
	x := newEnv(t, nil, "")
	bad := map[string]string{
		`trigger.signal|{}`:                                           "at least one filter",
		`trigger.signal|{ kinds: [nope] }`:                            "not one of",
		`trigger.signal|{ severities: [fatal] }`:                      "not one of",
		`trigger.git|{ repo: "-oops" }`:                               "repo",
		`trigger.git|{ repo: r, branch: "a..b" }`:                     "branch",
		`trigger.git|{ repo: r, interval: 1s }`:                       "minimum",
		`trigger.cron|{ schedule: "61 * * * *" }`:                     "outside",
		`trigger.cron|{ schedule: "* * * * *", timezone: Mars/Base }`: "unknown zone",
		`trigger.cron|{ schedule: "* * * * *", catchUp: sometimes }`:  "one of",
		`trigger.webhook|{ name: "bad name" }`:                        "name",
	}
	for k, want := range bad {
		typ, props, _ := strings.Cut(k, "|")
		_, err := x.e.Validate(context.Background(), []byte(policy("v", trig(typ, props))))
		if err == nil || !strings.Contains(err.Error(), want) {
			t.Errorf("%s %s: err = %v, want %q", typ, props, err, want)
		}
	}
}
