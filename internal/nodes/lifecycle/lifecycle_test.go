package lifecycle_test

import (
	"context"
	"errors"
	"fmt"
	"reflect"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/ACaccel/lcm/internal/audit"
	"github.com/ACaccel/lcm/internal/auth"
	"github.com/ACaccel/lcm/internal/catalog"
	"github.com/ACaccel/lcm/internal/clock"
	"github.com/ACaccel/lcm/internal/engine"
	"github.com/ACaccel/lcm/internal/engine/enginetest"
	"github.com/ACaccel/lcm/internal/execx"
	"github.com/ACaccel/lcm/internal/model"
	"github.com/ACaccel/lcm/internal/node"
	"github.com/ACaccel/lcm/internal/nodes/lifecycle"
	"github.com/ACaccel/lcm/internal/resource"
	rt "github.com/ACaccel/lcm/internal/runtime"
)

// fakeActions records calls and returns a canned outcome.
type fakeActions struct {
	mu     sync.Mutex
	calls  []string
	opts   []catalog.ActionOptions
	actors []auth.Actor
	err    error
	result catalog.ActionResult
	plan   catalog.Plan
}

func (f *fakeActions) record(ctx context.Context, action, name string, o catalog.ActionOptions) (catalog.ActionResult, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.calls = append(f.calls, action+" "+name)
	f.opts = append(f.opts, o)
	a, _ := auth.ActorFrom(ctx)
	f.actors = append(f.actors, a)
	if f.err != nil {
		return catalog.ActionResult{}, f.err
	}
	r := f.result
	r.Resource, r.Action = name, action
	return r, nil
}

func (f *fakeActions) Deploy(c context.Context, n string, o catalog.ActionOptions) (catalog.ActionResult, error) {
	return f.record(c, "deploy", n, o)
}

func (f *fakeActions) Rollback(c context.Context, n string, o catalog.ActionOptions) (catalog.ActionResult, error) {
	return f.record(c, "rollback", n, o)
}

func (f *fakeActions) Restart(c context.Context, n string, o catalog.ActionOptions) (catalog.ActionResult, error) {
	return f.record(c, "restart", n, o)
}

func (f *fakeActions) Start(c context.Context, n string, o catalog.ActionOptions) (catalog.ActionResult, error) {
	return f.record(c, "start", n, o)
}

func (f *fakeActions) Stop(c context.Context, n string, o catalog.ActionOptions) (catalog.ActionResult, error) {
	return f.record(c, "stop", n, o)
}

func (f *fakeActions) Backup(c context.Context, n string, o catalog.ActionOptions) (catalog.ActionResult, error) {
	return f.record(c, "backup", n, o)
}

func (f *fakeActions) RunJob(c context.Context, n string, o catalog.ActionOptions) (catalog.ActionResult, error) {
	return f.record(c, "run-job", n, o)
}

func (f *fakeActions) Plan(_ context.Context, n, action string, _ catalog.ActionOptions) (catalog.Plan, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.calls = append(f.calls, "plan "+action+" "+n)
	if f.err != nil {
		return catalog.Plan{}, f.err
	}
	p := f.plan
	p.Resource, p.Action = n, action
	return p, nil
}

func (f *fakeActions) CheckImpact(_ context.Context, n, _ string, _ bool) (resource.Impact, error) {
	return resource.Impact{Resource: n}, nil
}

func registry(a lifecycle.Actions) *node.Registry {
	r := node.NewRegistry()
	lifecycle.Register(r, a)
	return r
}

func decode(t *testing.T, r *node.Registry, typ string, props map[string]any) (*node.Definition, any, error) {
	t.Helper()
	d, ok := r.Lookup(typ)
	if !ok {
		t.Fatalf("type %s not registered", typ)
	}
	p, err := d.Decode(props)
	return d, p, err
}

func rc() *node.RunContext {
	return &node.RunContext{RunID: "01RUN", Policy: "self-heal", NodeID: "act", IdempotencyKey: "01RUN/act", Mode: node.ModeLive, Clock: clock.NewFake(time.UnixMilli(1_700_000_000_000))}
}

func TestRegistration(t *testing.T) {
	r := registry(&fakeActions{})
	want := []string{"lifecycle.backup", "lifecycle.deploy", "lifecycle.restart", "lifecycle.rollback", "lifecycle.run-job", "lifecycle.start", "lifecycle.stop"}
	var got []string
	for _, s := range r.Schemas() {
		got = append(got, s.Type)
		if s.Effect != node.EffectWrite || s.Category != lifecycle.Category {
			t.Errorf("%s: effect %s category %s", s.Type, s.Effect, s.Category)
		}
		if d, _ := r.Lookup(s.Type); !d.HasDryRun() {
			t.Errorf("%s has no dry run", s.Type)
		}
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("types = %v", got)
	}
}

func TestPropsValidation(t *testing.T) {
	r := registry(&fakeActions{})
	bad := []struct {
		typ   string
		props map[string]any
	}{
		{"lifecycle.restart", nil},
		{"lifecycle.restart", map[string]any{"resource": ""}},
		{"lifecycle.restart", map[string]any{"resource": "Bad Name"}},
		{"lifecycle.restart", map[string]any{"resource": "-x"}},
		{"lifecycle.restart", map[string]any{"resource": "a", "extra": 1}},
		{"lifecycle.run-job", map[string]any{"resource": "a"}},
		{"lifecycle.deploy", map[string]any{"resource": "a", "version": 5}},
	}
	for _, tt := range bad {
		if _, _, err := decode(t, r, tt.typ, tt.props); err == nil {
			t.Errorf("%s %v: accepted", tt.typ, tt.props)
		}
	}
	if _, _, err := decode(t, r, "lifecycle.deploy", map[string]any{"resource": "card-together"}); err != nil {
		t.Fatalf("version must be optional: %v", err)
	}
}

func TestRunPassesIdentityAndShapesOutput(t *testing.T) {
	f := &fakeActions{result: catalog.ActionResult{
		From: catalog.StateRunning, To: catalog.StateRunning, Version: "abc", PreviousVersion: "def",
		DeploymentID: 12, DurationMs: 340,
	}}
	r := registry(f)

	d, p, err := decode(t, r, "lifecycle.deploy", map[string]any{"resource": "web", "version": "abc1234"})
	if err != nil {
		t.Fatal(err)
	}
	res, err := d.Run(context.Background(), rc(), p)
	if err != nil {
		t.Fatal(err)
	}
	want := map[string]any{
		"resource": "web", "action": "deploy", "from": "running", "to": "running", "version": "abc",
		"previousVersion": "def", "deploymentId": int64(12), "durationMs": int64(340), "replayed": false,
		"startedMs": int64(1_700_000_000_000),
	}
	if !reflect.DeepEqual(res.Output, want) || res.Port != "" {
		t.Fatalf("output = %#v", res.Output)
	}
	if o := f.opts[0]; o.RunID != "01RUN" || o.IdempotencyKey != "01RUN/act" || o.Version != "abc1234" {
		t.Fatalf("options = %+v", o)
	}
	wantActor := auth.Actor{Kind: auth.KindSystem, ID: "run:01RUN", Name: "policy self-heal"}
	if f.actors[0] != wantActor {
		t.Fatalf("actor = %+v", f.actors[0])
	}

	// An actor already on the context wins.
	manual := auth.Actor{Kind: auth.KindUser, ID: "1", Name: "alice"}
	if _, err := d.Run(auth.WithActor(context.Background(), manual), rc(), p); err != nil || f.actors[1] != manual {
		t.Fatalf("actor = %+v %v", f.actors[1], err)
	}

	// Every type maps to its action.
	for typ, action := range map[string]string{
		"lifecycle.rollback": "rollback", "lifecycle.restart": "restart", "lifecycle.start": "start",
		"lifecycle.stop": "stop", "lifecycle.backup": "backup",
	} {
		d, p, err := decode(t, r, typ, map[string]any{"resource": "web"})
		if err != nil {
			t.Fatal(err)
		}
		if _, err := d.Run(context.Background(), rc(), p); err != nil {
			t.Fatal(err)
		}
		if got := f.calls[len(f.calls)-1]; got != action+" web" {
			t.Errorf("%s called %q", typ, got)
		}
	}
	d, p, _ = decode(t, r, "lifecycle.run-job", map[string]any{"resource": "web", "job": "reindex"})
	f.result.Output = "done"
	res, err = d.Run(context.Background(), rc(), p)
	if err != nil || f.opts[len(f.opts)-1].Job != "reindex" || res.Output.(map[string]any)["output"] != "done" {
		t.Fatalf("run-job: %+v %v %+v", res, err, f.opts[len(f.opts)-1])
	}
}

func TestErrorClassification(t *testing.T) {
	f := &fakeActions{}
	r := registry(f)
	d, p, _ := decode(t, r, "lifecycle.restart", map[string]any{"resource": "web"})

	tests := []struct {
		name      string
		err       error
		permanent bool
	}{
		{"busy", fmt.Errorf("lock: %w", catalog.ErrLockHeld), false},
		{"action failed", errors.New("systemctl exploded"), false},
		{"deploy failed", &catalog.StepError{Step: "build", Err: errors.New("exit 1")}, false},
		{"state forbids", &catalog.StateError{Resource: "web", Action: "restart", State: catalog.StateFailed}, true},
		{"no such service", fmt.Errorf("%w: web", catalog.ErrNotFound), true},
		{"precondition", catalog.ErrDirtyRepo, true},
		{"retired", catalog.ErrNotManaged, true},
		{"unsupported", fmt.Errorf("%w: external", rt.ErrUnsupported), true},
		{"invalid document", model.Issues{{Message: "x"}}, true},
	}
	for _, tt := range tests {
		f.err = tt.err
		_, err := d.Run(context.Background(), rc(), p)
		if err == nil || node.IsPermanent(err) != tt.permanent || !errors.Is(err, tt.err) && !errors.As(err, new(*catalog.StepError)) && !errors.As(err, new(model.Issues)) {
			t.Errorf("%s: err = %v permanent=%v, want permanent=%v", tt.name, err, node.IsPermanent(err), tt.permanent)
		}
	}
}

func TestDryRunReturnsPlan(t *testing.T) {
	f := &fakeActions{plan: catalog.Plan{From: catalog.StateRunning, To: catalog.StateRunning, Steps: []string{"lock resource:web", "restart systemd unit web.service"}}}
	r := registry(f)
	d, p, _ := decode(t, r, "lifecycle.restart", map[string]any{"resource": "web"})
	res, err := d.DryRun(context.Background(), rc(), p)
	if err != nil {
		t.Fatal(err)
	}
	out := res.Output.(map[string]any)
	if out["planned"] != true || out["to"] != "running" || !reflect.DeepEqual(out["steps"], []any{"lock resource:web", "restart systemd unit web.service"}) {
		t.Fatalf("output = %#v", out)
	}
	if len(f.calls) != 1 || f.calls[0] != "plan restart web" {
		t.Fatalf("a dry run must only plan: %v", f.calls)
	}

	f.err = &catalog.StateError{Resource: "web", Action: "restart", State: catalog.StateFailed}
	if _, err := d.DryRun(context.Background(), rc(), p); err == nil || !node.IsPermanent(err) {
		t.Fatalf("dry-run refusal: %v", err)
	}
}

// --- end to end through the engine with a real catalog.Manager ---

type engineAdapter struct {
	mu    sync.Mutex
	calls []string
	state rt.State
}

func (a *engineAdapter) Status(context.Context, model.Runtime) (rt.Status, error) {
	a.mu.Lock()
	defer a.mu.Unlock()
	return rt.Status{State: a.state}, nil
}

func (a *engineAdapter) act(name string, st rt.State) error {
	a.mu.Lock()
	defer a.mu.Unlock()
	a.calls = append(a.calls, name)
	a.state = st
	return nil
}
func (a *engineAdapter) Start(context.Context, model.Runtime) error {
	return a.act("start", rt.StateActive)
}
func (a *engineAdapter) Stop(context.Context, model.Runtime) error {
	return a.act("stop", rt.StateInactive)
}
func (a *engineAdapter) Restart(context.Context, model.Runtime) error {
	return a.act("restart", rt.StateActive)
}

type engineRuntimes struct{ a *engineAdapter }

func (r engineRuntimes) For(model.Runtime) (rt.Adapter, error) { return r.a, nil }

type memLocker struct {
	mu   sync.Mutex
	held map[string]bool
}

func (l *memLocker) Acquire(_ context.Context, name, _ string, _ time.Duration) (func(context.Context) error, error) {
	l.mu.Lock()
	defer l.mu.Unlock()
	if l.held[name] {
		return nil, catalog.ErrLockHeld
	}
	l.held[name] = true
	return func(context.Context) error {
		l.mu.Lock()
		defer l.mu.Unlock()
		delete(l.held, name)
		return nil
	}, nil
}

func TestPolicyRunsLifecycleNodes(t *testing.T) {
	h := enginetest.New(t)
	al := audit.New(h.DB, audit.WithClock(h.Clock.Now))
	res, err := resource.New(resource.Config{DB: h.DB, Audit: al, Clock: h.Clock})
	if err != nil {
		t.Fatal(err)
	}
	cat, err := catalog.New(catalog.Config{DB: h.DB, Audit: al, Resources: res, Clock: h.Clock})
	if err != nil {
		t.Fatal(err)
	}
	ad := &engineAdapter{state: rt.StateActive}
	mgr, err := catalog.NewManager(catalog.ManagerConfig{
		Catalog: cat, Runtimes: engineRuntimes{ad}, Runner: noRunner{}, Locker: &memLocker{held: map[string]bool{}},
	})
	if err != nil {
		t.Fatal(err)
	}
	lifecycle.Register(h.Registry, mgr)
	res.RegisterHooks(model.ResourceService, mgr.Hooks())

	if _, err := res.Put(h.Ctx, []byte("apiVersion: lcm/v2\nkind: Resource\nmetadata: { name: web }\nspec:\n  type: service\n  adapter: systemd\n  config: { unit: web.service }\n"), 0); err != nil {
		t.Fatal(err)
	}
	if err := cat.Transition(h.Ctx, "web", catalog.StateRegistered, catalog.StateRunning, catalog.TransitionInfo{}); err != nil {
		t.Fatal(err)
	}

	e := h.Open()
	h.Deploy(e, `apiVersion: lcm/v2
kind: Policy
metadata: { name: heal }
spec:
  nodes:
    - { id: start, type: trigger.manual, next: { out: [restart] } }
    - { id: restart, type: lifecycle.restart, props: { resource: "{{ trigger.resource }}" }, next: { out: [stop] } }
    - { id: stop, type: lifecycle.stop, props: { resource: "{{ nodes.restart.resource }}" } }
`)

	// Dry mode plans and touches nothing.
	dry, err := e.Start(context.Background(), "heal", "start", map[string]any{"resource": "web"}, engine.ModeDry)
	if err != nil {
		t.Fatal(err)
	}
	dv := h.Wait(e, dry)
	if dv.Status != engine.RunSucceeded || len(ad.calls) != 0 {
		t.Fatalf("dry run: status %s, adapter calls %v", dv.Status, ad.calls)
	}
	if out := dv.Nodes[1].Output.(map[string]any); out["planned"] != true {
		t.Fatalf("dry output = %#v", out)
	}

	id := h.Start(e, "heal", map[string]any{"resource": "web"})
	v := h.Wait(e, id)
	if v.Status != engine.RunSucceeded {
		t.Fatalf("run = %+v", v)
	}
	if !reflect.DeepEqual(ad.calls, []string{"restart", "stop"}) {
		t.Fatalf("adapter calls = %v", ad.calls)
	}
	restart := v.Nodes[1].Output.(map[string]any)
	if restart["to"] != "running" || restart["resource"] != "web" {
		t.Fatalf("restart output = %#v", restart)
	}
	if rec, _ := cat.Get(context.Background(), "web"); rec.State != catalog.StateStopped {
		t.Fatalf("state = %s", rec.State)
	}

	// The audit log attributes the actions to the run.
	entries, _ := audit.New(h.DB).List(context.Background(), 0, 0)
	var found bool
	for _, en := range entries {
		if en.Action == "service.restart" {
			found = true
			if en.Actor.ID != "run:"+id || en.Actor.Kind != auth.KindSystem || en.Details["runId"] != id {
				t.Fatalf("restart audit = %+v", en)
			}
		}
	}
	if !found {
		t.Fatal("no audit entry for the restart node")
	}

	// A retry of a completed node (same idempotency key) does not act twice.
	before := len(ad.calls)
	replay, err := mgr.Restart(h.Ctx, "web", catalog.ActionOptions{RunID: id, IdempotencyKey: id + "/restart"})
	if err != nil || !replay.Replayed || len(ad.calls) != before {
		t.Fatalf("replay = %+v %v, calls %v", replay, err, ad.calls)
	}

	// Permanent failure: a stopped service cannot be stopped again, and the
	// node fails the run without retrying.
	id2 := h.Start(e, "heal", map[string]any{"resource": "web"})
	v2 := h.Wait(e, id2)
	if v2.Status != engine.RunSucceeded {
		t.Fatalf("second run: %+v", v2) // restart brings it back, stop stops it
	}
	id3 := h.Start(e, "heal", map[string]any{"resource": "ghost"})
	v3 := h.Wait(e, id3)
	if v3.Status != engine.RunFailed || v3.Nodes[1].Attempts != 1 {
		t.Fatalf("unknown service: status %s attempts %d error %q", v3.Status, v3.Nodes[1].Attempts, v3.Nodes[1].Error)
	}
}

// noRunner fails the test run if a command is executed: the lifecycle nodes
// under test use only the runtime adapter.
type noRunner struct{}

func (noRunner) Run(context.Context, execx.Cmd) (execx.Result, error) {
	return execx.Result{}, errors.New("unexpected command")
}

func TestPolicyNodesRespectImpactGuard(t *testing.T) {
	h := enginetest.New(t)
	al := audit.New(h.DB, audit.WithClock(h.Clock.Now))
	res, err := resource.New(resource.Config{DB: h.DB, Audit: al, Clock: h.Clock})
	if err != nil {
		t.Fatal(err)
	}
	cat, err := catalog.New(catalog.Config{DB: h.DB, Audit: al, Resources: res, Clock: h.Clock})
	if err != nil {
		t.Fatal(err)
	}
	ad := &engineAdapter{state: rt.StateActive}
	mgr, err := catalog.NewManager(catalog.ManagerConfig{
		Catalog: cat, Runtimes: engineRuntimes{ad}, Runner: noRunner{}, Locker: &memLocker{held: map[string]bool{}},
	})
	if err != nil {
		t.Fatal(err)
	}
	lifecycle.Register(h.Registry, mgr)
	res.RegisterHooks(model.ResourceService, mgr.Hooks())
	for _, doc := range []string{
		"apiVersion: lcm/v2\nkind: Resource\nmetadata: { name: db }\nspec:\n  type: service\n  adapter: systemd\n  config: { unit: db.service }\n",
		"apiVersion: lcm/v2\nkind: Resource\nmetadata: { name: web }\nspec:\n  type: service\n  adapter: systemd\n  config: { unit: web.service }\n  relations: [{ type: dependsOn, target: db }]\n",
	} {
		if _, err := res.Put(h.Ctx, []byte(doc), 0); err != nil {
			t.Fatal(err)
		}
	}
	if err := cat.Transition(h.Ctx, "db", catalog.StateRegistered, catalog.StateRunning, catalog.TransitionInfo{}); err != nil {
		t.Fatal(err)
	}

	e := h.Open()
	h.Deploy(e, `apiVersion: lcm/v2
kind: Policy
metadata: { name: careless }
spec:
  nodes:
    - { id: start, type: trigger.manual, next: { out: [stop] } }
    - { id: stop, type: lifecycle.stop, props: { resource: db } }
`)
	h.Deploy(e, `apiVersion: lcm/v2
kind: Policy
metadata: { name: careful }
spec:
  nodes:
    - { id: start, type: trigger.manual, next: { out: [stop] } }
    - { id: stop, type: lifecycle.stop, props: { resource: db, acknowledgeImpact: true } }
`)

	// Without the prop the dry run and the run fail, once, and act on nothing.
	dry, err := e.Start(context.Background(), "careless", "start", nil, engine.ModeDry)
	if err != nil {
		t.Fatal(err)
	}
	if v := h.Wait(e, dry); v.Status != engine.RunFailed || !strings.Contains(v.Nodes[1].Error, "acknowledgeImpact") {
		t.Fatalf("dry run = %s %q", v.Status, v.Nodes[1].Error)
	}
	v := h.Wait(e, h.Start(e, "careless", nil))
	if v.Status != engine.RunFailed || v.Nodes[1].Attempts != 1 || !strings.Contains(v.Nodes[1].Error, "dependents [web]") || len(ad.calls) != 0 {
		t.Fatalf("run = %s attempts %d %q calls %v", v.Status, v.Nodes[1].Attempts, v.Nodes[1].Error, ad.calls)
	}

	// With it the dry run reports the impact and the run records it.
	dry, err = e.Start(context.Background(), "careful", "start", nil, engine.ModeDry)
	if err != nil {
		t.Fatal(err)
	}
	dv := h.Wait(e, dry)
	if dv.Status != engine.RunSucceeded {
		t.Fatalf("careful dry run = %+v", dv)
	}
	if imp := dv.Nodes[1].Output.(map[string]any)["impact"].(map[string]any); !reflect.DeepEqual(imp["dependents"], []any{"web"}) {
		t.Fatalf("dry impact = %#v", imp)
	}
	id := h.Start(e, "careful", nil)
	if v := h.Wait(e, id); v.Status != engine.RunSucceeded || !reflect.DeepEqual(ad.calls, []string{"stop"}) {
		t.Fatalf("careful run = %s calls %v", v.Status, ad.calls)
	}
	entries, _ := audit.New(h.DB).List(context.Background(), 0, 0)
	for _, en := range entries {
		if en.Action == "service.stop" {
			ack, _ := en.Details["acknowledgedImpact"].(map[string]any)
			if fmt.Sprint(ack["dependents"]) != "[web]" || en.Actor.ID != "run:"+id {
				t.Fatalf("stop audit = %+v", en)
			}
			return
		}
	}
	t.Fatal("no audit entry for the stop node")
}
