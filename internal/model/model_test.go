package model_test

import (
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/ACaccel/lcm/internal/model"
)

const validPolicy = `
apiVersion: lcm/v1
kind: Policy
metadata: { name: auto-restart, labels: { team: ops } }
spec:
  concurrency: { mode: parallel, limit: 2 }
  timeout: 30m
  onInterrupt: resume
  params: { maxRestarts: 3, ratio: 1.5 }
  nodes:
    - id: start
      type: trigger.manual
      next: { out: [check] }
    - id: check
      type: flow.if
      props: { condition: "trigger.status == 'down'" }
      next: { "true": [restart], "false": [] }
    - id: restart
      type: some.action
      props: { target: "{{ trigger.service }}", count: 2 }
      timeout: 2m
      retry: { max: 3, backoff: 2s, maxBackoff: 30s }
      joinMode: all
      next: { out: [], error: [] }
`

func TestDecodePolicy(t *testing.T) {
	p, err := model.DecodePolicy([]byte(validPolicy))
	if err != nil {
		t.Fatal(err)
	}
	if p.Metadata.Name != "auto-restart" || p.Metadata.Labels["team"] != "ops" {
		t.Fatalf("metadata = %+v", p.Metadata)
	}
	if p.Spec.Timeout.D() != 30*time.Minute || p.Spec.Concurrency.Limit != 2 {
		t.Fatalf("spec = %+v", p.Spec)
	}
	if v, ok := p.Spec.Params["maxRestarts"].(int64); !ok || v != 3 {
		t.Fatalf("maxRestarts = %#v, want int64 3", p.Spec.Params["maxRestarts"])
	}
	if v, ok := p.Spec.Params["ratio"].(float64); !ok || v != 1.5 {
		t.Fatalf("ratio = %#v", p.Spec.Params["ratio"])
	}
	r := p.Spec.Nodes[2].Retry
	if r == nil || r.Max != 3 || r.Backoff.D() != 2*time.Second || r.MaxBackoff.D() != 30*time.Second {
		t.Fatalf("retry = %+v", r)
	}
	if got := p.Spec.Nodes[2].Props["count"]; got != int64(2) {
		t.Fatalf("count = %#v", got)
	}
}

func TestEncodeRoundTrip(t *testing.T) {
	p, err := model.DecodePolicy([]byte(validPolicy))
	if err != nil {
		t.Fatal(err)
	}
	b, err := model.Encode(p)
	if err != nil {
		t.Fatal(err)
	}
	p2, err := model.DecodePolicy(b)
	if err != nil {
		t.Fatalf("decode canonical JSON: %v\n%s", err, b)
	}
	b2, err := model.Encode(p2)
	if err != nil {
		t.Fatal(err)
	}
	if string(b) != string(b2) {
		t.Fatalf("round trip differs:\n%s\n%s", b, b2)
	}
}

const validModule = `
apiVersion: lcm/v1
kind: Module
metadata: { name: restart-mod }
spec:
  params:
    - { name: target }
    - { name: limit, default: 3, description: max attempts }
    - { name: note, default: null }
  nodes:
    - { id: in, type: module.input, next: { out: [done] } }
    - { id: done, type: module.output, props: { port: ok } }
`

func TestDecodeModule(t *testing.T) {
	m, err := model.DecodeModule([]byte(validModule))
	if err != nil {
		t.Fatal(err)
	}
	ps := m.Spec.Params
	if !ps[0].Required() || ps[1].Required() || ps[1].Default != int64(3) || ps[2].Required() {
		t.Fatalf("params = %+v", ps)
	}
	b, err := model.Encode(m)
	if err != nil {
		t.Fatal(err)
	}
	m2, err := model.DecodeModule(b)
	if err != nil {
		t.Fatalf("round trip: %v\n%s", err, b)
	}
	if m2.Spec.Params[2].Required() {
		t.Fatal("explicit null default lost in round trip")
	}
}

func TestDecodeErrors(t *testing.T) {
	head := "apiVersion: lcm/v1\nkind: Policy\nmetadata: { name: p }\n"
	node := "spec:\n  nodes:\n    - { id: a, type: trigger.manual }\n"
	tests := []struct {
		name, doc, path, msg string
	}{
		{"empty", "", "", "empty document"},
		{"syntax", "a: [", "", "did not find"},
		{"multi", head + node + "---\n" + head + node, "", "single document"},
		{"api version", "apiVersion: v2\nkind: Policy\n", "apiVersion", "lcm/v1"},
		{"kind", "apiVersion: lcm/v1\nkind: Thing\n", "kind", "must be"},
		{"unknown field", head + node + "  bogus: 1\n", "", "field bogus not found"},
		{"unknown node field", head + "spec:\n  nodes:\n    - { id: a, type: t, color: red }\n", "", "field color not found"},
		{"bad duration", head + "spec:\n  timeout: soon\n  nodes:\n    - { id: a, type: t }\n", "", "invalid duration"},
		{"duration type", head + "spec:\n  timeout: [1]\n  nodes:\n    - { id: a, type: t }\n", "", "duration must be"},
		{"non-string key", head + "spec:\n  nodes:\n    - { id: a, type: t, props: { x: { true: 2 } } }\n", "spec.nodes[0].props", "string keys"},
		{"param unknown field", "apiVersion: lcm/v1\nkind: Module\nmetadata: { name: m }\nspec:\n  params: [{ name: a, req: true }]\n  nodes: []\n", "", "field req not found"},
		{"param not mapping", "apiVersion: lcm/v1\nkind: Module\nmetadata: { name: m }\nspec:\n  params: [a]\n  nodes: []\n", "", "must be a mapping"},
		{"module params default", "apiVersion: lcm/v1\nkind: Module\nmetadata: { name: m }\nspec:\n  params: [{ name: a, default: { true: 2 } }]\n  nodes: []\n", "spec.params[0].default", "string keys"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			_, err := model.Decode([]byte(tt.doc))
			assertIssue(t, err, tt.path, tt.msg)
		})
	}
}

func TestDecodeKindMismatch(t *testing.T) {
	if _, err := model.DecodeModule([]byte(validPolicy)); err == nil {
		t.Fatal("DecodeModule accepted a policy")
	}
	if _, err := model.DecodePolicy([]byte(validModule)); err == nil {
		t.Fatal("DecodePolicy accepted a module")
	}
	big := make([]byte, model.MaxDocumentSize+1)
	if _, err := model.Decode(big); err == nil {
		t.Fatal("oversized document accepted")
	}
}

func TestValidatePolicy(t *testing.T) {
	tests := []struct {
		name string
		edit func(p *model.Policy)
		path string
		msg  string
	}{
		{"api", func(p *model.Policy) { p.APIVersion = "x" }, "apiVersion", "lcm/v1"},
		{"kind", func(p *model.Policy) { p.Kind = model.KindModule }, "kind", "Policy"},
		{"name missing", func(p *model.Policy) { p.Metadata.Name = "" }, "metadata.name", "required"},
		{"name invalid", func(p *model.Policy) { p.Metadata.Name = "Bad_Name" }, "metadata.name", "lowercase"},
		{"name long", func(p *model.Policy) { p.Metadata.Name = strings.Repeat("a", 64) }, "metadata.name", "lowercase"},
		{"label key", func(p *model.Policy) { p.Metadata.Labels = map[string]string{"-x": "v"} }, "metadata.labels.-x", "invalid label key"},
		{"label value", func(p *model.Policy) { p.Metadata.Labels = map[string]string{"k": strings.Repeat("v", 256)} }, "metadata.labels.k", "exceeds"},
		{"annotation key", func(p *model.Policy) { p.Metadata.Annotations = map[string]string{"bad key": "v"} }, "metadata.annotations.bad key", "invalid annotation key"},
		{"annotations size", func(p *model.Policy) {
			p.Metadata.Annotations = map[string]string{"lcm.dev/layout": strings.Repeat("v", model.MaxAnnotationsSize)}
		}, "metadata.annotations", "exceeds"},
		{"mode", func(p *model.Policy) { p.Spec.Concurrency.Mode = "burst" }, "spec.concurrency.mode", "must be one of"},
		{"limit negative", func(p *model.Policy) { p.Spec.Concurrency.Limit = -1 }, "spec.concurrency.limit", "negative"},
		{"limit not parallel", func(p *model.Policy) {
			p.Spec.Concurrency = model.Concurrency{Mode: model.ConcurrencyQueue, Limit: 2}
		}, "spec.concurrency.limit", "only to parallel"},
		{"limit missing", func(p *model.Policy) { p.Spec.Concurrency.Limit = 0 }, "spec.concurrency.limit", "required for parallel"},
		{"timeout", func(p *model.Policy) { p.Spec.Timeout = -1 }, "spec.timeout", "negative"},
		{"onInterrupt", func(p *model.Policy) { p.Spec.OnInterrupt = "retry" }, "spec.onInterrupt", "resume or abort"},
		{"no nodes", func(p *model.Policy) { p.Spec.Nodes = nil }, "spec.nodes", "at least one"},
		{"too many nodes", func(p *model.Policy) {
			p.Spec.Nodes = make([]model.Node, model.MaxNodes+1)
		}, "spec.nodes", "at most"},
		{"id missing", func(p *model.Policy) { p.Spec.Nodes[1].ID = "" }, "spec.nodes[1].id", "required"},
		{"id invalid", func(p *model.Policy) { p.Spec.Nodes[1].ID = "Check" }, "spec.nodes[1].id", "must match"},
		{"id duplicate", func(p *model.Policy) { p.Spec.Nodes[2].ID = "check" }, "spec.nodes[2].id", "duplicate id"},
		{"type missing", func(p *model.Policy) { p.Spec.Nodes[1].Type = "" }, "spec.nodes[1].type", "required"},
		{"type invalid", func(p *model.Policy) { p.Spec.Nodes[1].Type = "Flow If" }, "spec.nodes[1].type", "invalid node type"},
		{"module input in policy", func(p *model.Policy) { p.Spec.Nodes[1].Type = model.TypeModuleInput }, "spec.nodes[1].type", "only allowed in modules"},
		{"port name", func(p *model.Policy) { p.Spec.Nodes[1].Next["Bad"] = nil }, "spec.nodes[1].next.Bad", "invalid port"},
		{"dangling", func(p *model.Policy) { p.Spec.Nodes[1].Next["true"] = []string{"nowhere"} }, "spec.nodes[1].next.true[0]", "unknown node"},
		{"self edge", func(p *model.Policy) { p.Spec.Nodes[1].Next["true"] = []string{"check"} }, "spec.nodes[1].next.true[0]", "itself"},
		{"duplicate edge", func(p *model.Policy) {
			p.Spec.Nodes[1].Next["true"] = []string{"restart", "restart"}
		}, "spec.nodes[1].next.true[1]", "duplicate edge"},
		{"node timeout", func(p *model.Policy) { p.Spec.Nodes[2].Timeout = -1 }, "spec.nodes[2].timeout", "negative"},
		{"retry max", func(p *model.Policy) { p.Spec.Nodes[2].Retry.Max = 101 }, "spec.nodes[2].retry.max", "between"},
		{"retry backoff", func(p *model.Policy) { p.Spec.Nodes[2].Retry.Backoff = -1 }, "spec.nodes[2].retry.backoff", "negative"},
		{"retry maxBackoff negative", func(p *model.Policy) { p.Spec.Nodes[2].Retry.MaxBackoff = -1 }, "spec.nodes[2].retry.maxBackoff", "negative"},
		{"retry maxBackoff small", func(p *model.Policy) {
			p.Spec.Nodes[2].Retry.MaxBackoff = model.Duration(time.Second)
		}, "spec.nodes[2].retry.maxBackoff", "less than backoff"},
		{"joinMode", func(p *model.Policy) { p.Spec.Nodes[2].JoinMode = "first" }, "spec.nodes[2].joinMode", "all or any"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			p, err := model.DecodePolicy([]byte(validPolicy))
			if err != nil {
				t.Fatal(err)
			}
			tt.edit(p)
			assertIssue(t, model.ValidatePolicy(p), tt.path, tt.msg)
		})
	}
}

func TestValidateModule(t *testing.T) {
	tests := []struct {
		name string
		edit func(m *model.Module)
		path string
		msg  string
	}{
		{"param name", func(m *model.Module) { m.Spec.Params[0].Name = "1x" }, "spec.params[0].name", "identifier"},
		{"param duplicate", func(m *model.Module) { m.Spec.Params[1].Name = "target" }, "spec.params[1].name", "duplicate parameter"},
		{"no input", func(m *model.Module) { m.Spec.Nodes[0].Type = "data.set" }, "spec.nodes", "exactly one module.input"},
		{"two inputs", func(m *model.Module) { m.Spec.Nodes[1].Type = model.TypeModuleInput }, "spec.nodes", "exactly one module.input"},
		{"no output", func(m *model.Module) { m.Spec.Nodes[1].Type = "data.set" }, "spec.nodes", "at least one module.output"},
		{"kind", func(m *model.Module) { m.Kind = model.KindPolicy }, "kind", "Module"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			m, err := model.DecodeModule([]byte(validModule))
			if err != nil {
				t.Fatal(err)
			}
			tt.edit(m)
			assertIssue(t, model.ValidateModule(m), tt.path, tt.msg)
		})
	}
}

func TestNormalize(t *testing.T) {
	type s struct {
		A int     `json:"a"`
		B float64 `json:"b"`
	}
	v, err := model.Normalize(map[string]any{"x": s{A: 1, B: 2.5}, "l": []int{1}})
	if err != nil {
		t.Fatal(err)
	}
	m := v.(map[string]any)
	x := m["x"].(map[string]any)
	if x["a"] != int64(1) || x["b"] != 2.5 || m["l"].([]any)[0] != int64(1) {
		t.Fatalf("normalized = %#v", v)
	}
	if _, err := model.Normalize(map[any]any{true: 2}); err == nil || !strings.Contains(err.Error(), "string keys") {
		t.Fatalf("err = %v", err)
	}
	if _, err := model.Normalize(func() {}); err == nil {
		t.Fatal("func accepted")
	}
	big, err := model.Normalize(1e300)
	if err != nil || big != 1e300 {
		t.Fatalf("big = %v, %v", big, err)
	}
}

func assertIssue(t *testing.T, err error, path, msg string) {
	t.Helper()
	var is model.Issues
	if !errors.As(err, &is) {
		t.Fatalf("err = %v (%T), want model.Issues", err, err)
	}
	for _, i := range is {
		if i.Path == path && strings.Contains(i.Message, msg) {
			return
		}
	}
	t.Fatalf("no issue at %q containing %q in %v", path, msg, is)
}

func TestAnnotationsRoundTrip(t *testing.T) {
	const src = `
apiVersion: lcm/v1
kind: Policy
metadata:
  name: p
  annotations:
    lcm.dev/layout: '{"a":{"x":1,"y":2}}'
spec:
  nodes:
    - { id: a, type: trigger.manual }
`
	p, err := model.DecodePolicy([]byte(src))
	if err != nil {
		t.Fatal(err)
	}
	want := `{"a":{"x":1,"y":2}}`
	if got := p.Metadata.Annotations["lcm.dev/layout"]; got != want {
		t.Fatalf("annotation = %q, want %q", got, want)
	}
	b, err := model.Encode(p)
	if err != nil {
		t.Fatal(err)
	}
	p2, err := model.DecodePolicy(b)
	if err != nil {
		t.Fatal(err)
	}
	if got := p2.Metadata.Annotations["lcm.dev/layout"]; got != want {
		t.Fatalf("annotation after round trip = %q, want %q", got, want)
	}
}
