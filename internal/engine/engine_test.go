package engine_test

import (
	"context"
	"errors"
	"fmt"
	"reflect"
	"strings"
	"sync"
	"testing"
	"time"

	"go.uber.org/goleak"

	"github.com/ACaccel/lcm/internal/audit"
	"github.com/ACaccel/lcm/internal/auth"
	"github.com/ACaccel/lcm/internal/engine"
	"github.com/ACaccel/lcm/internal/engine/enginetest"
	"github.com/ACaccel/lcm/internal/node"
)

func TestMain(m *testing.M) {
	goleak.VerifyTestMain(m)
}

func TestDryMode(t *testing.T) {
	h := enginetest.New(t)
	e := h.Open()
	h.Deploy(e, policy("dry", `
  nodes:
    - { id: start, type: trigger.manual, next: { out: [look] } }
    - { id: look, type: test.read, props: { value: "{{ trigger.svc }}" }, next: { out: [act, act2] } }
    - { id: act, type: test.write, props: { value: "restart {{ nodes.look }}" } }
    - { id: act2, type: test.writedry, props: { value: "{{ nodes.look }}" } }
`))
	id, err := e.Start(context.Background(), "dry", "start", map[string]any{"svc": "web"}, engine.ModeDry)
	if err != nil {
		t.Fatal(err)
	}
	v := h.Wait(e, id)
	if v.Status != engine.RunSucceeded || v.Mode != engine.ModeDry {
		t.Fatalf("status = %s mode = %s", v.Status, v.Mode)
	}
	if h.Calls("look") != 1 || h.Calls("act") != 0 || h.Calls("act2") != 0 {
		t.Fatalf("calls look=%d act=%d act2=%d", h.Calls("look"), h.Calls("act"), h.Calls("act2"))
	}
	want := map[string]any{"planned": true, "props": map[string]any{"value": "restart web"}}
	if got := output(t, v, "act"); !reflect.DeepEqual(got, want) {
		t.Fatalf("act = %#v", got)
	}
	if got := output(t, v, "act2"); !reflect.DeepEqual(got, map[string]any{"dry": "web", "key": id + "/act2"}) {
		t.Fatalf("act2 = %#v", got)
	}
	stored, err := e.Run(context.Background(), id)
	if err != nil || stored.Mode != engine.ModeDry {
		t.Fatalf("stored mode = %v, %v", stored.Mode, err)
	}
}

type fakeInterceptor struct {
	mu    sync.Mutex
	calls []engine.NodeCall
}

func (f *fakeInterceptor) Intercept(_ context.Context, c engine.NodeCall) (engine.Interception, bool) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.calls = append(f.calls, c)
	switch c.NodeID {
	case "act":
		return engine.Interception{Result: node.Result{Output: "recorded " + fmt.Sprint(c.Props.(map[string]any)["value"])}}, true
	case "broken":
		return engine.Interception{Err: errors.New("recorded failure")}, true
	}
	return engine.Interception{}, false
}

func TestInterceptor(t *testing.T) {
	h := enginetest.New(t)
	fi := &fakeInterceptor{}
	e := h.Open(func(c *engine.Config) { c.Interceptor = fi })
	h.Deploy(e, policy("intercept", `
  nodes:
    - { id: start, type: trigger.manual, next: { out: [act] } }
    - { id: act, type: test.write, props: { value: "{{ trigger.x }}" }, next: { out: [broken] } }
    - { id: broken, type: test.echo, next: { error: [after] } }
    - { id: after, type: test.echo, props: { value: "{{ nodes.act }}|{{ nodes.broken.message }}" } }
`))
	v := h.Wait(e, h.Start(e, "intercept", map[string]any{"x": 7}))
	if v.Status != engine.RunSucceeded {
		t.Fatalf("status = %s (%s)", v.Status, v.Error)
	}
	if got := output(t, v, "after"); got != "recorded 7|recorded failure" {
		t.Fatalf("after = %v", got)
	}
	if h.Calls("act") != 0 || h.Calls("broken") != 0 || h.Calls("after") != 1 {
		t.Fatal("intercepted nodes executed")
	}
	fi.mu.Lock()
	defer fi.mu.Unlock()
	if len(fi.calls) != 3 || fi.calls[0].Effect != node.EffectWrite || fi.calls[0].Mode != engine.ModeLive || fi.calls[0].Attempt != 1 || fi.calls[0].Version != 1 {
		t.Fatalf("calls = %+v", fi.calls)
	}
}

type blockingObserver struct {
	release chan struct{}
	once    sync.Once
}

func (b *blockingObserver) Observe(engine.Event) { <-b.release }

func TestSlowObserverDoesNotBlock(t *testing.T) {
	h := enginetest.New(t)
	slow := &blockingObserver{release: make(chan struct{})}
	e := h.Open(func(c *engine.Config) { c.Observers = append(c.Observers, slow) })
	t.Cleanup(func() { slow.once.Do(func() { close(slow.release) }) })
	var b strings.Builder
	b.WriteString("  concurrency: { mode: parallel, limit: 10 }\n  nodes:\n    - { id: start, type: trigger.manual, next: { out: [")
	for i := range 50 {
		if i > 0 {
			b.WriteString(", ")
		}
		fmt.Fprintf(&b, "n%d", i)
	}
	b.WriteString("] } }\n")
	for i := range 50 {
		fmt.Fprintf(&b, "    - { id: n%d, type: test.echo }\n", i)
	}
	h.Deploy(e, policy("wide", b.String()))
	var ids []string
	for range 50 {
		ids = append(ids, h.Start(e, "wide", nil))
	}
	for _, id := range ids {
		if v := h.Wait(e, id); v.Status != engine.RunSucceeded {
			t.Fatalf("%s: %s", id, v.Status)
		}
	}
	if e.ObserverDrops() == 0 {
		t.Fatal("expected dropped events for the blocked observer")
	}
}

func TestDeployVersionsAndAudit(t *testing.T) {
	h := enginetest.New(t)
	e := h.Open()
	doc := policy("versions", `
  nodes:
    - { id: start, type: trigger.manual, next: { out: [a] } }
    - { id: a, type: test.echo }
`)
	if _, err := e.Deploy(context.Background(), []byte(doc)); !errors.Is(err, audit.ErrNoActor) {
		t.Fatalf("deploy without actor: %v", err)
	}
	if v := h.Deploy(e, doc); v != 1 {
		t.Fatalf("first version = %d", v)
	}
	if v := h.Deploy(e, doc); v != 1 {
		t.Fatalf("identical deploy created version %d", v)
	}
	if v := h.Deploy(e, strings.Replace(doc, "test.echo", "test.read", 1)); v != 2 {
		t.Fatalf("changed deploy = %d", v)
	}
	if p, ok := e.Plan("versions"); !ok || p.Version() != 2 || len(e.Plans()) != 1 {
		t.Fatal("deployed plan")
	}
	if _, err := e.Deploy(h.Ctx, []byte("kind: nope")); err == nil {
		t.Fatal("invalid document deployed")
	}
	if _, err := e.Deploy(h.Ctx, []byte(module("m", "  nodes:\n    - { id: in, type: module.input }\n    - { id: o, type: module.output, props: { port: ok } }\n"))); !errors.Is(err, engine.ErrNotPolicy) {
		t.Fatalf("module deploy: %v", err)
	}
	user := auth.WithActor(context.Background(), auth.Actor{Kind: auth.KindUser, ID: "1", Name: "admin"})
	id, err := e.Start(user, "versions", "start", nil, engine.ModeLive)
	if err != nil {
		t.Fatal(err)
	}
	h.Wait(e, id)
	h.Wait(e, h.Start(e, "versions", nil)) // no actor: not audited
	if err := e.Undeploy(h.Ctx, "versions"); err != nil {
		t.Fatal(err)
	}

	entries, err := audit.New(h.DB).List(context.Background(), 100, 0)
	if err != nil {
		t.Fatal(err)
	}
	var got []string
	for i := len(entries) - 1; i >= 0; i-- {
		got = append(got, entries[i].Action+" "+entries[i].Target+" "+string(entries[i].Outcome))
	}
	want := []string{
		"policy.deploy policy:versions ok", "policy.deploy policy:versions ok", "policy.deploy policy:versions ok",
		"policy.deploy policy error", "policy.deploy policy error",
		"run.start run:" + id + " ok", "policy.undeploy policy:versions ok",
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("audit =\n%v\nwant\n%v", strings.Join(got, "\n"), strings.Join(want, "\n"))
	}
	if entries[0].Actor.Name != "cli" || entries[1].Actor.Name != "admin" {
		t.Fatalf("actors: %+v %+v", entries[0].Actor, entries[1].Actor)
	}
}

func TestValidate(t *testing.T) {
	h := enginetest.New(t)
	e := h.Open()
	warnings, err := e.Validate(context.Background(), []byte(policy("v", `
  nodes:
    - { id: start, type: trigger.manual }
    - { id: lonely, type: test.echo }
`)))
	if err != nil || len(warnings) != 1 || warnings[0].Path != "spec.nodes[1]" {
		t.Fatalf("warnings = %v, err = %v", warnings, err)
	}
	if _, err := e.Validate(context.Background(), []byte(policy("v", "  nodes: []\n"))); err == nil {
		t.Fatal("empty policy validated")
	}
	if _, ok := e.Plan("v"); ok {
		t.Fatal("validate deployed the policy")
	}
}

func TestModulesAtRuntime(t *testing.T) {
	h := enginetest.New(t)
	h.AddModule(module("scale", `
  params: [{ name: factor, default: 10 }, { name: base }]
  nodes:
    - { id: in, type: module.input, next: { out: [calc] } }
    - { id: calc, type: test.echo, props: { value: "{{ params.base * params.factor }}" }, next: { out: [big] } }
    - { id: big, type: flow.if, props: { condition: "nodes.calc > 100" }, next: { "true": [hi], "false": [lo] } }
    - { id: hi, type: module.output, props: { port: high, value: "{{ nodes.calc }}" } }
    - { id: lo, type: module.output, props: { port: low, value: "{{ -nodes.calc }}" } }
`))
	e := h.Open()
	h.Deploy(e, policy("usemod", `
  nodes:
    - { id: start, type: trigger.manual, next: { out: [m] } }
    - { id: m, type: module, props: { module: scale, params: { base: "{{ trigger.n }}" } }, next: { high: [h], low: [l] } }
    - { id: h, type: test.echo, props: { value: "high {{ nodes.m }}" }, next: { out: [join] } }
    - { id: l, type: test.echo, props: { value: "low {{ nodes.m }}" }, next: { out: [join] } }
    - { id: join, type: flow.merge, props: { value: "{{ nodes.h ?? nodes.l }}" } }
`))
	v := h.Wait(e, h.Start(e, "usemod", map[string]any{"n": 20}))
	if v.Status != engine.RunSucceeded || output(t, v, "join") != "high 200" {
		t.Fatalf("status = %s join = %v", v.Status, output(t, v, "join"))
	}
	wantStatus(t, v, map[string]engine.NodeStatus{"m/in": engine.NodeCompleted, "m/lo": engine.NodeDead, "l": engine.NodeDead})
	if got := output(t, v, "m/in"); !reflect.DeepEqual(got, map[string]any{"base": int64(20), "factor": int64(10)}) {
		t.Fatalf("module params = %#v", got)
	}
	v = h.Wait(e, h.Start(e, "usemod", map[string]any{"n": 5}))
	if output(t, v, "join") != "low -50" {
		t.Fatalf("join = %v", output(t, v, "join"))
	}
}

func TestExtraVars(t *testing.T) {
	h := enginetest.New(t)
	var mu sync.Mutex
	var seen []engine.RunInfo
	e := h.Open(func(c *engine.Config) {
		c.Vars = []engine.Var{{Name: "service", Value: func(_ context.Context, info engine.RunInfo) (any, error) {
			mu.Lock()
			seen = append(seen, info)
			mu.Unlock()
			if info.NodeID == "bad" {
				return nil, errors.New("lookup failed")
			}
			return map[string]any{"name": "web"}, nil
		}}}
	})
	h.Deploy(e, policy("vars", `
  nodes:
    - { id: start, type: trigger.manual, next: { out: [a, plain] } }
    - { id: a, type: test.echo, props: { value: "{{ service.name }}" }, next: { out: [bad] } }
    - { id: plain, type: test.echo }
    - { id: bad, type: test.echo, props: { value: "{{ service }}" }, next: { error: [z] } }
    - { id: z, type: test.echo }
`))
	v := h.Wait(e, h.Start(e, "vars", nil))
	if output(t, v, "a") != "web" {
		t.Fatalf("a = %v", output(t, v, "a"))
	}
	if n, _ := v.Node("bad"); !strings.Contains(n.Error, "variable service: lookup failed") {
		t.Fatalf("bad = %+v", n)
	}
	mu.Lock()
	defer mu.Unlock()
	if len(seen) != 2 {
		t.Fatalf("var evaluated %d times, want only for nodes that use it", len(seen))
	}
	dummy := func(context.Context, engine.RunInfo) (any, error) { return nil, nil }
	for _, bad := range []engine.Var{{Name: "trigger", Value: dummy}, {Name: "1x", Value: dummy}, {Name: "ok"}} {
		if _, err := engine.New(engine.Config{DB: h.DB, Registry: h.Registry, Vars: []engine.Var{bad}}); err == nil {
			t.Fatalf("var %q accepted", bad.Name)
		}
	}
}

func TestPrune(t *testing.T) {
	h := enginetest.New(t)
	e := h.Open()
	h.Deploy(e, policy("prune", `
  nodes:
    - { id: start, type: trigger.manual, next: { out: [a] } }
    - { id: a, type: test.echo }
`))
	var ids []string
	for range 4 {
		ids = append(ids, h.Start(e, "prune", nil))
		h.Wait(e, ids[len(ids)-1])
		h.Clock.Advance(time.Hour)
	}
	// Runs are 4h, 3h, 2h and 1h old: keep the newest two and anything
	// younger than 150 minutes.
	n, err := e.Prune(context.Background(), 1, 150*time.Minute)
	if err != nil || n != 2 {
		t.Fatalf("pruned %d, %v", n, err)
	}
	for i, id := range ids {
		_, err := e.Run(context.Background(), id)
		if gone := errors.Is(err, engine.ErrRunNotFound); gone != (i < 2) {
			t.Fatalf("run %d: err = %v", i, err)
		}
	}
	if _, err := e.Prune(context.Background(), -1, 0); err == nil {
		t.Fatal("negative keep accepted")
	}
}

func TestConfigValidation(t *testing.T) {
	h := enginetest.New(t)
	for _, cfg := range []engine.Config{{}, {DB: h.DB}, {DB: h.DB, Registry: h.Registry, Workers: -1}} {
		if _, err := engine.New(cfg); err == nil {
			t.Fatalf("config %+v accepted", cfg)
		}
	}
	if engine.DefaultWorkers() < 4 {
		t.Fatal("default workers below 4")
	}
}
