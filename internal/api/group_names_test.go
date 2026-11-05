package api_test

import (
	"fmt"
	"net/http"
	"testing"
)

func TestGroupActionSelectsByName(t *testing.T) {
	x := newSite(t)
	x.createService(serviceDoc("web", "  relations: [{ type: dependsOn, target: api }]\n"))
	x.createService(serviceDoc("api", ""))
	x.createService(serviceDoc("cache", ""))
	post := func(sel map[string]any, extra map[string]any) reply {
		body := map[string]any{"selector": sel, "action": "start", "dryRun": true}
		for k, v := range extra {
			body[k] = v
		}
		return x.call("POST", v1+"/resource-groups/actions", body)
	}

	// Names alone select the group, in dependency order.
	plan := post(map[string]any{"names": []string{"web", "api"}}, nil).want(http.StatusOK).obj()
	if fmt.Sprint(stepNamesOf(plan)) != "[api web]" {
		t.Fatalf("plan = %v", plan)
	}
	if names := plan["selector"].(map[string]any)["names"]; fmt.Sprint(names) != "[web api]" {
		t.Fatalf("selector = %v", plan["selector"])
	}

	// With labels, names narrow what the labels match.
	tier := map[string]string{"tier": "web"}
	plan = post(map[string]any{"labels": tier, "names": []string{"cache"}}, nil).want(http.StatusOK).obj()
	if fmt.Sprint(stepNamesOf(plan)) != "[cache]" {
		t.Fatalf("narrowed plan = %v", plan)
	}

	// A name that is not a managed service matching the selector is refused,
	// not dropped, so the group cannot be smaller than the one reviewed.
	post(map[string]any{"names": []string{"api", "ghost"}}, nil).wantError(http.StatusConflict, "precondition_failed")
	post(map[string]any{"labels": map[string]string{"tier": "none"}, "names": []string{"api"}}, nil).wantError(http.StatusConflict, "precondition_failed")

	// A selector without labels or names, or with an empty name, is invalid.
	post(map[string]any{"names": []string{}}, nil).wantError(http.StatusUnprocessableEntity, "validation_failed")
	post(map[string]any{"names": []string{"api", ""}}, nil).wantError(http.StatusUnprocessableEntity, "validation_failed")

	// The group runs and is audited by its names.
	started := post(map[string]any{"names": []string{"web", "api"}}, map[string]any{"dryRun": false}).want(http.StatusAccepted).obj()
	g := x.groupDone(started["id"].(string))
	if g["status"] != "succeeded" || stepSummary(g) != "[api:succeeded web:succeeded]" {
		t.Fatalf("group = %v", g)
	}
	if d := x.auditDetail("service.group-action", "services:api,web"); fmt.Sprint(d["names"]) != "[web api]" {
		t.Fatalf("group audit = %v", d)
	}
}
