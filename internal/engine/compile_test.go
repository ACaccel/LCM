package engine_test

import (
	"context"
	"errors"
	"reflect"
	"strings"
	"testing"

	"github.com/ACaccel/lcm/internal/engine"
	"github.com/ACaccel/lcm/internal/engine/enginetest"
	"github.com/ACaccel/lcm/internal/model"
)

// policy builds a Policy document; spec is the YAML body of spec, indented
// by two spaces.
func policy(name, spec string) string {
	return "apiVersion: lcm/v1\nkind: Policy\nmetadata: { name: " + name + " }\nspec:\n" + spec
}

func module(name, spec string) string {
	return "apiVersion: lcm/v1\nkind: Module\nmetadata: { name: " + name + " }\nspec:\n" + spec
}

func compile(t *testing.T, h *enginetest.Harness, doc string) (*engine.Plan, error) {
	t.Helper()
	p, err := model.DecodePolicy([]byte(doc))
	if err != nil {
		t.Fatalf("decode: %v", err)
	}
	return engine.Compile(context.Background(), p, engine.CompileOptions{Registry: h.Registry, Modules: h.Modules})
}

func wantIssue(t *testing.T, err error, path, msg string) {
	t.Helper()
	var is model.Issues
	if !errors.As(err, &is) {
		t.Fatalf("err = %v, want issues", err)
	}
	for _, i := range is {
		if i.Path == path && strings.Contains(i.Message, msg) {
			return
		}
	}
	t.Fatalf("no issue %q at %q in %v", msg, path, is)
}

func TestCompileRejects(t *testing.T) {
	h := enginetest.New(t)
	h.AddModule(module("mod", `
  params: [{ name: target }]
  nodes:
    - { id: in, type: module.input, next: { out: [o] } }
    - { id: o, type: module.output, props: { port: ok } }
`))
	tests := []struct {
		name, spec, path, msg string
	}{
		{"unknown type", `
  nodes:
    - { id: start, type: trigger.manual, next: { out: [x] } }
    - { id: x, type: nope.nope }`, "spec.nodes[1].type", "unknown node type"},
		{"unknown port", `
  nodes:
    - { id: start, type: trigger.manual, next: { out: [x] } }
    - { id: x, type: flow.if, props: { condition: "true" }, next: { maybe: [start] } }`, "spec.nodes[1].next.maybe", "unknown port"},
		{"cycle", `
  nodes:
    - { id: start, type: trigger.manual, next: { out: [a] } }
    - { id: a, type: test.echo, next: { out: [b] } }
    - { id: b, type: test.echo, next: { out: [a] } }`, "spec.nodes", "cycle among nodes a, b"},
		{"no trigger", `
  nodes:
    - { id: a, type: test.echo }`, "spec.nodes", "at least one trigger"},
		{"trigger inbound", `
  nodes:
    - { id: start, type: trigger.manual, next: { out: [other] } }
    - { id: other, type: trigger.manual }`, "spec.nodes[1]", "cannot have inbound edges"},
		{"trigger expression", `
  nodes:
    - { id: start, type: trigger.manual, props: { description: "{{ params.x }}" } }`, "spec.nodes[0].props", "must be static"},
		{"bad expression", `
  nodes:
    - { id: start, type: trigger.manual, next: { out: [x] } }
    - { id: x, type: flow.if, props: { condition: "trigger.a ==" } }`, "spec.nodes[1].props.condition", "unexpected"},
		{"unknown var", `
  nodes:
    - { id: start, type: trigger.manual, next: { out: [x] } }
    - { id: x, type: test.echo, props: { value: "{{ service.name }}" } }`, "spec.nodes[1].props.value", "unknown name service"},
		{"unknown ref", `
  nodes:
    - { id: start, type: trigger.manual, next: { out: [x] } }
    - { id: x, type: test.echo, props: { value: "{{ nodes.ghost }}" } }`, "spec.nodes[1].props", "nodes.ghost: unknown node"},
		{"ref not ancestor", `
  nodes:
    - { id: start, type: trigger.manual, next: { out: [a, b] } }
    - { id: a, type: test.echo }
    - { id: b, type: test.echo, props: { value: "{{ nodes.a }}" } }`, "spec.nodes[2].props", "not upstream"},
		{"self ref", `
  nodes:
    - { id: start, type: trigger.manual, next: { out: [a] } }
    - { id: a, type: test.echo, props: { value: "{{ nodes.a }}" } }`, "spec.nodes[1].props", "not upstream"},
		{"props unknown", `
  nodes:
    - { id: start, type: trigger.manual, next: { out: [a] } }
    - { id: a, type: test.echo, props: { valu: 1 } }`, "spec.nodes[1].props.valu", "unknown field"},
		{"props required", `
  nodes:
    - { id: start, type: trigger.manual, next: { out: [a] } }
    - { id: a, type: flow.if }`, "spec.nodes[1].props.condition", "is required"},
		{"props static type", `
  nodes:
    - { id: start, type: trigger.manual, next: { out: [a] } }
    - { id: a, type: test.fail, props: { times: many, message: "{{ trigger.m }}" } }`, "spec.nodes[1].props", "decode props"},
		{"missing module", `
  nodes:
    - { id: start, type: trigger.manual, next: { out: [m] } }
    - { id: m, type: module, props: { module: ghost } }`, "spec.nodes[1].props.module", "module not found"},
		{"module missing param", `
  nodes:
    - { id: start, type: trigger.manual, next: { out: [m] } }
    - { id: m, type: module, props: { module: mod } }`, "spec.nodes[1].props.params", "missing required parameter \"target\""},
		{"module unknown param", `
  nodes:
    - { id: start, type: trigger.manual, next: { out: [m] } }
    - { id: m, type: module, props: { module: mod, params: { target: x, extra: 1 } } }`, "spec.nodes[1].props.params.extra", "has no parameter"},
		{"module unknown port", `
  nodes:
    - { id: start, type: trigger.manual, next: { out: [m] } }
    - { id: m, type: module, props: { module: mod, params: { target: x } }, next: { failed: [start] } }`, "spec.nodes[1].next.failed", "unknown port"},
		{"module error port", `
  nodes:
    - { id: start, type: trigger.manual, next: { out: [m] } }
    - { id: m, type: module, props: { module: mod, params: { target: x } }, next: { error: [z] } }
    - { id: z, type: test.echo }`, "spec.nodes[1].next.error", "no error port"},
		{"module retry", `
  nodes:
    - { id: start, type: trigger.manual, next: { out: [m] } }
    - { id: m, type: module, timeout: 1s, props: { module: mod, params: { target: x } } }`, "spec.nodes[1]", "not supported on module nodes"},
		{"module param ref not upstream", `
  nodes:
    - { id: start, type: trigger.manual, next: { out: [m, z] } }
    - { id: z, type: test.echo }
    - { id: m, type: module, props: { module: mod, params: { target: "{{ nodes.z }}" } } }`, "spec.nodes[2]/mod:spec.nodes[0].props", "not upstream"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			_, err := compile(t, h, policy("p", tt.spec))
			wantIssue(t, err, tt.path, tt.msg)
		})
	}
}

func TestCompileModuleRules(t *testing.T) {
	h := enginetest.New(t)
	h.AddModule(module("loop-a", `
  nodes:
    - { id: in, type: module.input, next: { out: [b] } }
    - { id: b, type: module, props: { module: loop-b }, next: { ok: [o] } }
    - { id: o, type: module.output, props: { port: ok } }
`))
	h.AddModule(module("loop-b", `
  nodes:
    - { id: in, type: module.input, next: { out: [a] } }
    - { id: a, type: module, props: { module: loop-a }, next: { ok: [o] } }
    - { id: o, type: module.output, props: { port: ok } }
`))
	h.AddModule(module("trig", `
  nodes:
    - { id: in, type: module.input, next: { out: [o] } }
    - { id: t, type: trigger.manual }
    - { id: o, type: module.output, props: { port: ok } }
`))
	h.AddModule(module("dupport", `
  nodes:
    - { id: in, type: module.input, next: { out: [o1, o2] } }
    - { id: o1, type: module.output, props: { port: ok } }
    - { id: o2, type: module.output, props: { port: ok } }
`))
	h.AddModule(module("outnext", `
  nodes:
    - { id: in, type: module.input, next: { out: [o] } }
    - { id: o, type: module.output, props: { port: ok }, next: { out: [in] } }
`))
	h.AddModule(module("outerref", `
  nodes:
    - { id: in, type: module.input, next: { out: [x] } }
    - { id: x, type: test.echo, props: { value: "{{ nodes.start }}" }, next: { out: [o] } }
    - { id: o, type: module.output, props: { port: ok } }
`))
	use := func(mod string) string {
		return policy("p", `
  nodes:
    - { id: start, type: trigger.manual, next: { out: [m] } }
    - { id: m, type: module, props: { module: `+mod+` } }`)
	}
	_, err := compile(t, h, use("loop-a"))
	wantIssue(t, err, "spec.nodes[1]/loop-a:spec.nodes[1]/loop-b:spec.nodes[1].props.module", "module recursion: loop-a -> loop-b -> loop-a")
	_, err = compile(t, h, use("trig"))
	wantIssue(t, err, "spec.nodes[1]/trig:spec.nodes[1].type", "not allowed in modules")
	_, err = compile(t, h, use("dupport"))
	wantIssue(t, err, "spec.nodes[1]/dupport:spec.nodes[2].props.port", "already used")
	_, err = compile(t, h, use("outnext"))
	wantIssue(t, err, "spec.nodes[1]/outnext:spec.nodes[1].next", "no outgoing edges")
	_, err = compile(t, h, use("outerref"))
	wantIssue(t, err, "spec.nodes[1]/outerref:spec.nodes[1].props", "nodes.start: unknown node")
}

func TestCompileModuleExpansion(t *testing.T) {
	h := enginetest.New(t)
	h.AddModule(module("inner", `
  params: [{ name: n, default: 1 }]
  nodes:
    - { id: in, type: module.input, next: { out: [calc] } }
    - { id: calc, type: test.echo, props: { value: "{{ params.n * 10 }}" }, next: { out: [o] } }
    - { id: o, type: module.output, props: { port: done, value: "{{ nodes.calc }}" } }
`))
	h.AddModule(module("outer", `
  params: [{ name: target }, { name: n, default: 2 }]
  nodes:
    - { id: in, type: module.input, next: { out: [check] } }
    - { id: check, type: flow.if, props: { condition: "params.target == 'web'" }, next: { "true": [deep], "false": [no] } }
    - { id: deep, type: module, props: { module: inner, params: { n: "{{ params.n }}" } }, next: { done: [yes] } }
    - { id: yes, type: module.output, props: { port: ok, value: "{{ nodes.deep }}" } }
    - { id: no, type: module.output, props: { port: skipped } }
`))
	plan, err := compile(t, h, policy("p", `
  nodes:
    - { id: start, type: trigger.manual, next: { out: [m] } }
    - { id: m, type: module, props: { module: outer, params: { target: "{{ trigger.service }}" } }, next: { ok: [after], skipped: [after] } }
    - { id: after, type: test.echo, props: { value: "{{ nodes.m }}" } }
    - { id: orphan, type: test.echo }
`))
	if err != nil {
		t.Fatal(err)
	}
	want := []string{"start", "orphan", "m/in", "m/check", "m/no", "m/deep/in", "m/deep/calc", "m/deep/o", "m/yes", "after"}
	if got := plan.NodeIDs(); !reflect.DeepEqual(got, want) {
		t.Fatalf("ids = %v\nwant %v", got, want)
	}
	if w := plan.Warnings(); len(w) != 1 || !strings.Contains(w[0].Message, "orphan is unreachable") {
		t.Fatalf("warnings = %v", w)
	}
	if tr := plan.Triggers(); len(tr) != 1 || tr[0].ID != "start" || tr[0].Type != "trigger.manual" {
		t.Fatalf("triggers = %+v", tr)
	}
}

func TestCompileDocumentAndOptions(t *testing.T) {
	h := enginetest.New(t)
	if _, err := engine.Compile(context.Background(), &model.Policy{}, engine.CompileOptions{}); err == nil {
		t.Fatal("compile without registry accepted")
	}
	plan, err := compile(t, h, policy("p", `
  concurrency: { mode: parallel, limit: 3 }
  params: { a: 1 }
  nodes:
    - { id: start, type: trigger.manual, next: { out: [s] } }
    - { id: s, type: flow.switch, props: { cases: [{ port: x, when: "trigger.v == 1" }] }, next: { x: [e], default: [e] } }
    - { id: e, type: test.echo, props: { value: "{{ nodes.s }}" } }
`))
	if err != nil {
		t.Fatal(err)
	}
	if plan.Name() != "p" || plan.Version() != 0 || len(plan.Warnings()) != 0 {
		t.Fatalf("plan = %s v%d %v", plan.Name(), plan.Version(), plan.Warnings())
	}
	_, err = compile(t, h, policy("p", `
  nodes:
    - { id: start, type: trigger.manual, next: { out: [s] } }
    - { id: s, type: flow.switch, props: { cases: [{ port: x, when: "true" }] }, next: { y: [start] } }
`))
	wantIssue(t, err, "spec.nodes[1].next.y", "unknown port \"y\" (ports: default, error, x)")
}
