package expr_test

import (
	"reflect"
	"strings"
	"testing"

	"github.com/ACaccel/lcm/internal/expr"
)

func env() expr.Env {
	return expr.NewEnv(expr.Base{
		Trigger: map[string]any{"status": "down", "service": "web", "n": int64(2)},
		Nodes:   map[string]any{"check": map[string]any{"ok": true, "count": int64(3)}},
		Params:  map[string]any{"maxRestarts": int64(3)},
		Run:     expr.RunVars{ID: "r1", Policy: "p", Attempt: 2},
	}, map[string]any{"service": map[string]any{"name": "web"}})
}

func TestCompileAndEval(t *testing.T) {
	tests := []struct {
		src  string
		want any
	}{
		{"trigger.status == 'down'", true},
		{"nodes.check.count + params.maxRestarts", 6},
		{"run.attempt", 2},
		{"run.id + '/' + run.policy", "r1/p"},
		{"nodes['check'].ok", true},
		{"nodes?.check?.missing ?? 'dflt'", "dflt"},
		{"upper(trigger.service)", "WEB"},
		{"service.name", "web"},
		{"let x = 2; x * 3", 6},
	}
	for _, tt := range tests {
		p, err := expr.Compile(tt.src, []string{"service"})
		if err != nil {
			t.Fatalf("%s: %v", tt.src, err)
		}
		got, err := p.Eval(env())
		if err != nil {
			t.Fatalf("%s: %v", tt.src, err)
		}
		if !reflect.DeepEqual(got, tt.want) {
			t.Fatalf("%s = %#v, want %#v", tt.src, got, tt.want)
		}
	}
}

func TestReferences(t *testing.T) {
	p, err := expr.Compile("nodes.a.x + nodes['b'].y > params.z && nodes.a?.w == trigger", nil)
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(p.Refs(), []string{"a", "b"}) {
		t.Fatalf("refs = %v", p.Refs())
	}
	if !reflect.DeepEqual(p.Vars(), []string{"nodes", "params", "trigger"}) {
		t.Fatalf("vars = %v", p.Vars())
	}
	tpl, err := expr.ParseTemplate("{{ nodes.a }}-{{ service.id }}-{{ nodes.c }}", []string{"service"})
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(tpl.Refs(), []string{"a", "c"}) || !reflect.DeepEqual(tpl.Vars(), []string{"nodes", "service"}) {
		t.Fatalf("template refs = %v vars = %v", tpl.Refs(), tpl.Vars())
	}
}

func TestCompileErrors(t *testing.T) {
	tests := []struct{ src, msg string }{
		{"", "empty expression"},
		{"unknown.x", "unknown name unknown"},
		{"nodes[trigger.id]", "literal node id"},
		{"keys(nodes)", "literal node id"},
		{"$env.nodes.a", "$env is not allowed"},
		{"now()", "now"},
		{"let params = 1; params", "shadows"},
		{"1 +", "unexpected"},
	}
	for _, tt := range tests {
		_, err := expr.Compile(tt.src, nil)
		if err == nil || !strings.Contains(err.Error(), tt.msg) {
			t.Fatalf("%q: err = %v, want %q", tt.src, err, tt.msg)
		}
		if err != nil && strings.Contains(err.Error(), "\n") {
			t.Fatalf("%q: multi-line error %q", tt.src, err)
		}
	}
}

func TestTemplate(t *testing.T) {
	tests := []struct {
		src    string
		native bool
		want   any
	}{
		{"{{ trigger.n }}", true, int64(2)},
		{"{{ nodes.check }}", true, map[string]any{"ok": true, "count": int64(3)}},
		{"n={{ trigger.n }}", false, "n=2"},
		{"{{ trigger.service }}/{{ run.attempt }}", false, "web/2"},
		{" {{ trigger.n }}", false, " 2"},
		{"{{ nodes.check }}!", false, `{"count":3,"ok":true}!`},
		{"{{ nil }}x", false, "x"},
		{"{{ 1.5 }} {{ true }}", false, "1.5 true"},
		{"{{ {'a': {'b': 1}}.a.b }}", true, 1},
		{"{{ '}}' }}", true, "}}"},
		{"{{ \"a\\\"}}\" }}", true, "a\"}}"},
		{"plain }} text", false, "plain }} text"},
	}
	for _, tt := range tests {
		tpl, err := expr.ParseTemplate(tt.src, nil)
		if err != nil {
			t.Fatalf("%q: %v", tt.src, err)
		}
		if tpl.Native() != tt.native {
			t.Fatalf("%q: native = %v", tt.src, tpl.Native())
		}
		got, err := tpl.Eval(env())
		if err != nil {
			t.Fatalf("%q: %v", tt.src, err)
		}
		if !reflect.DeepEqual(got, tt.want) {
			t.Fatalf("%q = %#v, want %#v", tt.src, got, tt.want)
		}
	}
	if !expr.HasTemplate("a {{ b }}") || expr.HasTemplate("a } b") {
		t.Fatal("HasTemplate")
	}
}

func TestTemplateErrors(t *testing.T) {
	for _, src := range []string{"{{ trigger", "{{ 'abc }}", "{{ } }}", "{{ }}", "{{ bogus }}"} {
		if _, err := expr.ParseTemplate(src, nil); err == nil {
			t.Fatalf("%q: no error", src)
		}
	}
}

func TestEvalErrors(t *testing.T) {
	p, err := expr.Compile("nodes.check.count.x", nil)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := p.Eval(env()); err == nil || !strings.Contains(err.Error(), "evaluate") {
		t.Fatalf("err = %v", err)
	}
	big, err := expr.Compile("repeat('x', 999999) + repeat('y', 999999)", nil)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := big.Eval(env()); err == nil || !strings.Contains(err.Error(), "memory budget") {
		t.Fatalf("memory budget not enforced: %v", err)
	}
	tpl, err := expr.ParseTemplate("x{{ nodes.check.count.y }}", nil)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := tpl.Eval(env()); err == nil {
		t.Fatal("template eval error not reported")
	}
}

func TestStringify(t *testing.T) {
	if s, _ := expr.Stringify(int64(5)); s != "5" {
		t.Fatal(s)
	}
	if s, _ := expr.Stringify(3); s != "3" {
		t.Fatal(s)
	}
	if s, _ := expr.Stringify([]any{"a"}); s != `["a"]` {
		t.Fatal(s)
	}
	if _, err := expr.Stringify(func() {}); err == nil {
		t.Fatal("func stringified")
	}
}
