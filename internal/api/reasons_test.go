package api_test

import (
	"net/http"
	"testing"
)

// reasonCode returns the code of the message in field of m and fails when
// its English text differs from the plain reason.
func reasonCode(t *testing.T, m map[string]any, field string) string {
	t.Helper()
	msg, ok := m[field].(map[string]any)
	if !ok {
		t.Fatalf("%s missing from %v", field, m)
	}
	if msg["message"] != m["reason"] {
		t.Fatalf("%s = %v, want the text of reason %q", field, msg, m["reason"])
	}
	code, _ := msg["code"].(string)
	return code
}

func TestReasonsCarryCodes(t *testing.T) {
	x, _ := routerSite(t)
	x.call("POST", v1+"/resources", routerResource, yamlBody()).want(http.StatusCreated)
	x.call("POST", v1+"/resources/gateway/observe", nil).want(http.StatusOK)

	body := map[string]any{"action": "reboot", "idempotencyKey": "op-1", "reason": "clear stuck WAN", "confirm": "gateway"}
	op := x.call("POST", v1+"/resources/gateway/operations", body, ifMatch(`"1"`)).want(http.StatusAccepted).obj()
	if code := reasonCode(t, op, "reasonMessage"); code != "op.routerAccepted" {
		t.Errorf("operation reason code = %q", code)
	}

	// With the operation unresolved, reboot is refused and says why.
	caps := x.call("GET", v1+"/resources/gateway/capabilities", nil).want(http.StatusOK).items()
	if len(caps) != 1 || caps[0]["available"] != false {
		t.Fatalf("capabilities = %v", caps)
	}
	if code := reasonCode(t, caps[0], "reasonMessage"); code != "action.operationUnresolved" {
		t.Errorf("capability reason code = %q", code)
	}
	detail := x.call("GET", v1+"/resources/gateway", nil).want(http.StatusOK).obj()
	var seen bool
	for _, a := range detail["actions"].([]any) {
		action := a.(map[string]any)
		if action["reason"] == "" {
			if m := action["reasonMessage"].(map[string]any); m["code"] != "" {
				t.Errorf("available action %v has a reason message", action)
			}
			continue
		}
		if action["name"] == "retire" {
			seen = true
			if code := reasonCode(t, action, "reasonMessage"); code != "action.resolveFirst" {
				t.Errorf("retire reason code = %q", code)
			}
		}
	}
	if !seen {
		t.Errorf("retire was not blocked in %v", detail["actions"])
	}

	// A snapshot without a reason carries no message.
	hist := x.call("GET", v1+"/resources/gateway/history", nil).want(http.StatusOK).items()
	if snap := hist[0]["router"].(map[string]any); snap["reasonMessage"] != nil {
		t.Errorf("a successful snapshot has a reason message: %v", snap)
	}
}
