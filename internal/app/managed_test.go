package app_test

import (
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"slices"
	"strings"
	"sync"
	"testing"

	"github.com/ACaccel/lcm/internal/auth"
	"github.com/ACaccel/lcm/internal/catalog"
	"github.com/ACaccel/lcm/internal/deployhelper"
	"github.com/ACaccel/lcm/internal/execx"
	"github.com/ACaccel/lcm/internal/hostsetup"
	"github.com/ACaccel/lcm/internal/model"
	rt "github.com/ACaccel/lcm/internal/runtime"
)

// fakeDeployHelper answers like the deploy helper: builds become releases
// and activate makes one current.
type fakeDeployHelper struct {
	deployhelper.Fake
	mu       sync.Mutex
	n        int
	commits  map[string]string
	releases []deployhelper.Release
	current  string
}

func newFakeDeployHelper() *fakeDeployHelper {
	f := &fakeDeployHelper{commits: map[string]string{}}
	f.Handle = f.handle
	return f
}

func (f *fakeDeployHelper) Ready() error { return nil }

func (f *fakeDeployHelper) handle(req deployhelper.Request) (deployhelper.Response, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	resp := deployhelper.Response{Version: deployhelper.Version, Op: req.Op, OperationID: req.OperationID}
	if req.Op.Changes() {
		resp.State = deployhelper.StateSucceeded
	}
	f.n++
	id := fmt.Sprintf("20261101T1200%02dZ-%08x", f.n, f.n)
	switch req.Op {
	case deployhelper.OpBuild:
		f.commits[id] = req.Commit
		resp.Build = &deployhelper.BuildResult{BuildID: id, Commit: req.Commit}
	case deployhelper.OpInstall:
		m := deployhelper.Manifest{Version: 1, Service: req.Service, ReleaseID: id, BuildID: req.BuildID, Commit: f.commits[req.BuildID]}
		f.releases = append([]deployhelper.Release{{ReleaseID: id, Manifest: &m}}, f.releases...)
		resp.Install = &deployhelper.InstallResult{ReleaseID: id, Manifest: m}
	case deployhelper.OpActivate:
		resp.Activate = &deployhelper.ActivateResult{ReleaseID: req.ReleaseID, Previous: f.current, Switched: true}
		f.current = req.ReleaseID
	case deployhelper.OpStatus:
		resp.Unit = &deployhelper.UnitStatus{LoadState: "loaded", ActiveState: "active", SubState: "running", Release: f.current}
	case deployhelper.OpReleases:
		resp.Releases = append([]deployhelper.Release{}, f.releases...)
	}
	return resp, nil
}

const managedWeb = `
apiVersion: lcm/v2
kind: Resource
metadata: { name: web }
spec:
  type: service
  adapter: managed
  config:
    source: { url: https://example.com/web.git, branch: main, build: npm ci && npm run build }
    artifact: dist
    start: server.mjs
    migrate: [node, migrate.mjs]
`

// apiCall returns the decoded JSON of an admin request.
func apiCall(t *testing.T, h *harness, srv *httptest.Server, method, path string) map[string]any {
	t.Helper()
	_, token, err := h.app.Auth.CreateToken(h.ctx, "managed-"+fmt.Sprint(len(path)), auth.ScopeAdmin)
	if err != nil {
		t.Fatal(err)
	}
	req, err := http.NewRequest(method, srv.URL+path, nil)
	if err != nil {
		t.Fatal(err)
	}
	req.Header.Set("Authorization", "Bearer "+token)
	client := &http.Client{Transport: &http.Transport{DisableKeepAlives: true}}
	resp, err := client.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = resp.Body.Close() }()
	var body map[string]any
	if err := json.NewDecoder(resp.Body).Decode(&body); err != nil {
		t.Fatal(err)
	}
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("%s %s = %d %v", method, path, resp.StatusCode, body)
	}
	return body
}

func actionReasons(body map[string]any) map[string]string {
	out := map[string]string{}
	for _, a := range body["actions"].([]any) {
		m := a.(map[string]any)
		out[m["name"].(string)] = m["reason"].(string)
	}
	return out
}

// TestManagedServiceThroughTheApp: the app hands the deploy helper to both
// the runtime adapter and the deploy pipeline.
func TestManagedServiceThroughTheApp(t *testing.T) {
	const v1, v2 = "1111111111111111111111111111111111111111", "2222222222222222222222222222222222222222"
	helper := newFakeDeployHelper()
	runner := &execx.Fake{}
	head := v1
	runner.On(func(c execx.Cmd) bool { return slices.Contains(c.Argv, "rev-parse") }, func(execx.Cmd) (execx.Result, error) {
		return execx.Result{Output: head + "\n"}, nil
	})
	h := newHarness(t, func(o *testOpts) { o.deployHelper, o.runner = helper, runner })
	h.putService(managedWeb)
	srv := httptest.NewServer(h.app.Handler())
	defer srv.Close()

	if r := actionReasons(apiCall(t, h, srv, "GET", "/api/v1/resources/web")); r["deploy"] != "" || r["stop"] != "" {
		t.Fatalf("reasons = %v", r)
	}
	plan := apiCall(t, h, srv, "POST", "/api/v1/resources/web/actions/deploy?dryRun=true")
	var codes []string
	for _, m := range plan["stepMessages"].([]any) {
		codes = append(codes, m.(map[string]any)["code"].(string))
	}
	if !slices.Contains(codes, "step.helperBuild") || !slices.Contains(codes, "step.helperActivate") || slices.Contains(codes, "step.restart") {
		t.Fatalf("plan steps = %v", codes)
	}
	if len(helper.Requests()) != 0 {
		t.Fatal("a dry run of a deploy called the helper")
	}

	if _, err := h.app.Manager.Deploy(h.ctx, "web", catalog.ActionOptions{}); err != nil {
		t.Fatal(err)
	}
	head = v2
	res, err := h.app.Manager.Deploy(h.ctx, "web", catalog.ActionOptions{})
	if err != nil || res.Version != v2 || res.PreviousVersion != v1 {
		t.Fatalf("second deploy = %+v, %v", res, err)
	}
	first := helper.Requests()[2].ReleaseID
	before := len(helper.Requests())
	if res, err := h.app.Manager.Rollback(h.ctx, "web", catalog.ActionOptions{}); err != nil || res.Version != v1 {
		t.Fatalf("rollback = %+v, %v", res, err)
	}
	reqs := helper.Requests()[before:]
	if len(reqs) != 2 || reqs[1].Op != deployhelper.OpActivate || reqs[1].ReleaseID != first || reqs[1].Migrate != nil {
		t.Fatalf("rollback requests = %+v", reqs)
	}
	for _, c := range runner.Calls() {
		if strings.Contains(c.Display(), "npm") || strings.Contains(c.Display(), "lcm-deploy") {
			t.Fatalf("LCM ran %q itself", c.Display())
		}
	}

	rec, err := h.app.Catalog.Get(h.ctx, "web")
	if err != nil || rec.State != catalog.StateRunning {
		t.Fatalf("record = %+v, %v", rec, err)
	}
	ad, err := h.app.Runtimes.For(rec.Runtime())
	if err != nil {
		t.Fatal(err)
	}
	if st, err := ad.Status(h.ctx, rec.Runtime()); err != nil || st.State != rt.StateActive || st.Detail["release"] != first {
		t.Fatalf("status = %+v, %v", st, err)
	}
	var deploys int
	for _, e := range h.auditEntries() {
		if e.Action == "service.deploy" || e.Action == "service.rollback" {
			deploys++
		}
	}
	if deploys != 3 {
		t.Fatalf("audited deploys and rollbacks = %d", deploys)
	}
}

// TestManagedServiceWithoutHelper: on a host without the deploy component
// (here: a data directory other than /var/lib/lcm), the managed actions
// are unavailable with a reason and refused, and nothing is recorded.
func TestManagedServiceWithoutHelper(t *testing.T) {
	h := newHarness(t)
	h.putService(managedWeb)
	srv := httptest.NewServer(h.app.Handler())
	defer srv.Close()

	r := actionReasons(apiCall(t, h, srv, "GET", "/api/v1/resources/web"))
	for _, a := range []string{"start", "stop", "deploy", "rollback"} {
		if r[a] != catalog.ReasonHelperUnavailable {
			t.Fatalf("reasons = %v", r)
		}
	}
	_, err := h.app.Manager.Deploy(h.ctx, "web", catalog.ActionOptions{})
	if !errors.Is(err, catalog.ErrHelperUnavailable) {
		t.Fatalf("deploy err = %v", err)
	}
	if deps, _ := h.app.Catalog.Deployments(h.ctx, "web", 10, 0); len(deps) != 0 {
		t.Fatalf("deployments = %+v", deps)
	}
	for _, c := range h.runner.Calls() {
		if strings.Contains(c.Display(), "lcm-deploy") {
			t.Fatalf("the helper was called: %q", c.Display())
		}
	}
}

// TestManagedNameLimitMatchesTheHelper keeps the document limit equal to the
// names host allow and the deploy helper accept, so that a valid document
// never names a service that can never be granted.
func TestManagedNameLimitMatchesTheHelper(t *testing.T) {
	for n := model.MaxManagedName - 1; n <= model.MaxManagedName+1; n++ {
		name := "s" + strings.Repeat("a", n-1)
		granted := hostsetup.ValidServiceName(name) == nil
		if want := n <= model.MaxManagedName; granted != want {
			t.Errorf("a %d-character name: host allow accepts = %v, model limit %d", n, granted, model.MaxManagedName)
		}
	}
}
