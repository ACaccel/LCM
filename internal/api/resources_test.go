package api_test

import (
	"fmt"
	"net/http"
	"strings"
	"testing"
)

func TestResourceCRUDAndVersions(t *testing.T) {
	x := newSite(t)

	// Create from YAML; the response is the resource detail.
	r := x.createService(serviceDoc("web", ""))
	created := r.obj()
	if created["version"] != float64(1) || r.Head.Get("ETag") != `"1"` || r.Head.Get("Location") != v1+"/resources/web" ||
		len(created["unresolvedRelations"].([]any)) != 0 {
		t.Fatalf("create = %v, headers %v", created, r.Head)
	}
	doc := created["document"].(map[string]any)
	if doc["metadata"].(map[string]any)["name"] != "web" || created["updatedBy"].(map[string]any)["kind"] != "token" ||
		created["type"] != "service" || created["adapter"] != "command" || created["id"] == "" {
		t.Fatalf("document/meta = %v", created)
	}
	id := created["id"].(string)
	x.call("POST", v1+"/resources", serviceDoc("web", ""), yamlBody()).wantError(http.StatusConflict, "already_exists")

	// Detail view.
	d := x.call("GET", v1+"/resources/web", nil).want(http.StatusOK).obj()
	health := d["health"].(map[string]any)
	if d["state"] != "registered" || d["phase"] != "managed" || health["status"] != "unknown" || d["name"] != "web" ||
		d["service"].(map[string]any)["lastDeployment"] != nil || len(d["checks"].([]any)) != 0 || len(d["alerts"].([]any)) != 0 ||
		d["operation"] != nil || d["machine"] != nil {
		t.Fatalf("detail = %v", d)
	}
	actions := map[string]map[string]any{}
	for _, a := range d["actions"].([]any) {
		m := a.(map[string]any)
		actions[m["name"].(string)] = m
	}
	if len(actions) != 12 || actions["start"]["available"] != true || actions["deploy"]["reason"] != "notConfigured" ||
		actions["retire"]["kind"] != "phase" || actions["resume"]["available"] != false || actions["stop"]["risk"] != "disruptive" ||
		actions["restart"]["policy"] != true {
		t.Fatalf("actions = %v", d["actions"])
	}

	// Update needs the current version.
	upd := serviceDoc("web", "  relations: [{ type: dependsOn, target: db }]\n")
	x.call("PUT", v1+"/resources/web", upd, yamlBody()).wantError(http.StatusBadRequest, "bad_request")
	x.call("PUT", v1+"/resources/web", upd, yamlBody(), ifMatch("*")).wantError(http.StatusBadRequest, "bad_request")
	x.call("PUT", v1+"/resources/web", upd, yamlBody(), ifMatch("banana")).wantError(http.StatusBadRequest, "bad_request")
	x.call("PUT", v1+"/resources/other", upd, yamlBody(), ifMatch("1")).wantError(http.StatusUnprocessableEntity, "validation_failed")
	r = x.call("PUT", v1+"/resources/web", upd, yamlBody(), ifMatch(`"1"`)).want(http.StatusOK)
	if r.obj()["version"] != float64(2) || r.Head.Get("ETag") != `"2"` || fmt.Sprint(r.obj()["unresolvedRelations"]) != "[db]" {
		t.Fatalf("update = %s", r)
	}
	// A stale writer gets the current version back.
	details := x.call("PUT", v1+"/resources/web", serviceDoc("web", "  relations: [{ type: dependsOn, target: cache }]\n"), yamlBody(), ifMatch("1")).
		wantError(http.StatusConflict, "version_conflict")
	if details["currentVersion"] != float64(2) || details["expectedVersion"] != float64(1) {
		t.Fatalf("conflict details = %v", details)
	}
	x.call("PUT", v1+"/resources/ghost", serviceDoc("ghost", ""), yamlBody(), ifMatch("1")).wantError(http.StatusNotFound, "not_found")
	// Type and adapter are part of the identity.
	external := "apiVersion: lcm/v2\nkind: Resource\nmetadata: { name: web }\nspec: { type: service, adapter: external }\n"
	x.call("PUT", v1+"/resources/web", external, yamlBody(), ifMatch("2")).wantError(http.StatusConflict, "precondition_failed")

	// JSON bodies work too, and an unchanged document keeps the version.
	jsonDoc := `{"apiVersion":"lcm/v2","kind":"Resource","metadata":{"name":"web","labels":{"tier":"web"}},` +
		`"spec":{"type":"service","adapter":"command","config":{"start":"web-start","stop":"web-stop","restart":"web-restart","status":"web-status"},` +
		`"relations":[{"type":"dependsOn","target":"db"}]}}`
	r = x.call("PUT", v1+"/resources/web", jsonDoc, header("Content-Type", "application/json"), ifMatch("2")).want(http.StatusOK)
	if r.obj()["version"] != float64(2) {
		t.Fatalf("identical update changed the version: %s", r)
	}

	// History.
	vs := x.call("GET", v1+"/resources/web/versions", nil).want(http.StatusOK).items()
	if len(vs) != 2 || vs[0]["version"] != float64(2) || vs[1]["version"] != float64(1) || vs[0]["restoredFrom"] != nil {
		t.Fatalf("versions = %v", vs)
	}
	old := x.call("GET", v1+"/resources/web/versions/1", nil).want(http.StatusOK).obj()
	if _, has := old["document"].(map[string]any)["spec"].(map[string]any)["relations"]; has || old["version"] != float64(1) {
		t.Fatalf("version 1 = %v", old)
	}
	x.call("GET", v1+"/resources/web/versions/9", nil).wantError(http.StatusNotFound, "not_found")
	x.call("GET", v1+"/resources/web/versions/x", nil).wantError(http.StatusNotFound, "not_found")

	// Restore saves the old document as a new version.
	x.call("POST", v1+"/resources/web/restore", map[string]int{"version": 0}).wantError(http.StatusUnprocessableEntity, "validation_failed")
	x.call("POST", v1+"/resources/web/restore", map[string]int{"version": 9}).wantError(http.StatusNotFound, "not_found")
	r = x.call("POST", v1+"/resources/web/restore", map[string]int{"version": 1}).want(http.StatusOK)
	if r.obj()["version"] != float64(3) || len(r.obj()["unresolvedRelations"].([]any)) != 0 {
		t.Fatalf("restore = %s", r)
	}

	vs = x.call("GET", v1+"/resources/web/versions", nil).items()
	if vs[0]["restoredFrom"] != float64(1) {
		t.Fatalf("restored version = %v", vs[0])
	}
	x.call("POST", v1+"/resources/web/restore", map[string]int{"version": 2}, ifMatch("1")).wantError(http.StatusConflict, "version_conflict")

	// Phases: retire, resume, retire again and unmanage. The version comes
	// from the body or If-Match.
	x.call("POST", v1+"/resources/web/phase", map[string]any{"phase": "retired"}).wantError(http.StatusUnprocessableEntity, "validation_failed")
	x.call("POST", v1+"/resources/web/phase", map[string]any{"phase": "gone", "version": 3}).wantError(http.StatusUnprocessableEntity, "validation_failed")
	x.call("POST", v1+"/resources/web/phase", map[string]any{"phase": "retired", "version": 1}).wantError(http.StatusConflict, "version_conflict")
	x.call("POST", v1+"/resources/web/phase", map[string]any{"phase": "unmanaged", "version": 3}).wantError(http.StatusConflict, "precondition_failed")
	d = x.call("POST", v1+"/resources/web/phase", map[string]any{"phase": "retired", "version": 3, "reason": "moving"}).want(http.StatusOK).obj()
	if d["phase"] != "retired" {
		t.Fatalf("retired = %v", d)
	}
	x.startActionFails("web", "start", http.StatusConflict, "precondition_failed")
	d = x.call("POST", v1+"/resources/web/phase", map[string]any{"phase": "managed"}, ifMatch("3")).want(http.StatusOK).obj()
	if d["phase"] != "managed" {
		t.Fatalf("resumed = %v", d)
	}
	x.call("POST", v1+"/resources/web/phase", map[string]any{"phase": "retired", "version": 3}).want(http.StatusOK)
	d = x.call("POST", v1+"/resources/web/phase", map[string]any{"phase": "unmanaged", "version": 3}).want(http.StatusOK).obj()
	if d["phase"] != "unmanaged" || d["id"] != id {
		t.Fatalf("unmanaged = %v", d)
	}

	// The archive is read only and its name is free again.
	x.call("GET", v1+"/resources/web", nil).wantError(http.StatusNotFound, "not_found")
	archived := x.call("GET", v1+"/resources/@"+id, nil).want(http.StatusOK).obj()
	if archived["phase"] != "unmanaged" || archived["name"] != "web" {
		t.Fatalf("archive = %v", archived)
	}
	x.call("GET", v1+"/resources/@"+id+"/versions", nil).want(http.StatusOK)
	arch := x.call("GET", v1+"/resource-archives?type=service", nil).want(http.StatusOK).items()
	if len(arch) != 1 || arch[0]["id"] != id {
		t.Fatalf("archives = %v", arch)
	}
	x.call("POST", v1+"/resources/@"+id+"/restore", map[string]int{"version": 1}).wantError(http.StatusConflict, "precondition_failed")
	again := x.createService(serviceDoc("web", "")).obj()
	if again["id"] == id || again["version"] != float64(1) {
		t.Fatalf("new incarnation = %v", again)
	}
}

func (x *site) startActionFails(name, action string, status int, code string) {
	x.t.Helper()
	x.call("POST", v1+"/resources/"+name+"/actions/"+action, nil).wantError(status, code)
}

func TestResourceTypes(t *testing.T) {
	x := newSite(t)
	types := x.call("GET", v1+"/resource-types", nil).want(http.StatusOK).items()
	byType := map[string]map[string]any{}
	for _, ty := range types {
		byType[ty["type"].(string)] = ty
	}
	svc, mach, router := byType["service"], byType["machine"], byType["router"]
	if svc == nil || mach == nil || router == nil || router["needsConnection"] != true || len(svc["adapters"].([]any)) != 6 {
		t.Fatalf("types = %v", types)
	}
	names := func(ty map[string]any) string {
		var out []string
		for _, a := range ty["actions"].([]any) {
			out = append(out, a.(map[string]any)["name"].(string))
		}
		return strings.Join(out, ",")
	}
	if got := names(svc); got != "start,stop,restart,deploy,rollback,backup,restore,run-job,apply-route,retire,resume,unmanage" {
		t.Fatalf("service actions = %s", got)
	}
	if got := names(mach); !strings.Contains(got, "reboot") || !strings.Contains(got, "observe") {
		t.Fatalf("machine actions = %s", got)
	}
	if got := names(router); got != "observe,reboot,retire,resume,unmanage" {
		t.Fatalf("router actions = %s", got)
	}
	for _, a := range router["actions"].([]any) {
		if a := a.(map[string]any); a["policy"] != false || (a["name"] == "reboot" && (a["risk"] != "disruptive" || a["kind"] != "operation")) {
			t.Fatalf("router action = %v", a)
		}
	}
}

func TestResourceRoutesOfOtherTypesAreUnsupported(t *testing.T) {
	x := newSite(t)
	x.createService(serviceDoc("web", ""))
	for _, path := range []string{"/observe", "/maintenance/exit", "/identity/rebind"} {
		x.call("POST", v1+"/resources/web"+path, map[string]any{"reason": "x"}, ifMatch("1")).wantError(http.StatusConflict, "unsupported")
	}
	for _, path := range []string{"/history", "/events", "/capabilities", "/operations", "/maintenance", "/maintenance/preview"} {
		x.call("GET", v1+"/resources/web"+path, nil).wantError(http.StatusConflict, "unsupported")
	}
	x.call("POST", v1+"/resources/web/operations", map[string]any{"action": "reboot", "idempotencyKey": "k"}, ifMatch("1")).
		wantError(http.StatusConflict, "unsupported")

	machineDoc := "apiVersion: lcm/v2\nkind: Resource\nmetadata: { name: host }\nspec: { type: machine, adapter: local }\n"
	created := x.call("POST", v1+"/resources", machineDoc, yamlBody()).want(http.StatusCreated).obj()
	if created["type"] != "machine" || created["machine"] == nil || created["service"] != nil || created["state"] != "unknown" {
		t.Fatalf("machine = %v", created)
	}
	x.call("POST", v1+"/resources/host/actions/restart", nil).wantError(http.StatusConflict, "unsupported")
	// Only one local machine exists.
	x.call("POST", v1+"/resources", strings.Replace(machineDoc, "host", "other", 1), yamlBody()).wantError(http.StatusConflict, "already_exists")
	// Machines and services list together, filtered by type.
	if n := len(x.call("GET", v1+"/resources", nil).items()); n != 2 {
		t.Fatalf("resources = %d", n)
	}
	only := x.call("GET", v1+"/resources?type=machine", nil).items()
	if len(only) != 1 || only[0]["name"] != "host" || only[0]["deployedVersion"] != "" {
		t.Fatalf("machines = %v", only)
	}
}

func TestResourceValidationErrors(t *testing.T) {
	x := newSite(t)

	// Structural problems carry paths.
	bad := "apiVersion: lcm/v2\nkind: Resource\nmetadata: { name: web }\nspec:\n  type: service\n  adapter: systemd\n"
	d := x.call("POST", v1+"/resources", bad, yamlBody()).wantError(http.StatusUnprocessableEntity, "validation_failed")
	issues := d["issues"].([]any)
	if len(issues) == 0 || !strings.HasPrefix(issues[0].(map[string]any)["path"].(string), "spec.config") {
		t.Fatalf("issues = %v", issues)
	}
	// lcm/v1 documents are refused with a pointer to lcm convert.
	v1Doc := "apiVersion: lcm/v1\nkind: Service\nmetadata: { name: web }\nspec:\n  runtime: { type: command, start: x }\n"
	d = x.call("POST", v1+"/resources", v1Doc, yamlBody()).wantError(http.StatusUnprocessableEntity, "validation_failed")
	if msg := d["issues"].([]any)[0].(map[string]any)["message"].(string); !strings.Contains(msg, "lcm convert") {
		t.Fatalf("v1 issue = %q", msg)
	}
	// Bad check props are found by the running system.
	badCheck := serviceDoc("web", "  checks:\n    - { name: c, type: http, props: { nope: 1 } }\n")
	d = x.call("POST", v1+"/resources", badCheck, yamlBody()).wantError(http.StatusUnprocessableEntity, "validation_failed")
	if p := d["issues"].([]any)[0].(map[string]any)["path"].(string); !strings.HasPrefix(p, "spec.checks[0]") {
		t.Fatalf("check issue path = %q", p)
	}
	// A dependency cycle is refused; a missing target is allowed.
	x.createService(serviceDoc("a", "  relations: [{ type: dependsOn, target: b }]\n"))
	x.call("POST", v1+"/resources", serviceDoc("b", "  relations: [{ type: dependsOn, target: a }]\n"), yamlBody()).
		wantError(http.StatusUnprocessableEntity, "validation_failed")

	// Malformed requests.
	x.call("POST", v1+"/resources", "", yamlBody()).wantError(http.StatusBadRequest, "bad_request")
	x.call("POST", v1+"/resources", "x", header("Content-Type", "text/plain")).wantError(http.StatusBadRequest, "bad_request")
	d = x.call("POST", v1+"/resources", ": : :\n\t- [", yamlBody()).wantError(http.StatusUnprocessableEntity, "validation_failed")
	if len(d["issues"].([]any)) == 0 {
		t.Fatal("no issues for broken YAML")
	}
	x.call("GET", v1+"/resources/web", nil).wantError(http.StatusNotFound, "not_found")
}

func TestResourceListFiltersAndPaging(t *testing.T) {
	x := newSite(t)
	for _, n := range []string{"alpha", "bravo", "charlie"} {
		x.createService(strings.Replace(serviceDoc(n, ""), "tier: web", "tier: "+map[string]string{"alpha": "web", "bravo": "db", "charlie": "web"}[n], 1))
	}
	if _, err := x.app.Manager.Start(x.ctx, "bravo", startOpts()); err != nil {
		t.Fatal(err)
	}

	names := func(path string) []string {
		var out []string
		for _, it := range x.call("GET", path, nil).want(http.StatusOK).items() {
			out = append(out, it["name"].(string))
		}
		return out
	}
	if got := strings.Join(names(v1+"/resources"), ","); got != "alpha,bravo,charlie" {
		t.Fatalf("all = %s", got)
	}
	if got := strings.Join(names(v1+"/resources?label=tier=web"), ","); got != "alpha,charlie" {
		t.Fatalf("label filter = %s", got)
	}
	if got := strings.Join(names(v1+"/resources?label=tier=web&label=nope=x"), ","); got != "" {
		t.Fatalf("two labels = %s", got)
	}
	if got := strings.Join(names(v1+"/resources?state=running"), ","); got != "bravo" {
		t.Fatalf("state filter = %s", got)
	}
	if got := strings.Join(names(v1+"/resources?health=unknown&type=service&phase=managed"), ","); got != "alpha,bravo,charlie" {
		t.Fatalf("health filter = %s", got)
	}
	if got := strings.Join(names(v1+"/resources?health=down"), ","); got != "" {
		t.Fatalf("down filter = %s", got)
	}
	if got := strings.Join(names(v1+"/resources?type=machine"), ","); got != "" {
		t.Fatalf("type filter = %s", got)
	}

	// Paging walks the list with the opaque cursor.
	first := x.call("GET", v1+"/resources?limit=2", nil).want(http.StatusOK).obj()
	cursor, _ := first["nextCursor"].(string)
	if cursor == "" || len(first["items"].([]any)) != 2 {
		t.Fatalf("first page = %v", first)
	}
	second := x.call("GET", v1+"/resources?limit=2&cursor="+cursor, nil).want(http.StatusOK).obj()
	if len(second["items"].([]any)) != 1 || second["nextCursor"] != nil {
		t.Fatalf("second page = %v", second)
	}
	item := first["items"].([]any)[0].(map[string]any)
	if item["state"] != "registered" || item["version"] != float64(1) || item["deployedVersion"] != "" || item["type"] != "service" {
		t.Fatalf("item = %v", item)
	}

	for _, bad := range []string{"limit=0", "limit=501", "limit=x", "cursor=!!!", "label=novalue", "health=nope", "type=nope", "phase=unmanaged"} {
		x.call("GET", v1+"/resources?"+bad, nil).wantError(http.StatusBadRequest, "bad_request")
	}
}

// TestArchiveIsReadOnly checks that an archive addressed as "@<id>" can be
// read but never changed or acted on: the components act by name, so a
// write would otherwise reach the live resource that now carries the name.
func TestArchiveIsReadOnly(t *testing.T) {
	x := newSite(t)
	oldID := x.createService(serviceDoc("web", "")).obj()["id"].(string)
	if _, err := x.app.Catalog.BeginDeployment(x.ctx, deployment("web", "abc1234")); err != nil {
		t.Fatal(err)
	}
	x.call("POST", v1+"/resources/web/phase", map[string]any{"phase": "retired", "version": 1}).want(http.StatusOK)
	x.call("POST", v1+"/resources/web/phase", map[string]any{"phase": "unmanaged", "version": 1}).want(http.StatusOK)
	newID := x.createService(serviceDoc("web", "")).obj()["id"].(string)

	old := v1 + "/resources/@" + oldID
	x.call("POST", old+"/actions/start", nil).wantError(http.StatusConflict, "precondition_failed")
	x.call("POST", old+"/actions/start?dryRun=true", nil).wantError(http.StatusConflict, "precondition_failed")
	x.call("PUT", old, serviceDoc("web", ""), yamlBody(), ifMatch("1")).wantError(http.StatusConflict, "precondition_failed")
	x.call("POST", old+"/restore", map[string]int{"version": 1}).wantError(http.StatusConflict, "precondition_failed")
	x.call("POST", old+"/phase", map[string]any{"phase": "managed", "version": 1}).wantError(http.StatusConflict, "precondition_failed")
	for _, d := range x.runner.Displays() {
		if strings.Contains(d, "web-start") {
			t.Fatalf("an archive action ran %q on the live service", d)
		}
	}

	// Reads of the archive work and follow its name where history is
	// name-keyed.
	if deps := x.call("GET", old+"/deployments", nil).want(http.StatusOK).items(); len(deps) != 1 || deps[0]["resource"] != "web" {
		t.Fatalf("archive deployments = %v", deps)
	}
	if tl := x.call("GET", old+"/timeline", nil).want(http.StatusOK).items(); len(tl) == 0 {
		t.Fatal("archive timeline is empty")
	}
	x.call("GET", v1+"/resources/@nope/timeline", nil).wantError(http.StatusNotFound, "not_found")
	x.call("GET", v1+"/resources/@nope/deployments", nil).wantError(http.StatusNotFound, "not_found")

	// The active incarnation is writable by "@<id>" too.
	cur := v1 + "/resources/@" + newID
	x.call("POST", cur+"/actions/start", nil).want(http.StatusAccepted)
	x.call("PUT", cur, serviceDoc("web", "  relations: [{ type: dependsOn, target: db }]\n"), yamlBody(), ifMatch("1")).want(http.StatusOK)
	x.call("PUT", cur, serviceDoc("other", ""), yamlBody(), ifMatch("2")).wantError(http.StatusUnprocessableEntity, "validation_failed")

	// Machine writes: observe, maintenance and operations.
	machineDoc := "apiVersion: lcm/v2\nkind: Resource\nmetadata: { name: host }\nspec: { type: machine, adapter: local }\n"
	hostID := x.call("POST", v1+"/resources", machineDoc, yamlBody()).want(http.StatusCreated).obj()["id"].(string)
	x.call("POST", v1+"/resources/host/phase", map[string]any{"phase": "retired", "version": 1}).want(http.StatusOK)
	x.call("POST", v1+"/resources/host/phase", map[string]any{"phase": "unmanaged", "version": 1}).want(http.StatusOK)
	x.call("POST", v1+"/resources", machineDoc, yamlBody()).want(http.StatusCreated)
	host := v1 + "/resources/@" + hostID
	x.call("POST", host+"/observe", nil).wantError(http.StatusConflict, "precondition_failed")
	x.call("POST", host+"/maintenance", map[string]any{"reason": "x", "previewHash": strings.Repeat("a", 64)}, ifMatch("1")).
		wantError(http.StatusConflict, "precondition_failed")
	x.call("POST", host+"/maintenance/exit", map[string]any{"reason": "x"}, ifMatch("1")).wantError(http.StatusConflict, "precondition_failed")
	x.call("POST", host+"/operations", map[string]any{"action": "reboot", "idempotencyKey": "k", "reason": "x", "confirm": "host"}, ifMatch("1")).
		wantError(http.StatusConflict, "precondition_failed")
	x.call("POST", host+"/identity/rebind", map[string]any{"reason": "x", "confirm": "host"}, ifMatch("1")).wantError(http.StatusConflict, "precondition_failed")
	x.call("POST", host+"/operations/x/resolve", map[string]any{"reason": "x"}, ifMatch("1")).wantError(http.StatusConflict, "precondition_failed")
	x.call("GET", host+"/history", nil).want(http.StatusOK)
	x.call("GET", host+"/operations", nil).want(http.StatusOK)
}

func TestResourceDetailReportsUnresolvedRelations(t *testing.T) {
	x := newSite(t)
	rel := "  relations: [{ type: dependsOn, target: db }, { type: runsOn, target: host }]\n"
	if got := x.createService(serviceDoc("web", rel)).obj()["unresolvedRelations"]; fmt.Sprint(got) != "[db host]" {
		t.Fatalf("created = %v", got)
	}
	// A relation resolves once its target exists.
	x.createService(serviceDoc("db", ""))
	if got := x.call("GET", v1+"/resources/web", nil).want(http.StatusOK).obj()["unresolvedRelations"]; fmt.Sprint(got) != "[host]" {
		t.Fatalf("detail = %v", got)
	}
	x.call("PUT", v1+"/resources/web", serviceDoc("web", ""), yamlBody(), ifMatch("1")).want(http.StatusOK)
	if got := x.call("POST", v1+"/resources/web/restore", map[string]int{"version": 1}).want(http.StatusOK).obj()["unresolvedRelations"]; fmt.Sprint(got) != "[host]" {
		t.Fatalf("restored = %v", got)
	}
}
