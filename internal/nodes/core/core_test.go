package core_test

import (
	"context"
	"reflect"
	"strings"
	"testing"

	"github.com/ACaccel/lcm/internal/node"
	"github.com/ACaccel/lcm/internal/nodes/core"
)

func run(t *testing.T, r *node.Registry, typ string, props map[string]any) (node.Result, error) {
	t.Helper()
	d, ok := r.Lookup(typ)
	if !ok {
		t.Fatalf("type %s not registered", typ)
	}
	p, err := d.Decode(props)
	if err != nil {
		return node.Result{}, err
	}
	return d.Run(context.Background(), &node.RunContext{}, p)
}

func TestCoreNodes(t *testing.T) {
	r := node.NewRegistry()
	core.Register(r)

	tests := []struct {
		typ   string
		props map[string]any
		port  string
		out   any
	}{
		{"flow.if", map[string]any{"condition": true}, "true", true},
		{"flow.if", map[string]any{"condition": false}, "false", false},
		{"flow.switch", map[string]any{"cases": []any{
			map[string]any{"port": "a", "when": false},
			map[string]any{"port": "b", "when": true},
			map[string]any{"port": "c", "when": true},
		}}, "b", "b"},
		{"flow.switch", map[string]any{"cases": []any{map[string]any{"port": "a", "when": false}}}, "default", "default"},
		{"flow.merge", map[string]any{"value": int64(3)}, "", int64(3)},
		{"data.set", map[string]any{"values": map[string]any{"x": int64(1)}}, "", map[string]any{"x": int64(1)}},
		{"module.input", map[string]any{"limit": int64(3)}, "", map[string]any{"limit": int64(3)}},
		{"module.output", map[string]any{"port": "ok", "value": "v"}, "", "v"},
	}
	for _, tt := range tests {
		res, err := run(t, r, tt.typ, tt.props)
		if err != nil {
			t.Fatalf("%s: %v", tt.typ, err)
		}
		if res.Port != tt.port || !reflect.DeepEqual(res.Output, tt.out) {
			t.Fatalf("%s: got %q %#v, want %q %#v", tt.typ, res.Port, res.Output, tt.port, tt.out)
		}
	}

	d, _ := r.Lookup("flow.switch")
	p, err := d.CheckStatic(map[string]any{"cases": []any{map[string]any{"port": "up", "when": "trigger.up"}}})
	if err != nil {
		t.Fatal(err)
	}
	if ports, err := d.Ports(p); err != nil || !reflect.DeepEqual(ports, []string{"up", "default"}) {
		t.Fatalf("switch ports = %v, %v", ports, err)
	}
	if mt, ok := r.Lookup("trigger.manual"); !ok || !mt.IsTrigger() {
		t.Fatal("trigger.manual")
	}
}

func TestCoreValidation(t *testing.T) {
	r := node.NewRegistry()
	core.Register(r)
	bad := []struct {
		typ   string
		props map[string]any
		msg   string
	}{
		{"flow.switch", map[string]any{"cases": []any{}}, "at least one case"},
		{"flow.switch", map[string]any{"cases": []any{map[string]any{"port": "default", "when": true}}}, "reserved"},
		{"flow.switch", map[string]any{"cases": []any{map[string]any{"port": "a", "when": true}, map[string]any{"port": "a", "when": false}}}, "duplicate"},
		{"flow.switch", map[string]any{"cases": []any{map[string]any{"port": "A", "when": true}}}, "invalid port"},
		{"module.output", map[string]any{"port": "error"}, "other than error"},
		{"flow.if", map[string]any{}, "condition: is required"},
		{"data.set", map[string]any{}, "values: is required"},
	}
	for _, b := range bad {
		_, err := run(t, r, b.typ, b.props)
		if err == nil || !strings.Contains(err.Error(), b.msg) {
			t.Fatalf("%s %v: err = %v, want %q", b.typ, b.props, err, b.msg)
		}
	}
	if _, err := run(t, r, "module", map[string]any{"module": "m"}); !node.IsPermanent(err) {
		t.Fatalf("module run err = %v", err)
	}
}
