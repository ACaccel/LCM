package api_test

import (
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"

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
	doc := "connections:\n  - name: home\n    type: xiaomi\n    url: " + fake.URL() + "\n    passwordSecret: router-password\n    allowActions: [reboot]\n    allowResources: [gateway]\n"
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

func TestRouterHistoryRange(t *testing.T) {
	x, _ := routerSite(t)
	x.call("POST", v1+"/resources", routerResource, yamlBody()).want(http.StatusCreated)
	// Eight samples 30 s apart, i.e. four minute points.
	for i := range 8 {
		if i > 0 {
			x.clk.Advance(30 * time.Second)
		}
		x.call("POST", v1+"/resources/gateway/observe", nil).want(http.StatusOK)
	}
	stamp := func(i int) string {
		return url.QueryEscape(epoch.Add(time.Duration(i) * 30 * time.Second).Format(time.RFC3339))
	}
	observed := func(path string) []string {
		t.Helper()
		var out []string
		for _, it := range x.call("GET", path, nil).want(http.StatusOK).items() {
			out = append(out, it["observedAt"].(string))
		}
		return out
	}
	base := v1 + "/resources/gateway/history"

	// Without a range the order and the cursor are those of the original API.
	all := observed(base)
	if len(all) != 8 || !slices.IsSorted(all) {
		t.Fatalf("unranged = %v, want 8 oldest first", all)
	}
	page := x.call("GET", base+"?limit=3", nil).want(http.StatusOK).obj()
	if cursor, _ := page["nextCursor"].(string); cursor == "" || len(page["items"].([]any)) != 3 {
		t.Fatalf("unranged page = %v", page)
	} else if next := observed(base + "?limit=3&cursor=" + cursor); next[0] != all[3] {
		t.Fatalf("unranged continuation starts at %s, want %s", next[0], all[3])
	}

	// A range is newest first, From inclusive and To exclusive.
	ranged := observed(base + "?from=" + stamp(2) + "&to=" + stamp(6))
	if len(ranged) != 4 || ranged[0] != all[5] || ranged[3] != all[2] {
		t.Fatalf("ranged = %v, want %v..%v newest first", ranged, all[5], all[2])
	}
	if got := observed(base + "?from=" + stamp(6)); len(got) != 2 || got[0] != all[7] {
		t.Fatalf("from only = %v", got)
	}

	// The cursor of a ranged page continues below its last item.
	first := x.call("GET", base+"?from="+stamp(1)+"&limit=3", nil).want(http.StatusOK).obj()
	cursor, _ := first["nextCursor"].(string)
	if cursor == "" {
		t.Fatalf("no cursor: %v", first)
	}
	if rest := observed(base + "?from=" + stamp(1) + "&limit=3&cursor=" + cursor); len(rest) != 3 || rest[0] != all[4] || rest[2] != all[2] {
		t.Fatalf("ranged continuation = %v", rest)
	}

	// A step thins the range to the newest sample of each interval; minute
	// points are placed by their minute.
	if got := observed(base + "?step=1m"); len(got) != 4 || got[0] != all[7] || got[1] != all[5] {
		t.Fatalf("step = %v", got)
	}
	if got := observed(base + "?resolution=minute&from=" + stamp(2) + "&to=" + stamp(6)); len(got) != 2 {
		t.Fatalf("minute points = %v", got)
	}

	for _, q := range []string{"from=yesterday", "to=1", "step=soon", "step=100ms", "step=-1m", "from=" + stamp(6) + "&to=" + stamp(2), "from=" + stamp(2) + "&to=" + stamp(2)} {
		x.call("GET", base+"?"+q, nil).wantError(http.StatusBadRequest, "bad_request")
	}
}

func TestRouterEventsOrder(t *testing.T) {
	x, _ := routerSite(t)
	x.call("POST", v1+"/resources", routerResource, yamlBody()).want(http.StatusCreated)
	x.call("POST", v1+"/resources/gateway/observe", nil).want(http.StatusOK)
	x.clk.Advance(time.Minute)
	body := map[string]any{"action": "reboot", "idempotencyKey": "op-1", "reason": "clear stuck WAN", "confirm": "gateway"}
	x.call("POST", v1+"/resources/gateway/operations", body, ifMatch(`"1"`)).want(http.StatusAccepted)
	base := v1 + "/resources/gateway/events"
	ids := func(path string) []float64 {
		t.Helper()
		var out []float64
		for _, e := range x.call("GET", path, nil).want(http.StatusOK).items() {
			out = append(out, e["id"].(float64))
		}
		return out
	}
	oldest := ids(base)
	if len(oldest) < 2 || !slices.IsSorted(oldest) {
		t.Fatalf("default order = %v, want oldest first", oldest)
	}
	newest := ids(base + "?order=desc")
	reversed := slices.Clone(oldest)
	slices.Reverse(reversed)
	if !slices.Equal(newest, reversed) {
		t.Fatalf("desc = %v, want %v", newest, reversed)
	}

	// The cursor of a newest-first page continues below its last event.
	var walked []float64
	path := base + "?order=desc&limit=1"
	for range len(oldest) + 1 {
		page := x.call("GET", path, nil).want(http.StatusOK).obj()
		for _, e := range page["items"].([]any) {
			walked = append(walked, e.(map[string]any)["id"].(float64))
		}
		cursor, _ := page["nextCursor"].(string)
		if cursor == "" {
			break
		}
		path = base + "?order=desc&limit=1&cursor=" + cursor
	}
	if !slices.Equal(walked, newest) {
		t.Fatalf("walked = %v, want %v", walked, newest)
	}

	// The range bounds the time the event occurred (the bounds themselves are
	// covered by the component tests).
	stamp := func(d time.Duration) string { return url.QueryEscape(epoch.Add(d).Format(time.RFC3339)) }
	if got := ids(base + "?from=" + stamp(-time.Hour)); len(got) != len(oldest) {
		t.Fatalf("from before everything = %v of %v", got, oldest)
	}
	if got := ids(base + "?from=" + stamp(time.Hour)); len(got) != 0 {
		t.Fatalf("from after everything = %v", got)
	}
	if got := ids(base + "?order=desc&to=" + stamp(-time.Hour)); len(got) != 0 {
		t.Fatalf("to before everything = %v", got)
	}
	after := stamp(30 * time.Second)
	for _, q := range []string{"order=sideways", "from=now", "to=1", "order=desc&cursor=bad", "from=" + after + "&to=" + after} {
		x.call("GET", base+"?"+q, nil).wantError(http.StatusBadRequest, "bad_request")
	}
}

func TestRouterObservationAndReboot(t *testing.T) {
	x, fake := routerSite(t)
	x.call("POST", v1+"/resources", routerResource, yamlBody()).want(http.StatusCreated)
	detail := x.call("POST", v1+"/resources/gateway/observe", nil).want(http.StatusOK).obj()
	status := detail["router"].(map[string]any)["status"].(map[string]any)
	if detail["state"] != "online" || status["health"] != "healthy" || status["fresh"] != true || status["snapshot"].(map[string]any)["outcome"] != "ok" {
		t.Fatalf("detail = %v", detail)
	}
	if ms, ok := status["reasonMessages"].([]any); !ok || len(ms) != 0 {
		t.Fatalf("reason messages = %v", status["reasonMessages"])
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
	x.call("POST", v1+"/resources/gateway/maintenance", map[string]any{"reason": "x", "previewHash": strings.Repeat("a", 64)},
		ifMatch(`"1"`)).wantError(http.StatusConflict, "unsupported")

	// One request with a reason and the typed name; nothing is sent otherwise.
	body := map[string]any{"action": "reboot", "idempotencyKey": "op-1", "reason": "clear stuck WAN", "confirm": "router"}
	x.call("POST", v1+"/resources/gateway/operations", body, ifMatch(`"1"`)).wantError(http.StatusConflict, "precondition_failed")
	body["confirm"] = "gateway"
	delete(body, "reason")
	x.call("POST", v1+"/resources/gateway/operations", body, ifMatch(`"1"`)).wantError(http.StatusConflict, "precondition_failed")
	if fake.Reboots() != 0 {
		t.Fatalf("unconfirmed request sent: %d", fake.Reboots())
	}
	body["reason"] = "clear stuck WAN"
	op := x.call("POST", v1+"/resources/gateway/operations", body, ifMatch(`"1"`)).want(http.StatusAccepted).obj()
	if op["state"] != "observing" || op["unresolved"] != true || op["requestReason"] != "clear stuck WAN" || fake.Reboots() != 1 {
		t.Fatalf("operation = %v", op)
	}
	// The same request replays the stored operation without sending again.
	replay := x.call("POST", v1+"/resources/gateway/operations", body, ifMatch(`"1"`)).want(http.StatusAccepted).obj()
	if replay["id"] != op["id"] || fake.Reboots() != 1 {
		t.Fatalf("replay = %v, reboots = %d", replay, fake.Reboots())
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
	var reasoned int
	for _, e := range evs {
		m, _ := e["reasonMessage"].(map[string]any)
		if (e["reason"] != nil) != (m != nil) || m != nil && (m["message"] != e["reason"] || m["code"] == "") {
			t.Fatalf("event %v has reason message %v", e, m)
		}
		if m != nil {
			reasoned++
		}
	}
	if reasoned == 0 {
		t.Fatalf("no event with a reason: %v", evs)
	}
	x.call("POST", v1+"/resources/gateway/operations/"+op["id"].(string)+"/resolve", map[string]any{"reason": "checked"}, ifMatch(`"1"`)).
		wantError(http.StatusConflict, "precondition_failed")
}
