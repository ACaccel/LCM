package api_test

import (
	"net/http"
	"testing"
)

func TestAlertStateFilter(t *testing.T) {
	x := newSite(t)
	x.createService(serviceDoc("web", ""))
	hook := func(body string) {
		x.call("POST", v1+"/hooks/alerts/generic", body, bearer(x.ingest)).want(http.StatusAccepted)
	}
	hook(`{"service":"web","alert":"Latency","status":"firing","severity":"warning"}`)
	hook(`{"service":"web","alert":"Errors","status":"firing","severity":"critical"}`)
	hook(`{"service":"web","alert":"Latency","status":"resolved"}`)

	names := func(query string) []string {
		var out []string
		for _, a := range x.call("GET", v1+"/alerts"+query, nil).want(http.StatusOK).items() {
			out = append(out, a["name"].(string))
		}
		return out
	}
	if got := names(""); len(got) != 1 || got[0] != "Errors" {
		t.Fatalf("default = %v, want only the firing alert", got)
	}
	resolved := x.call("GET", v1+"/alerts?state=resolved", nil).want(http.StatusOK).items()
	if len(resolved) != 1 || resolved[0]["name"] != "Latency" || resolved[0]["resolvedAt"] == nil {
		t.Fatalf("resolved = %v", resolved)
	}
	if got := names("?state=all"); len(got) != 2 || got[0] != "Errors" || got[1] != "Latency" {
		t.Fatalf("all = %v, want firing first", got)
	}
	if got := names("?state=all&resource=db"); len(got) != 0 {
		t.Fatalf("other resource = %v", got)
	}
	x.call("GET", v1+"/alerts?state=bogus", nil).wantError(http.StatusBadRequest, "bad_request")
}

func TestAuditFilters(t *testing.T) {
	x := newSite(t)
	x.createService(serviceDoc("web", ""))
	x.createService(serviceDoc("db", ""))

	list := func(query string) []map[string]any {
		return x.call("GET", v1+"/audit"+query, nil).want(http.StatusOK).items()
	}
	all := list("")
	if len(all) < 2 {
		t.Fatalf("audit = %v", all)
	}
	web := list("?target=web")
	if len(web) == 0 {
		t.Fatalf("no entries for web in %v", all)
	}
	for _, e := range web {
		if e["target"] != "web" {
			t.Errorf("target filter returned %v", e)
		}
	}
	action := web[0]["action"].(string)
	for _, e := range list("?action=" + action) {
		if e["action"] != action {
			t.Errorf("action filter returned %v", e)
		}
	}
	actor := web[0]["actor"].(map[string]any)["name"].(string)
	if got := list("?actor=" + actor); len(got) != len(all) {
		t.Errorf("actor filter returned %d of %d entries", len(got), len(all))
	}
	if got := list("?actor=nobody"); len(got) != 0 {
		t.Errorf("unknown actor returned %v", got)
	}
	// Everything was recorded at the fake clock's epoch.
	if got := list("?from=2026-03-01T12:00:00Z&to=2026-03-01T12:00:01Z"); len(got) != len(all) {
		t.Errorf("time window returned %d of %d entries", len(got), len(all))
	}
	if got := list("?to=2026-03-01T12:00:00Z"); len(got) != 0 {
		t.Errorf("window before the epoch returned %v", got)
	}
	x.call("GET", v1+"/audit?from=yesterday", nil).wantError(http.StatusBadRequest, "bad_request")
	x.call("GET", v1+"/audit?from=2026-03-02T00:00:00Z&to=2026-03-01T00:00:00Z", nil).wantError(http.StatusBadRequest, "bad_request")
}
