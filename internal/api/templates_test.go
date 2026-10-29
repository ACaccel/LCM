package api_test

import (
	"net/http"
	"strings"
	"testing"
)

func TestTemplatesList(t *testing.T) {
	x := newSite(t)
	items := x.call("GET", v1+"/templates", nil).want(http.StatusOK).items()
	if len(items) != 4 || items[0]["id"] != "auto-deploy" || items[0]["serviceParam"] != "service" {
		t.Fatalf("templates = %v", items)
	}
	params := items[0]["params"].([]any)
	byName := map[string]map[string]any{}
	for _, p := range params {
		m := p.(map[string]any)
		byName[m["name"].(string)] = m
	}
	if byName["repo"]["required"] != true || byName["repo"]["default"] != nil ||
		byName["requireCI"]["default"] != true || byName["ciRepo"]["requiredIf"] != "requireCI" {
		t.Fatalf("params = %v", byName)
	}
	x.anon("GET", v1+"/templates", nil).wantError(http.StatusUnauthorized, "unauthenticated")
	x.call("GET", v1+"/templates", nil, bearer(x.ingest)).wantError(http.StatusForbidden, "forbidden")
}

func TestTemplateInstantiate(t *testing.T) {
	x := newSite(t)
	x.createService(serviceDoc("web", ""))
	body := map[string]any{
		"name": "web-restart", "params": map[string]any{"service": "web", "healthTimeout": "2m"}, "dryRun": true,
	}
	path := v1 + "/templates/restart-on-down/instantiate"

	// A dry run stores nothing.
	dry := x.call("POST", path, body).want(http.StatusOK).obj()
	if dry["deployed"] != false || dry["version"] != nil || !strings.Contains(dry["yaml"].(string), "healthTimeout: 2m") {
		t.Fatalf("dry run = %v", dry)
	}
	x.call("GET", v1+"/policies/web-restart", nil).wantError(http.StatusNotFound, "not_found")

	body["dryRun"] = false
	r := x.call("POST", path, body).want(http.StatusCreated)
	out := r.obj()
	if out["deployed"] != true || out["version"] != float64(1) || r.Head.Get("Location") != v1+"/policies/web-restart" {
		t.Fatalf("instantiate = %v", out)
	}
	pol := x.call("GET", v1+"/policies/web-restart", nil).want(http.StatusOK).obj()
	meta := pol["document"].(map[string]any)["metadata"].(map[string]any)
	if pol["deployed"] != true || meta["labels"].(map[string]any)["service"] != "web" ||
		meta["annotations"].(map[string]any)["lcm.dev/template"] != "restart-on-down" {
		t.Fatalf("policy = %v", pol)
	}
	trig := pol["triggers"].([]any)[0].(map[string]any)["props"].(map[string]any)
	if trig["services"].([]any)[0] != "web" {
		t.Fatalf("trigger props = %v", trig)
	}
	// The template's lifecycle nodes read params.service, which resolves
	// without running the policy.
	list := x.call("GET", v1+"/policies", nil).want(http.StatusOK).items()
	targets := list[0]["targets"].([]any)
	if len(targets) == 0 {
		t.Fatalf("no targets in %v", list[0])
	}
	for _, tg := range targets {
		m := tg.(map[string]any)
		if m["service"] != "web" || !strings.HasPrefix(m["type"].(string), "lifecycle.") {
			t.Fatalf("targets = %v", targets)
		}
	}

	// The audit log names the actor and the policy.
	var entry struct {
		Items []struct {
			Action, Target string
		} `json:"items"`
	}
	x.call("GET", v1+"/audit", nil).want(http.StatusOK).decode(&entry)
	found := false
	for _, e := range entry.Items {
		found = found || (strings.HasPrefix(e.Action, "policy.") && e.Target == "policy:web-restart")
	}
	if !found {
		t.Errorf("no audit entry for the instantiated policy: %+v", entry.Items)
	}

	x.call("POST", path, body).wantError(http.StatusConflict, "already_exists")
}

func TestTemplateInstantiateErrors(t *testing.T) {
	x := newSite(t)
	x.createService(serviceDoc("web", ""))
	path := v1 + "/templates/restart-on-down/instantiate"

	x.call("POST", v1+"/templates/nope/instantiate", map[string]any{"name": "p"}).wantError(http.StatusNotFound, "not_found")
	x.call("POST", path, map[string]any{"params": map[string]any{"service": "web"}}).wantError(http.StatusUnprocessableEntity, "validation_failed")
	x.call("POST", path, `{"name":"p","bogus":1}`).wantError(http.StatusBadRequest, "bad_request")

	issues := func(params map[string]any, name string) []any {
		d := x.call("POST", path, map[string]any{"name": name, "params": params}).wantError(http.StatusUnprocessableEntity, "validation_failed")
		return d["issues"].([]any)
	}
	hasPath := func(is []any, p string) bool {
		for _, i := range is {
			if i.(map[string]any)["path"] == p {
				return true
			}
		}
		return false
	}
	if is := issues(map[string]any{}, "p"); !hasPath(is, "params.service") {
		t.Errorf("missing service: %v", is)
	}
	if is := issues(map[string]any{"service": "ghost"}, "p"); !hasPath(is, "params.service") {
		t.Errorf("unknown service: %v", is)
	}
	if is := issues(map[string]any{"service": "web", "healthTimeout": "soon"}, "p"); !hasPath(is, "params.healthTimeout") {
		t.Errorf("bad duration: %v", is)
	}
	// A name the compiler rejects is reported as a document problem.
	if is := issues(map[string]any{"service": "web"}, "Bad Name"); len(is) == 0 {
		t.Error("invalid policy name accepted")
	}
	x.call("GET", v1+"/policies", nil).want(http.StatusOK)
	if got := len(x.call("GET", v1+"/policies", nil).items()); got != 0 {
		t.Errorf("%d policies stored after failed instantiations", got)
	}
	// Token scope: ingest tokens may not instantiate.
	x.call("POST", path, map[string]any{"name": "p", "params": map[string]any{"service": "web"}}, bearer(x.ingest)).wantError(http.StatusForbidden, "forbidden")
}
