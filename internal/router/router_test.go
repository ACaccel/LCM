package router_test

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"errors"
	"slices"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/ACaccel/lcm/internal/audit"
	"github.com/ACaccel/lcm/internal/auth"
	"github.com/ACaccel/lcm/internal/clock"
	"github.com/ACaccel/lcm/internal/connection"
	"github.com/ACaccel/lcm/internal/maintenance"
	"github.com/ACaccel/lcm/internal/model"
	"github.com/ACaccel/lcm/internal/monitor"
	"github.com/ACaccel/lcm/internal/resource"
	"github.com/ACaccel/lcm/internal/router"
	"github.com/ACaccel/lcm/internal/router/xiaomi/xiaomitest"
	"github.com/ACaccel/lcm/internal/secret"
	"github.com/ACaccel/lcm/internal/store"
	"github.com/ACaccel/lcm/internal/store/storetest"
)

var epoch = time.Date(2026, 11, 1, 0, 0, 0, 0, time.UTC)

type secrets map[string]string

func (s secrets) Get(name string) (secret.Value, error) {
	v, ok := s[name]
	if !ok {
		return secret.Value{}, secret.ErrNotFound
	}
	return secret.NewValue(v), nil
}

type nativeCall struct {
	health  monitor.Health
	reasons []string
}

type fakeNative struct {
	mu      sync.Mutex
	set     map[string]nativeCall
	cleared chan string
	// hold, when set, stops the next SetNative before it records anything:
	// it signals entered and waits until release is closed.
	hold *nativeHold
}

type nativeHold struct{ entered, release chan struct{} }

func (f *fakeNative) SetNative(resource, _ string, _ map[string]string, h monitor.Health, reasons []string) {
	f.mu.Lock()
	hold := f.hold
	f.hold = nil
	f.mu.Unlock()
	if hold != nil {
		close(hold.entered)
		<-hold.release
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	f.set[resource] = nativeCall{h, reasons}
}

func (f *fakeNative) Forget(resource string) {
	f.mu.Lock()
	defer f.mu.Unlock()
	delete(f.set, resource)
	select {
	case f.cleared <- resource:
	default:
	}
}

func (f *fakeNative) get(name string) (nativeCall, bool) {
	f.mu.Lock()
	defer f.mu.Unlock()
	c, ok := f.set[name]
	return c, ok
}

type env struct {
	db    *store.DB
	clk   *clock.Fake
	ctx   context.Context
	store *resource.Store
	m     *router.Manager
	fake  *xiaomitest.Router
	// down is a second router for the connection "down".
	down   *xiaomitest.Router
	gate   *maintenance.Gate
	native *fakeNative

	mu     sync.Mutex
	events []resource.Event
}

// connYAML is a connections file for the fake router; %URL% is replaced
// with its address.
const connYAML = `connections:
  - name: home
    type: xiaomi
    url: %URL%
    passwordSecret: router-password
    allowActions: [reboot]
  - name: readonly
    type: xiaomi
    url: %URL%
    passwordSecret: router-password
  - name: other-only
    type: xiaomi
    url: %URL%
    passwordSecret: router-password
    allowResources: [other]
  - name: down
    type: xiaomi
    url: %DOWN%
    passwordSecret: router-password
`

func newEnv(t *testing.T) *env {
	t.Helper()
	clk := clock.NewFake(epoch)
	db := storetest.Open(t)
	e := &env{db: db, clk: clk, ctx: auth.WithActor(context.Background(), auth.CLI), fake: xiaomitest.New(t, clk), down: xiaomitest.New(t, clk),
		native: &fakeNative{set: map[string]nativeCall{}, cleared: make(chan string, 16)}}
	sec := secrets{"router-password": xiaomitest.DefaultPassword}
	conns, err := connection.Parse([]byte(strings.NewReplacer("%URL%", e.fake.URL(), "%DOWN%", e.down.URL()).Replace(connYAML)), sec)
	if err != nil {
		t.Fatal(err)
	}
	e.gate = maintenance.New(db)
	log := audit.New(db, audit.WithClock(clk.Now))
	e.store, err = resource.New(resource.Config{DB: db, Gate: e.gate, Audit: log, Clock: clk, OnChange: func(c resource.Change) {
		if e.m != nil {
			e.m.Changed(c)
		}
	}})
	if err != nil {
		t.Fatal(err)
	}
	e.m, err = router.New(router.Config{
		DB: db, Resources: e.store, Audit: log, Gate: e.gate, Connections: conns, Secrets: sec, Native: e.native,
		Clock: clk, HTTP: e.fake.HTTPClient(),
		OnEvent: func(ev resource.Event) {
			e.mu.Lock()
			e.events = append(e.events, ev)
			e.mu.Unlock()
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	e.store.RegisterHooks(model.ResourceRouter, e.m.Hooks())
	return e
}

func routerDoc(name, conn string) string {
	return "apiVersion: lcm/v2\nkind: Resource\nmetadata: { name: " + name + ", labels: { site: home } }\nspec:\n" +
		"  type: router\n  adapter: xiaomi\n  connection: " + conn + "\n  config:\n    interval: 60s\n" +
		"    thresholds: { cpuPercent: 80, memoryPercent: 90, consecutive: 2, hysteresis: 10 }\n"
}

func (e *env) create(t *testing.T, name, conn string) {
	t.Helper()
	if _, err := e.store.Put(e.ctx, []byte(routerDoc(name, conn)), 0); err != nil {
		t.Fatal(err)
	}
}

func (e *env) observe(t *testing.T, name string) router.Record {
	t.Helper()
	r, err := e.m.Observe(e.ctx, name)
	if err != nil {
		t.Fatal(err)
	}
	return r
}

func (e *env) kinds() []string {
	e.mu.Lock()
	defer e.mu.Unlock()
	var out []string
	for _, ev := range e.events {
		out = append(out, ev.Kind+":"+ev.Reason)
	}
	return out
}

func (e *env) direct() context.Context { return auth.WithDirect(e.ctx) }

func TestSaveValidatesConnection(t *testing.T) {
	e := newEnv(t)
	for _, c := range []struct{ name, conn, want string }{
		{"router", "absent", "not configured"},
		{"router", "other-only", "does not allow resource"},
	} {
		_, err := e.store.Put(e.ctx, []byte(routerDoc(c.name, c.conn)), 0)
		var is model.Issues
		if !errors.Is(err, resource.ErrInvalid) || !errors.As(err, &is) || len(is) != 1 || is[0].Path != "spec.connection" || !strings.Contains(is[0].Message, c.want) {
			t.Fatalf("%s: err = %v", c.conn, err)
		}
	}
	if _, err := e.store.Get(e.ctx, "router"); !errors.Is(err, resource.ErrNotFound) {
		t.Fatalf("rejected router stored: %v", err)
	}
	e.create(t, "other", "other-only")
	e.create(t, "router", "home")
	r, err := e.m.Get(e.ctx, "router")
	if err != nil {
		t.Fatal(err)
	}
	var status string
	if err := e.db.Reader().QueryRow(`SELECT status FROM resource_status WHERE resource_id = ?`, r.ID).Scan(&status); err != nil || status != "{}" {
		t.Fatalf("status row = %q, %v", status, err)
	}
	if r.Status.Fresh || r.Status.Health != "unknown" || r.Status.Reachability != "unknown" {
		t.Fatalf("new router status = %+v", r.Status)
	}
	// An update may not move the router to a connection it may not use.
	if _, err := e.store.Put(e.ctx, []byte(routerDoc("router", "other-only")), 1); !errors.Is(err, resource.ErrInvalid) {
		t.Fatalf("update to a forbidden connection: %v", err)
	}
}

func TestObserveHealthyRouter(t *testing.T) {
	e := newEnv(t)
	e.create(t, "router", "home")
	r := e.observe(t, "router")
	s := r.Status.Snapshot
	if s == nil || s.Outcome != router.OutcomeOK || s.CPUPercent == nil || *s.CPUPercent < 12 || *s.CPUPercent > 13 ||
		s.MemoryPercent == nil || *s.MemoryPercent != 45 || s.Clients == nil || *s.Clients != 5 || s.UptimeSeconds == nil ||
		*s.UptimeSeconds != 86400 || s.WAN.Link == nil || !*s.WAN.Link || s.WAN.Type != "pppoe" || s.Model == "" ||
		s.Sources["status"].State != "available" || s.Sources["wan"].State != "available" {
		t.Fatalf("snapshot = %+v", s)
	}
	if !r.Status.Fresh || r.Status.Health != "healthy" || r.Status.Reachability != "reachable" || r.Status.BootAt == nil || !r.Status.BootAt.Equal(epoch.Add(-24*time.Hour)) {
		t.Fatalf("status = %+v", r.Status)
	}
	if c, ok := e.native.get("router"); !ok || c.health != monitor.HealthHealthy {
		t.Fatalf("native = %+v", c)
	}
	rec, _ := e.store.Get(e.ctx, "router")
	st, err := e.m.Type().Status(e.ctx, rec)
	if err != nil || st.State != resource.StateOnline || !st.StateSince.Equal(epoch) {
		t.Fatalf("type status = %+v, %v", st, err)
	}
	// The stored answer never contains what the router must not reveal.
	raw, _ := json.Marshal(r.Status)
	for _, secretValue := range []string{xiaomitest.DefaultPassword, xiaomitest.Serial, xiaomitest.HardwareMAC, xiaomitest.WANSecret, xiaomitest.WANAddress} {
		if strings.Contains(string(raw), secretValue) {
			t.Fatalf("status contains %q", secretValue)
		}
	}
	e.clk.Advance(20 * time.Second)
	e.observe(t, "router")
	hist, err := e.m.History(e.ctx, "router", 0, 10, false)
	if err != nil || len(hist) != 2 || hist[0].ID == 0 || hist[1].ID <= hist[0].ID {
		t.Fatalf("history = %+v, %v", hist, err)
	}
	minute, err := e.m.History(e.ctx, "router", 0, 10, true)
	if err != nil || len(minute) != 1 || minute[0].CPUPercent == nil {
		t.Fatalf("minute history = %+v, %v", minute, err)
	}
	// Data older than staleAfter says nothing about now.
	e.clk.Advance(4 * time.Minute)
	stale, _ := e.m.Get(e.ctx, "router")
	if stale.Status.Fresh || stale.Status.Health != "unknown" {
		t.Fatalf("stale status = %+v", stale.Status)
	}
	if st, _ := e.m.Type().Status(e.ctx, rec); st.State != resource.StateUnknown || !st.StateSince.Equal(epoch.Add(20*time.Second+3*time.Minute)) {
		t.Fatalf("stale type status = %+v", st)
	}
}

func TestThresholdsAndReachability(t *testing.T) {
	e := newEnv(t)
	e.create(t, "router", "home")
	e.observe(t, "router")
	busy := map[string]any{"cpu": map[string]any{"core": 4, "load": 0.95}, "mem": map[string]any{"usage": 0.5}}
	e.fake.SetStatusBody(busy)
	e.clk.Advance(time.Minute)
	if r := e.observe(t, "router"); r.Status.Health != "healthy" {
		t.Fatalf("degraded after one sample: %+v", r.Status)
	}
	e.clk.Advance(time.Minute)
	r := e.observe(t, "router")
	if r.Status.Health != "degraded" || !slices.Equal(r.Status.Reasons, []string{"cpu"}) {
		t.Fatalf("status after two busy samples = %+v", r.Status)
	}
	if c, _ := e.native.get("router"); c.health != monitor.HealthDegraded {
		t.Fatalf("native = %+v", c)
	}
	// Within the hysteresis the threshold stays active.
	e.fake.SetStatusBody(map[string]any{"cpu": map[string]any{"load": 0.75}, "mem": map[string]any{"usage": 0.5}})
	e.clk.Advance(time.Minute)
	if r := e.observe(t, "router"); r.Status.Health != "degraded" {
		t.Fatalf("recovered within hysteresis: %+v", r.Status)
	}
	e.fake.SetStatusBody(nil)
	e.clk.Advance(time.Minute)
	if r := e.observe(t, "router"); r.Status.Health != "healthy" {
		t.Fatalf("not recovered: %+v", r.Status)
	}

	e.fake.Close()
	e.clk.Advance(time.Minute)
	r = e.observe(t, "router")
	if r.Status.Snapshot.Outcome != router.OutcomeUnreachable || r.Status.Health != "down" || r.Status.Reachability != "unreachable" {
		t.Fatalf("unreachable status = %+v", r.Status)
	}
	if c, _ := e.native.get("router"); c.health != monitor.HealthDown {
		t.Fatalf("native = %+v", c)
	}
	rec, _ := e.store.Get(e.ctx, "router")
	if st, _ := e.m.Type().Status(e.ctx, rec); st.State != resource.StateOffline {
		t.Fatalf("type status = %+v", st)
	}
	want := []string{
		"router.threshold.firing:cpu", "router.health.changed:degraded",
		"router.threshold.recovered:cpu", "router.health.changed:healthy",
		"router.reachability.changed:unreachable", "router.health.changed:down",
	}
	if got := e.kinds(); !slices.Equal(got, want) {
		t.Fatalf("events = %v", got)
	}
	evs, err := e.m.Events(e.ctx, "router", 0, 100)
	if err != nil || len(evs) != len(want) || evs[0].ResourceID != rec.ID || evs[0].Source != "poll" || evs[0].ID == 0 {
		t.Fatalf("stored events = %+v, %v", evs, err)
	}
}

func TestFailedAttempts(t *testing.T) {
	e := newEnv(t)
	e.create(t, "router", "home")
	e.observe(t, "router")

	e.fake.SetPassword("rotated-password")
	e.fake.ExpireTokens()
	e.clk.Advance(time.Minute)
	r := e.observe(t, "router")
	if s := r.Status.Snapshot; s.Outcome != router.OutcomeAuthFailed || s.Reason != "authentication rejected" || r.Status.Health != "unknown" || r.Status.Reachability != "reachable" {
		t.Fatalf("auth failure = %+v / %+v", s, r.Status)
	}
	rec, _ := e.store.Get(e.ctx, "router")
	if st, _ := e.m.Type().Status(e.ctx, rec); st.State != resource.StateUnknown {
		t.Fatalf("state after auth failure = %+v", st)
	}

	e.fake.SetPassword(xiaomitest.DefaultPassword)
	e.fake.SetFailure(xiaomitest.Status, xiaomitest.FailHTTP500)
	e.clk.Advance(time.Minute)
	r = e.observe(t, "router")
	if s := r.Status.Snapshot; s.Outcome != router.OutcomeOK || s.Sources["status"].State != "unavailable" || s.CPUPercent != nil || r.Status.Health != "unknown" {
		t.Fatalf("partial status = %+v / %+v", s, r.Status)
	}
	e.fake.SetFailure(xiaomitest.WAN, xiaomitest.FailCode)
	e.clk.Advance(time.Minute)
	if r = e.observe(t, "router"); r.Status.Snapshot.Outcome != router.OutcomeUnavailable {
		t.Fatalf("no usable source = %+v", r.Status.Snapshot)
	}
	if got := e.kinds(); !slices.Contains(got, "router.collector.changed:status unavailable") {
		t.Fatalf("events = %v", got)
	}
}

func TestNotConfiguredConnection(t *testing.T) {
	e := newEnv(t)
	e.create(t, "router", "home")
	sec := secrets{}
	conns, err := connection.Parse([]byte(strings.NewReplacer("%URL%", e.fake.URL(), "%DOWN%", e.down.URL()).Replace(connYAML)), secrets{"router-password": "x"})
	if err != nil {
		t.Fatal(err)
	}
	m, err := router.New(router.Config{DB: e.db, Resources: e.store, Audit: audit.New(e.db), Connections: conns, Secrets: sec, Clock: e.clk})
	if err != nil {
		t.Fatal(err)
	}
	r, err := m.Observe(e.ctx, "router")
	if err != nil || r.Status.Snapshot.Outcome != router.OutcomeNotConfigured || r.Status.Snapshot.Reason != "password secret is unavailable" {
		t.Fatalf("observe = %+v, %v", r.Status.Snapshot, err)
	}
	if e.fake.LoginAttempts() != 0 {
		t.Fatal("router contacted without a password")
	}
}

func TestRebootSucceedsOnUptimeReset(t *testing.T) {
	e := newEnv(t)
	e.create(t, "router", "home")
	e.observe(t, "router")
	e.fake.SetReboot(xiaomitest.RebootAcceptAndDrop, 90*time.Second)
	op, err := e.m.Submit(e.direct(), "router", "reboot", "key-1", 1)
	if err != nil {
		t.Fatal(err)
	}
	if op.State != "observing" || !op.Unresolved || op.Receipt == nil || op.Receipt.Outcome != "unknown" || op.Request.Connection != "home" || e.fake.Reboots() != 1 {
		t.Fatalf("submitted = %+v", op)
	}
	// Retrying with the same key returns the operation without sending.
	again, err := e.m.Submit(e.direct(), "router", "reboot", "key-1", 1)
	if err != nil || again.ID != op.ID || e.fake.Reboots() != 1 {
		t.Fatalf("retry = %+v, %v", again, err)
	}
	if _, err := e.m.Submit(e.direct(), "router", "reboot", "key-2", 1); !errors.Is(err, router.ErrPrecondition) {
		t.Fatalf("second operation: %v", err)
	}
	e.clk.Advance(30 * time.Second)
	if r := e.observe(t, "router"); r.Status.Health != "down" {
		t.Fatalf("during reboot = %+v", r.Status)
	}
	if got, _ := e.m.Operation(e.ctx, "router", op.ID); got.State != "observing" {
		t.Fatalf("decided while down: %+v", got)
	}
	e.clk.Advance(2 * time.Minute)
	e.observe(t, "router")
	got, err := e.m.Operation(e.ctx, "router", op.ID)
	if err != nil || got.State != "succeeded" || got.Unresolved || got.Evidence == nil || got.Evidence.Snapshot.Outcome != router.OutcomeOK {
		t.Fatalf("after reboot = %+v, %v", got, err)
	}
	kinds := e.kinds()
	for _, want := range []string{"router.operation.submit:", "router.operation.changed:submitting", "router.operation.changed:observing", "router.boot.changed:uptime restarted", "router.operation.changed:succeeded"} {
		if !slices.Contains(kinds, want) {
			t.Fatalf("events %v lack %s", kinds, want)
		}
	}
	var n int
	if err := e.db.Reader().QueryRow(`SELECT COUNT(*) FROM audit_log WHERE action = 'router.operation.submit'`).Scan(&n); err != nil || n != 1 {
		t.Fatalf("audit entries = %d, %v", n, err)
	}
}

func TestRebootIndeterminateWithoutEvidence(t *testing.T) {
	e := newEnv(t)
	e.create(t, "router", "home")
	e.observe(t, "router")
	e.fake.SetReboot(xiaomitest.RebootAccept, time.Hour)
	op, err := e.m.Submit(e.direct(), "router", "reboot", "key-1", 1)
	if err != nil || op.State != "observing" || op.Receipt.Outcome != "accepted" {
		t.Fatalf("submit = %+v, %v", op, err)
	}
	e.clk.Advance(5 * time.Minute)
	e.observe(t, "router")
	if _, err := e.m.Resolve(e.direct(), "router", op.ID, "checked", 1); !errors.Is(err, router.ErrPrecondition) {
		t.Fatalf("resolved an observing operation: %v", err)
	}
	e.clk.Advance(6 * time.Minute)
	e.observe(t, "router")
	got, _ := e.m.Operation(e.ctx, "router", op.ID)
	if got.State != "indeterminate" || !got.Unresolved {
		t.Fatalf("after deadline = %+v", got)
	}
	// Retiring needs the outcome settled first.
	rec, _ := e.store.Get(e.ctx, "router")
	if err := e.m.Type().ChangePhase(e.ctx, rec, resource.PhaseRetired, 1, ""); !errors.Is(err, resource.ErrPrecondition) {
		t.Fatalf("retired with an open operation: %v", err)
	}
	if _, err := e.m.Resolve(e.ctx, "router", op.ID, "checked", 1); !errors.Is(err, router.ErrForbidden) {
		t.Fatalf("resolve without a direct request: %v", err)
	}
	resolved, err := e.m.Resolve(e.direct(), "router", op.ID, "power-cycled by hand", 1)
	if err != nil || resolved.Unresolved || resolved.State != "indeterminate" || resolved.ResolvedBy == nil || resolved.Resolution != "power-cycled by hand" {
		t.Fatalf("resolve = %+v, %v", resolved, err)
	}
	if err := e.m.Type().ChangePhase(e.ctx, rec, resource.PhaseRetired, 1, ""); err != nil {
		t.Fatal(err)
	}
	if _, ok := e.native.get("router"); ok {
		t.Fatal("native health kept after retire")
	}
}

func TestRebootRefusedOrNotSent(t *testing.T) {
	e := newEnv(t)
	e.create(t, "router", "home")
	e.observe(t, "router")
	e.fake.SetReboot(xiaomitest.RebootRefuse, 0)
	op, err := e.m.Submit(e.direct(), "router", "reboot", "key-1", 1)
	if err != nil || op.State != "failed" || op.Unresolved || op.Receipt.Outcome != "rejected" {
		t.Fatalf("refused = %+v, %v", op, err)
	}
	e.fake.SetReboot(xiaomitest.RebootAccept, 0)
	e.fake.SetFailure(xiaomitest.Reboot, xiaomitest.FailInvalidToken)
	e.clk.Advance(time.Second)
	op, err = e.m.Submit(e.direct(), "router", "reboot", "key-2", 1)
	if err != nil || op.State != "failed" || op.Unresolved || op.Receipt.Outcome != "rejected" {
		t.Fatalf("token refused twice = %+v, %v", op, err)
	}
	// A request that never left LCM certainly did nothing.
	e.fake.Close()
	op, err = e.m.Submit(e.direct(), "router", "reboot", "key-3", 1)
	if err != nil || op.State != "failed" || op.Unresolved || op.Receipt.Outcome != "not-sent" || op.Reason != "request was not delivered" {
		t.Fatalf("not sent = %+v, %v", op, err)
	}
	if e.fake.Reboots() != 0 {
		t.Fatalf("reboots = %d", e.fake.Reboots())
	}
}

func TestRebootPreconditions(t *testing.T) {
	e := newEnv(t)
	e.create(t, "router", "home")
	e.create(t, "readonly", "readonly")
	if _, err := e.m.Submit(e.ctx, "router", "reboot", "k", 1); !errors.Is(err, router.ErrForbidden) {
		t.Fatalf("policy-like request: %v", err)
	}
	policy := auth.WithDirect(auth.WithActor(context.Background(), auth.Actor{Kind: auth.KindSystem, ID: "policy:p", Name: "p"}))
	if _, err := e.m.Submit(policy, "router", "reboot", "k", 1); !errors.Is(err, router.ErrForbidden) {
		t.Fatalf("system actor: %v", err)
	}
	if _, err := e.m.Submit(e.direct(), "router", "shutdown", "k", 1); !errors.Is(err, router.ErrUnsupported) {
		t.Fatalf("shutdown: %v", err)
	}
	if _, err := e.m.Submit(e.direct(), "router", "reboot", "bad key", 1); !errors.Is(err, router.ErrPrecondition) {
		t.Fatalf("bad key: %v", err)
	}
	if _, err := e.m.Submit(e.direct(), "router", "reboot", "k", 1); err == nil || !strings.Contains(err.Error(), "fresh successful observation") {
		t.Fatalf("unobserved: %v", err)
	}
	e.observe(t, "router")
	e.observe(t, "readonly")
	if _, err := e.m.Submit(e.direct(), "readonly", "reboot", "k", 1); err == nil || !strings.Contains(err.Error(), "does not allow reboot") {
		t.Fatalf("read-only connection: %v", err)
	}
	if _, err := e.m.Submit(e.direct(), "router", "reboot", "k", 2); !errors.Is(err, router.ErrConflict) {
		t.Fatalf("stale version: %v", err)
	}
	// Durable machine maintenance, as the machine type stores it.
	if err := e.db.Write(e.ctx, func(tx *sql.Tx) error {
		if _, err := tx.Exec(`INSERT INTO resources(id,name,type,adapter,phase,version,created_at,updated_at) VALUES('m','local','machine','local','managed',1,0,0)`); err != nil {
			return err
		}
		_, err := tx.Exec(`INSERT INTO machine_maintenance(host,resource_id,state,entered_at,body) VALUES('local','m','active',0,'{}')`)
		return err
	}); err != nil {
		t.Fatal(err)
	}
	caps, err := e.m.Capabilities(e.ctx, "router")
	if err != nil || caps[0].Available || caps[0].Reason != "local machine maintenance is active" {
		t.Fatalf("capabilities in maintenance = %+v, %v", caps, err)
	}
	if _, err := e.m.Submit(e.direct(), "router", "reboot", "k", 1); !errors.Is(err, router.ErrPrecondition) || e.fake.Reboots() != 0 {
		t.Fatalf("reboot in maintenance: %v", err)
	}
	// Observation continues during maintenance.
	e.clk.Advance(time.Minute)
	if r := e.observe(t, "router"); !r.Status.Fresh {
		t.Fatal("not observed during maintenance")
	}
}

func TestReconcileNeverResubmits(t *testing.T) {
	e := newEnv(t)
	e.create(t, "router", "home")
	r := e.observe(t, "router")
	now := e.clk.Now()
	for i, state := range []string{"pending", "submitting"} {
		id := "op-" + state
		body, _ := json.Marshal(router.Operation{ID: id, ResourceID: r.ID, Action: "reboot", State: state, Unresolved: true, Version: 1, CreatedAt: now, UpdatedAt: now,
			Request: router.Request{Action: "reboot", Deadline: now.Add(10 * time.Minute)}})
		if err := e.db.Write(e.ctx, func(tx *sql.Tx) error {
			if i > 0 {
				// One unresolved operation per resource: settle the first.
				if _, err := tx.Exec(`UPDATE resource_operations SET unresolved = 0`); err != nil {
					return err
				}
			}
			_, err := tx.Exec(`INSERT INTO resource_operations(id,resource_id,idempotency_key,request_hash,action,state,created_at,updated_at,body) VALUES(?,?,?,?,?,?,?,?,?)`,
				id, r.ID, id, "h", "reboot", state, now.UnixMilli(), now.UnixMilli(), string(body))
			return err
		}); err != nil {
			t.Fatal(err)
		}
	}
	if err := e.m.Reconcile(e.ctx); err != nil {
		t.Fatal(err)
	}
	got, _ := e.m.Operation(e.ctx, "router", "op-submitting")
	if got.State != "indeterminate" || !got.Unresolved || !strings.Contains(got.Reason, "never resubmitted") {
		t.Fatalf("recovered = %+v", got)
	}
	// A request interrupted before it was sent failed.
	if err := e.db.Write(e.ctx, func(tx *sql.Tx) error {
		_, err := tx.Exec(`UPDATE resource_operations SET unresolved = 0`)
		return err
	}); err != nil {
		t.Fatal(err)
	}
	insertOperation(t, e, r, "op-unsent", "op-unsent", "pending")
	if err := e.m.Reconcile(e.ctx); err != nil {
		t.Fatal(err)
	}
	got, _ = e.m.Operation(e.ctx, "router", "op-unsent")
	if got.State != "failed" || got.Unresolved || got.Receipt == nil || got.Receipt.Outcome != "not-sent" {
		t.Fatalf("recovered pending = %+v", got)
	}
	if e.fake.Reboots() != 0 {
		t.Fatal("recovery resubmitted a reboot")
	}
}

func TestLoopsFollowResources(t *testing.T) {
	e := newEnv(t)
	e.create(t, "router", "home")
	e.create(t, "other", "down")
	e.down.Close()
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() {
		e.m.Run(ctx)
		close(done)
	}()
	t.Cleanup(func() {
		cancel()
		<-done
	})
	wait := func(n int) {
		t.Helper()
		wctx, wcancel := context.WithTimeout(context.Background(), 20*time.Second)
		defer wcancel()
		if err := e.clk.WaitForTimers(wctx, n); err != nil {
			t.Fatalf("waiting for %d timers: %v", n, err)
		}
	}
	wait(2)
	for _, name := range []string{"router", "other"} {
		if r, _ := e.m.Get(e.ctx, name); !r.Status.Fresh {
			t.Fatalf("%s not observed by its loop: %+v", name, r.Status)
		}
	}
	if r, _ := e.m.Get(e.ctx, "other"); r.Status.Health != "down" {
		t.Fatalf("down router = %+v", r.Status)
	}
	rec, _ := e.store.Get(e.ctx, "other")
	if err := e.m.Type().ChangePhase(e.ctx, rec, resource.PhaseRetired, 1, "decommissioned"); err != nil {
		t.Fatal(err)
	}
	if _, ok := e.native.get("other"); ok {
		t.Fatal("retired router still reports native health")
	}
	// ChangePhase clears native health at once, the loop supervisor again
	// once the retired router's loop has ended.
	for range 2 {
		select {
		case name := <-e.native.cleared:
			if name != "other" {
				t.Fatalf("cleared %s", name)
			}
		case <-time.After(20 * time.Second):
			t.Fatal("loop of the retired router did not stop")
		}
	}
	if n := e.clk.Pending(); n != 1 {
		t.Fatalf("pending timers = %d", n)
	}
	before, _ := e.m.History(e.ctx, "router", 0, 100, false)
	e.clk.Advance(time.Minute)
	wait(1)
	after, _ := e.m.History(e.ctx, "router", 0, 100, false)
	if len(after) != len(before)+1 {
		t.Fatalf("history %d -> %d", len(before), len(after))
	}
}

// A router observed rarely must still have its reboot verified before the
// deadline: Submit wakes its loop and the loop observes at most every 30s
// while the operation waits for evidence.
func TestRebootVerifiedWithLongInterval(t *testing.T) {
	e := newEnv(t)
	doc := strings.Replace(routerDoc("router", "home"), "interval: 60s", "interval: 1h", 1)
	if _, err := e.store.Put(e.ctx, []byte(doc), 0); err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() {
		e.m.Run(ctx)
		close(done)
	}()
	t.Cleanup(func() {
		cancel()
		<-done
	})
	samples := func() int {
		h, err := e.m.History(e.ctx, "router", 0, 100, false)
		if err != nil {
			t.Fatal(err)
		}
		return len(h)
	}
	// nextSample waits until the loop stored one more sample and armed its
	// next timer.
	nextSample := func(n int) int {
		t.Helper()
		deadline := time.Now().Add(20 * time.Second)
		for samples() <= n {
			if time.Now().After(deadline) {
				t.Fatalf("no sample after %d", n)
			}
			time.Sleep(5 * time.Millisecond)
		}
		wctx, wcancel := context.WithTimeout(context.Background(), 20*time.Second)
		defer wcancel()
		if err := e.clk.WaitForTimers(wctx, 1); err != nil {
			t.Fatal(err)
		}
		return samples()
	}
	n := nextSample(0)
	e.fake.SetReboot(xiaomitest.RebootAcceptAndDrop, 90*time.Second)
	op, err := e.m.Submit(e.direct(), "router", "reboot", "key-1", 1)
	if err != nil || op.State != "observing" {
		t.Fatalf("submit = %+v, %v", op, err)
	}
	n = nextSample(n)
	for range 6 {
		e.clk.Advance(30 * time.Second)
		n = nextSample(n)
		if got, _ := e.m.Operation(e.ctx, "router", op.ID); got.State == "succeeded" {
			// Back to the configured interval once nothing waits.
			e.clk.Advance(30 * time.Second)
			time.Sleep(20 * time.Millisecond)
			if samples() != n {
				t.Fatal("observed every 30s after the operation was decided")
			}
			return
		}
	}
	got, _ := e.m.Operation(e.ctx, "router", op.ID)
	t.Fatalf("reboot not verified within 3 minutes: %+v", got)
}

// failSubmitAudit makes the audit entry of a reboot submission fail.
func failSubmitAudit(t *testing.T, e *env) {
	t.Helper()
	if err := e.db.Write(e.ctx, func(tx *sql.Tx) error {
		_, err := tx.Exec(`CREATE TRIGGER fail_submit BEFORE INSERT ON audit_log WHEN NEW.action = 'router.operation.submit' BEGIN SELECT RAISE(ABORT, 'audit unavailable'); END`)
		return err
	}); err != nil {
		t.Fatal(err)
	}
}

func TestRebootNotSentWhenAuditFails(t *testing.T) {
	e := newEnv(t)
	e.create(t, "router", "home")
	e.observe(t, "router")
	failSubmitAudit(t, e)
	op, err := e.m.Submit(e.direct(), "router", "reboot", "key-1", 1)
	if err == nil || op.State != "failed" || op.Unresolved || op.Receipt == nil || op.Receipt.Outcome != "not-sent" {
		t.Fatalf("submit = %+v, %v", op, err)
	}
	stored, _ := e.m.Operation(e.ctx, "router", op.ID)
	if stored.State != "failed" || stored.Unresolved {
		t.Fatalf("stored = %+v", stored)
	}
	// A retry with the same key reports the failure without sending.
	again, err := e.m.Submit(e.direct(), "router", "reboot", "key-1", 1)
	if err != nil || again.ID != op.ID || again.State != "failed" || e.fake.Reboots() != 0 {
		t.Fatalf("retry = %+v, %v", again, err)
	}
}

// insertOperation stores an operation as an interrupted Submit leaves it.
func insertOperation(t *testing.T, e *env, r router.Record, id, key, state string) {
	t.Helper()
	now := e.clk.Now()
	sum := sha256.Sum256([]byte(r.ID + "\x00reboot\x00" + strconv.Itoa(r.Version)))
	hash := hex.EncodeToString(sum[:])
	body, _ := json.Marshal(router.Operation{ID: id, ResourceID: r.ID, Action: "reboot", State: state, Unresolved: true, Version: r.Version, CreatedAt: now, UpdatedAt: now,
		Request: router.Request{Action: "reboot", RequestHash: hash, IssuedAt: now, Deadline: now.Add(10 * time.Minute)}})
	if err := e.db.Write(e.ctx, func(tx *sql.Tx) error {
		_, err := tx.Exec(`INSERT INTO resource_operations(id,resource_id,idempotency_key,request_hash,action,state,created_at,updated_at,body) VALUES(?,?,?,?,?,?,?,?,?)`,
			id, r.ID, key, hash, "reboot", state, now.UnixMilli(), now.UnixMilli(), string(body))
		return err
	}); err != nil {
		t.Fatal(err)
	}
}

func TestAbandonedOperationsAreClosed(t *testing.T) {
	e := newEnv(t)
	e.create(t, "router", "home")
	r := e.observe(t, "router")

	// A retry finds the pending request of a Submit that stopped before
	// sending: it is closed as failed and never sent.
	insertOperation(t, e, r, "op-pending", "key-1", "pending")
	op, err := e.m.Submit(e.direct(), "router", "reboot", "key-1", 1)
	if err != nil || op.ID != "op-pending" || op.State != "failed" || op.Unresolved || e.fake.Reboots() != 0 {
		t.Fatalf("retry of a pending request = %+v, %v", op, err)
	}

	// Observation closes a submitting request once its deadline passed.
	insertOperation(t, e, r, "op-submitting", "key-2", "submitting")
	e.clk.Advance(5 * time.Minute)
	e.observe(t, "router")
	if got, _ := e.m.Operation(e.ctx, "router", "op-submitting"); got.State != "submitting" {
		t.Fatalf("closed before the deadline: %+v", got)
	}
	e.clk.Advance(6 * time.Minute)
	e.observe(t, "router")
	got, _ := e.m.Operation(e.ctx, "router", "op-submitting")
	if got.State != "indeterminate" || !got.Unresolved {
		t.Fatalf("after the deadline = %+v", got)
	}
	if _, err := e.m.Resolve(e.direct(), "router", "op-submitting", "checked", 1); err != nil {
		t.Fatal(err)
	}

	// Resolve closes a pending request instead of refusing it as open.
	insertOperation(t, e, r, "op-late", "key-3", "pending")
	if _, err := e.m.Resolve(e.direct(), "router", "op-late", "checked", 1); !errors.Is(err, router.ErrPrecondition) || !strings.Contains(err.Error(), "already resolved") {
		t.Fatalf("resolve of a pending request: %v", err)
	}
	if got, _ := e.m.Operation(e.ctx, "router", "op-late"); got.State != "failed" || got.Unresolved {
		t.Fatalf("pending after resolve = %+v", got)
	}
	if e.fake.Reboots() != 0 {
		t.Fatal("an abandoned request was sent")
	}
}

// An observation that read the router as managed must not report its
// health after a concurrent retire has forgotten it.
func TestObserveDoesNotReviveRetiredRouter(t *testing.T) {
	e := newEnv(t)
	e.create(t, "router", "home")
	e.observe(t, "router")
	hold := &nativeHold{entered: make(chan struct{}), release: make(chan struct{})}
	e.native.mu.Lock()
	e.native.hold = hold
	e.native.mu.Unlock()
	observed := make(chan struct{})
	go func() {
		defer close(observed)
		_, _ = e.m.Observe(e.ctx, "router")
	}()
	<-hold.entered
	rec, _ := e.store.Get(e.ctx, "router")
	retired := make(chan error, 1)
	go func() { retired <- e.m.Type().ChangePhase(e.ctx, rec, resource.PhaseRetired, 1, "") }()
	// The retire waits for the observation; without that it would finish
	// here, before the observation reports health.
	select {
	case err := <-retired:
		retired <- err
	case <-time.After(100 * time.Millisecond):
	}
	close(hold.release)
	<-observed
	if err := <-retired; err != nil {
		t.Fatal(err)
	}
	if _, ok := e.native.get("router"); ok {
		t.Fatal("retired router reports native health")
	}
}
