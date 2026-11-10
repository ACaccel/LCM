package app_test

import (
	"encoding/json"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/ACaccel/lcm/internal/api"
	"github.com/ACaccel/lcm/internal/catalog"
	"github.com/ACaccel/lcm/internal/deployhelper"
	"github.com/ACaccel/lcm/internal/hostsetup"
)

const routeWeb = `
apiVersion: lcm/v2
kind: Resource
metadata: { name: web }
spec:
  type: service
  adapter: external
  config:
    route:
      proxy:
        - { path: api/, upstream: /api/, bodyLimit: 3m }
`

func ingressCalls(h *fakeDeployHelper) []deployhelper.Request {
	var out []deployhelper.Request
	for _, r := range h.Requests() {
		if r.Op == deployhelper.OpIngressApply {
			out = append(out, r)
		}
	}
	return out
}

func routeOf(t *testing.T, h *harness, srv *httptest.Server) map[string]any {
	t.Helper()
	body := apiCall(t, h, srv, "GET", "/api/v1/resources/web")
	route, _ := body["service"].(map[string]any)["route"].(map[string]any)
	return route
}

// TestServiceRouteThroughTheApp: a saved service document whose route
// changed reaches the helper once; a refusal is shown on the service and
// never rejects the document.
func TestServiceRouteThroughTheApp(t *testing.T) {
	helper := newFakeDeployHelper()
	h := newHarness(t, func(o *testOpts) { o.deployHelper = helper })
	srv := httptest.NewServer(h.app.Handler())
	defer srv.Close()

	h.putService(routeWeb)
	if err := h.app.SyncRoutes(h.ctx); err != nil {
		t.Fatal(err)
	}
	calls := ingressCalls(helper)
	if len(calls) != 1 || calls[0].Service != "web" || calls[0].Route == nil || len(calls[0].Route.Proxy) != 1 || calls[0].Route.Proxy[0].BodyLimit != "3m" {
		t.Fatalf("calls = %+v", calls)
	}
	route := routeOf(t, h, srv)
	if route["state"] != "applied" || route["current"] != true || route["configured"] != true || route["operationId"] != calls[0].OperationID {
		t.Fatalf("route = %v", route)
	}

	// Saving a document whose route did not change sends nothing.
	if _, err := h.app.Resources.Put(h.ctx, []byte(strings.Replace(routeWeb, "name: web }", "name: web, labels: { tier: web } }", 1)), 1); err != nil {
		t.Fatal(err)
	}
	if err := h.app.SyncRoutes(h.ctx); err != nil {
		t.Fatal(err)
	}
	if n := len(ingressCalls(helper)); n != 1 {
		t.Fatalf("calls after an unchanged route = %d", n)
	}

	// A changed route is sent again.
	changed := strings.Replace(routeWeb, "bodyLimit: 3m", "bodyLimit: 5m", 1)
	if _, err := h.app.Resources.Put(h.ctx, []byte(changed), 2); err != nil {
		t.Fatal(err)
	}
	if err := h.app.SyncRoutes(h.ctx); err != nil {
		t.Fatal(err)
	}
	if calls = ingressCalls(helper); len(calls) != 2 || calls[1].Route.Proxy[0].BodyLimit != "5m" {
		t.Fatalf("calls = %+v", calls)
	}

	// The host refuses: the write succeeds, the reason is on the service.
	helper.mu.Lock()
	helper.routeRefusal = "service web has no route in host.json"
	helper.mu.Unlock()
	if _, err := h.app.Resources.Put(h.ctx, []byte(strings.Replace(changed, "bodyLimit: 5m", "bodyLimit: 7m", 1)), 3); err != nil {
		t.Fatalf("a refused route must not reject the document: %v", err)
	}
	if err := h.app.SyncRoutes(h.ctx); err != nil {
		t.Fatal(err)
	}
	route = routeOf(t, h, srv)
	if route["state"] != "refused" || route["current"] != true || !strings.Contains(route["message"].(string), "no route in host.json") {
		t.Fatalf("route = %v", route)
	}
	rec, err := h.app.Catalog.Get(h.ctx, "web")
	if err != nil || rec.Version != 4 {
		t.Fatalf("document = %+v, %v", rec, err)
	}

	// Once the host agrees, the explicit action applies it again.
	helper.mu.Lock()
	helper.routeRefusal = ""
	helper.mu.Unlock()
	if _, err := h.app.Manager.Do(h.ctx, "web", catalog.ActionApplyRoute, catalog.ActionOptions{}); err != nil {
		t.Fatal(err)
	}
	if route = routeOf(t, h, srv); route["state"] != "applied" {
		t.Fatalf("route = %v", route)
	}
	var audited int
	for _, e := range h.auditEntries() {
		if e.Action == "service.apply-route" {
			audited++
		}
	}
	// Four sends: created, changed, refused (system) and the action.
	if audited != 4 {
		t.Fatalf("audited route applies = %d", audited)
	}
}

// TestServiceRouteWithoutHelper: without the deploy helper nothing is sent
// and the service says why.
func TestServiceRouteWithoutHelper(t *testing.T) {
	h := newHarness(t)
	srv := httptest.NewServer(h.app.Handler())
	defer srv.Close()
	h.putService(routeWeb)
	if err := h.app.SyncRoutes(h.ctx); err != nil {
		t.Fatal(err)
	}
	route := routeOf(t, h, srv)
	if route["state"] != "skipped" || route["current"] != true || !strings.Contains(route["message"].(string), "deploy helper") {
		t.Fatalf("route = %v", route)
	}
	r := actionReasons(apiCall(t, h, srv, "GET", "/api/v1/resources/web"))
	if r["apply-route"] != catalog.ReasonHelperUnavailable {
		t.Fatalf("reasons = %v", r)
	}
}

// TestHostComponentsShowTheIngressStatus: the helper's view of the site
// (rendered against installed digest, last result) completes the drift view.
func TestHostComponentsShowTheIngressStatus(t *testing.T) {
	helper := newFakeDeployHelper()
	rendered, installed := strings.Repeat("a", 64), strings.Repeat("b", 64)
	helper.ingress = &hostsetup.IngressStatus{
		Rendered: rendered, Installed: installed, Enabled: true, TLS: true,
		Last:   &hostsetup.LastApply{At: 1_700_000_000_000, Outcome: "failed", Message: "nginx -t failed"},
		Routes: []hostsetup.RouteStatus{{Service: "web", Prefix: "/web/", Options: true}},
	}
	var status api.HostStatusFunc = func() ([]hostsetup.ComponentStatus, error) {
		return []hostsetup.ComponentStatus{{Name: hostsetup.ComponentIngress, State: hostsetup.StateUpToDate, Files: []hostsetup.FileStatus{
			{Path: hostsetup.RenewalHook, State: hostsetup.StateUpToDate},
			{Path: hostsetup.SiteFile, State: hostsetup.StateUpToDate, Installed: installed},
		}}}, nil
	}
	h := newHarness(t, func(o *testOpts) { o.deployHelper, o.hostStatus = helper, status })
	srv := httptest.NewServer(h.app.Handler())
	defer srv.Close()

	body := apiCall(t, h, srv, "GET", "/api/v1/host/components")
	raw, _ := json.Marshal(body)
	var got struct {
		Items []struct {
			Name, State string
			Files       []struct{ Path, State, Installed, Embedded, Detail string }
		}
	}
	if err := json.Unmarshal(raw, &got); err != nil || len(got.Items) != 1 {
		t.Fatalf("components = %s, %v", raw, err)
	}
	c := got.Items[0]
	site := c.Files[1]
	if c.State != "outdated" || site.State != "outdated" || site.Installed != installed[:12] || site.Embedded != rendered[:12] ||
		!strings.Contains(site.Detail, "differs from the site host.json renders") || !strings.Contains(site.Detail, "last apply") ||
		!strings.Contains(site.Detail, "nginx -t failed") {
		t.Fatalf("component = %+v", c)
	}
}

// TestServiceRouteLoop: the running app sends a saved route by itself and
// retries the routes of earlier starts.
func TestServiceRouteLoop(t *testing.T) {
	helper := newFakeDeployHelper()
	called := make(chan string, 8)
	inner := helper.Handle
	helper.Handle = func(req deployhelper.Request) (deployhelper.Response, error) {
		resp, err := inner(req)
		if req.Op == deployhelper.OpIngressApply {
			called <- req.Service
		}
		return resp, err
	}
	h := newHarness(t, func(o *testOpts) { o.deployHelper = helper })
	h.putService(routeWeb)
	stop := h.run()
	select {
	case name := <-called:
		if name != "web" {
			t.Fatalf("service = %q", name)
		}
	case <-time.After(waitTimeout):
		t.Fatal("the route was not sent")
	}
	if err := stop(); err != nil {
		t.Fatal(err)
	}
}
