package api_test

import (
	"net/http"
	"strings"
	"testing"
	"time"
)

func moduleDoc(name, param string) string {
	return "apiVersion: lcm/v1\nkind: Module\nmetadata: { name: " + name + " }\nspec:\n  params: [ { name: " + param + " } ]\n" +
		"  nodes:\n    - { id: in, type: module.input, next: { out: [o] } }\n    - { id: o, type: module.output, props: { port: out } }\n"
}

func TestModules(t *testing.T) {
	x := newSite(t)

	r := x.call("PUT", v1+"/modules/pass", moduleDoc("pass", "target"), yamlBody()).want(http.StatusCreated)
	if r.obj()["version"] != float64(1) || r.Head.Get("ETag") != `"1"` || r.Head.Get("Location") != v1+"/modules/pass" {
		t.Fatalf("create = %s, headers %v", r, r.Head)
	}
	x.call("PUT", v1+"/modules/pass", moduleDoc("pass", "other"), yamlBody()).wantError(http.StatusBadRequest, "bad_request")
	x.call("PUT", v1+"/modules/other", moduleDoc("pass", "target"), yamlBody()).wantError(http.StatusUnprocessableEntity, "validation_failed")
	x.call("PUT", v1+"/modules/ghost", moduleDoc("ghost", "x"), yamlBody(), ifMatch("1")).wantError(http.StatusNotFound, "not_found")
	d := x.call("PUT", v1+"/modules/pass", moduleDoc("pass", "other"), yamlBody(), ifMatch("4")).wantError(http.StatusConflict, "version_conflict")
	if d["currentVersion"] != float64(1) {
		t.Fatalf("conflict details = %v", d)
	}
	x.call("PUT", v1+"/modules/pass", "kind: Module", yamlBody(), ifMatch("1")).wantError(http.StatusUnprocessableEntity, "validation_failed")
	r = x.call("PUT", v1+"/modules/pass", moduleDoc("pass", "other"), yamlBody(), ifMatch("1")).want(http.StatusOK)
	if r.obj()["version"] != float64(2) {
		t.Fatalf("update = %s", r)
	}

	got := x.call("GET", v1+"/modules/pass", nil).want(http.StatusOK).obj()
	params := got["document"].(map[string]any)["spec"].(map[string]any)["params"].([]any)
	if got["version"] != float64(2) || params[0].(map[string]any)["name"] != "other" {
		t.Fatalf("module = %v", got)
	}
	list := x.call("GET", v1+"/modules", nil).want(http.StatusOK).items()
	if len(list) != 1 || list[0]["name"] != "pass" || list[0]["version"] != float64(2) {
		t.Fatalf("list = %v", list)
	}
	vs := x.call("GET", v1+"/modules/pass/versions", nil).want(http.StatusOK).items()
	if len(vs) != 2 || vs[0]["version"] != float64(2) {
		t.Fatalf("versions = %v", vs)
	}
	x.call("GET", v1+"/modules/ghost", nil).wantError(http.StatusNotFound, "not_found")
	x.call("GET", v1+"/modules/ghost/versions", nil).wantError(http.StatusNotFound, "not_found")

	// Policies resolve modules at deploy time.
	usesModule := policyDoc("uses", "    - { id: t, type: trigger.manual, next: { out: [m] } }\n    - { id: m, type: module, props: { module: pass, params: { other: 1 } } }\n")
	x.call("PUT", v1+"/policies/uses", usesModule, yamlBody()).want(http.StatusCreated)
	missing := strings.Replace(usesModule, "module: pass", "module: ghost", 1)
	x.call("PUT", v1+"/policies/uses", missing, yamlBody(), ifMatch("1")).wantError(http.StatusUnprocessableEntity, "validation_failed")
}

func TestValidateEndpoint(t *testing.T) {
	x := newSite(t)
	validate := func(doc string) map[string]any {
		return x.call("POST", v1+"/validate", doc, yamlBody()).want(http.StatusOK).obj()
	}
	issues := func(r map[string]any) []any { return r["issues"].([]any) }

	ok := validate(policyDoc("p", manualPolicyNodes))
	if ok["valid"] != true || ok["kind"] != "Policy" || ok["name"] != "p" || len(issues(ok)) != 0 {
		t.Fatalf("valid policy = %v", ok)
	}
	warn := validate(policyDoc("p", manualPolicyNodes+"    - { id: orphan, type: data.set, props: { values: { a: 1 } } }\n"))
	if warn["valid"] != true || len(warn["warnings"].([]any)) == 0 {
		t.Fatalf("policy warnings = %v", warn)
	}
	bad := validate(policyDoc("p", "    - { id: t, type: trigger.manual, next: { out: [x] } }\n    - { id: x, type: no.such }\n"))
	if bad["valid"] != false || len(issues(bad)) == 0 || !strings.HasPrefix(issues(bad)[0].(map[string]any)["path"].(string), "spec.nodes[1]") {
		t.Fatalf("invalid policy = %v", bad)
	}
	// Nothing was stored.
	if n := len(x.call("GET", v1+"/policies", nil).items()); n != 0 {
		t.Fatalf("validate stored %d policies", n)
	}

	svc := validate(serviceDoc("web", "  checks:\n    - { name: c, type: http, props: { nope: 1 } }\n"))
	if svc["valid"] != false || svc["kind"] != "Service" || svc["name"] != "web" || len(issues(svc)) != 1 {
		t.Fatalf("invalid service = %v", svc)
	}
	// Structural failures still say which document they belong to.
	if host := validate(serviceDoc("web", "  host: mars\n")); host["valid"] != false || host["kind"] != "Service" || host["name"] != "web" {
		t.Fatalf("unknown host = %v", host)
	}
	if v := validate(serviceDoc("web", "")); v["valid"] != true {
		t.Fatalf("valid service = %v", v)
	}
	if m := validate(moduleDoc("m", "x")); m["valid"] != true || m["kind"] != "Module" {
		t.Fatalf("module = %v", m)
	}
	broken := validate("kind: Nope\n")
	if broken["valid"] != false || broken["kind"] != "Nope" || len(issues(broken)) == 0 {
		t.Fatalf("unknown kind = %v", broken)
	}
	x.call("POST", v1+"/validate", "", yamlBody()).wantError(http.StatusBadRequest, "bad_request")
	if n := len(x.call("GET", v1+"/services", nil).items()); n != 0 {
		t.Fatalf("validate stored %d services", n)
	}
}

func TestNodeTypes(t *testing.T) {
	x := newSite(t)
	types := x.call("GET", v1+"/node-types", nil).want(http.StatusOK).items()
	byType := map[string]map[string]any{}
	for _, ty := range types {
		byType[ty["type"].(string)] = ty
	}
	for _, want := range []string{"trigger.manual", "flow.if", "data.set", "lifecycle.restart", "trigger.webhook", "http.request"} {
		if byType[want] == nil {
			t.Errorf("node type %s missing", want)
		}
	}
	if ty := byType["flow.if"]; ty["category"] == "" || ty["effect"] == nil || len(ty["ports"].([]any)) != 2 || len(ty["props"].([]any)) == 0 {
		t.Fatalf("flow.if schema = %v", ty)
	}
}

func TestLocks(t *testing.T) {
	x := newSite(t)
	if n := len(x.call("GET", v1+"/locks", nil).want(http.StatusOK).items()); n != 0 {
		t.Fatalf("locks = %d", n)
	}
	if _, err := x.app.Locks.TryAcquire(x.ctx, "service:web", "run:abc", time.Hour); err != nil {
		t.Fatal(err)
	}
	if _, err := x.app.Locks.TryAcquire(x.ctx, "deploys/prod", "run:def", time.Hour); err != nil {
		t.Fatal(err)
	}
	list := x.call("GET", v1+"/locks", nil).want(http.StatusOK).items()
	if len(list) != 2 || list[0]["name"] != "deploys/prod" || list[1]["owner"] != "run:abc" || list[1]["expiresAt"] == nil {
		t.Fatalf("locks = %v", list)
	}

	// Names may contain slashes and colons; clients percent-encode them.
	x.call("DELETE", v1+"/locks/deploys%2Fprod", nil).want(http.StatusNoContent)
	x.call("DELETE", v1+"/locks/service:web", nil).want(http.StatusNoContent)
	x.call("DELETE", v1+"/locks/service:web", nil).wantError(http.StatusNotFound, "not_found")
	if n := len(x.call("GET", v1+"/locks", nil).items()); n != 0 {
		t.Fatalf("locks after release = %d", n)
	}
	var forced int
	for _, a := range x.call("GET", v1+"/audit", nil).items() {
		if a["action"] == "lock.force_release" && a["actor"].(map[string]any)["kind"] == "token" {
			forced++
		}
	}
	if forced != 2 {
		t.Fatalf("force release audit entries = %d, want 2", forced)
	}
}

func TestTimelineAndDeployments(t *testing.T) {
	x := newSite(t)
	x.createService(serviceDoc("web", ""))
	x.createService(serviceDoc("db", ""))
	if _, err := x.app.Manager.Start(x.ctx, "web", startOpts()); err != nil {
		t.Fatal(err)
	}

	tl := x.call("GET", v1+"/services/web/timeline", nil).want(http.StatusOK).items()
	if len(tl) < 3 || tl[0]["service"] != "web" || tl[0]["data"] == nil {
		t.Fatalf("service timeline = %v", tl)
	}
	all := x.call("GET", v1+"/timeline", nil).want(http.StatusOK).items()
	services := map[any]bool{}
	for _, e := range all {
		services[e["service"]] = true
	}
	if !services["web"] || !services["db"] {
		t.Fatalf("global timeline services = %v", services)
	}
	if n := len(x.call("GET", v1+"/timeline?kinds=lifecycle.changed", nil).items()); n != 1 {
		t.Fatalf("lifecycle entries = %d", n)
	}
	// Paging by cursor, and the time window.
	page := x.call("GET", v1+"/services/web/timeline?limit=1", nil).want(http.StatusOK).obj()
	if page["nextCursor"] == nil {
		t.Fatalf("expected a next page: %v", page)
	}
	next := x.call("GET", v1+"/services/web/timeline?limit=1&cursor="+page["nextCursor"].(string), nil).items()
	if len(next) != 1 || next[0]["id"] == page["items"].([]any)[0].(map[string]any)["id"] {
		t.Fatalf("second timeline page = %v", next)
	}
	future := epoch.Add(time.Hour).Format(time.RFC3339)
	if n := len(x.call("GET", v1+"/timeline?from="+future, nil).items()); n != 0 {
		t.Fatalf("future entries = %d", n)
	}
	x.call("GET", v1+"/timeline?from=nope", nil).wantError(http.StatusBadRequest, "bad_request")
	x.call("GET", v1+"/timeline?cursor=bm9wZQ", nil).wantError(http.StatusBadRequest, "bad_request")

	if n := len(x.call("GET", v1+"/services/web/deployments", nil).want(http.StatusOK).items()); n != 0 {
		t.Fatalf("deployments = %d", n)
	}
	// Deployments are recorded by the catalog; list one.
	id, err := x.app.Catalog.BeginDeployment(x.ctx, deployment("web", "abc1234"))
	if err != nil {
		t.Fatal(err)
	}
	if err := x.app.Catalog.FinishDeployment(x.ctx, id, "succeeded", "", "built"); err != nil {
		t.Fatal(err)
	}
	deps := x.call("GET", v1+"/services/web/deployments", nil).want(http.StatusOK).items()
	if len(deps) != 1 || deps[0]["version"] != "abc1234" || deps[0]["outcome"] != "succeeded" || deps[0]["finishedAt"] == nil {
		t.Fatalf("deployments = %v", deps)
	}
	detail := x.call("GET", v1+"/services/web", nil).obj()
	if detail["deployedVersion"] != "abc1234" || detail["lastDeployment"].(map[string]any)["id"] != float64(id) {
		t.Fatalf("detail = %v", detail)
	}
	if sum := x.call("GET", v1+"/services", nil).items()[1]; sum["deployedVersion"] != "abc1234" {
		t.Fatalf("summary = %v", sum)
	}
}
