package monitor

import (
	"context"
	"errors"
	"testing"
	"time"

	"go.uber.org/goleak"

	"github.com/ACaccel/lcm/internal/model"
	"github.com/ACaccel/lcm/internal/signal"
)

func TestMain(m *testing.M) {
	goleak.VerifyTestMain(m)
}

func TestStatusChangesAfterThreshold(t *testing.T) {
	h := newHarness(t)
	h.sync(fakeDef("api", "web", threshold(3)))
	h.step(0)
	if got := h.check("api", "web"); got.Status != StatusUnknown {
		t.Fatalf("status after 1 success with threshold 3 = %s, want unknown", got.Status)
	}
	h.step(testInterval)
	h.step(testInterval)
	got := h.check("api", "web")
	if got.Status != StatusUp || h.health("api") != HealthHealthy {
		t.Fatalf("after 3 successes: %+v health %s", got, h.health("api"))
	}
	sigs := h.drain()
	if n := len(kinds(sigs, signal.KindCheck)); n != 1 {
		t.Fatalf("check signals = %d, want 1: %+v", n, sigs)
	}
	if n := len(kinds(sigs, signal.KindHealth)); n != 1 {
		t.Fatalf("health signals = %d, want 1", n)
	}

	h.fail("api/web")
	h.step(testInterval)
	h.step(testInterval)
	if got := h.check("api", "web"); got.Status != StatusUp || got.ConsecutiveFailures != 0 {
		t.Fatalf("2 failures with threshold 3 changed status: %+v", got)
	}
	if sigs := h.drain(); len(sigs) != 0 {
		t.Fatalf("signals before threshold: %+v", sigs)
	}
	// A success in between resets the streak.
	h.pass("api/web")
	h.step(testInterval)
	h.fail("api/web")
	h.step(testInterval)
	h.step(testInterval)
	if h.check("api", "web").Status != StatusUp {
		t.Fatal("failure streak was not reset by a success")
	}
	h.step(testInterval)
	got = h.check("api", "web")
	if got.Status != StatusDown || got.Message != "boom" || got.ConsecutiveFailures != 3 {
		t.Fatalf("after 3 failures: %+v", got)
	}
	sigs = h.drain()
	cs, hs := kinds(sigs, signal.KindCheck), kinds(sigs, signal.KindHealth)
	if len(cs) != 1 || len(hs) != 1 {
		t.Fatalf("signals = %+v", sigs)
	}
	c := cs[0]
	if c.Service != "api" || c.Severity != signal.SeverityCritical || c.Data["status"] != "down" ||
		c.Data["previous"] != "up" || c.Data["message"] != "boom" || c.Data["check"] != "web" {
		t.Errorf("check signal = %+v", c)
	}
	if hs[0].Severity != signal.SeverityCritical || hs[0].Data["health"] != "down" || hs[0].Data["previous"] != "healthy" {
		t.Errorf("health signal = %+v", hs[0])
	}
	if f, _ := hs[0].Data["failing"].([]string); len(f) != 1 || f[0] != "web" {
		t.Errorf("failing = %v", hs[0].Data["failing"])
	}
	// Staying down emits nothing more.
	h.step(testInterval)
	h.step(testInterval)
	if sigs := h.drain(); len(sigs) != 0 {
		t.Fatalf("repeated signals while down: %+v", sigs)
	}
}

func TestServiceHealthAggregation(t *testing.T) {
	h := newHarness(t)
	h.sync(fakeDef("api", "web"), fakeDef("api", "cache", nonCritical), fakeDef("idle", "x"))
	if h.health("api") != HealthUnknown {
		t.Fatalf("before results = %s", h.health("api"))
	}
	h.step(0)
	if h.health("api") != HealthHealthy {
		t.Fatalf("all up = %s", h.health("api"))
	}
	h.drain()

	h.fail("api/cache")
	h.step(testInterval)
	if h.health("api") != HealthDegraded {
		t.Fatalf("non-critical down = %s", h.health("api"))
	}
	hs := kinds(h.drain(), signal.KindHealth)
	if len(hs) != 1 || hs[0].Severity != signal.SeverityWarning || hs[0].Data["health"] != "degraded" {
		t.Fatalf("degraded signal = %+v", hs)
	}

	h.fail("api/web")
	h.step(testInterval)
	if h.health("api") != HealthDown {
		t.Fatalf("critical down = %s", h.health("api"))
	}
	h.drain()

	h.pass("api/web")
	h.step(testInterval)
	if h.health("api") != HealthDegraded {
		t.Fatalf("critical recovered = %s", h.health("api"))
	}
	h.pass("api/cache")
	h.step(testInterval)
	if h.health("api") != HealthHealthy {
		t.Fatalf("recovered = %s", h.health("api"))
	}
	hs = kinds(h.drain(), signal.KindHealth)
	if len(hs) != 2 || hs[1].Severity != signal.SeverityInfo || hs[1].Data["previous"] != "degraded" {
		t.Fatalf("recovery signals = %+v", hs)
	}
	// A non-critical check going down emits a warning check signal.
	h.fail("api/cache")
	h.step(testInterval)
	cs := kinds(h.drain(), signal.KindCheck)
	if len(cs) != 1 || cs[0].Severity != signal.SeverityWarning {
		t.Fatalf("non-critical check signal = %+v", cs)
	}
}

func TestSignalsPublishedAfterStorage(t *testing.T) {
	h := newHarness(t)
	h.sync(fakeDef("api", "web"))
	h.fail("api/web")
	h.step(0)
	sigs := h.drain()
	if len(kinds(sigs, signal.KindCheck)) != 1 {
		t.Fatalf("signals = %+v", sigs)
	}
	var status string
	if err := h.db.Reader().QueryRow(`SELECT status FROM monitor_checks WHERE check_id = 'api/web'`).Scan(&status); err != nil || status != "down" {
		t.Fatalf("stored status = %q, %v", status, err)
	}
}

func TestSyncAddUpdateRemoveKeepsState(t *testing.T) {
	h := newHarness(t)
	h.sync(fakeDef("api", "web"), fakeDef("api", "db"))
	h.fail("api/db")
	h.step(0)
	h.drain()
	web, db := h.check("api", "web"), h.check("api", "db")
	if web.Status != StatusUp || db.Status != StatusDown {
		t.Fatalf("setup: %+v %+v", web, db)
	}
	calls := h.callCount("api/web")

	// Same definitions: nothing changes, nothing re-runs, no signals.
	h.sync(fakeDef("api", "web"), fakeDef("api", "db"))
	h.step(0)
	if h.callCount("api/web") != calls {
		t.Errorf("unchanged check was rescheduled: %d -> %d calls", calls, h.callCount("api/web"))
	}
	if got := h.check("api", "web"); got.Since != web.Since || got.Status != StatusUp {
		t.Errorf("unchanged check lost state: %+v", got)
	}
	h.step(testInterval - 0)
	if h.callCount("api/web") != calls+1 {
		t.Errorf("unchanged check kept its schedule? calls %d", h.callCount("api/web"))
	}
	h.drain()

	// Update: the check keeps its status and gets its new interval.
	h.sync(fakeDef("api", "web", func(c *model.Check) { c.Interval = model.Duration(20 * time.Second) }), fakeDef("api", "db"))
	if got := h.check("api", "web"); got.Status != StatusUp || got.Interval != 20*time.Second {
		t.Errorf("updated check = %+v", got)
	}
	if sigs := h.drain(); len(sigs) != 0 {
		t.Errorf("update emitted %+v", sigs)
	}

	// Remove the failing check: the service recovers.
	h.sync(fakeDef("api", "web", func(c *model.Check) { c.Interval = model.Duration(20 * time.Second) }))
	if h.health("api") != HealthHealthy {
		t.Errorf("health after removing the failing check = %s", h.health("api"))
	}
	if hs := kinds(h.drain(), signal.KindHealth); len(hs) != 1 || hs[0].Data["health"] != "healthy" {
		t.Errorf("health signal after removal = %+v", hs)
	}
	var n int
	_ = h.db.Reader().QueryRow(`SELECT COUNT(*) FROM monitor_checks WHERE check_id = 'api/db'`).Scan(&n)
	if n != 0 {
		t.Error("stored state of the removed check was kept")
	}

	// Remove the service entirely.
	h.sync()
	if _, ok := h.m.Service("api"); ok {
		t.Error("service without checks still listed")
	}
	_ = h.db.Reader().QueryRow(`SELECT COUNT(*) FROM monitor_services`).Scan(&n)
	if n != 0 {
		t.Error("stored service was kept")
	}
	if len(h.m.Services()) != 0 {
		t.Error("Services not empty")
	}
}

func TestSyncInvalidDefinitions(t *testing.T) {
	h := newHarness(t)
	h.sync(fakeDef("api", "web"))
	h.step(0)
	h.drain()

	bad := fakeDef("api", "web", func(c *model.Check) { c.Props = map[string]any{"nope": 1} })
	good := fakeDef("api", "db")
	err := h.m.Sync(h.ctx, []Def{bad, good, fakeDef("Bad Name", "x"), {Service: "api", Check: model.Check{Name: "t", Type: "teapot"}}, good})
	var is model.Issues
	if !errors.As(err, &is) {
		t.Fatalf("error = %v, want model.Issues", err)
	}
	paths := map[string]bool{}
	for _, i := range is {
		paths[i.Path] = true
	}
	for _, want := range []string{"api/web.props", "Bad Name/x", "api/t.type", "api/db"} {
		if !paths[want] {
			t.Errorf("no issue at %s: %v", want, is)
		}
	}
	// The valid definition was applied and the invalid update kept the
	// running check untouched.
	s, ok := h.m.Service("api")
	if !ok || len(s.Checks) != 2 {
		t.Fatalf("service = %+v", s)
	}
	if h.check("api", "web").Status != StatusUp {
		t.Error("check with an invalid update lost its state")
	}
}

func TestRestartDoesNotEmitChanges(t *testing.T) {
	h := newHarness(t)
	defs := []Def{fakeDef("api", "web"), fakeDef("api", "db")}
	h.fail("api/db")
	h.sync(defs...)
	h.step(0)
	h.step(testInterval)
	if h.health("api") != HealthDown {
		t.Fatal("setup")
	}
	h.drain()
	before := h.check("api", "db")

	h.restart()
	h.sync(defs...)
	if sigs := h.drain(); len(sigs) != 0 {
		t.Fatalf("restart emitted %+v", sigs)
	}
	after := h.check("api", "db")
	if after.Status != StatusDown || !after.Since.Equal(before.Since) || after.Stale {
		t.Fatalf("restored check = %+v, want %+v", after, before)
	}
	if h.health("api") != HealthDown {
		t.Fatalf("restored health = %s", h.health("api"))
	}
	// The restored streak counts toward later results without a new signal.
	h.step(0)
	h.step(testInterval)
	if sigs := h.drain(); len(sigs) != 0 {
		t.Fatalf("continued failure emitted %+v", sigs)
	}
}

func TestStaleStatusAfterRestart(t *testing.T) {
	h := newHarness(t)
	defs := []Def{fakeDef("api", "web")}
	h.sync(defs...)
	h.step(0)
	h.drain()
	if err := h.m.Close(); err != nil {
		t.Fatal(err)
	}

	// The monitor was down for longer than twice the interval.
	h.clk.Advance(3 * testInterval)
	h.m = h.open()
	h.sync(defs...)
	got := h.check("api", "web")
	if got.Status != StatusUnknown || !got.Stale || h.health("api") != HealthUnknown {
		t.Fatalf("stale check = %+v health %s", got, h.health("api"))
	}
	if sigs := h.drain(); len(sigs) != 0 {
		t.Fatalf("restoring emitted %+v", sigs)
	}
	// A fresh result that agrees with the stored status is not a change.
	h.step(0)
	got = h.check("api", "web")
	if got.Status != StatusUp || got.Stale || h.health("api") != HealthHealthy {
		t.Fatalf("after fresh result = %+v", got)
	}
	if sigs := h.drain(); len(sigs) != 0 {
		t.Fatalf("confirming a stored status emitted %+v", sigs)
	}
}

func TestStaleStatusThenFailure(t *testing.T) {
	h := newHarness(t)
	defs := []Def{fakeDef("api", "web")}
	h.sync(defs...)
	h.step(0)
	h.drain()
	if err := h.m.Close(); err != nil {
		t.Fatal(err)
	}
	h.clk.Advance(time.Hour)
	h.fail("api/web")
	h.m = h.open()
	h.sync(defs...)
	h.step(0)
	if cs := kinds(h.drain(), signal.KindCheck); len(cs) != 1 || cs[0].Data["previous"] != "up" || cs[0].Data["status"] != "down" {
		t.Fatalf("signals = %+v", cs)
	}
}

func TestHeartbeat(t *testing.T) {
	h := newHarness(t)
	h.sync(heartbeatDef("jobs", "backup", map[string]any{"grace": "5s"}), fakeDef("jobs", "plain"))
	if err := h.m.Heartbeat(h.ctx, "jobs", "nope", nil); !errors.Is(err, ErrUnknownCheck) {
		t.Errorf("unknown check error = %v", err)
	}
	if err := h.m.Heartbeat(h.ctx, "nojobs", "backup", nil); !errors.Is(err, ErrUnknownCheck) {
		t.Errorf("unknown service error = %v", err)
	}
	if err := h.m.Heartbeat(h.ctx, "jobs", "plain", nil); !errors.Is(err, ErrNotHeartbeat) {
		t.Errorf("non-heartbeat error = %v", err)
	}

	h.step(0)
	if got := h.check("jobs", "backup"); got.Status != StatusUnknown {
		t.Fatalf("before any beat or deadline: %+v", got)
	}
	if err := h.m.Heartbeat(h.ctx, "jobs", "backup", []byte("backup ok\nsecond line")); err != nil {
		t.Fatal(err)
	}
	got := h.check("jobs", "backup")
	if got.Status != StatusUp || got.Message != "backup ok" {
		t.Fatalf("after beat: %+v", got)
	}
	if cs := checkSignals(h.drain(), "backup"); len(cs) != 1 {
		t.Fatalf("signals after first beat = %+v", cs)
	}

	// Within interval + grace (15s) the check stays up.
	h.step(testInterval)
	if h.check("jobs", "backup").Status != StatusUp {
		t.Fatal("check failed within its deadline")
	}
	// The beat is now 20s old, past interval + grace.
	h.step(testInterval)
	got = h.check("jobs", "backup")
	if got.Status != StatusDown || got.Message != "no heartbeat for 20s" {
		t.Fatalf("after missed beat: %+v", got)
	}
	if h.health("jobs") != HealthDown {
		t.Fatalf("health = %s", h.health("jobs"))
	}
	h.drain()
	if err := h.m.Heartbeat(h.ctx, "jobs", "backup", nil); err != nil {
		t.Fatal(err)
	}
	if got := h.check("jobs", "backup"); got.Status != StatusUp || got.Message != "heartbeat received" {
		t.Fatalf("recovered: %+v", got)
	}
	cs := checkSignals(h.drain(), "backup")
	if len(cs) != 1 || cs[0].Data["status"] != "up" {
		t.Fatalf("recovery signals = %+v", cs)
	}
}

func TestHeartbeatNeverReceived(t *testing.T) {
	h := newHarness(t)
	h.sync(heartbeatDef("jobs", "backup", nil))
	// Default grace equals the interval: the deadline is 20s after Sync.
	h.step(0)
	h.step(testInterval)
	if h.check("jobs", "backup").Status != StatusUnknown {
		t.Fatal("failed before the deadline")
	}
	h.step(testInterval)
	h.step(testInterval)
	if got := h.check("jobs", "backup"); got.Status != StatusDown {
		t.Fatalf("status = %+v", got)
	}
}

func TestConcurrencyLimit(t *testing.T) {
	h := newHarness(t, func(c *Config) { c.Concurrency = 2 })
	var defs []Def
	for _, n := range []string{"a", "b", "c", "d"} {
		defs = append(defs, fakeDef("svc", n))
		h.gates["svc/"+n] = make(chan struct{})
	}
	h.sync(defs...)
	h.clk.Advance(0)
	for range 2 {
		<-h.enter
	}
	h.mu.Lock()
	running := h.cur
	h.mu.Unlock()
	if running != 2 {
		t.Fatalf("running probes = %d, want 2", running)
	}
	for _, d := range defs {
		close(h.gates["svc/"+d.Check.Name])
	}
	if err := h.m.Flush(h.ctx); err != nil {
		t.Fatal(err)
	}
	h.mu.Lock()
	peak := h.peak
	h.mu.Unlock()
	if peak > 2 {
		t.Fatalf("peak concurrency = %d", peak)
	}
	for _, d := range defs {
		if h.check("svc", d.Check.Name).Status != StatusUp {
			t.Errorf("%s did not run", d.Check.Name)
		}
	}
}

func TestProbeTimeout(t *testing.T) {
	h := newHarness(t)
	h.gates["api/web"] = make(chan struct{})
	h.sync(fakeDef("api", "web"))
	h.clk.Advance(0)
	<-h.enter
	h.step(5 * time.Second) // the default timeout
	got := h.check("api", "web")
	if got.Status != StatusDown || got.Message != "timed out" {
		t.Fatalf("after timeout: %+v", got)
	}
}

func TestSlowProbeIsNotStartedTwice(t *testing.T) {
	h := newHarness(t, func(c *Config) { c.Concurrency = 1 })
	h.gates["s/a"] = make(chan struct{})
	h.gates["s/b"] = make(chan struct{})
	h.sync(fakeDef("s", "a"), fakeDef("s", "b"))
	h.clk.Advance(0)
	<-h.enter
	// One probe holds the only slot and the other waits for it. Their
	// next firings must not queue second runs behind them.
	h.clk.Advance(testInterval)
	close(h.gates["s/a"])
	close(h.gates["s/b"])
	if err := h.m.Flush(h.ctx); err != nil {
		t.Fatal(err)
	}
	if a, b := h.callCount("s/a"), h.callCount("s/b"); a != 1 || b != 1 {
		t.Errorf("runs = %d and %d, want 1 each", a, b)
	}
}

func TestCloseDiscardsInterruptedProbes(t *testing.T) {
	h := newHarness(t)
	h.gates["api/web"] = make(chan struct{})
	h.sync(fakeDef("api", "web"))
	h.clk.Advance(0)
	<-h.enter
	if err := h.m.Close(); err != nil {
		t.Fatal(err)
	}
	var n int
	_ = h.db.Reader().QueryRow(`SELECT COUNT(*) FROM monitor_results`).Scan(&n)
	if n != 0 {
		t.Errorf("a shutdown-interrupted probe stored %d results", n)
	}
	if err := h.m.Sync(h.ctx, nil); !errors.Is(err, ErrClosed) {
		t.Errorf("Sync after Close = %v", err)
	}
	if err := h.m.Heartbeat(h.ctx, "a", "b", nil); !errors.Is(err, ErrClosed) {
		t.Errorf("Heartbeat after Close = %v", err)
	}
}

func TestStartSpreadIsDeterministic(t *testing.T) {
	h := newHarness(t, func(c *Config) { c.StartSpread = 8 * time.Second })
	d1 := h.m.firstDelay("api/web", testInterval)
	d2 := h.m.firstDelay("api/web", testInterval)
	if d1 != d2 || d1 < 0 || d1 >= 8*time.Second {
		t.Fatalf("delays %v %v", d1, d2)
	}
	if d := h.m.firstDelay("api/web", 3*time.Second); d >= 3*time.Second {
		t.Fatalf("delay %v exceeds the interval", d)
	}
	distinct := map[time.Duration]bool{}
	for _, n := range []string{"a", "b", "c", "d", "e", "f"} {
		distinct[h.m.firstDelay("svc/"+n, testInterval)] = true
	}
	if len(distinct) < 3 {
		t.Errorf("only %d distinct delays for 6 checks", len(distinct))
	}
}

func TestConfigValidation(t *testing.T) {
	h := newHarness(t)
	cfg := h.cfg
	cfg.RawRetention, cfg.RollupRetention = 48*time.Hour, 24*time.Hour
	if _, err := New(context.Background(), cfg); err == nil {
		t.Error("rollup retention shorter than raw retention accepted")
	}
	if _, err := New(context.Background(), Config{}); err == nil {
		t.Error("missing DB and Bus accepted")
	}
}

func heartbeatDef(service, name string, props map[string]any) Def {
	return Def{Service: service, Check: model.Check{Name: name, Type: "heartbeat", Interval: model.Duration(testInterval), Props: props}}
}

func checkSignals(sigs []signal.Signal, check string) []signal.Signal {
	var out []signal.Signal
	for _, s := range kinds(sigs, signal.KindCheck) {
		if s.Data["check"] == check {
			out = append(out, s)
		}
	}
	return out
}

func TestValidateChecksAndTypes(t *testing.T) {
	h := newHarness(t)
	if got := h.m.CheckTypes(); len(got) != 2 || got[0].Type != "fake" || got[1].Type != "heartbeat" {
		t.Fatalf("CheckTypes = %+v", got)
	}
	checks := []model.Check{
		{Name: "ok", Type: "fake", Props: map[string]any{"key": "a"}},
		{Name: "bad-props", Type: "fake", Props: map[string]any{"nope": 1}},
		{Name: "bad-type", Type: "ghost"},
	}
	is := h.m.ValidateChecks(checks, "spec.checks")
	if len(is) != 2 || is[0].Path != "spec.checks[1].props" || is[1].Path != "spec.checks[2].type" {
		t.Fatalf("issues = %+v", is)
	}
	if is := h.m.ValidateChecks(checks[:1], "spec.checks"); len(is) != 0 {
		t.Fatalf("valid checks reported: %+v", is)
	}
}
