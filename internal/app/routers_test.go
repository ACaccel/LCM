package app_test

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/ACaccel/lcm/internal/app"
	"github.com/ACaccel/lcm/internal/auth"
	"github.com/ACaccel/lcm/internal/clock"
	"github.com/ACaccel/lcm/internal/config"
	"github.com/ACaccel/lcm/internal/engine"
	"github.com/ACaccel/lcm/internal/model"
	"github.com/ACaccel/lcm/internal/monitor"
	"github.com/ACaccel/lcm/internal/router/xiaomi/xiaomitest"
)

// writeConnections writes a secrets directory with the fake router's
// password and a connections file with one connection, "home", to url.
func writeConnections(t *testing.T, url, extra string) (secrets, file string) {
	t.Helper()
	dir := t.TempDir()
	secrets = filepath.Join(dir, "secrets")
	if err := os.Mkdir(secrets, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(secrets, "router-password"), []byte(xiaomitest.DefaultPassword), 0o600); err != nil {
		t.Fatal(err)
	}
	file = filepath.Join(dir, "connections.yaml")
	doc := "connections:\n  - name: home\n    type: xiaomi\n    url: " + url + "\n    passwordSecret: router-password\n    allowActions: [reboot]\n    allowResources: [gateway]\n" + extra
	if err := os.WriteFile(file, []byte(doc), 0o600); err != nil {
		t.Fatal(err)
	}
	return secrets, file
}

// A router is a resource like any other: it is observed through the API,
// its native health reaches policies as health.changed signals, and a
// reboot is a durable operation that succeeds on an uptime reset and turns
// indeterminate when no reboot evidence arrives before the deadline.
func TestRouterObservationHealthAndReboot(t *testing.T) {
	clk := clock.NewFake(epoch)
	fake := xiaomitest.New(t, clk)
	secrets, file := writeConnections(t, fake.URL(), "")
	h := newHarness(t, func(o *testOpts) {
		o.clk = clk
		o.configure = func(c *config.Config) { c.SecretsDir, c.ConnectionsFile = secrets, file }
	})
	_, admin, err := h.app.Auth.CreateToken(h.ctx, "router-admin", auth.ScopeAdmin)
	if err != nil {
		t.Fatal(err)
	}
	call := func(method, path string, body any, version string, want int) map[string]any {
		t.Helper()
		var raw []byte
		if body != nil {
			raw, _ = json.Marshal(body)
		}
		r := httptest.NewRequest(method, "/api/v1"+path, strings.NewReader(string(raw)))
		r.Header.Set("Content-Type", "application/json")
		r.Header.Set("Authorization", "Bearer "+admin)
		if version != "" {
			r.Header.Set("If-Match", version)
		}
		w := httptest.NewRecorder()
		h.app.Handler().ServeHTTP(w, r)
		if w.Code != want {
			t.Fatalf("%s %s: got %d want %d: %s", method, path, w.Code, want, w.Body.String())
		}
		var out map[string]any
		if w.Body.Len() > 0 {
			if err := json.Unmarshal(w.Body.Bytes(), &out); err != nil {
				t.Fatal(err)
			}
		}
		return out
	}

	h.putService("apiVersion: lcm/v2\nkind: Resource\nmetadata: { name: gateway, labels: { site: home } }\nspec:\n  type: router\n  adapter: xiaomi\n  connection: home\n  config: { interval: 60s }\n")
	h.deploy(`
apiVersion: lcm/v2
kind: Policy
metadata: { name: router-down }
spec:
  nodes:
    - id: t
      type: trigger.signal
      props: { kinds: [health.changed], types: [router], statuses: [down] }
      next: { out: [seen] }
    - id: seen
      type: data.set
      props:
        values:
          name: "{{ trigger.resource }}"
          type: "{{ trigger.resourceType }}"
          state: "{{ resource[trigger.resource].state }}"
`)
	h.syncTriggers()

	detail := call("POST", "/resources/gateway/observe", nil, "", http.StatusOK)
	if detail["state"] != "online" || detail["router"].(map[string]any)["status"].(map[string]any)["health"] != "healthy" {
		t.Fatalf("observed = %v", detail)
	}
	if st, ok := h.app.Monitor.Resource("gateway"); !ok || st.Type != model.ResourceRouter || st.Health != monitor.HealthHealthy {
		t.Fatalf("monitor = %+v", st)
	}

	// Reboot with an unknown answer (the router drops the connection) and
	// a two-minute outage: the reset uptime proves the reboot.
	fake.SetReboot(xiaomitest.RebootAcceptAndDrop, 2*time.Minute)
	op := call("POST", "/resources/gateway/operations", map[string]any{"action": "reboot", "idempotencyKey": "reboot-1"}, `"1"`, http.StatusAccepted)
	if op["state"] != "observing" || op["receipt"].(map[string]any)["outcome"] != "unknown" {
		t.Fatalf("operation = %v", op)
	}
	h.advance(time.Minute)
	call("POST", "/resources/gateway/observe", nil, "", http.StatusOK)
	view := h.waitRun("router-down", map[string]bool{})
	if view.Status != engine.RunSucceeded {
		t.Fatalf("run = %+v", view)
	}
	n, _ := view.Node("seen")
	if out, _ := json.Marshal(n.Output); !strings.Contains(string(out), `"type":"router"`) || !strings.Contains(string(out), `"state":"offline"`) {
		t.Fatalf("policy saw %s", out)
	}
	h.advance(2 * time.Minute)
	call("POST", "/resources/gateway/observe", nil, "", http.StatusOK)
	got := call("GET", "/resources/gateway/operations/"+op["id"].(string), nil, "", http.StatusOK)
	if got["state"] != "succeeded" || got["unresolved"] != false || got["evidence"] == nil {
		t.Fatalf("after reboot = %v", got)
	}

	// A router that stays away past the deadline leaves the outcome open.
	fake.SetReboot(xiaomitest.RebootAccept, time.Hour)
	op = call("POST", "/resources/gateway/operations", map[string]any{"action": "reboot", "idempotencyKey": "reboot-2"}, `"1"`, http.StatusAccepted)
	h.advance(11 * time.Minute)
	call("POST", "/resources/gateway/observe", nil, "", http.StatusOK)
	got = call("GET", "/resources/gateway/operations/"+op["id"].(string), nil, "", http.StatusOK)
	if got["state"] != "indeterminate" || got["unresolved"] != true {
		t.Fatalf("after deadline = %v", got)
	}
	detail = call("GET", "/resources/gateway", nil, "", http.StatusOK)
	if o, _ := detail["operation"].(map[string]any); o == nil || o["id"] != op["id"] {
		t.Fatalf("open operation = %v", detail["operation"])
	}
	call("POST", "/resources/gateway/operations/"+op["id"].(string)+"/resolve", map[string]any{"reason": "power-cycled by hand"}, `"1"`, http.StatusOK)
	if fake.Reboots() != 2 {
		t.Fatalf("reboots = %d", fake.Reboots())
	}

	conns := call("GET", "/connections", nil, "", http.StatusOK)
	items := conns["items"].([]any)
	if len(items) != 1 || items[0].(map[string]any)["resources"].([]any)[0] != "gateway" {
		t.Fatalf("connections = %v", conns)
	}
}

// The observation loop starts with Run and follows resource changes.
func TestRouterLoopRunsWithApp(t *testing.T) {
	clk := clock.NewFake(epoch)
	fake := xiaomitest.New(t, clk)
	secrets, file := writeConnections(t, fake.URL(), "")
	h := newHarness(t, func(o *testOpts) {
		o.clk = clk
		o.configure = func(c *config.Config) { c.SecretsDir, c.ConnectionsFile = secrets, file }
	})
	h.run()
	h.putService("apiVersion: lcm/v2\nkind: Resource\nmetadata: { name: gateway }\nspec: { type: router, adapter: xiaomi, connection: home }\n")
	ctx, cancel := context.WithTimeout(context.Background(), waitTimeout)
	defer cancel()
	sub := h.app.Bus.Subscribe("router-test", 16)
	defer sub.Close()
	for {
		if r, err := h.app.Routers.Get(h.ctx, "gateway"); err == nil && r.Status.Fresh {
			break
		}
		select {
		case <-sub.C:
		case <-ctx.Done():
			t.Fatal("the router loop did not observe the new router")
		}
	}
	if fake.Logins() != 1 {
		t.Fatalf("logins = %d", fake.Logins())
	}
}

func TestNewRejectsInvalidConnections(t *testing.T) {
	secrets, file := writeConnections(t, "http://admin:pw@192.0.2.1", "")
	cfg := config.Default()
	cfg.DataDir = filepath.Join(t.TempDir(), "d")
	cfg.Listen = "127.0.0.1:0"
	cfg.SecretsDir, cfg.ConnectionsFile = secrets, file
	trust := app.WithConnectionsTrust(trustTestFile)
	_, err := app.New(context.Background(), cfg, app.WithLogger(testLogger(t)), trust)
	if err == nil || !strings.Contains(err.Error(), "credentials") || strings.Contains(err.Error(), "pw@") {
		t.Fatalf("err = %v", err)
	}
	_, file = writeConnections(t, "http://192.0.2.1", "")
	cfg.SecretsDir, cfg.ConnectionsFile = t.TempDir(), file
	if _, err := app.New(context.Background(), cfg, app.WithLogger(testLogger(t)), trust); err == nil || !strings.Contains(err.Error(), "cannot be resolved") {
		t.Fatalf("err = %v", err)
	}
	// A password secret that other users can read is refused, not used.
	secrets, file = writeConnections(t, "http://192.0.2.1", "")
	if err := os.Chmod(filepath.Join(secrets, "router-password"), 0o640); err != nil { //nolint:gosec // the test checks that a group-readable secret is refused
		t.Fatal(err)
	}
	cfg.SecretsDir, cfg.ConnectionsFile = secrets, file
	if _, err := app.New(context.Background(), cfg, app.WithLogger(testLogger(t)), trust); err == nil || !strings.Contains(err.Error(), "cannot be resolved") {
		t.Fatalf("err = %v", err)
	}
	// Without the test override, a file the LCM account could rewrite stops
	// startup.
	secrets, file = writeConnections(t, "http://192.0.2.1", "")
	cfg.SecretsDir, cfg.ConnectionsFile = secrets, file
	if _, err := app.New(context.Background(), cfg, app.WithLogger(testLogger(t))); err == nil || !strings.Contains(err.Error(), "connections file") {
		t.Fatalf("err = %v", err)
	}
}

// trustTestFile stands in for the root ownership check of the connections
// file, which files created by tests cannot pass.
func trustTestFile(string, os.FileInfo) error { return nil }
