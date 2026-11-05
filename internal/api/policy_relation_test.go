package api_test

import (
	"net/http"
	"testing"
)

func watchPolicy(props string) string {
	return "    - { id: t, type: trigger.signal, props: " + props + ", next: { out: [echo] } }\n" +
		"    - { id: echo, type: data.set, props: { values: { a: 1 } } }\n"
}

func TestPolicyListFilteredByResource(t *testing.T) {
	x := newSite(t)
	x.createService(serviceDoc("web", ""))
	x.createService(serviceDoc("db", ""))
	x.call("POST", v1+"/templates/restart-on-down/instantiate",
		map[string]any{"name": "web-restart", "params": map[string]any{"resource": "web"}}).want(http.StatusCreated)
	x.putPolicy("watch-web", watchPolicy("{ resources: [web] }"))
	x.putPolicy("watch-db", watchPolicy("{ resources: [db] }"))
	x.putPolicy("by-label", watchPolicy("{ labels: { tier: web } }"))

	if all := x.call("GET", v1+"/policies", nil).want(http.StatusOK).items(); len(all) != 4 || all[0]["relation"] != nil {
		t.Fatalf("unfiltered list = %v", all)
	}
	got := map[string]map[string]any{}
	for _, p := range x.call("GET", v1+"/policies?resource=web", nil).want(http.StatusOK).items() {
		got[p["name"].(string)] = p["relation"].(map[string]any)
	}
	if len(got) != 3 || got["watch-db"] != nil {
		t.Fatalf("filtered = %v", got)
	}
	if got["web-restart"]["kind"] != "acts" || len(got["web-restart"]["types"].([]any)) == 0 ||
		got["watch-web"]["kind"] != "watches" || got["by-label"]["kind"] != "watches" {
		t.Fatalf("relations = %v", got)
	}
	x.call("GET", v1+"/policies?resource=ghost", nil).wantError(http.StatusNotFound, "not_found")
}
