package hostsetup

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/ACaccel/lcm/internal/ingress"
)

const renewalSample = `# renew_before_expiry = 30 days
version = 2.9.0
archive_dir = /etc/letsencrypt/archive/example.org
cert = /etc/letsencrypt/live/example.org/cert.pem
privkey = /etc/letsencrypt/live/example.org/privkey.pem
chain = /etc/letsencrypt/live/example.org/chain.pem
fullchain = /etc/letsencrypt/live/example.org/fullchain.pem

# Options used in the renewal process
[renewalparams]
account = 0123456789abcdef0123456789abcdef
authenticator = webroot
webroot_path = /var/www/old-acme,
server = https://acme-v02.api.letsencrypt.org/directory
key_type = ecdsa
[[webroot_map]]
example.org = /var/www/old-acme
`

const handMadeSite = "server {\n    listen 80;\n    server_name example.org;\n}\n"

// ingressHost is a converged host with nginx, a hand-made site to adopt, a
// certificate with its renewal configuration and a configured but not yet
// applied ingress component with two external routes.
func ingressHost(t *testing.T, cert bool) *testHost {
	t.Helper()
	th := converged(t)
	m := th.fs
	for _, d := range []string{NginxDir, SitesAvailable, SitesEnabled, "/var/www", "/srv", "/srv/site", "/etc/lcm/ingress"} {
		m.dir(d, 0o755, 0, 0)
	}
	m.file(Nginx, "nginx", 0o755, 0, 0)
	m.file(SitesAvailable+"/example.org", handMadeSite, 0o644, 0, 0)
	m.link(SitesEnabled+"/example.org", SitesAvailable+"/example.org", 0, 0)
	m.file("/srv/site/index.html", "hello", 0o644, 0, 0)
	m.file("/etc/lcm/ingress/assetlinks.json", "[]\n", 0o644, 0, 0)
	m.file(RenewalHooksDir+"/reload-nginx", "#!/bin/sh\nsystemctl reload nginx\n", 0o755, 0, 0)
	if cert {
		m.link("/etc/letsencrypt/live/example.org/fullchain.pem", "../../archive/example.org/fullchain1.pem", 0, 0)
		m.link("/etc/letsencrypt/live/example.org/privkey.pem", "../../archive/example.org/privkey1.pem", 0, 0)
		m.file(RenewalConf("example.org"), renewalSample, 0o644, 0, 0)
	}
	th.cfg.Components.Ingress = &Ingress{
		Domain: "example.org", Listen: []string{"192.0.2.10"}, TLS: IngressTLS{Email: "ops@example.org"},
		WellKnown: map[string]string{"assetlinks.json": "/etc/lcm/ingress/assetlinks.json"}, Adopt: "example.org",
	}
	th.cfg.Services = map[string]ServiceGrant{
		"site": {External: true, Route: &RouteGrant{Prefix: "/", Static: "/srv/site"}},
		"web":  {External: true, Port: 3200, Route: &RouteGrant{Prefix: "/web/"}},
	}
	th.writeConfig(th.cfg)
	th.r.resetCalls()
	return th
}

// wantSite renders the site the test host's configuration should get.
func (th *testHost) wantSite(tls bool, options map[string]ingress.Options) []byte {
	th.t.Helper()
	out, err := ingress.Render(SiteFor(th.cfg, options, tls))
	if err != nil {
		th.t.Fatal(err)
	}
	return out
}

func TestIngressApply(t *testing.T) {
	th := ingressHost(t, true)
	th.fs.resetOps()
	res, out, err := th.apply(Options{Check: true})
	if err != nil || res.Manual != 0 {
		t.Fatalf("check: %+v %v\n%s", res, err, out)
	}
	if w := th.fs.writes(); len(w) > 0 {
		t.Fatalf("check wrote %v", w)
	}
	want := th.wantSite(true, nil)
	for _, s := range []string{
		"--- rendered " + SiteFile + " (sha256 " + sha(string(want)) + ") ---\n" + string(want) + "--- end of " + SiteFile + " ---\n",
		"would change: copy /etc/lcm/ingress/assetlinks.json to /var/www/lcm-ingress/well-known/assetlinks.json; install " + SiteFile +
			"; enable lcm-ingress.conf in /etc/nginx/sites-enabled; disable the adopted site /etc/nginx/sites-enabled/example.org (its file in /etc/nginx/sites-available stays)",
		"would change: install " + RenewalHook,
		"would change: set webroot_path = /var/www/lcm-acme,; [[webroot_map]] example.org = /var/www/lcm-acme in " + RenewalConf("example.org"),
		"other certbot deploy hooks in /etc/letsencrypt/renewal-hooks/deploy are kept (reload-nginx)",
		"deploy:          would change: install /usr/local/libexec/lcm-deploy\n",
	} {
		if !strings.Contains(out, s) {
			t.Errorf("check output lacks %q:\n%s", s, out)
		}
	}
	if th.r.reloads != 0 || len(th.probes) != 0 {
		t.Fatalf("check reloaded or probed: %d %v", th.r.reloads, th.probes)
	}

	out = th.converge()
	if got := th.fs.content(SiteFile); got != string(want) {
		t.Fatalf("site file:\n%s", got)
	}
	if n := th.fs.get(SiteFile); n.mode != 0o644 || n.uid != 0 {
		t.Errorf("site file mode %v owner %d", n.mode, n.uid)
	}
	if n := th.fs.get(SiteLink); n == nil || n.target != SiteFile {
		t.Errorf("site link %+v", n)
	}
	if th.fs.get(SitesEnabled+"/example.org") != nil || th.fs.content(SitesAvailable+"/example.org") != handMadeSite {
		t.Error("the adopted site link is still enabled, or its file changed")
	}
	var recs []adoptedRecord
	if err := json.Unmarshal([]byte(th.fs.content(adoptedFile)), &recs); err != nil || len(recs) != 1 || recs[0] != (adoptedRecord{Name: "example.org", Target: SitesAvailable + "/example.org"}) {
		t.Errorf("adopted records %+v %v", recs, err)
	}
	var orig renewalOriginal
	if err := json.Unmarshal([]byte(th.fs.content(renewalFile)), &orig); err != nil || orig.Content != renewalSample || orig.Path != RenewalConf("example.org") || orig.Mode != 0o644 {
		t.Errorf("kept renewal configuration %+v %v", orig, err)
	}
	if got := th.fs.content(IngressWellKnown + "/assetlinks.json"); got != "[]\n" {
		t.Errorf("well-known copy %q", got)
	}
	if got := th.fs.content(RenewalHook); got != string(asset("lcm-ingress-reload")) || th.fs.get(RenewalHook).mode != 0o755 {
		t.Errorf("renewal hook %q", got)
	}
	if th.fs.get(RenewalHooksDir+"/reload-nginx") == nil {
		t.Error("the hand-made hook was removed")
	}
	conf := th.fs.content(RenewalConf("example.org"))
	if !strings.Contains(conf, "webroot_path = /var/www/lcm-acme,\n") || !strings.Contains(conf, "[[webroot_map]]\nexample.org = /var/www/lcm-acme\n") ||
		!strings.Contains(conf, "account = 0123456789abcdef0123456789abcdef\n") {
		t.Errorf("renewal conf:\n%s", conf)
	}
	if n := th.fs.get(IngressState); n == nil || n.mode.Perm() != 0o700 {
		t.Errorf("state dir %+v", n)
	}
	test, reload := th.r.index(Nginx+" -t -q"), th.r.index(Systemctl+" reload nginx.service")
	probe := th.r.index("probe http://example.org/.well-known/lcm-probe")
	if test < 0 || reload < test || probe < reload {
		t.Errorf("nginx -t %d, reload %d, probe %d: %v", test, reload, probe, th.r.calls)
	}
	if strings.Join(th.probes, "\n") != "192.0.2.10:80 http://example.org/.well-known/lcm-probe\n192.0.2.10:443 https://example.org/.well-known/lcm-probe" {
		t.Errorf("probes %v", th.probes)
	}
	last, err := ReadLastApply(th.host.ingressEnv())
	if err != nil || last == nil || last.Outcome != "succeeded" || last.SHA256 != sha(string(want)) {
		t.Errorf("last apply %+v %v", last, err)
	}
	if !strings.Contains(out, ComponentIngress) {
		t.Errorf("no ingress line:\n%s", out)
	}
	th.expectConverged()

	// The status as root compares the installed site with the rendered one.
	st := statusOf(t, th)[ComponentIngress]
	if st.State != StateUpToDate || st.Files[1].Installed != sha(string(want)) || st.Files[1].Embedded != sha(string(want)) {
		t.Errorf("status %+v", st)
	}
	th.host.Privileged = false
	if st := statusOf(t, th)[ComponentIngress]; st.State != StateUpToDate || st.Files[1].Embedded != "" {
		t.Errorf("status as a user %+v", st)
	}
	th.host.Privileged = true

	// A configuration change is rendered and applied again.
	th.cfg.Components.Ingress.HSTS = true
	th.writeConfig(th.cfg)
	if st := statusOf(t, th)[ComponentIngress]; st.State != StateOutdated {
		t.Errorf("status after a change %+v", st)
	}
	th.converge()
	if got := th.fs.content(SiteFile); !strings.Contains(got, "Strict-Transport-Security") {
		t.Errorf("HSTS not applied:\n%s", got)
	}
	if th.fs.content(previousFile) != string(want) {
		t.Error("the previous site was not kept")
	}
	th.expectConverged()
}

func TestIngressFailuresPutThePreviousSiteBack(t *testing.T) {
	for _, tc := range []struct {
		name    string
		fail    func(th *testHost)
		reloads int
		want    string
	}{
		{"nginx -t", func(th *testHost) {
			th.r.nginxTest = func() error {
				return &ExitError{Command: "nginx -t", Code: 1, Stderr: "nginx: [emerg] unknown directive"}
			}
		}, 0, "nginx -t refused the configuration"},
		{"reload", func(th *testHost) { th.r.failReload = true }, 2, "reloading nginx failed"},
		{"probe", func(th *testHost) { th.probeStatus = 500 }, 2, "answered 500"},
		{"probe answered by another server", func(th *testHost) { th.probeStatus = 200 }, 2, "answered 200"},
		{"probe connection", func(th *testHost) { th.probeErr = errors.New("connection refused") }, 2, "connection refused"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			th := ingressHost(t, true)
			tc.fail(th)
			_, out, err := th.apply(Options{})
			if err == nil || !strings.Contains(err.Error(), tc.want) || !strings.Contains(err.Error(), "the previous site was put back") {
				t.Fatalf("apply: %v\n%s", err, out)
			}
			if th.fs.get(SiteFile) != nil || th.fs.get(SiteLink) != nil {
				t.Error("the new site stayed")
			}
			if n := th.fs.get(SitesEnabled + "/example.org"); n == nil || n.target != SitesAvailable+"/example.org" {
				t.Errorf("the adopted site was not enabled again: %+v", n)
			}
			if th.r.reloads != tc.reloads {
				t.Errorf("reloads %d, want %d", th.r.reloads, tc.reloads)
			}
			if th.fs.content(RenewalConf("example.org")) != renewalSample || th.fs.get(renewalFile) != nil {
				t.Error("the renewal configuration changed although the site failed")
			}
			last, _ := ReadLastApply(th.host.ingressEnv())
			if last == nil || last.Outcome != "failed" || !strings.Contains(last.Message, tc.want) {
				t.Errorf("last apply %+v", last)
			}
		})
	}

	// A failed update keeps the installed site as it was.
	th := ingressHost(t, true)
	th.converge()
	before := th.fs.content(SiteFile)
	th.cfg.Components.Ingress.HSTS = true
	th.writeConfig(th.cfg)
	th.r.nginxTest = func() error { return exit(1) }
	if _, out, err := th.apply(Options{}); err == nil {
		t.Fatalf("apply succeeded:\n%s", out)
	}
	if th.fs.content(SiteFile) != before || th.fs.get(SiteLink) == nil || th.fs.get(SitesEnabled+"/example.org") != nil {
		t.Error("the installed site was not kept")
	}
}

func TestIngressRefusesFilesItDidNotMake(t *testing.T) {
	for _, tc := range []struct {
		name  string
		setup func(th *testHost)
		want  string
	}{
		{"hand-made file at the site path", func(th *testHost) {
			th.fs.file(SiteFile, "server {}\n", 0o644, 0, 0)
		}, "was not written by LCM"},
		{"site file of another owner", func(th *testHost) {
			th.fs.file(SiteFile, ingress.Marker+"\n", 0o644, aliceUID, aliceUID)
		}, "not a root-owned regular file"},
		{"another link at the site link", func(th *testHost) {
			th.fs.link(SiteLink, "/tmp/evil.conf", 0, 0)
		}, "links to"},
		{"adopted site is a file", func(th *testHost) {
			th.fs.remove(SitesEnabled + "/example.org")
			th.fs.file(SitesEnabled+"/example.org", handMadeSite, 0o644, 0, 0)
		}, "is not a link"},
		{"adopted site revert could not enable again", func(th *testHost) {
			th.fs.file(SitesAvailable+"/example.org", handMadeSite, 0o666, 0, 0)
		}, "the adopted site example.org cannot be enabled again"},
		{"adopted site outside sites-available", func(th *testHost) {
			th.fs.remove(SitesEnabled + "/example.org")
			th.fs.link(SitesEnabled+"/example.org", "/srv/site/index.html", 0, 0)
		}, "is not in /etc/nginx/sites-available"},
		{"static directory of another account", func(th *testHost) {
			th.fs.dir("/srv/site", 0o755, aliceUID, aliceUID)
		}, "services.site.route.static: /srv/site is not owned by root"},
		{"static directory is a link", func(th *testHost) {
			th.fs.remove("/srv/site")
			th.fs.link("/srv/site", "/home/alice", 0, 0)
		}, "/srv/site links to /home/alice, which must not be in /home"},
		{"writable static directory", func(th *testHost) {
			th.fs.dir("/srv/site", 0o777, 0, 0)
		}, "writable by group or others"},
		{"well-known file of another account", func(th *testHost) {
			th.fs.file("/etc/lcm/ingress/assetlinks.json", "[]", 0o644, aliceUID, aliceUID)
		}, "wellKnown.assetlinks.json"},
		{"writable sites-enabled", func(th *testHost) {
			th.fs.dir(SitesEnabled, 0o777, 0, 0)
		}, "nginx: /etc/nginx/sites-enabled is writable"},
		{"nginx missing", func(th *testHost) { th.fs.remove(Nginx) }, "nginx is not installed"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			th := ingressHost(t, true)
			tc.setup(th)
			res, out, err := th.apply(Options{})
			if err == nil && res.Manual == 0 {
				t.Fatalf("applied:\n%s", out)
			}
			if msg := out + errString(err); !strings.Contains(msg, tc.want) {
				t.Fatalf("want %q:\n%s", tc.want, msg)
			}
			if th.r.reloads != 0 {
				t.Error("nginx was reloaded")
			}
		})
	}
}

func errString(err error) string {
	if err == nil {
		return ""
	}
	return err.Error()
}

func TestIngressRevert(t *testing.T) {
	th := ingressHost(t, true)
	th.converge()
	installed := th.fs.content(SiteFile)
	var out bytes.Buffer
	if err := RevertIngress(context.Background(), th.host, RevertOptions{Out: &out}); err != nil {
		t.Fatalf("revert: %v\n%s", err, out.String())
	}
	if th.fs.get(SiteFile) != nil || th.fs.get(SiteLink) != nil {
		t.Error("the site stayed")
	}
	if n := th.fs.get(SitesEnabled + "/example.org"); n == nil || n.target != SitesAvailable+"/example.org" {
		t.Errorf("adopted site link %+v", n)
	}
	if th.fs.content(previousFile) != installed || !Reverted(th.fs) || th.fs.get(adoptedFile) != nil {
		t.Error("the state was not recorded")
	}
	for _, s := range []string{"changed: disable lcm-ingress.conf", "changed: enable the adopted site example.org again", "the next sudo bin/lcm host apply installs the site again"} {
		if !strings.Contains(out.String(), s) {
			t.Errorf("revert output lacks %q:\n%s", s, out.String())
		}
	}
	if th.r.index(Nginx+" -t -q") < 0 || th.r.reloads != 2 {
		t.Errorf("revert did not test and reload: %d", th.r.reloads)
	}

	// Apply takes over again and clears the mark.
	_, check, err := th.apply(Options{Check: true})
	if err != nil || !strings.Contains(check, "(the site was reverted; this applies it again)") {
		t.Fatalf("check after revert: %v\n%s", err, check)
	}
	th.converge()
	if Reverted(th.fs) || th.fs.get(SitesEnabled+"/example.org") != nil || th.fs.content(SiteFile) != installed {
		t.Error("apply did not take over again")
	}

	// A failing revert keeps the site.
	th.r.nginxTest = func() error { return exit(1) }
	if err := RevertIngress(context.Background(), th.host, RevertOptions{}); err == nil {
		t.Fatal("revert succeeded")
	}
	if th.fs.get(SiteLink) == nil || th.fs.get(SitesEnabled+"/example.org") != nil || th.fs.content(SiteFile) != installed {
		t.Error("a failed revert changed the site")
	}

	// Revert as a user is refused.
	th.host.Privileged = false
	if err := RevertIngress(context.Background(), th.host, RevertOptions{}); err == nil || !strings.Contains(err.Error(), "must run as root") {
		t.Fatalf("revert as a user: %v", err)
	}
}

func TestIssueCertificate(t *testing.T) {
	th := ingressHost(t, false)
	th.cfg.Components.Ingress.Adopt = ""
	th.fs.remove(SitesEnabled + "/example.org")
	th.writeConfig(th.cfg)
	_, out, err := th.apply(Options{})
	if err != nil {
		t.Fatalf("apply: %v\n%s", err, out)
	}
	if !strings.Contains(out, "there is no certificate for example.org yet") || th.fs.content(SiteFile) != string(th.wantSite(false, nil)) {
		t.Fatalf("apply without a certificate:\n%s", out)
	}
	if strings.Join(th.probes, "\n") != "192.0.2.10:80 http://example.org/.well-known/lcm-probe" {
		t.Errorf("probes %v", th.probes)
	}
	var buf bytes.Buffer
	if err := IssueCertificate(context.Background(), th.host, CertOptions{Out: &buf}); err != nil {
		t.Fatalf("issue: %v\n%s", err, buf.String())
	}
	want := []string{"certonly", "--webroot", "-w", "/var/www/lcm-acme", "-d", "example.org", "--email", "ops@example.org", "--key-type", "ecdsa", "--agree-tos", "-n"}
	if strings.Join(th.r.certbot, " ") != strings.Join(want, " ") {
		t.Errorf("certbot %v", th.r.certbot)
	}
	if th.fs.content(SiteFile) != string(th.wantSite(true, nil)) {
		t.Errorf("the site has no TLS server:\n%s", th.fs.content(SiteFile))
	}
	if th.locked {
		t.Error("the lock was not released")
	}
	if err := IssueCertificate(context.Background(), th.host, CertOptions{}); err == nil || !strings.Contains(err.Error(), "a certificate for example.org exists") {
		t.Fatalf("second issue: %v", err)
	}
}

func TestIngressUsesStoredOptions(t *testing.T) {
	th := ingressHost(t, true)
	th.converge()
	opts := ingress.Options{Static: &ingress.Static{SPA: true}, Headers: &ingress.Headers{CSP: "default-src 'self'"}}
	th.fs.file(RouteOptionsFile("site"), string(EncodeRouteOptions(opts)), 0o600, 0, 0)
	th.fs.file(RouteOptionsFile("gone"), string(EncodeRouteOptions(ingress.Options{})), 0o600, 0, 0)
	// The options of a service no longer in services are removed.
	if _, out, err := th.apply(Options{Check: true}); err != nil || !strings.Contains(out, "would change: remove "+RouteOptionsFile("gone")+": gone is no longer in services") {
		t.Fatalf("check: %v\n%s", err, out)
	}
	th.converge()
	if got := th.fs.content(SiteFile); got != string(th.wantSite(true, map[string]ingress.Options{"site": opts})) {
		t.Fatalf("stored options not rendered:\n%s", got)
	}
	if _, err := th.fs.Lstat(RouteOptionsFile("gone")); err == nil {
		t.Fatal("the options of a removed service are kept")
	}
	if _, err := th.fs.Lstat(RouteOptionsFile("site")); err != nil {
		t.Fatalf("the options of a granted service were removed: %v", err)
	}
	th.expectConverged()
	// Stored options that someone else could have written are refused.
	th.fs.file(RouteOptionsFile("site"), string(EncodeRouteOptions(opts)), 0o666, 0, 0)
	if res, out, err := th.apply(Options{}); err == nil && res.Manual == 0 {
		t.Fatalf("applied:\n%s", out)
	}
}

func TestIngressConfigValidation(t *testing.T) {
	base := func() Config {
		return Config{
			Operator: "alice", Repository: DefaultRepository, Toolchains: Toolchains{Go: "/opt/go", Node: "/opt/node"}, Tailscale: Tailscale{Port: 9443},
			Components: Components{Ingress: &Ingress{Domain: "example.org", TLS: IngressTLS{Email: "ops@example.org"}}},
			Services: map[string]ServiceGrant{
				"web":  {Port: 3200, Route: &RouteGrant{Prefix: "/web/"}},
				"site": {External: true, Route: &RouteGrant{Prefix: "/", Static: "/var/www/site"}},
			},
		}
	}
	if err := base().Validate(); err != nil {
		t.Fatalf("base: %v", err)
	}
	// A managed service may serve its publish directory, with or without
	// a port.
	ok := base()
	ok.Services["web"] = ServiceGrant{Port: 3200, Route: &RouteGrant{Prefix: "/web/", Static: "/opt/web/www"}}
	ok.Services["docs"] = ServiceGrant{Route: &RouteGrant{Prefix: "/docs/", Static: "/opt/docs/www"}}
	if err := ok.Validate(); err != nil {
		t.Fatalf("publish directories: %v", err)
	}
	for name, tc := range map[string]struct {
		mutate func(c *Config)
		want   string
	}{
		"domain":            {func(c *Config) { c.Components.Ingress.Domain = "Example.org" }, "components.ingress.domain"},
		"domain injection":  {func(c *Config) { c.Components.Ingress.Domain = "example.org;" }, "components.ingress.domain"},
		"listen":            {func(c *Config) { c.Components.Ingress.Listen = []string{"192.0.2.010"} }, "components.ingress.listen"},
		"listen twice":      {func(c *Config) { c.Components.Ingress.Listen = []string{"192.0.2.10", "192.0.2.10"} }, "twice"},
		"email":             {func(c *Config) { c.Components.Ingress.TLS.Email = "-x@example.org --staging" }, "tls.email"},
		"key type":          {func(c *Config) { c.Components.Ingress.TLS.KeyType = "dsa" }, "tls.keyType"},
		"webroot in /etc":   {func(c *Config) { c.Components.Ingress.ACMEWebroot = "/etc/acme" }, "acmeWebroot must not be in /etc"},
		"webroot in static": {func(c *Config) { c.Components.Ingress.ACMEWebroot = "/var/www/site/acme" }, "must not overlap services.site"},
		"well-known name":   {func(c *Config) { c.Components.Ingress.WellKnown = map[string]string{"passwd": "/etc/passwd"} }, "is not one of"},
		"well-known path": {func(c *Config) {
			c.Components.Ingress.WellKnown = map[string]string{"security.txt": "/etc/x/../shadow"}
		}, "wellKnown.security.txt"},
		"adopt path":        {func(c *Config) { c.Components.Ingress.Adopt = "../default" }, "adopt"},
		"adopt ours":        {func(c *Config) { c.Components.Ingress.Adopt = SiteName }, "adopt"},
		"legacy lock":       {func(c *Config) { c.Components.Ingress.LegacyLock = "/tmp/x.lock" }, "legacyLock"},
		"prefix":            {func(c *Config) { c.Services["web"].Route.Prefix = "/web" }, "services.web.route.prefix"},
		"reserved prefix":   {func(c *Config) { c.Services["web"].Route.Prefix = "/.well-known/" }, "services.web.route.prefix"},
		"duplicate prefix":  {func(c *Config) { c.Services["web"].Route.Prefix = "/" }, "is also the prefix of"},
		"static in /etc":    {func(c *Config) { c.Services["site"].Route.Static = "/etc/lcm" }, "must not be in /etc"},
		"static in home":    {func(c *Config) { c.Services["site"].Route.Static = "/home/alice/www" }, "must not be in /home"},
		"static data":       {func(c *Config) { c.Services["site"].Route.Static = "/var/lib/web" }, "must not be in /var/lib"},
		"static opt lcm":    {func(c *Config) { c.Services["site"].Route.Static = "/opt/lcm/x" }, "must not overlap /opt/lcm"},
		"static is /srv":    {func(c *Config) { c.Services["site"].Route.Static = "/srv" }, "not /srv"},
		"static injection":  {func(c *Config) { c.Services["site"].Route.Static = "/var/www/a;b" }, "route.static"},
		"external no route": {func(c *Config) { c.Services["x"] = ServiceGrant{External: true, Port: 3300} }, "external needs a route"},
		"external no port": {func(c *Config) {
			c.Services["x"] = ServiceGrant{External: true, Route: &RouteGrant{Prefix: "/x/"}}
		}, "port is required unless"},
		"external runtimes": {func(c *Config) {
			c.Runtimes = map[string]string{"node": "/srv/node"}
			c.Services["x"] = ServiceGrant{External: true, Port: 3300, Runtimes: []string{"node"}, Route: &RouteGrant{Prefix: "/x/"}}
		}, "no runtimes or inputs"},
		"no port no static": {func(c *Config) {
			c.Services["x"] = ServiceGrant{Route: &RouteGrant{Prefix: "/x/"}}
		}, "services.x.port is required unless the service only publishes static content (route.static /opt/x/www)"},
		"managed static elsewhere": {func(c *Config) {
			c.Services["web"].Route.Static = "/var/www/web"
		}, "services.web.route.static must be empty or /opt/web/www"},
		"managed static of another": {func(c *Config) {
			c.Services["web"].Route.Static = "/opt/site/www"
		}, "services.web.route.static must be empty or /opt/web/www"},
		"managed no port elsewhere": {func(c *Config) {
			c.Services["x"] = ServiceGrant{Route: &RouteGrant{Prefix: "/x/", Static: "/var/www/x"}}
		}, "services.x.port is required unless"},
		"external publish dir": {func(c *Config) { c.Services["site"].Route.Static = "/opt/web/www" }, "must not overlap /opt/web"},
	} {
		c := base()
		c.Services = map[string]ServiceGrant{}
		for k, v := range base().Services {
			r := *v.Route
			v.Route = &r
			c.Services[k] = v
		}
		tc.mutate(&c)
		if err := c.Validate(); err == nil || !strings.Contains(err.Error(), tc.want) {
			t.Errorf("%s: %v, want %q", name, err, tc.want)
		}
	}
	// The strict decoder knows the new keys and still reserves extraPaths.
	raw := string(base().Encode())
	if _, err := ParseConfig([]byte(raw)); err != nil {
		t.Fatalf("round trip: %v\n%s", err, raw)
	}
	bad := strings.Replace(raw, `"prefix": "/web/"`, `"prefix": "/web/", "upstream": "http://evil/"`, 1)
	if _, err := ParseConfig([]byte(bad)); err == nil || !strings.Contains(err.Error(), `unknown key "services.web.route.upstream"`) {
		t.Fatalf("unknown route key: %v", err)
	}
}

func TestFixRenewal(t *testing.T) {
	fixed, drift, problems, err := FixRenewal(renewalSample, "example.org", "/var/www/lcm-acme")
	if err != nil || len(problems) != 0 || len(drift) != 2 {
		t.Fatalf("%v %v %v", drift, problems, err)
	}
	want := strings.Replace(strings.Replace(renewalSample, "webroot_path = /var/www/old-acme,", "webroot_path = /var/www/lcm-acme,", 1),
		"example.org = /var/www/old-acme", "example.org = /var/www/lcm-acme", 1)
	if fixed != want {
		t.Fatalf("fixed:\n%s", fixed)
	}
	if again, drift, _, _ := FixRenewal(fixed, "example.org", "/var/www/lcm-acme"); again != fixed || len(drift) != 0 {
		t.Fatalf("not converged: %v", drift)
	}
	// Missing keys and a missing map are added; installer is reported.
	conf := "version = 2.9.0\n[renewalparams]\nauthenticator = nginx\ninstaller = nginx\nkey_type = ecdsa"
	fixed, drift, problems, err = FixRenewal(conf, "example.org", "/var/www/lcm-acme")
	want = "version = 2.9.0\n[renewalparams]\nauthenticator = webroot\ninstaller = nginx\nkey_type = ecdsa\n" +
		"[[webroot_map]]\nexample.org = /var/www/lcm-acme\n"
	if err != nil || len(drift) != 3 || len(problems) != 1 || !strings.Contains(fixed, "webroot_path = /var/www/lcm-acme,\n") {
		t.Fatalf("%q %v %v %v", fixed, drift, problems, err)
	}
	if strings.Replace(fixed, "webroot_path = /var/www/lcm-acme,\n", "", 1) != want {
		t.Fatalf("fixed:\n%s", fixed)
	}
	if _, _, _, err := FixRenewal("cert = x\n", "example.org", "/var/www/lcm-acme"); err == nil {
		t.Fatal("a file without [renewalparams] was accepted")
	}
}

func TestLockIngress(t *testing.T) {
	root := t.TempDir()
	if err := os.MkdirAll(filepath.Join(root, "run/lock"), 0o755); err != nil { //nolint:gosec // a test /run, readable like the real one
		t.Fatal(err)
	}
	if err := os.Chmod(filepath.Join(root, "run"), 0o755); err != nil { //nolint:gosec // a test /run
		t.Fatal(err)
	}
	uid := os.Getuid()
	unlock, err := LockIngress(root, uid, "/run/lock/legacy.lock", 0)
	if err != nil {
		t.Fatalf("lock without a legacy lock file: %v", err)
	}
	if _, err := LockIngress(root, uid, "", 0); !errors.Is(err, ErrIngressBusy) {
		t.Fatalf("second lock: %v", err)
	}
	unlock()
	info, err := os.Stat(filepath.Join(root, IngressLock))
	if err != nil || info.Mode().Perm() != 0o600 {
		t.Fatalf("lock file %v %v", info, err)
	}

	legacy := filepath.Join(root, "run/lock/legacy.lock")
	if err := os.WriteFile(legacy, nil, 0o644); err != nil { //nolint:gosec // a test lock file
		t.Fatal(err)
	}
	held, err := openLock(legacy, uid, false)
	if err != nil {
		t.Fatal(err)
	}
	if err := flockWait(held, 0); err != nil {
		t.Fatal(err)
	}
	if _, err := LockIngress(root, uid, "/run/lock/legacy.lock", 0); !errors.Is(err, ErrIngressBusy) {
		t.Fatalf("lock while the legacy lock is held: %v", err)
	}
	_ = held.Close()
	unlock, err = LockIngress(root, uid, "/run/lock/legacy.lock", 0)
	if err != nil {
		t.Fatalf("lock with the legacy lock: %v", err)
	}
	unlock()

	// A link planted at the legacy lock is never followed, and a lock file
	// of another owner is refused.
	if err := os.Remove(legacy); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(filepath.Join(root, "victim"), legacy); err != nil {
		t.Fatal(err)
	}
	if _, err := LockIngress(root, uid, "/run/lock/legacy.lock", 0); err == nil {
		t.Fatal("followed a link at the legacy lock")
	}
	if _, err := os.Lstat(filepath.Join(root, "victim")); !errors.Is(err, fs.ErrNotExist) {
		t.Fatal("the link target was created")
	}
	if _, err := LockIngress(root, uid+1, "", 0); err == nil {
		t.Fatal("accepted a lock directory of another owner")
	}
}

func TestProbeHTTP(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	if _, err := ProbeHTTP(ctx, Probe{Scheme: "http", Address: "127.0.0.1:1", Host: "example.org", Path: "/"}); err == nil {
		t.Fatal("a closed port answered")
	}
}

func TestProbeOK(t *testing.T) {
	for status, ok := range map[int]bool{204: true, 200: false, 301: false, 404: false, 500: false, 502: false} {
		if probeOK(status) != ok {
			t.Errorf("%d: %v", status, !ok)
		}
	}
}

func TestAllowExternalRoute(t *testing.T) {
	th := ingressHost(t, true)
	th.fs.dir("/srv/docs", 0o755, 0, 0)
	grant := ServiceGrant{External: true, Route: &RouteGrant{Prefix: "/docs/", Static: "/srv/docs"}}
	res, out, err := th.allow(AllowOptions{Name: "docs", Grant: grant})
	if err != nil || res.Manual != 0 {
		t.Fatalf("allow: %+v %v\n%s", res, err, out)
	}
	cfg, err := LoadConfig(th.fs, ConfigPath)
	if err != nil || cfg.Services["docs"].Route == nil || cfg.Services["docs"].Route.Static != "/srv/docs" || cfg.Services["docs"].Port != 0 {
		t.Fatalf("config %+v %v", cfg.Services["docs"], err)
	}
	if len(th.r.called("useradd")) != 0 || !strings.Contains(out, "external: LCM creates no account") || !strings.Contains(out, "the route is not published yet") {
		t.Fatalf("allow of an external service:\n%s\n%v", out, th.r.calls)
	}
	if !strings.Contains(string(cfg.Encode()), `"external": true`) {
		t.Fatalf("encoded:\n%s", cfg.Encode())
	}
	// A route that another service has is refused.
	grant.Route = &RouteGrant{Prefix: "/web/"}
	grant.Port = 3300
	if _, _, err := th.allow(AllowOptions{Name: "docs", Grant: grant}); err == nil || !strings.Contains(err.Error(), "is also the prefix of") {
		t.Fatalf("duplicate prefix: %v", err)
	}
}

// releaseHost is an ingress host whose static site is published through a
// root-owned "current" link to a release directory.
func releaseHost(t *testing.T) *testHost {
	t.Helper()
	th := ingressHost(t, true)
	th.fs.dir("/srv/site/releases/r1", 0o755, 0, 0)
	th.fs.dir("/srv/site/releases/r2", 0o755, 0, 0)
	th.fs.link("/srv/site/current", "releases/r1", 0, 0)
	th.cfg.Services["site"].Route.Static = "/srv/site/current"
	th.writeConfig(th.cfg)
	return th
}

func TestIngressStaticSymlink(t *testing.T) {
	th := releaseHost(t)
	th.converge()
	site := th.fs.content(SiteFile)
	if !strings.Contains(site, `root "/srv/site/current";`) || strings.Contains(site, "releases/r1") {
		t.Fatalf("the site does not name the link:\n%s", site)
	}
	// A release switch replaces the link; the site stays as it is.
	th.fs.remove("/srv/site/current")
	th.fs.link("/srv/site/current", "/srv/site/releases/r2", 0, 0)
	th.expectConverged()

	for _, tc := range []struct {
		name  string
		setup func(th *testHost)
		want  string
	}{
		{"link in a writable directory", func(th *testHost) {
			th.fs.dir("/srv/site", 0o777, 0, 0)
		}, "/srv/site is writable by group or others"},
		{"link in a directory of another account", func(th *testHost) {
			th.fs.dir("/srv/site", 0o755, aliceUID, aliceUID)
		}, "/srv/site is not owned by root"},
		{"link of another account", func(th *testHost) {
			th.fs.remove("/srv/site/current")
			th.fs.link("/srv/site/current", "releases/r1", aliceUID, aliceUID)
		}, "/srv/site/current is a symbolic link that is not owned by root"},
		{"target in an excluded area", func(th *testHost) {
			th.fs.remove("/srv/site/current")
			th.fs.link("/srv/site/current", "/etc/lcm", 0, 0)
		}, "links to /etc/lcm, which must not be in /etc"},
		{"target of another account", func(th *testHost) {
			th.fs.dir("/srv/site/releases/r1", 0o755, aliceUID, aliceUID)
		}, "/srv/site/releases/r1 is not owned by root"},
		{"writable target directory", func(th *testHost) {
			th.fs.dir("/srv/site/releases", 0o777, 0, 0)
		}, "/srv/site/releases is writable by group or others"},
		{"missing target", func(th *testHost) {
			th.fs.remove("/srv/site/releases/r1")
		}, "links to /srv/site/releases/r1"},
		{"target is a file", func(th *testHost) {
			th.fs.remove("/srv/site/releases/r1")
			th.fs.file("/srv/site/releases/r1", "x", 0o644, 0, 0)
		}, "/srv/site/releases/r1 is not a directory"},
		{"chained link", func(th *testHost) {
			th.fs.link("/srv/site/releases/latest", "r1", 0, 0)
			th.fs.remove("/srv/site/current")
			th.fs.link("/srv/site/current", "releases/latest", 0, 0)
		}, "/srv/site/releases/latest is a symbolic link"},
		{"link in the target's path", func(th *testHost) {
			th.fs.link("/srv/rel", "/srv/site/releases", 0, 0)
			th.fs.remove("/srv/site/current")
			th.fs.link("/srv/site/current", "/srv/rel/r1", 0, 0)
		}, "/srv/rel is a symbolic link"},
		{"target with ..", func(th *testHost) {
			th.fs.remove("/srv/site/current")
			th.fs.link("/srv/site/current", "releases/../releases/r1", 0, 0)
		}, `without ".."`},
	} {
		t.Run(tc.name, func(t *testing.T) {
			th := releaseHost(t)
			tc.setup(th)
			res, out, err := th.apply(Options{})
			if err == nil && res.Manual == 0 {
				t.Fatalf("applied:\n%s", out)
			}
			if msg := out + errString(err); !strings.Contains(msg, tc.want) {
				t.Fatalf("want %q:\n%s", tc.want, msg)
			}
			if th.fs.get(SiteFile) != nil || th.r.reloads != 0 {
				t.Error("the site was applied")
			}
		})
	}

	// The link is checked at every render: after a switch to a refused
	// target the next change is refused and the installed site stays.
	th.fs.remove("/srv/site/current")
	th.fs.link("/srv/site/current", "/home/alice", 0, 0)
	th.cfg.Components.Ingress.HSTS = true
	th.writeConfig(th.cfg)
	if res, out, err := th.apply(Options{}); err == nil && res.Manual == 0 || th.fs.content(SiteFile) != site {
		t.Fatalf("applied with a refused target: %v\n%s", err, out)
	}
}

func TestIngressRevertRestoresRenewalAndAllAdoptedSites(t *testing.T) {
	th := ingressHost(t, true)
	th.converge()
	// A second hand-made site is adopted later.
	th.fs.file(SitesAvailable+"/old2", handMadeSite, 0o644, 0, 0)
	th.fs.link(SitesEnabled+"/old2", "../sites-available/old2", 0, 0)
	th.cfg.Components.Ingress.Adopt = "old2"
	th.cfg.Components.Ingress.LegacyLock = "/run/lock/example.lock"
	th.writeConfig(th.cfg)
	th.converge()
	if th.fs.get(SitesEnabled+"/old2") != nil {
		t.Fatal("the second site was not adopted")
	}
	if !strings.Contains(th.fs.content(RenewalConf("example.org")), "webroot_path = /var/www/lcm-acme,") {
		t.Fatal("the renewal configuration was not changed")
	}
	var out bytes.Buffer
	if err := RevertIngress(context.Background(), th.host, RevertOptions{Out: &out}); err != nil {
		t.Fatalf("revert: %v\n%s", err, out.String())
	}
	if th.lockedLegacy != "" {
		t.Errorf("revert took the legacy lock %q", th.lockedLegacy)
	}
	if n := th.fs.get(SitesEnabled + "/example.org"); n == nil || n.target != SitesAvailable+"/example.org" {
		t.Errorf("first adopted site %+v", n)
	}
	if n := th.fs.get(SitesEnabled + "/old2"); n == nil || n.target != "../sites-available/old2" {
		t.Errorf("second adopted site %+v", n)
	}
	if th.fs.content(RenewalConf("example.org")) != renewalSample || th.fs.get(renewalFile) != nil {
		t.Errorf("renewal configuration not restored:\n%s", th.fs.content(RenewalConf("example.org")))
	}
	if !strings.Contains(out.String(), "restore "+RenewalConf("example.org")) {
		t.Errorf("revert output:\n%s", out.String())
	}
}

func TestIngressRevertDoesNotReadTheSiteBack(t *testing.T) {
	th := ingressHost(t, true)
	th.converge()
	th.fs.file(SiteFile, ingress.Marker+"\n"+strings.Repeat("#\n", maxSite), 0o644, 0, 0)
	if err := RevertIngress(context.Background(), th.host, RevertOptions{}); err != nil {
		t.Fatalf("revert of an oversized site: %v", err)
	}
	if th.fs.get(SiteFile) != nil || th.fs.get(SiteLink) != nil {
		t.Fatal("the site stayed")
	}
	// A file without the marker is never removed.
	th.fs.file(SiteFile, "server {}\n", 0o644, 0, 0)
	if err := RevertIngress(context.Background(), th.host, RevertOptions{}); err == nil || th.fs.get(SiteFile) == nil {
		t.Fatalf("removed a site LCM did not write: %v", err)
	}
}

func TestIngressDropsStoredOptionsThatNoLongerFit(t *testing.T) {
	th := ingressHost(t, true)
	th.converge()
	fits := ingress.Options{Headers: &ingress.Headers{CSP: "default-src 'self'"}}
	// The site has no port, so proxy options do not fit its grant.
	stale := ingress.Options{Proxy: []ingress.Proxy{{Path: "api/", Upstream: "/api/"}}}
	th.fs.file(RouteOptionsFile("web"), string(EncodeRouteOptions(fits)), 0o600, 0, 0)
	th.fs.file(RouteOptionsFile("site"), string(EncodeRouteOptions(stale)), 0o600, 0, 0)
	res, out, err := th.apply(Options{})
	if err != nil || res.Manual != 1 || !strings.Contains(out, "services.site: the stored route options no longer fit") {
		t.Fatalf("apply: %+v %v\n%s", res, err, out)
	}
	if got := th.fs.content(SiteFile); got != string(th.wantSite(true, map[string]ingress.Options{"web": fits})) {
		t.Fatalf("site:\n%s", got)
	}
	st, err := SiteStatus(th.host.ingressEnv(), th.cfg)
	if err != nil || len(st.Dropped) != 1 || st.Rendered != st.Installed {
		t.Fatalf("status %+v %v", st, err)
	}
	// Options that cannot be decoded are dropped the same way.
	th.fs.file(RouteOptionsFile("site"), "{not json", 0o600, 0, 0)
	res, out, err = th.apply(Options{})
	if err != nil || res.Manual != 1 || !strings.Contains(out, "services.site: the stored route options /var/lib/lcm-ingress/routes/site.json cannot be used") {
		t.Fatalf("apply: %+v %v\n%s", res, err, out)
	}
}

func TestIngressHooksMustBeRoots(t *testing.T) {
	for _, tc := range []struct {
		name  string
		setup func(m *memFS)
		want  string
	}{
		{"writable hooks directory", func(m *memFS) { m.dir(ingress.HooksDir, 0o777, 0, 0) }, "hooks: /etc/nginx/lcm/hooks is writable"},
		{"service hooks of another account", func(m *memFS) {
			m.dir(ingress.HooksDir+"/web", 0o755, aliceUID, aliceUID)
		}, "hooks: /etc/nginx/lcm/hooks/web is not a directory only root can change"},
		{"writable hook file", func(m *memFS) {
			m.file(ingress.HooksDir+"/web/api-observe.conf", "", 0o666, 0, 0)
		}, "hooks: /etc/nginx/lcm/hooks/web/api-observe.conf is not a regular file only root can change"},
		{"hook file is a link", func(m *memFS) {
			m.link(ingress.HooksDir+"/web/api-observe.conf", "/tmp/x.conf", 0, 0)
		}, "api-observe.conf is not a regular file"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			th := ingressHost(t, true)
			th.fs.dir(ingress.HooksDir+"/web", 0o755, 0, 0)
			tc.setup(th.fs)
			res, out, err := th.apply(Options{})
			if err == nil && res.Manual == 0 || !strings.Contains(out+errString(err), tc.want) {
				t.Fatalf("want %q: %+v %v\n%s", tc.want, res, err, out)
			}
			if th.fs.get(SiteFile) != nil {
				t.Error("the site was applied")
			}
		})
	}
	th := ingressHost(t, true)
	th.fs.file(ingress.HooksDir+"/web/api-observe.conf", "", 0o644, 0, 0)
	th.converge()
}

// TestAllowManagedPublish: a managed service's route may serve its publish
// directory, which allow and apply create; without a port the service only
// publishes.
func TestAllowManagedPublish(t *testing.T) {
	th := ingressHost(t, true)
	th.cfg.Components.Deploy = true
	th.writeConfig(th.cfg)
	app := ServiceGrant{Port: 3300, Route: &RouteGrant{Prefix: "/app/", Static: ServicePublishDir("app")}}
	if res, out, err := th.allow(AllowOptions{Name: "app", Grant: app}); err != nil || res.Manual != 0 {
		t.Fatalf("allow: %+v %v\n%s", res, err, out)
	}
	docs := ServiceGrant{Route: &RouteGrant{Prefix: "/docs/", Static: ServicePublishDir("docs")}}
	if res, out, err := th.allow(AllowOptions{Name: "docs", Grant: docs}); err != nil || res.Manual != 0 {
		t.Fatalf("allow: %+v %v\n%s", res, err, out)
	}
	for _, d := range []string{"/opt/app/www", "/opt/docs/www"} {
		if info, err := th.fs.Lstat(d); err != nil || !info.IsDir() || info.UID != 0 || info.Mode.Perm() != 0o755 {
			t.Fatalf("%s: %+v %v", d, info, err)
		}
	}
	// A managed service without a route gets no publish directory.
	if res, out, err := th.allow(AllowOptions{Name: "api", Grant: ServiceGrant{Port: 3400}}); err != nil || res.Manual != 0 {
		t.Fatalf("allow: %+v %v\n%s", res, err, out)
	}
	if _, err := th.fs.Lstat("/opt/api/www"); err == nil {
		t.Fatal("a service without a route got a publish directory")
	}
	// Another static directory is refused for a managed service, and a
	// static-only grant needs the publish directory.
	if _, _, err := th.allow(AllowOptions{Name: "app", Grant: ServiceGrant{Port: 3300, Route: &RouteGrant{Prefix: "/app/", Static: "/srv/site"}}}); err == nil ||
		!strings.Contains(err.Error(), "must be empty or /opt/app/www") {
		t.Fatalf("other static: %v", err)
	}
	if _, _, err := th.allow(AllowOptions{Name: "docs", Grant: ServiceGrant{Route: &RouteGrant{Prefix: "/docs/"}}}); err == nil ||
		!strings.Contains(err.Error(), "services.docs.port is required unless the service only publishes static content") {
		t.Fatalf("no port: %v", err)
	}

	cfg, err := LoadConfig(th.fs, ConfigPath)
	if err != nil {
		t.Fatal(err)
	}
	th.cfg = cfg
	th.converge()
	site := th.fs.content(SiteFile)
	for _, want := range []string{`alias "/opt/app/www/";`, `alias "/opt/docs/www/";`} {
		if !strings.Contains(site, want) {
			t.Fatalf("site lacks %s:\n%s", want, site)
		}
	}
	th.expectConverged()
	// apply creates a missing publish directory again.
	th.fs.remove("/opt/docs/www")
	if out := th.converge(); !strings.Contains(out, "/opt/docs/www") {
		t.Fatalf("apply:\n%s", out)
	}
	if _, err := th.fs.Lstat("/opt/docs/www"); err != nil {
		t.Fatal(err)
	}
}
