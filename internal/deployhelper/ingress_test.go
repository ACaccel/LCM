package deployhelper

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"os"
	"slices"
	"strings"
	"sync"
	"testing"

	"github.com/ACaccel/lcm/internal/hostsetup"
	"github.com/ACaccel/lcm/internal/ingress"
)

// ingressTestEnv is a helper host with nginx, the ingress component, the
// service web (with a route) and the external static site, with the site
// installed as "lcm host apply" leaves it.
type ingressTestEnv struct {
	*env
	mu     sync.Mutex
	probes []string
}

func newIngressEnv(t *testing.T) *ingressTestEnv {
	t.Helper()
	e := &ingressTestEnv{env: newEnv(t)}
	for _, d := range []string{hostsetup.SitesAvailable, hostsetup.SitesEnabled, "/usr/sbin", "/srv/site"} {
		e.mkdir(d, 0o755)
	}
	e.mkdir(hostsetup.IngressRoutes, 0o700)
	e.mkdir(hostsetup.IngressState, 0o700)
	e.write(hostsetup.Nginx, "nginx", 0o755)
	e.write("/srv/site/index.html", "hello", 0o644)
	e.mkdir("/etc/letsencrypt/live/example.org", 0o700)
	for _, f := range []string{"fullchain.pem", "privkey.pem"} {
		if err := os.Symlink("../../archive/example.org/"+f, e.real("/etc/letsencrypt/live/example.org/"+f)); err != nil {
			t.Fatal(err)
		}
	}
	e.cfg.Components.Ingress = &hostsetup.Ingress{Domain: "example.org", TLS: hostsetup.IngressTLS{Email: "ops@example.org"}}
	web := e.cfg.Services["web"]
	web.Route = &hostsetup.RouteGrant{Prefix: "/web/"}
	e.cfg.Services["web"] = web
	e.cfg.Services["site"] = hostsetup.ServiceGrant{External: true, Route: &hostsetup.RouteGrant{Prefix: "/", Static: "/srv/site"}}
	uid := os.Getuid()
	e.h.IngressLock = func(legacy string) (func(), error) { return hostsetup.LockIngress(e.root, uid, legacy, 0) }
	e.h.Probe = func(_ context.Context, p hostsetup.Probe) (int, error) {
		e.mu.Lock()
		defer e.mu.Unlock()
		e.probes = append(e.probes, p.String())
		return 204, nil
	}
	env := e.h.ingressEnv()
	plan, err := hostsetup.PlanSite(env, e.cfg, hostsetup.RouteOptions{}, "")
	if err != nil {
		t.Fatal(err)
	}
	if err := hostsetup.ApplySite(context.Background(), env, plan); err != nil {
		t.Fatal(err)
	}
	e.systemctls, e.probes = nil, nil
	return e
}

func (e *ingressTestEnv) rendered(options map[string]ingress.Options) string {
	e.t.Helper()
	out, err := ingress.Render(hostsetup.SiteFor(e.cfg, options, true))
	if err != nil {
		e.t.Fatal(err)
	}
	return string(out)
}

var webOptions = ingress.Options{
	Proxy: []ingress.Proxy{
		{Path: "api/", Upstream: "/api/", BodyLimit: "3m", Category: ingress.CategoryAPI},
		{Path: "socket.io/", Upstream: "/socket.io/", WebSocket: true, Timeout: "75s", Category: ingress.CategorySocket},
	},
	Headers: &ingress.Headers{CSP: "default-src 'self'"},
}

func TestIngressApplyStoresOptionsAndAppliesTheSite(t *testing.T) {
	e := newIngressEnv(t)
	resp := e.ok(Request{Op: OpIngressApply, Service: "web", Route: &webOptions})
	want := e.rendered(map[string]ingress.Options{"web": webOptions})
	if got := e.read(hostsetup.SiteFile); got != want {
		t.Fatalf("site:\n%s", got)
	}
	if r := resp.IngressApply; r == nil || r.SHA256 != digestOf(want) || !slices.Equal(r.Changes, []string{"update " + hostsetup.SiteFile}) {
		t.Fatalf("result %+v", resp.IngressApply)
	}
	stored := e.read(hostsetup.RouteOptionsFile("web"))
	if got, err := hostsetup.DecodeRouteOptions([]byte(stored)); err != nil || got.Proxy[1].Timeout != "75s" {
		t.Fatalf("stored options %q: %v", stored, err)
	}
	if info, err := os.Stat(e.real(hostsetup.RouteOptionsFile("web"))); err != nil || info.Mode().Perm() != 0o600 {
		t.Fatalf("options file %v %v", info, err)
	}
	if !slices.Contains(e.systemctlCalls(), "reload nginx.service") {
		t.Errorf("no reload: %v", e.systemctlCalls())
	}
	if strings.Join(e.probes, " ") != "http://example.org/.well-known/lcm-probe https://example.org/.well-known/lcm-probe" {
		t.Errorf("probes %v", e.probes)
	}

	// The same options again change nothing.
	e.systemctls = nil
	resp = e.ok(Request{Op: OpIngressApply, Service: "web", Route: &webOptions})
	if len(resp.IngressApply.Changes) != 0 || len(e.systemctlCalls()) != 0 {
		t.Fatalf("second apply %+v %v", resp.IngressApply, e.systemctlCalls())
	}

	// Without a route section the service gets the default route again.
	e.ok(Request{Op: OpIngressApply, Service: "web"})
	if e.exists(hostsetup.RouteOptionsFile("web")) || e.read(hostsetup.SiteFile) != e.rendered(nil) {
		t.Fatal("the options were not removed")
	}

	st := e.ok(Request{Op: OpIngressStatus}).Ingress
	if st == nil || st.Rendered == "" || st.Rendered != st.Installed || !st.Enabled || st.Last == nil || st.Last.Outcome != "succeeded" ||
		len(st.Routes) != 2 || st.Routes[1].Service != "web" || st.Routes[1].Options {
		t.Fatalf("status %+v", st)
	}
}

func digestOf(s string) string {
	sum := sha256.Sum256([]byte(s))
	return hex.EncodeToString(sum[:])
}

func TestIngressApplyFailureKeepsTheOptions(t *testing.T) {
	e := newIngressEnv(t)
	e.ok(Request{Op: OpIngressApply, Service: "web", Route: &webOptions})
	before, site := e.read(hostsetup.RouteOptionsFile("web")), e.read(hostsetup.SiteFile)
	e.nginxTest = 1
	changed := webOptions
	changed.Headers = &ingress.Headers{CSP: "default-src 'none'"}
	resp := e.call(Request{Op: OpIngressApply, Service: "web", Route: &changed})
	if resp.Code != CodeFailed || resp.State != StateFailed || !strings.Contains(resp.Message, "nginx -t refused the configuration") {
		t.Fatalf("reply %+v", resp)
	}
	if !strings.Contains(resp.Message, "nginx: [emerg] test failure") || strings.Contains(resp.Message, "?") {
		t.Errorf("the message lacks the nginx output or shows line breaks as ?: %q", resp.Message)
	}
	if e.read(hostsetup.RouteOptionsFile("web")) != before || e.read(hostsetup.SiteFile) != site {
		t.Fatal("a failed apply changed the options or the site")
	}
	// A new service's options are removed again.
	resp = e.call(Request{Op: OpIngressApply, Service: "site", Route: &ingress.Options{Static: &ingress.Static{SPA: true}}})
	if resp.Code != CodeFailed || e.exists(hostsetup.RouteOptionsFile("site")) {
		t.Fatalf("reply %+v", resp)
	}
}

func TestIngressApplyRefusals(t *testing.T) {
	e := newIngressEnv(t)
	e.cfg.Services["api"] = hostsetup.ServiceGrant{Port: 3300}
	e.refused(Request{Op: OpIngressApply, Service: "api"}, CodeRefused, "services.api has no route")
	e.refused(Request{Op: OpIngressApply, Service: "nope"}, CodeRefused, "is not in the services")
	// Options the grant does not cover, or that would take another
	// route's requests.
	e.refused(Request{Op: OpIngressApply, Service: "web", Route: &ingress.Options{Static: &ingress.Static{SPA: true}}}, CodeRefused, "needs a static directory")
	e.refused(Request{Op: OpIngressApply, Service: "site", Route: &ingress.Options{Static: &ingress.Static{Immutable: []string{"web/assets/"}}}},
		CodeRefused, "lies in the prefix of web")
	e.refused(Request{Op: OpIngressApply, Service: "site", Route: &ingress.Options{Proxy: []ingress.Proxy{{Path: "api/", Upstream: "/"}}}}, CodeRefused, "needs a port")
	if e.exists(hostsetup.RouteOptionsFile("site")) || len(e.systemctlCalls()) != 0 {
		t.Fatal("a refused request changed something")
	}

	// The external site has no deploy operations.
	e.refused(Request{Op: OpStatus, Service: "site"}, CodeRefused, "external")
	build := buildReq(commit1)
	build.Service = "site"
	e.refused(build, CodeRefused, "external")

	// Busy while another change holds the lock.
	unlock, err := hostsetup.LockIngress(e.root, os.Getuid(), "", 0)
	if err != nil {
		t.Fatal(err)
	}
	e.refused(Request{Op: OpIngressApply, Service: "web"}, CodeBusy, "another change of the nginx site")
	unlock()

	// Reverted or never installed: an administrator decides.
	e.write(hostsetup.IngressState+"/reverted.json", "{}", 0o600)
	e.refused(Request{Op: OpIngressApply, Service: "web"}, CodeRefused, "was reverted")
	if err := os.Remove(e.real(hostsetup.IngressState + "/reverted.json")); err != nil {
		t.Fatal(err)
	}
	e.write(hostsetup.SitesEnabled+"/old", "", 0o644)
	if err := os.Remove(e.real(hostsetup.SiteFile)); err != nil {
		t.Fatal(err)
	}
	e.refused(Request{Op: OpIngressApply, Service: "web"}, CodeRefused, "not installed yet")
	// Revert removes the site file too; the refusal still names the revert.
	e.write(hostsetup.IngressState+"/reverted.json", "{}", 0o600)
	e.refused(Request{Op: OpIngressApply, Service: "web"}, CodeRefused, "was reverted")

	// Without the component nothing is done.
	e.cfg.Components.Ingress = nil
	e.refused(Request{Op: OpIngressApply, Service: "web"}, CodeDisabled, "components.ingress is not configured")
	e.refused(Request{Op: OpIngressStatus}, CodeDisabled, "components.ingress is not configured")
}

func TestIngressWithoutDeployComponent(t *testing.T) {
	e := newIngressEnv(t)
	e.cfg.Components.Deploy = false
	e.refused(buildReq(commit1), CodeRefused, "components.deploy is false")
	e.ok(Request{Op: OpIngressApply, Service: "web", Route: &webOptions})
}

func TestIngressAdoptedSiteEnabledAgain(t *testing.T) {
	e := newIngressEnv(t)
	e.cfg.Components.Ingress.Adopt = "example.org"
	e.write(hostsetup.SitesAvailable+"/example.org", "server {}\n", 0o644)
	if err := os.Symlink(hostsetup.SitesAvailable+"/example.org", e.real(hostsetup.SitesEnabled+"/example.org")); err != nil {
		t.Fatal(err)
	}
	e.refused(Request{Op: OpIngressApply, Service: "web"}, CodeRefused, "enabled again")
	if !e.exists(hostsetup.SitesEnabled + "/example.org") {
		t.Fatal("the helper disabled the adopted site")
	}
}

func TestIngressRequestDecoding(t *testing.T) {
	for name, raw := range map[string]string{
		"injection in a path":   `{"version":1,"op":"ingress.apply","operationId":"00000000-0000-4000-8000-000000000001","service":"web","route":{"proxy":[{"path":"api/;}","upstream":"/"}]}}`,
		"injection in the CSP":  `{"version":1,"op":"ingress.apply","operationId":"00000000-0000-4000-8000-000000000001","service":"web","route":{"headers":{"csp":"default-src \"x\"; }"}}}`,
		"unknown option":        `{"version":1,"op":"ingress.apply","operationId":"00000000-0000-4000-8000-000000000001","service":"web","route":{"snippet":"return 200;"}}`,
		"duplicate option":      `{"version":1,"op":"ingress.apply","operationId":"00000000-0000-4000-8000-000000000001","service":"web","route":{"static":{},"static":{}}}`,
		"status with a service": `{"version":1,"op":"ingress.status","operationId":"00000000-0000-4000-8000-000000000001","service":"web"}`,
		"route on another op":   `{"version":1,"op":"status","operationId":"00000000-0000-4000-8000-000000000001","service":"web","route":{}}`,
		"apply without service": `{"version":1,"op":"ingress.apply","operationId":"00000000-0000-4000-8000-000000000001"}`,
	} {
		if _, err := DecodeRequest([]byte(raw)); err == nil {
			t.Errorf("%s: accepted", name)
		}
	}
	if Timeout(Request{Op: OpIngressApply}) <= replyMargin {
		t.Error("ingress.apply has no budget")
	}
	if lockName("web", OpIngressApply) != "lcm-ingress" || hostsetup.ValidServiceName("lcm-ingress") == nil {
		t.Error("the ingress ledger lock could be a service's")
	}
}

// TestIngressStaticReleaseLink publishes the static site through a
// "current" link on a real file system: the site names the link, and a
// chained link is refused.
func TestIngressStaticReleaseLink(t *testing.T) {
	e := newIngressEnv(t)
	e.mkdir("/srv/site/releases/r1", 0o755)
	e.write("/srv/site/releases/r1/index.html", "r1", 0o644)
	if err := os.Symlink("releases/r1", e.real("/srv/site/current")); err != nil {
		t.Fatal(err)
	}
	e.cfg.Services["site"].Route.Static = "/srv/site/current"
	opts := ingress.Options{Static: &ingress.Static{SPA: true}}
	e.ok(Request{Op: OpIngressApply, Service: "site", Route: &opts})
	if site := e.read(hostsetup.SiteFile); !strings.Contains(site, `root "/srv/site/current";`) {
		t.Fatalf("site:\n%s", site)
	}
	if err := os.Symlink("r1", e.real("/srv/site/releases/latest")); err != nil {
		t.Fatal(err)
	}
	if err := os.Remove(e.real("/srv/site/current")); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink("releases/latest", e.real("/srv/site/current")); err != nil {
		t.Fatal(err)
	}
	e.refused(Request{Op: OpIngressApply, Service: "site"}, CodeRefused, "/srv/site/releases/latest is a symbolic link")
}

// TestIngressApplyChecksTheSiteUnderTheLock covers a revert or a manual
// disable that happens while the request waits for the lock, and a site
// disabled by hand.
func TestIngressApplyChecksTheSiteUnderTheLock(t *testing.T) {
	e := newIngressEnv(t)
	lock := e.h.IngressLock
	e.h.IngressLock = func(legacy string) (func(), error) {
		e.write(hostsetup.IngressState+"/reverted.json", "{}", 0o600)
		return lock(legacy)
	}
	e.refused(Request{Op: OpIngressApply, Service: "web"}, CodeRefused, "was reverted")
	if err := os.Remove(e.real(hostsetup.IngressState + "/reverted.json")); err != nil {
		t.Fatal(err)
	}
	e.h.IngressLock = func(legacy string) (func(), error) {
		_ = os.Remove(e.real(hostsetup.SiteLink))
		return lock(legacy)
	}
	e.refused(Request{Op: OpIngressApply, Service: "web"}, CodeRefused, "is not enabled")
	e.h.IngressLock = lock
	e.refused(Request{Op: OpIngressApply, Service: "web"}, CodeRefused, "is not enabled")
	if e.exists(hostsetup.SiteLink) || len(e.systemctlCalls()) != 0 {
		t.Fatal("the helper enabled the site again")
	}
}

// TestIngressUndoOutlivesCancellation cancels the request while the probe
// runs: the previous site is still put back and reloaded.
func TestIngressUndoOutlivesCancellation(t *testing.T) {
	e := newIngressEnv(t)
	site := e.read(hostsetup.SiteFile)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	e.h.Probe = func(context.Context, hostsetup.Probe) (int, error) {
		cancel()
		return 0, context.Canceled
	}
	raw, err := json.Marshal(Request{Version: Version, Op: OpIngressApply, OperationID: "00000000-0000-4000-8000-000000000099", Service: "web", Route: &webOptions})
	if err != nil {
		t.Fatal(err)
	}
	resp := e.h.Handle(ctx, raw)
	if resp.Code != CodeFailed || !strings.Contains(resp.Message, "the previous site was put back") || strings.Contains(resp.Message, "after putting the previous site back") {
		t.Fatalf("reply %+v", resp)
	}
	if e.read(hostsetup.SiteFile) != site {
		t.Fatal("the previous site was not put back")
	}
	if n := slices.Index(e.systemctlCalls(), "reload nginx.service"); n < 0 || strings.Count(strings.Join(e.systemctlCalls(), "\n"), "reload nginx.service") != 2 {
		t.Fatalf("reloads %v", e.systemctlCalls())
	}
}

func TestIngressApplyLeavesStaleOptionsOfOthersOut(t *testing.T) {
	e := newIngressEnv(t)
	stale := ingress.Options{Proxy: []ingress.Proxy{{Path: "api/", Upstream: "/api/"}}}
	e.write(hostsetup.RouteOptionsFile("site"), string(hostsetup.EncodeRouteOptions(stale)), 0o600)
	resp := e.ok(Request{Op: OpIngressApply, Service: "web", Route: &webOptions})
	if len(resp.IngressApply.Dropped) != 1 || !strings.Contains(resp.IngressApply.Dropped[0], "services.site") {
		t.Fatalf("result %+v", resp.IngressApply)
	}
	if e.read(hostsetup.SiteFile) != e.rendered(map[string]ingress.Options{"web": webOptions}) {
		t.Fatal("the site was not applied without the stale options")
	}
	// The requested service's own options must fit.
	e.refused(Request{Op: OpIngressApply, Service: "site", Route: &stale}, CodeRefused, "needs a port")
}
