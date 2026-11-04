package api_test

import (
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/ACaccel/lcm/internal/clock"
	"github.com/ACaccel/lcm/internal/config"
	"github.com/ACaccel/lcm/internal/router/xiaomi/xiaomitest"
)

// routerSite is a site with one connection, "home", to a fake router.
func routerSite(t *testing.T) (*site, *xiaomitest.Router) {
	t.Helper()
	fake := xiaomitest.New(t, clock.NewFake(epoch))
	dir := t.TempDir()
	secrets := filepath.Join(dir, "secrets")
	if err := os.Mkdir(secrets, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(secrets, "router-password"), []byte(xiaomitest.DefaultPassword), 0o600); err != nil {
		t.Fatal(err)
	}
	conns := filepath.Join(dir, "connections.yaml")
	doc := "connections:\n  - name: home\n    type: xiaomi\n    url: " + fake.URL() + "\n    passwordSecret: router-password\n    allowActions: [reboot]\n"
	if err := os.WriteFile(conns, []byte(doc), 0o600); err != nil {
		t.Fatal(err)
	}
	x := newSite(t, func(c *config.Config) { c.SecretsDir, c.ConnectionsFile = secrets, conns })
	return x, fake
}

const routerResource = "apiVersion: lcm/v2\nkind: Resource\nmetadata: { name: gateway }\nspec:\n  type: router\n  adapter: xiaomi\n  connection: home\n"

func TestConnectionsAndRouterValidation(t *testing.T) {
	x, fake := routerSite(t)
	items := x.call("GET", v1+"/connections", nil).want(http.StatusOK).items()
	if len(items) != 1 || items[0]["name"] != "home" || items[0]["type"] != "xiaomi" || len(items[0]["resources"].([]any)) != 0 {
		t.Fatalf("connections = %v", items)
	}
	bad := strings.Replace(routerResource, "connection: home", "connection: absent", 1)
	details := x.call("POST", v1+"/resources", bad, yamlBody()).wantError(http.StatusUnprocessableEntity, "validation_failed")
	issues := details["issues"].([]any)
	if len(issues) != 1 || issues[0].(map[string]any)["path"] != "spec.connection" {
		t.Fatalf("issues = %v", issues)
	}
	x.call("POST", v1+"/resources", routerResource, yamlBody()).want(http.StatusCreated)
	r := x.call("GET", v1+"/connections", nil).want(http.StatusOK)
	if items := r.items(); len(items[0]["resources"].([]any)) != 1 || items[0]["resources"].([]any)[0] != "gateway" {
		t.Fatalf("connections = %v", items)
	}
	// Addresses and secrets stay on the host.
	if body := string(r.Body); strings.Contains(body, strings.TrimPrefix(fake.URL(), "http://")) || strings.Contains(body, "router-password") {
		t.Fatalf("connection list leaks host configuration: %s", body)
	}
	x.anon("GET", v1+"/connections", nil).want(http.StatusUnauthorized)
	x.call("GET", v1+"/connections", nil, bearer(x.ingest)).want(http.StatusForbidden)
}

func TestRouterObservationAndReboot(t *testing.T) {
	x, fake := routerSite(t)
	x.call("POST", v1+"/resources", routerResource, yamlBody()).want(http.StatusCreated)
	detail := x.call("POST", v1+"/resources/gateway/observe", nil).want(http.StatusOK).obj()
	status := detail["router"].(map[string]any)["status"].(map[string]any)
	if detail["state"] != "online" || status["health"] != "healthy" || status["fresh"] != true || status["snapshot"].(map[string]any)["outcome"] != "ok" {
		t.Fatalf("detail = %v", detail)
	}
	hist := x.call("GET", v1+"/resources/gateway/history", nil).want(http.StatusOK).items()
	if len(hist) != 1 || hist[0]["type"] != "router" || hist[0]["router"].(map[string]any)["clients"] != float64(5) {
		t.Fatalf("history = %v", hist)
	}
	x.call("GET", v1+"/resources/gateway/history?resolution=minute", nil).want(http.StatusOK)
	caps := x.call("GET", v1+"/resources/gateway/capabilities", nil).want(http.StatusOK).items()
	if len(caps) != 1 || caps[0]["action"] != "reboot" || caps[0]["available"] != true {
		t.Fatalf("capabilities = %v", caps)
	}
	x.call("POST", v1+"/resources/gateway/maintenance", map[string]any{"reason": "x", "acknowledged": true, "previewHash": strings.Repeat("a", 64)},
		ifMatch(`"1"`)).wantError(http.StatusConflict, "unsupported")

	op := x.call("POST", v1+"/resources/gateway/operations", map[string]any{"action": "reboot", "idempotencyKey": "op-1"}, ifMatch(`"1"`)).
		want(http.StatusAccepted).obj()
	if op["state"] != "observing" || op["unresolved"] != true || fake.Reboots() != 1 {
		t.Fatalf("operation = %v", op)
	}
	got := x.call("GET", v1+"/resources/gateway/operations/"+op["id"].(string), nil).want(http.StatusOK).obj()
	if got["id"] != op["id"] {
		t.Fatalf("operation = %v", got)
	}
	detail = x.call("GET", v1+"/resources/gateway", nil).want(http.StatusOK).obj()
	if o, _ := detail["operation"].(map[string]any); o == nil || o["id"] != op["id"] {
		t.Fatalf("open operation = %v", detail["operation"])
	}
	evs := x.call("GET", v1+"/resources/gateway/events", nil).want(http.StatusOK).items()
	if len(evs) == 0 || evs[0]["kind"] != "router.operation.submit" {
		t.Fatalf("events = %v", evs)
	}
	x.call("POST", v1+"/resources/gateway/operations/"+op["id"].(string)+"/resolve", map[string]any{"reason": "checked"}, ifMatch(`"1"`)).
		wantError(http.StatusConflict, "precondition_failed")
}
