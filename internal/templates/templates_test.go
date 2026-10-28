package templates_test

import (
	"errors"
	"slices"
	"strings"
	"testing"

	"github.com/ACaccel/lcm/internal/model"
	"github.com/ACaccel/lcm/internal/templates"
)

// minimal returns the parameters each template cannot do without.
func minimal(id string) map[string]any {
	switch id {
	case "auto-deploy":
		return map[string]any{"service": "web", "repo": "git@host:me/web.git", "requireCI": false}
	default:
		return map[string]any{"service": "web"}
	}
}

func TestCatalogIsCompleteAndSorted(t *testing.T) {
	var ids []string
	for _, tpl := range templates.List() {
		ids = append(ids, tpl.ID)
		if tpl.Title == "" || tpl.Description == "" || len(tpl.Params) == 0 {
			t.Errorf("%s: incomplete descriptor", tpl.ID)
		}
		if !strings.Contains(tpl.Document(), "kind: Policy") {
			t.Errorf("%s: document is not a policy", tpl.ID)
		}
	}
	want := []string{"auto-deploy", "post-deploy-verify", "restart-on-down", "scheduled-backup"}
	if !slices.Equal(ids, want) {
		t.Fatalf("templates = %v, want %v", ids, want)
	}
	if _, ok := templates.Get("nope"); ok {
		t.Fatal("unknown template found")
	}
}

func TestRenderProducesADecodablePolicy(t *testing.T) {
	for _, tpl := range templates.List() {
		t.Run(tpl.ID, func(t *testing.T) {
			out, err := tpl.Render("web-"+tpl.ID, minimal(tpl.ID))
			if err != nil {
				t.Fatal(err)
			}
			pol, err := model.DecodePolicy(out)
			if err != nil {
				t.Fatalf("%v\n%s", err, out)
			}
			if pol.Metadata.Name != "web-"+tpl.ID || pol.Metadata.Labels["service"] != "web" || pol.Metadata.Annotations[templates.Annotation] != tpl.ID {
				t.Errorf("metadata = %+v", pol.Metadata)
			}
			if pol.Spec.Params["service"] != "web" {
				t.Errorf("params = %v", pol.Spec.Params)
			}
			for _, n := range pol.Spec.Nodes {
				if strings.HasPrefix(n.Type, "trigger.") && strings.Contains(string(out), "trigger.") {
					for k, v := range n.Props {
						if s, ok := v.(string); ok && strings.Contains(s, "{{") {
							t.Errorf("trigger prop %s still holds %q", k, s)
						}
					}
				}
			}
		})
	}
}

func TestRenderSubstitutesTriggerPropsAndKeepsOthersAsParams(t *testing.T) {
	tpl, _ := templates.Get("scheduled-backup")
	out, err := tpl.Render("nightly", map[string]any{
		"service": "db", "schedule": "30 2 * * *", "timezone": "UTC", "heartbeatCheck": "dump", "notifySecret": "discord-ops",
	})
	if err != nil {
		t.Fatal(err)
	}
	pol, err := model.DecodePolicy(out)
	if err != nil {
		t.Fatal(err)
	}
	tick := pol.Spec.Nodes[0]
	if tick.Props["schedule"] != "30 2 * * *" || tick.Props["timezone"] != "UTC" || tick.Props["catchUp"] != "once" {
		t.Fatalf("trigger props = %v", tick.Props)
	}
	// Schedule and timezone live only in the trigger; the rest stays editable.
	if _, ok := pol.Spec.Params["schedule"]; ok {
		t.Errorf("trigger-only parameter kept in spec.params: %v", pol.Spec.Params)
	}
	if pol.Spec.Params["heartbeatCheck"] != "dump" || pol.Spec.Params["notifySecret"] != "discord-ops" || pol.Spec.Params["heartbeatTimeout"] != "30m" {
		t.Errorf("params = %v", pol.Spec.Params)
	}
	// A parameter used by both a trigger and other nodes appears in both.
	dep, _ := templates.Get("restart-on-down")
	out, _ = dep.Render("r", map[string]any{"service": "web"})
	pol, _ = model.DecodePolicy(out)
	if got := pol.Spec.Nodes[0].Props["services"]; !slices.Equal(toStrings(got), []string{"web"}) {
		t.Errorf("trigger services = %v", got)
	}
	if pol.Spec.Params["service"] != "web" {
		t.Errorf("service missing from spec.params: %v", pol.Spec.Params)
	}
}

func toStrings(v any) []string {
	var out []string
	for _, e := range v.([]any) {
		out = append(out, e.(string))
	}
	return out
}

func TestRenderRejectsBadParameters(t *testing.T) {
	tpl, _ := templates.Get("auto-deploy")
	tests := []struct {
		name   string
		values map[string]any
		path   string
	}{
		{"required missing", map[string]any{"service": "web"}, "params.repo"},
		{"service missing", map[string]any{"repo": "x"}, "params.service"},
		{"unknown", map[string]any{"service": "web", "repo": "x", "nope": 1}, "params.nope"},
		{"bad service name", map[string]any{"service": "Web App", "repo": "x"}, "params.service"},
		{"wrong type", map[string]any{"service": "web", "repo": "x", "requireCI": "yes"}, "params.requireCI"},
		{"bad duration", map[string]any{"service": "web", "repo": "x", "ciRepo": "a/b", "healthTimeout": "soon"}, "params.healthTimeout"},
		{"negative duration", map[string]any{"service": "web", "repo": "x", "ciRepo": "a/b", "healthTimeout": "-5m"}, "params.healthTimeout"},
		{"bad secret name", map[string]any{"service": "web", "repo": "x", "ciRepo": "a/b", "notifySecret": "../etc"}, "params.notifySecret"},
		{"list of numbers", map[string]any{"service": "web", "repo": "x", "ciRepo": "a/b", "ciWorkflows": []any{1}}, "params.ciWorkflows"},
		{"ciRepo needed while requireCI", map[string]any{"service": "web", "repo": "x"}, "params.ciRepo"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			_, err := tpl.Render("p", tt.values)
			var is model.Issues
			if !errors.As(err, &is) {
				t.Fatalf("err = %v, want issues", err)
			}
			if !slices.ContainsFunc(is, func(i model.Issue) bool { return i.Path == tt.path }) {
				t.Fatalf("issues = %v, want one at %s", is, tt.path)
			}
		})
	}
}

func TestRenderEmptyValuesFallBackOrOverride(t *testing.T) {
	tpl, _ := templates.Get("auto-deploy")
	out, err := tpl.Render("p", map[string]any{
		"service": "web", "repo": "x", "requireCI": false, "branch": "", "healthTimeout": "", "notifySecret": "",
	})
	if err != nil {
		t.Fatal(err)
	}
	pol, _ := model.DecodePolicy(out)
	if pol.Spec.Params["healthTimeout"] != "5m" || pol.Spec.Params["notifySecret"] != "" || pol.Spec.Nodes[0].Props["branch"] != "main" {
		t.Fatalf("params = %v trigger = %v", pol.Spec.Params, pol.Spec.Nodes[0].Props)
	}
}

func TestEveryParameterIsUsed(t *testing.T) {
	// load panics at init when a described parameter is unused or missing,
	// so reaching this point already proves it; check the converse too.
	for _, tpl := range templates.List() {
		for _, p := range tpl.Params {
			if p.Required && p.Default != nil {
				t.Errorf("%s.%s: required parameter exposes a default", tpl.ID, p.Name)
			}
			if !p.Required && p.RequiredIf == "" && p.Default == nil {
				t.Errorf("%s.%s: optional parameter has no default", tpl.ID, p.Name)
			}
		}
	}
}
