package engine_test

import (
	"context"
	"reflect"
	"testing"

	"github.com/ACaccel/lcm/internal/engine/enginetest"
	"github.com/ACaccel/lcm/internal/node"
)

type actProps struct {
	Service string `json:"service"`
}

func TestServiceTargets(t *testing.T) {
	h := enginetest.New(t)
	node.Register(h.Registry, node.Spec[actProps]{
		Type: "test.act", Category: "test", ServiceProp: "service",
		Run: func(context.Context, *node.RunContext, actProps) (node.Result, error) { return node.Result{}, nil },
	})
	h.AddModule(module("acts", `
  params: [{ name: target }]
  nodes:
    - { id: in, type: module.input, next: { out: [a] } }
    - { id: a, type: test.act, props: { service: "{{ params.target }}" }, next: { out: [o] } }
    - { id: o, type: module.output, props: { port: ok } }
`))
	p, err := compile(t, h, policy("p", `
  params: { svc: web }
  nodes:
    - { id: start, type: trigger.manual, next: { out: [lit, par, dyn, mod, mdyn] } }
    - { id: lit, type: test.act, props: { service: api } }
    - { id: par, type: test.act, props: { service: "{{ params.svc }}" } }
    - { id: dyn, type: test.act, props: { service: "{{ trigger.service }}" } }
    - { id: mod, type: module, props: { module: acts, params: { target: "{{ params.svc }}-2" } } }
    - { id: mdyn, type: module, props: { module: acts, params: { target: "{{ trigger.name }}" } } }
`))
	if err != nil {
		t.Fatal(err)
	}
	got := map[string]string{}
	for _, tg := range p.ServiceTargets() {
		if tg.Type != "test.act" {
			t.Errorf("target %+v has the wrong type", tg)
		}
		got[tg.Node] = tg.Service
	}
	want := map[string]string{"lit": "api", "par": "web", "dyn": "", "mod/a": "web-2", "mdyn/a": ""}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("targets = %v, want %v", got, want)
	}

	plain, err := compile(t, h, policy("q", `
  nodes:
    - { id: start, type: trigger.manual, next: { out: [x] } }
    - { id: x, type: test.echo }
`))
	if err != nil {
		t.Fatal(err)
	}
	if ts := plain.ServiceTargets(); len(ts) != 0 {
		t.Fatalf("a policy without service nodes has targets %v", ts)
	}
}
