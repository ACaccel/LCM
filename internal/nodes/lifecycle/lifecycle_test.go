package lifecycle_test

import (
	"context"
	"errors"
	"fmt"
	"reflect"
	"sync"
	"testing"
	"time"

	"github.com/ACaccel/lcm/internal/audit"
	"github.com/ACaccel/lcm/internal/auth"
	"github.com/ACaccel/lcm/internal/catalog"
	"github.com/ACaccel/lcm/internal/engine"
	"github.com/ACaccel/lcm/internal/engine/enginetest"
	"github.com/ACaccel/lcm/internal/execx"
	"github.com/ACaccel/lcm/internal/model"
	"github.com/ACaccel/lcm/internal/node"
	"github.com/ACaccel/lcm/internal/nodes/lifecycle"
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
	r.Service, r.Action = name, action
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
	p.Service, p.Action = n, action
	return p, nil
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
	return &node.RunContext{RunID: "01RUN", Policy: "self-heal", NodeID: "act", IdempotencyKey: "01RUN/act", Mode: node.ModeLive}
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
		{"lifecycle.restart", map[string]any{"service": ""}},
		{"lifecycle.restart", map[string]any{"service": "Bad Name"}},
		{"lifecycle.restart", map[string]any{"service": "-x"}},
		{"lifecycle.restart", map[string]any{"service": "a", "extra": 1}},
		{"lifecycle.run-job", map[string]any{"service": "a"}},
		{"lifecycle.deploy", map[string]any{"service": "a", "version": 5}},
	}
	for _, tt := range bad {
		if _, _, err := decode(t, r, tt.typ, tt.props); err == nil {
			t.Errorf("%s %v: accepted", tt.typ, tt.props)
		}
	}
	if _, _, err := decode(t, r, "lifecycle.deploy", map[string]any{"service": "card-together"}); err != nil {
		t.Fatalf("version must be optional: %v", err)
	}
}

func TestRunPassesIdentityAndShapesOutput(t *testing.T) {
	f := &fakeActions{result: catalog.ActionResult{
		From: catalog.StateRunning, To: catalog.StateRunning, Version: "abc", PreviousVersion: "def",
		DeploymentID: 12, DurationMs: 340,
	}}
	r := registry(f)

	d, p, err := decode(t, r, "lifecycle.deploy", map[string]any{"service": "web", "version": "abc1234"})
	if err != nil {
		t.Fatal(err)
	}
	res, err := d.Run(context.Background(), rc(), p)
	if err != nil {
		t.Fatal(err)
	}
	want := map[string]any{
		"service": "web", "action": "deploy", "from": "running", "to": "running", "version": "abc",
		"previousVersion": "def", "deploymentId": int64(12), "durationMs": int64(340), "replayed": false,
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
		d, p, err := decode(t, r, typ, map[string]any{"service": "web"})
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
	d, p, _ = decode(t, r, "lifecycle.run-job", map[string]any{"service": "web", "job": "reindex"})
	f.result.Output = "done"
	res, err = d.Run(context.Background(), rc(), p)
	if err != nil || f.opts[len(f.opts)-1].Job != "reindex" || res.Output.(map[string]any)["output"] != "done" {
		t.Fatalf("run-job: %+v %v %+v", res, err, f.opts[len(f.opts)-1])
	}
}

func TestErrorClassification(t *testing.T) {
	f := &fakeActions{}
	r := registry(f)
	d, p, _ := decode(t, r, "lifecycle.restart", map[string]any{"service": "web"})

	tests := []struct {
		name      string
		err       error
		permanent bool
	}{
		{"busy", fmt.Errorf("lock: %w", catalog.ErrLockHeld), false},
		{"action failed", errors.New("systemctl exploded"), false},
		{"deploy failed", &catalog.StepError{Step: "build", Err: errors.New("exit 1")}, false},
		{"state forbids", &catalog.StateError{Service: "web", Action: "restart", State: catalog.StateRetired}, true},
		{"no such service", fmt.Errorf("%w: web", catalog.ErrNotFound), true},
		{"precondition", catalog.ErrDirtyRepo, true},
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
	f := &fakeActions{plan: catalog.Plan{From: catalog.StateRunning, To: catalog.StateRunning, Steps: []string{"lock service:web", "restart systemd unit web.service"}}}
	r := registry(f)
	d, p, _ := decode(t, r, "lifecycle.restart", map[string]any{"service": "web"})
	res, err := d.DryRun(context.Background(), rc(), p)
	if err != nil {
		t.Fatal(err)
	}
	out := res.Output.(map[string]any)
	if out["planned"] != true || out["to"] != "running" || !reflect.DeepEqual(out["steps"], []any{"lock service:web", "restart systemd unit web.service"}) {
		t.Fatalf("output = %#v", out)
	}
	if len(f.calls) != 1 || f.calls[0] != "plan restart web" {
		t.Fatalf("a dry run must only plan: %v", f.calls)
	}

	f.err = &catalog.StateError{Service: "web", Action: "restart", State: catalog.StateRetired}
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

func (a *engineAdapter) Status(context.Context, *model.Service) (rt.Status, error) {
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
func (a *engineAdapter) Start(context.Context, *model.Service) error {
	return a.act("start", rt.StateActive)
}
func (a *engineAdapter) Stop(context.Context, *model.Service) error {
	return a.act("stop", rt.StateInactive)
}
func (a *engineAdapter) Restart(context.Context, *model.Service) error {
	return a.act("restart", rt.StateActive)
}

type engineRuntimes struct{ a *engineAdapter }

func (r engineRuntimes) For(*model.Service) (rt.Adapter, error) { return r.a, nil }

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
	cat, err := catalog.New(catalog.Config{DB: h.DB, Audit: audit.New(h.DB, audit.WithClock(h.Clock.Now)), Clock: h.Clock})
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

	if _, err := cat.Put(h.Ctx, []byte("apiVersion: lcm/v1\nkind: Service\nmetadata: { name: web }\nspec:\n  runtime: { type: systemd, unit: web.service }\n"), 0); err != nil {
		t.Fatal(err)
	}
	if err := cat.Transition(h.Ctx, "web", catalog.StateRegistered, catalog.StateRunning, catalog.TransitionInfo{}); err != nil {
		t.Fatal(err)
	}

	e := h.Open()
	h.Deploy(e, `apiVersion: lcm/v1
kind: Policy
metadata: { name: heal }
spec:
  nodes:
    - { id: start, type: trigger.manual, next: { out: [restart] } }
    - { id: restart, type: lifecycle.restart, props: { service: "{{ trigger.service }}" }, next: { out: [stop] } }
    - { id: stop, type: lifecycle.stop, props: { service: "{{ nodes.restart.service }}" } }
`)

	// Dry mode plans and touches nothing.
	dry, err := e.Start(context.Background(), "heal", "start", map[string]any{"service": "web"}, engine.ModeDry)
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

	id := h.Start(e, "heal", map[string]any{"service": "web"})
	v := h.Wait(e, id)
	if v.Status != engine.RunSucceeded {
		t.Fatalf("run = %+v", v)
	}
	if !reflect.DeepEqual(ad.calls, []string{"restart", "stop"}) {
		t.Fatalf("adapter calls = %v", ad.calls)
	}
	restart := v.Nodes[1].Output.(map[string]any)
	if restart["to"] != "running" || restart["service"] != "web" {
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
	res, err := mgr.Restart(h.Ctx, "web", catalog.ActionOptions{RunID: id, IdempotencyKey: id + "/restart"})
	if err != nil || !res.Replayed || len(ad.calls) != before {
		t.Fatalf("replay = %+v %v, calls %v", res, err, ad.calls)
	}

	// Permanent failure: a stopped service cannot be stopped again, and the
	// node fails the run without retrying.
	id2 := h.Start(e, "heal", map[string]any{"service": "web"})
	v2 := h.Wait(e, id2)
	if v2.Status != engine.RunSucceeded {
		t.Fatalf("second run: %+v", v2) // restart brings it back, stop stops it
	}
	id3 := h.Start(e, "heal", map[string]any{"service": "ghost"})
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
