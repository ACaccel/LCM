package node_test

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/ACaccel/lcm/internal/node"
)

type caseProps struct {
	Port string `json:"port" lcm:"required"`
	When bool   `json:"when" lcm:"required,expr" desc:"condition"`
}

// Inner is exported so that embedding it is visible to reflection.
type Inner struct {
	A string `json:"a"`
}

type testProps struct {
	Target  string         `json:"target" lcm:"required" desc:"what to act on"`
	Count   int            `json:"count" default:"2"`
	Level   string         `json:"level" enum:"low,high" default:"low"`
	Wait    node.Duration  `json:"wait" default:"5s"`
	Ratio   float64        `json:"ratio"`
	Enabled *bool          `json:"enabled"`
	Tags    []string       `json:"tags"`
	Labels  map[string]int `json:"labels"`
	Cases   []caseProps    `json:"cases"`
	Extra   any            `json:"extra"`
	Skip    string         `json:"-"`
}

func register(t *testing.T) (*node.Registry, *node.Definition) {
	t.Helper()
	r := node.NewRegistry()
	d := node.Register(r, node.Spec[testProps]{
		Type: "test.thing", Category: "test", Description: "a test node", Effect: node.EffectRead,
		Ports: []string{"out", "other"},
		PortsOf: func(p testProps) []string {
			ports := []string{"out"}
			for _, c := range p.Cases {
				ports = append(ports, c.Port)
			}
			return ports
		},
		Validate: func(p testProps) error {
			if p.Count > 10 {
				return errors.New("count too large")
			}
			return nil
		},
		Run: func(_ context.Context, rc *node.RunContext, p testProps) (node.Result, error) {
			return node.Result{Output: p.Target + "/" + rc.NodeID}, nil
		},
	})
	return r, d
}

func TestSchema(t *testing.T) {
	r, _ := register(t)
	node.Register(r, node.Spec[map[string]any]{Type: "a.free", Category: "x", Run: func(context.Context, *node.RunContext, map[string]any) (node.Result, error) {
		return node.Result{}, nil
	}})
	schemas := r.Schemas()
	if len(schemas) != 2 || schemas[0].Type != "a.free" || !schemas[0].AdditionalProps {
		t.Fatalf("schemas = %+v", schemas)
	}
	s := schemas[1]
	if s.Effect != node.EffectRead || !s.DynamicPorts || !reflect.DeepEqual(s.Ports, []string{"out", "other"}) {
		t.Fatalf("schema = %+v", s)
	}
	b, err := json.Marshal(s)
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{
		`{"name":"target","type":"string","required":true,"description":"what to act on"}`,
		`{"name":"count","type":"integer","default":2}`,
		`{"name":"level","type":"string","enum":["low","high"],"default":"low"}`,
		`{"name":"wait","type":"duration","default":"5s"}`,
		`{"name":"ratio","type":"number"}`,
		`{"name":"enabled","type":"boolean"}`,
		`{"name":"tags","type":"array","items":{"name":"","type":"string"}}`,
		`{"name":"labels","type":"object","items":{"name":"","type":"integer"}}`,
		`"props":[{"name":"port","type":"string","required":true},{"name":"when","type":"boolean","required":true,"expr":true,"description":"condition"}]`,
		`{"name":"extra","type":"any"}`,
	} {
		if !strings.Contains(string(b), want) {
			t.Errorf("schema JSON lacks %s\n%s", want, b)
		}
	}
	if strings.Contains(string(b), "Skip") {
		t.Fatal("skipped fields in schema")
	}
}

func TestRegisterPanics(t *testing.T) {
	run := func(context.Context, *node.RunContext, struct{}) (node.Result, error) { return node.Result{}, nil }
	type noTag struct{ A string }
	type badEnum struct {
		A int `json:"a" enum:"1,2"`
	}
	type badDefault struct {
		A int `json:"a" default:"x"`
	}
	type reqDefault struct {
		A string `json:"a" lcm:"required" default:"x"`
	}
	type enumDefault struct {
		A string `json:"a" enum:"x,y" default:"z"`
	}
	type stdDur struct {
		A time.Duration `json:"a"`
	}
	type intProp struct {
		N int `json:"n"`
	}
	type badOpt struct {
		A string `json:"a" lcm:"weird"`
	}
	type embedded struct{ Inner }
	type chanField struct {
		C chan int `json:"c"`
	}
	tests := map[string]func(r *node.Registry){
		"bad type":    func(r *node.Registry) { node.Register(r, node.Spec[struct{}]{Type: "Bad", Category: "x", Run: run}) },
		"no category": func(r *node.Registry) { node.Register(r, node.Spec[struct{}]{Type: "a.b", Run: run}) },
		"no run":      func(r *node.Registry) { node.Register(r, node.Spec[struct{}]{Type: "a.b", Category: "x"}) },
		"bad effect": func(r *node.Registry) {
			node.Register(r, node.Spec[struct{}]{Type: "a.b", Category: "x", Effect: "big", Run: run})
		},
		"error port": func(r *node.Registry) {
			node.Register(r, node.Spec[struct{}]{Type: "a.b", Category: "x", Ports: []string{"error"}, Run: run})
		},
		"dup port": func(r *node.Registry) {
			node.Register(r, node.Spec[struct{}]{Type: "a.b", Category: "x", Ports: []string{"a", "a"}, Run: run})
		},
		"bad port": func(r *node.Registry) {
			node.Register(r, node.Spec[struct{}]{Type: "a.b", Category: "x", Ports: []string{"A"}, Run: run})
		},
		"dry non-write": func(r *node.Registry) {
			node.Register(r, node.Spec[struct{}]{Type: "a.b", Category: "x", Run: run, DryRun: run})
		},
		"props kind": func(r *node.Registry) { node.Register(r, node.Spec[int]{Type: "a.b", Category: "x", Run: nil}) },
		"no json tag": func(r *node.Registry) {
			node.Register(r, node.Spec[noTag]{Type: "a.b", Category: "x", Run: runOf[noTag]()})
		},
		"enum int": func(r *node.Registry) {
			node.Register(r, node.Spec[badEnum]{Type: "a.b", Category: "x", Run: runOf[badEnum]()})
		},
		"bad default": func(r *node.Registry) {
			node.Register(r, node.Spec[badDefault]{Type: "a.b", Category: "x", Run: runOf[badDefault]()})
		},
		"req default": func(r *node.Registry) {
			node.Register(r, node.Spec[reqDefault]{Type: "a.b", Category: "x", Run: runOf[reqDefault]()})
		},
		"enum default": func(r *node.Registry) {
			node.Register(r, node.Spec[enumDefault]{Type: "a.b", Category: "x", Run: runOf[enumDefault]()})
		},
		"std duration": func(r *node.Registry) {
			node.Register(r, node.Spec[stdDur]{Type: "a.b", Category: "x", Run: runOf[stdDur]()})
		},
		"bad option": func(r *node.Registry) {
			node.Register(r, node.Spec[badOpt]{Type: "a.b", Category: "x", Run: runOf[badOpt]()})
		},
		"embedded": func(r *node.Registry) {
			node.Register(r, node.Spec[embedded]{Type: "a.b", Category: "x", Run: runOf[embedded]()})
		},
		"chan": func(r *node.Registry) {
			node.Register(r, node.Spec[chanField]{Type: "a.b", Category: "x", Run: runOf[chanField]()})
		},
		"service prop missing": func(r *node.Registry) {
			node.Register(r, node.Spec[struct{}]{Type: "a.b", Category: "x", Run: run, ServiceProp: "service"})
		},
		"service prop not a string": func(r *node.Registry) {
			node.Register(r, node.Spec[intProp]{Type: "a.b", Category: "x", Run: runOf[intProp](), ServiceProp: "n"})
		},
		"duplicate": func(r *node.Registry) {
			node.Register(r, node.Spec[struct{}]{Type: "a.b", Category: "x", Run: run})
			node.Register(r, node.Spec[struct{}]{Type: "a.b", Category: "x", Run: run})
		},
	}
	for name, fn := range tests {
		t.Run(name, func(t *testing.T) {
			defer func() {
				if recover() == nil {
					t.Fatal("no panic")
				}
			}()
			fn(node.NewRegistry())
		})
	}
	// Trigger types need no Run.
	node.Register(node.NewRegistry(), node.Spec[struct{}]{Type: "trigger.x", Category: node.CategoryTrigger})
}

func runOf[P any]() func(context.Context, *node.RunContext, P) (node.Result, error) {
	return func(context.Context, *node.RunContext, P) (node.Result, error) { return node.Result{}, nil }
}

func TestDecode(t *testing.T) {
	_, d := register(t)
	p, err := d.Decode(map[string]any{
		"target": "web", "tags": []any{"a"}, "labels": map[string]any{"x": int64(1)},
		"cases": []any{map[string]any{"port": "p1", "when": true}},
	})
	if err != nil {
		t.Fatal(err)
	}
	tp := p.(testProps)
	if tp.Target != "web" || tp.Count != 2 || tp.Level != "low" || tp.Wait.D() != 5*time.Second || !tp.Cases[0].When {
		t.Fatalf("props = %+v", tp)
	}
	ports, err := d.Ports(p)
	if err != nil || !reflect.DeepEqual(ports, []string{"out", "p1"}) {
		t.Fatalf("ports = %v, %v", ports, err)
	}
	res, err := d.Run(context.Background(), &node.RunContext{NodeID: "n"}, p)
	if err != nil || res.Output != "web/n" {
		t.Fatalf("run = %+v, %v", res, err)
	}

	errs := []struct {
		raw  map[string]any
		path string
		msg  string
	}{
		{map[string]any{}, "target", "is required"},
		{map[string]any{"target": "x", "bogus": 1}, "bogus", "unknown field"},
		{map[string]any{"target": "x", "level": "mid"}, "level", "must be one of"},
		{map[string]any{"target": "x", "cases": []any{map[string]any{"when": true}}}, "cases[0].port", "is required"},
		{map[string]any{"target": "x", "cases": []any{map[string]any{"port": "a", "when": true, "x": 1}}}, "cases[0].x", "unknown field"},
		{map[string]any{"target": "x", "count": "many"}, "", "decode props"},
		{map[string]any{"target": "x", "count": int64(11)}, "", "count too large"},
		{map[string]any{"target": "x", "wait": "soon"}, "", "invalid duration"},
	}
	for _, e := range errs {
		_, err := d.Decode(e.raw)
		assertIssue(t, err, e.path, e.msg)
	}
}

func TestCheckStaticAndLeaves(t *testing.T) {
	_, d := register(t)
	raw := map[string]any{
		"target": "{{ trigger.service }}",
		"count":  "{{ params.n }}",
		"tags":   []any{"a", "{{ nodes.x }}"},
		"extra":  map[string]any{"deep": []any{"{{ run.id }}"}},
		"cases": []any{
			map[string]any{"port": "up", "when": "trigger.status == 'up'"},
			map[string]any{"port": "down", "when": "trigger.status == 'down'"},
		},
	}
	p, err := d.CheckStatic(raw)
	if err != nil {
		t.Fatal(err)
	}
	ports, err := d.Ports(p)
	if err != nil || !reflect.DeepEqual(ports, []string{"out", "up", "down"}) {
		t.Fatalf("ports = %v, %v", ports, err)
	}
	var got []string
	for _, l := range d.Leaves(raw) {
		got = append(got, fmt.Sprintf("%s=%s/%v", node.PathString(l.Path), l.Source, l.Bare))
	}
	want := []string{
		"cases[0].when=trigger.status == 'up'/true",
		"cases[1].when=trigger.status == 'down'/true",
		"count={{ params.n }}/false",
		"extra.deep[0]={{ run.id }}/false",
		"tags[1]={{ nodes.x }}/false",
		"target={{ trigger.service }}/false",
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("leaves =\n%v\nwant\n%v", got, want)
	}

	bad := []struct {
		raw  map[string]any
		path string
		msg  string
	}{
		{map[string]any{"count": "{{ x }}"}, "target", "is required"},
		{map[string]any{"target": "x", "cases": []any{map[string]any{"port": "a", "when": true}}}, "cases[0].when", "must be an expression string"},
		{map[string]any{"target": "x", "level": "mid"}, "level", "must be one of"},
		{map[string]any{"target": "x", "ratio": "high"}, "", "decode props"},
		{map[string]any{"target": "x", "nope": "{{ 1 }}"}, "nope", "unknown field"},
	}
	for _, b := range bad {
		_, err := d.CheckStatic(b.raw)
		assertIssue(t, err, b.path, b.msg)
	}
}

func TestFreeFormProps(t *testing.T) {
	r := node.NewRegistry()
	d := node.Register(r, node.Spec[map[string]any]{Type: "a.free", Category: "x", Run: runOf[map[string]any]()})
	raw := map[string]any{"a": "{{ trigger }}", "b": int64(1)}
	if _, err := d.CheckStatic(raw); err != nil {
		t.Fatal(err)
	}
	if len(d.Leaves(raw)) != 1 {
		t.Fatal("leaves")
	}
	p, err := d.Decode(map[string]any{"a": "x"})
	if err != nil || p.(map[string]any)["a"] != "x" {
		t.Fatalf("decode = %v, %v", p, err)
	}
}

func TestPermanent(t *testing.T) {
	base := errors.New("boom")
	err := fmt.Errorf("wrapped: %w", node.Permanent(base))
	if !node.IsPermanent(err) || !errors.Is(err, base) {
		t.Fatal("permanent not detected")
	}
	if node.IsPermanent(base) || node.Permanent(nil) != nil {
		t.Fatal("plain error is permanent")
	}
	if !node.ModeDry.Valid() || node.Mode("x").Valid() || !node.EffectWrite.Valid() {
		t.Fatal("valid")
	}
}

func TestUnexecutable(t *testing.T) {
	r := node.NewRegistry()
	d := node.Register(r, node.Spec[struct{}]{Type: "trigger.x", Category: node.CategoryTrigger})
	if _, err := d.Run(context.Background(), &node.RunContext{}, struct{}{}); !node.IsPermanent(err) {
		t.Fatalf("err = %v", err)
	}
	if _, err := d.DryRun(context.Background(), &node.RunContext{}, struct{}{}); err == nil || d.HasDryRun() || !d.IsTrigger() {
		t.Fatal("dry run")
	}
	if got, ok := r.Lookup("trigger.x"); !ok || got != d || d.Type() != "trigger.x" || d.Category() != "trigger" || d.Effect() != node.EffectNone {
		t.Fatal("lookup")
	}
}

func assertIssue(t *testing.T, err error, path, msg string) {
	t.Helper()
	if err == nil {
		t.Fatalf("no error, want %s: %s", path, msg)
	}
	s := err.Error()
	want := msg
	if path != "" {
		want = path + ": " + msg
	}
	if !strings.Contains(s, want) {
		t.Fatalf("err = %q, want %q", s, want)
	}
}
