package api_test

import (
	"net/http"
	"strings"
	"testing"
)

func TestServiceCRUDAndVersions(t *testing.T) {
	x := newSite(t)

	// Create from YAML; the response carries the document and metadata.
	r := x.createService(serviceDoc("web", ""))
	created := r.obj()
	if created["version"] != float64(1) || r.Head.Get("ETag") != `"1"` || r.Head.Get("Location") != v1+"/services/web" {
		t.Fatalf("create = %v, headers %v", created, r.Head)
	}
	doc := created["document"].(map[string]any)
	if doc["metadata"].(map[string]any)["name"] != "web" || created["updatedBy"].(map[string]any)["kind"] != "token" {
		t.Fatalf("document/meta = %v", created)
	}
	x.call("POST", v1+"/services", serviceDoc("web", ""), yamlBody()).wantError(http.StatusConflict, "already_exists")

	// Detail view.
	d := x.call("GET", v1+"/services/web", nil).want(http.StatusOK).obj()
	if d["state"] != "registered" || d["health"] != "unknown" || d["name"] != "web" || d["host"] != "local" ||
		d["lastDeployment"] != nil || len(d["checks"].([]any)) != 0 || len(d["alerts"].([]any)) != 0 {
		t.Fatalf("detail = %v", d)
	}

	// Update needs the current version.
	upd := serviceDoc("web", "  dependsOn: [db]\n")
	x.call("PUT", v1+"/services/web", upd, yamlBody()).wantError(http.StatusBadRequest, "bad_request")
	x.call("PUT", v1+"/services/web", upd, yamlBody(), ifMatch("*")).wantError(http.StatusBadRequest, "bad_request")
	x.call("PUT", v1+"/services/web", upd, yamlBody(), ifMatch("banana")).wantError(http.StatusBadRequest, "bad_request")
	x.call("PUT", v1+"/services/other", upd, yamlBody(), ifMatch("1")).wantError(http.StatusUnprocessableEntity, "validation_failed")
	r = x.call("PUT", v1+"/services/web", upd, yamlBody(), ifMatch(`"1"`)).want(http.StatusOK)
	if r.obj()["version"] != float64(2) || r.Head.Get("ETag") != `"2"` {
		t.Fatalf("update = %s", r)
	}
	// A stale writer gets the current version back.
	details := x.call("PUT", v1+"/services/web", serviceDoc("web", "  dependsOn: [cache]\n"), yamlBody(), ifMatch("1")).
		wantError(http.StatusConflict, "version_conflict")
	if details["currentVersion"] != float64(2) || details["expectedVersion"] != float64(1) {
		t.Fatalf("conflict details = %v", details)
	}
	x.call("PUT", v1+"/services/ghost", serviceDoc("ghost", ""), yamlBody(), ifMatch("1")).wantError(http.StatusNotFound, "not_found")

	// JSON bodies work too, and an unchanged document keeps the version.
	jsonDoc := `{"apiVersion":"lcm/v1","kind":"Service","metadata":{"name":"web","labels":{"tier":"web"}},` +
		`"spec":{"runtime":{"type":"command","start":"web-start","stop":"web-stop","restart":"web-restart","status":"web-status"},"dependsOn":["db"]}}`
	r = x.call("PUT", v1+"/services/web", jsonDoc, header("Content-Type", "application/json"), ifMatch("2")).want(http.StatusOK)
	if r.obj()["version"] != float64(2) {
		t.Fatalf("identical update changed the version: %s", r)
	}

	// History.
	vs := x.call("GET", v1+"/services/web/versions", nil).want(http.StatusOK).items()
	if len(vs) != 2 || vs[0]["version"] != float64(2) || vs[1]["version"] != float64(1) || vs[0]["restoredFrom"] != nil {
		t.Fatalf("versions = %v", vs)
	}
	old := x.call("GET", v1+"/services/web/versions/1", nil).want(http.StatusOK).obj()
	if _, has := old["document"].(map[string]any)["spec"].(map[string]any)["dependsOn"]; has || old["version"] != float64(1) {
		t.Fatalf("version 1 = %v", old)
	}
	x.call("GET", v1+"/services/web/versions/9", nil).wantError(http.StatusNotFound, "not_found")
	x.call("GET", v1+"/services/web/versions/x", nil).wantError(http.StatusNotFound, "not_found")

	// Restore saves the old document as a new version.
	x.call("POST", v1+"/services/web/restore", map[string]int{"version": 0}).wantError(http.StatusUnprocessableEntity, "validation_failed")
	x.call("POST", v1+"/services/web/restore", map[string]int{"version": 9}).wantError(http.StatusNotFound, "not_found")
	r = x.call("POST", v1+"/services/web/restore", map[string]int{"version": 1}).want(http.StatusOK)
	if r.obj()["version"] != float64(3) {
		t.Fatalf("restore = %s", r)
	}
	vs = x.call("GET", v1+"/services/web/versions", nil).items()
	if vs[0]["restoredFrom"] != float64(1) {
		t.Fatalf("restored version = %v", vs[0])
	}
	x.call("POST", v1+"/services/web/restore", map[string]int{"version": 2}, ifMatch("1")).wantError(http.StatusConflict, "version_conflict")

	// Delete, then it is gone but its history stays.
	x.call("DELETE", v1+"/services/web", nil).want(http.StatusNoContent)
	x.call("GET", v1+"/services/web", nil).wantError(http.StatusNotFound, "not_found")
	x.call("DELETE", v1+"/services/web", nil).wantError(http.StatusNotFound, "not_found")
	x.call("GET", v1+"/services/web/versions", nil).want(http.StatusOK)

	hosts := x.call("GET", v1+"/hosts", nil).want(http.StatusOK).items()
	if len(hosts) != 1 || hosts[0]["name"] != "local" || hosts[0]["kind"] != "local" {
		t.Fatalf("hosts = %v", hosts)
	}
}

func TestServiceValidationErrors(t *testing.T) {
	x := newSite(t)

	// Structural problems carry paths.
	bad := "apiVersion: lcm/v1\nkind: Service\nmetadata: { name: web }\nspec:\n  runtime: { type: systemd }\n"
	d := x.call("POST", v1+"/services", bad, yamlBody()).wantError(http.StatusUnprocessableEntity, "validation_failed")
	issues := d["issues"].([]any)
	if len(issues) == 0 || !strings.HasPrefix(issues[0].(map[string]any)["path"].(string), "spec.runtime") {
		t.Fatalf("issues = %v", issues)
	}
	// Unknown host and bad check props are found by the running system.
	d = x.call("POST", v1+"/services", serviceDoc("web", "  host: mars\n"), yamlBody()).wantError(http.StatusUnprocessableEntity, "validation_failed")
	if p := d["issues"].([]any)[0].(map[string]any)["path"]; p != "spec.host" {
		t.Fatalf("host issue path = %v", p)
	}
	badCheck := serviceDoc("web", "  checks:\n    - { name: c, type: http, props: { nope: 1 } }\n")
	d = x.call("POST", v1+"/services", badCheck, yamlBody()).wantError(http.StatusUnprocessableEntity, "validation_failed")
	if p := d["issues"].([]any)[0].(map[string]any)["path"].(string); !strings.HasPrefix(p, "spec.checks[0]") {
		t.Fatalf("check issue path = %q", p)
	}

	// Malformed requests.
	x.call("POST", v1+"/services", "", yamlBody()).wantError(http.StatusBadRequest, "bad_request")
	x.call("POST", v1+"/services", "x", header("Content-Type", "text/plain")).wantError(http.StatusBadRequest, "bad_request")
	d = x.call("POST", v1+"/services", ": : :\n\t- [", yamlBody()).wantError(http.StatusUnprocessableEntity, "validation_failed")
	if len(d["issues"].([]any)) == 0 {
		t.Fatal("no issues for broken YAML")
	}
	x.call("GET", v1+"/services/web", nil).wantError(http.StatusNotFound, "not_found")
}

func TestServiceListFiltersAndPaging(t *testing.T) {
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
	if got := strings.Join(names(v1+"/services"), ","); got != "alpha,bravo,charlie" {
		t.Fatalf("all = %s", got)
	}
	if got := strings.Join(names(v1+"/services?label=tier=web"), ","); got != "alpha,charlie" {
		t.Fatalf("label filter = %s", got)
	}
	if got := strings.Join(names(v1+"/services?label=tier=web&label=nope=x"), ","); got != "" {
		t.Fatalf("two labels = %s", got)
	}
	if got := strings.Join(names(v1+"/services?state=running"), ","); got != "bravo" {
		t.Fatalf("state filter = %s", got)
	}
	if got := strings.Join(names(v1+"/services?health=unknown"), ","); got != "alpha,bravo,charlie" {
		t.Fatalf("health filter = %s", got)
	}
	if got := strings.Join(names(v1+"/services?health=down"), ","); got != "" {
		t.Fatalf("down filter = %s", got)
	}

	// Paging walks the list with the opaque cursor.
	first := x.call("GET", v1+"/services?limit=2", nil).want(http.StatusOK).obj()
	cursor, _ := first["nextCursor"].(string)
	if cursor == "" || len(first["items"].([]any)) != 2 {
		t.Fatalf("first page = %v", first)
	}
	second := x.call("GET", v1+"/services?limit=2&cursor="+cursor, nil).want(http.StatusOK).obj()
	if len(second["items"].([]any)) != 1 || second["nextCursor"] != nil {
		t.Fatalf("second page = %v", second)
	}
	item := first["items"].([]any)[0].(map[string]any)
	if item["state"] != "registered" || item["version"] != float64(1) || item["deployedVersion"] != "" {
		t.Fatalf("item = %v", item)
	}

	for _, bad := range []string{"limit=0", "limit=501", "limit=x", "cursor=!!!", "label=novalue", "state=nope", "health=nope"} {
		x.call("GET", v1+"/services?"+bad, nil).wantError(http.StatusBadRequest, "bad_request")
	}
}
