package main

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

// The output of an instance action names the instance.
func TestResourceActionNamesTheInstance(t *testing.T) {
	var bodies []map[string]any
	stub := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		switch {
		case r.Method == http.MethodGet && strings.HasSuffix(r.URL.Path, "/impact"):
			_, _ = w.Write([]byte(`{"dependents":[],"controlDependents":[],"acknowledgementRequired":false}`))
		case r.URL.Query().Get("dryRun") == "true":
			_, _ = w.Write([]byte(`{"from":"running","to":"running","steps":["stop and disable bots@alpha.service"],"impact":{"dependents":[],"controlDependents":[]}}`))
		default:
			var b map[string]any
			_ = json.NewDecoder(r.Body).Decode(&b)
			bodies = append(bodies, b)
			w.WriteHeader(http.StatusAccepted)
			_, _ = w.Write([]byte(`{"actionId":"act_1","status":"running"}`))
		}
	}))
	defer stub.Close()
	run := func(args ...string) string {
		var out, errb bytes.Buffer
		vars := map[string]string{"LCM_SERVER": stub.URL, "LCM_TOKEN": "lcm_test"}
		a := &app{stdin: strings.NewReader(""), stdout: &out, stderr: &errb, getenv: func(k string) string { return vars[k] }, poll: time.Millisecond}
		if code := a.run(context.Background(), args); code != 0 {
			t.Fatalf("%v: exit %d\n%s%s", args, code, out.String(), errb.String())
		}
		return out.String()
	}
	if out := run("resource", "action", "bots", "stop", "--instance", "alpha", "--dry-run"); !strings.Contains(out, "plan: stop bots instance alpha (running -> running)") {
		t.Fatalf("dry run output %q", out)
	}
	if out := run("resource", "action", "bots", "stop", "--instance", "alpha"); !strings.Contains(out, "action act_1 started (stop bots instance alpha)") {
		t.Fatalf("output %q", out)
	}
	if len(bodies) != 1 || bodies[0]["instance"] != "alpha" {
		t.Fatalf("bodies %v", bodies)
	}
}
