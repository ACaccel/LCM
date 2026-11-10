package ingress

import (
	"flag"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
)

var update = flag.Bool("update", false, "rewrite the golden files")

const liveCSP = "default-src 'none'; script-src 'self'; style-src 'self' 'unsafe-inline'; img-src 'self'; connect-src 'self'; base-uri 'none'; form-action 'none'; frame-ancestors 'none'"

// liveShape is a site shaped like the first host LCM took over, with
// example values only: a static personal site at the root, a static app
// with an API, a WebSocket, a health endpoint and an APK download, a
// proxied service without options and a domain-level assetlinks.json.
func liveShape() Site {
	return Site{
		Domain:      "example.org",
		Listen:      []string{"192.0.2.10"},
		ACMEWebroot: "/var/www/lcm-acme",
		TLS:         ptr(CertificateFiles("example.org")),
		WellKnown:   []WellKnown{{Name: "assetlinks.json", Path: "/var/www/lcm-ingress/well-known/assetlinks.json"}},
		Routes: []Route{
			{Service: "site", Prefix: "/", Static: "/var/www/site/current", Options: Options{
				Static:    &Static{NotFound: "404.html", Immutable: []string{"_astro/"}},
				Redirects: []Redirect{{From: "en/", To: "/", Code: 301}},
				Headers:   &Headers{CSP: liveCSP},
			}},
			{Service: "cards", Prefix: "/cards/", Port: 3001, Static: "/opt/cards/www", Options: Options{
				Proxy: []Proxy{
					{Path: "api/", Upstream: "/api/", BodyLimit: "3m", CookiePath: true, Category: CategoryAPI},
					{Path: "socket.io/", Upstream: "/socket.io/", WebSocket: true, Timeout: "75s", Category: CategorySocket},
					{Path: "health", Exact: true, Upstream: "/health", Category: CategoryHealth},
				},
				Static: &Static{SPA: true, Immutable: []string{"assets/", "provided-emoji/"},
					Downloads: []Download{{Path: "download/cards.apk", Type: "application/vnd.android.package-archive"}}},
				Headers: &Headers{CSP: "default-src 'self'; img-src 'self' data: blob:; connect-src 'self' wss://example.org", CSPReportOnly: true},
			}},
			{Service: "web", Prefix: "/web/", Port: 3200},
		},
	}
}

func ptr[T any](v T) *T { return &v }

func TestRenderGolden(t *testing.T) {
	httpOnly := liveShape()
	httpOnly.TLS = nil
	minimal := Site{Domain: "example.org", ACMEWebroot: "/var/www/lcm-acme", TLS: ptr(CertificateFiles("example.org")), HSTS: true,
		Routes: []Route{{Service: "web", Prefix: "/web/", Port: 3200}}}
	v6 := Site{Domain: "www.example.org", Listen: []string{"192.0.2.10", "2001:db8::10"}, ACMEWebroot: "/srv/acme",
		TLS: ptr(CertificateFiles("www.example.org")),
		WellKnown: []WellKnown{
			{Name: "security.txt", Path: "/var/www/lcm-ingress/well-known/security.txt"},
			{Name: "apple-app-site-association", Path: "/var/www/lcm-ingress/well-known/apple-app-site-association"},
		},
		Routes: []Route{
			{Service: "docs", Prefix: "/", Static: "/srv/docs", Options: Options{Static: &Static{SPA: true}}},
			{Service: "api", Prefix: "/api/v1/", Port: 4000, Options: Options{
				Proxy: []Proxy{{Path: "upload/", Upstream: "/upload/", BodyLimit: "64m", Timeout: "1h", Category: CategoryUpload},
					{Path: "login", Exact: true, Upstream: "/login", Category: CategoryAuth}},
				Redirects: []Redirect{{From: "old", To: "/new/", Code: 308}},
			}},
		},
	}
	for name, s := range map[string]Site{"live-shape": liveShape(), "http-only": httpOnly, "minimal-hsts": minimal, "ipv6-and-well-known": v6} {
		t.Run(name, func(t *testing.T) {
			got, err := Render(s)
			if err != nil {
				t.Fatal(err)
			}
			again, err := Render(s)
			if err != nil || string(again) != string(got) {
				t.Fatal("rendering is not deterministic")
			}
			file := filepath.Join("testdata", name+".conf")
			if *update {
				if err := os.WriteFile(file, got, 0o644); err != nil { //nolint:gosec // golden files are repository files
					t.Fatal(err)
				}
			}
			want, err := os.ReadFile(file) //nolint:gosec // a golden file of this package
			if err != nil {
				t.Fatal(err)
			}
			if string(got) != string(want) {
				t.Fatalf("%s differs from the rendered site (run go test ./internal/ingress -update after reviewing):\n%s", file, got)
			}
		})
	}
}

// TestRenderRouteOrderDoesNotMatter checks that the order of routes and
// well-known files in the input does not change the file.
func TestRenderRouteOrderDoesNotMatter(t *testing.T) {
	a := liveShape()
	b := liveShape()
	b.Routes[0], b.Routes[2] = b.Routes[2], b.Routes[0]
	ra, err1 := Render(a)
	rb, err2 := Render(b)
	if err1 != nil || err2 != nil || string(ra) != string(rb) {
		t.Fatalf("order changed the rendering: %v %v", err1, err2)
	}
}

func TestRenderRefusesCollisions(t *testing.T) {
	cases := map[string]func(s *Site){
		"duplicate prefix": func(s *Site) {
			s.Routes = append(s.Routes, Route{Service: "other", Prefix: "/web/", Port: 3300})
		},
		"location in another prefix": func(s *Site) {
			s.Routes[0].Options.Static.Immutable = append(s.Routes[0].Options.Static.Immutable, "web/assets/")
		},
		"exact location in another prefix": func(s *Site) {
			s.Routes[0].Options.Redirects = append(s.Routes[0].Options.Redirects, Redirect{From: "cards/x", To: "/"})
		},
		"redirect shadowing a bare prefix": func(s *Site) {
			s.Routes[0].Options.Redirects = append(s.Routes[0].Options.Redirects, Redirect{From: "web", To: "/"})
		},
		"same location twice in a route": func(s *Site) {
			s.Routes[1].Options.Proxy = append(s.Routes[1].Options.Proxy, Proxy{Path: "api/", Upstream: "/v2/"})
		},
		"proxy shadowing an immutable directory": func(s *Site) {
			s.Routes[1].Options.Proxy = append(s.Routes[1].Options.Proxy, Proxy{Path: "assets/", Upstream: "/"})
		},
		"service twice": func(s *Site) {
			s.Routes = append(s.Routes, Route{Service: "web", Prefix: "/web2/", Port: 3300})
		},
		"proxy without a port": func(s *Site) {
			s.Routes[0].Options.Proxy = []Proxy{{Path: "api/", Upstream: "/"}}
		},
		"static options without a directory": func(s *Site) {
			s.Routes[2].Options.Static = &Static{SPA: true}
		},
		"nothing to serve": func(s *Site) {
			s.Routes = append(s.Routes, Route{Service: "empty", Prefix: "/empty/"})
		},
		"listen twice":       func(s *Site) { s.Listen = []string{"192.0.2.10", "192.0.2.10"} },
		"unknown well-known": func(s *Site) { s.WellKnown = []WellKnown{{Name: "passwd", Path: "/etc/passwd"}} },
	}
	for name, mutate := range cases {
		t.Run(name, func(t *testing.T) {
			s := liveShape()
			mutate(&s)
			if out, err := Render(s); err == nil {
				t.Fatalf("rendered:\n%s", out)
			}
		})
	}
}

// injections are values that would change the nginx configuration if they
// reached it unchecked.
var injections = []string{
	"a\nb", "a\rb", "x;", "x; return 200 pwned", "x}", "x{", `x"`, "x'", "$host", `a\b`, "a\x00b", "a b", "a\tb",
	"é", "\u202e", "\u2028", "../etc", "..", ".", ".well-known/", "a/../b", "a//b", "/abs", "~", "^/x$", "(.*)", "a*",
	"a?b", "a#b", "%2e%2e", strings.Repeat("a", 300), "",
}

func TestOptionsRefuseInjections(t *testing.T) {
	fields := map[string]func(v string) Options{
		"proxy.path":     func(v string) Options { return Options{Proxy: []Proxy{{Path: v, Upstream: "/"}}} },
		"proxy.exact":    func(v string) Options { return Options{Proxy: []Proxy{{Path: v, Exact: true, Upstream: "/"}}} },
		"proxy.upstream": func(v string) Options { return Options{Proxy: []Proxy{{Path: "api/", Upstream: "/" + v + "/"}}} },
		"proxy.bodyLimit": func(v string) Options {
			return Options{Proxy: []Proxy{{Path: "api/", Upstream: "/", BodyLimit: "1m" + v}}}
		},
		"proxy.timeout": func(v string) Options {
			return Options{Proxy: []Proxy{{Path: "api/", Upstream: "/", Timeout: "75s" + v}}}
		},
		"proxy.category": func(v string) Options {
			return Options{Proxy: []Proxy{{Path: "api/", Upstream: "/", Category: "api" + v}}}
		},
		"static.notFound":  func(v string) Options { return Options{Static: &Static{NotFound: v}} },
		"static.immutable": func(v string) Options { return Options{Static: &Static{Immutable: []string{v}}} },
		"static.download": func(v string) Options {
			return Options{Static: &Static{Downloads: []Download{{Path: v, Type: "text/plain"}}}}
		},
		"static.type": func(v string) Options {
			return Options{Static: &Static{Downloads: []Download{{Path: "a.txt", Type: "text/plain" + v}}}}
		},
		"redirect.from": func(v string) Options { return Options{Redirects: []Redirect{{From: v, To: "/"}}} },
		"redirect.to":   func(v string) Options { return Options{Redirects: []Redirect{{From: "a", To: "/" + v + "x"}}} },
		"csp":           func(v string) Options { return Options{Headers: &Headers{CSP: "default-src 'self' " + v}} },
	}
	// Appending nothing keeps a valid value. In a CSP, values made of
	// source characters and ";" stay a valid policy: it is emitted inside
	// one quoted string (TestCSPStaysQuoted).
	allowed := map[string]map[string]bool{
		"csp": {"x;": true, "x; return 200 pwned": true, "a b": true, "a/../b": true, "a//b": true,
			strings.Repeat("a", 300): true, "a*": true, "a?b": true, "%2e%2e": true},
	}
	for field, build := range fields {
		for _, v := range injections {
			if v == "" && field != "proxy.path" && field != "proxy.exact" && field != "static.immutable" &&
				field != "static.download" && field != "redirect.from" && field != "proxy.upstream" || allowed[field][v] {
				continue
			}
			o := build(v)
			if is := o.Validate(); len(is) == 0 {
				t.Errorf("%s accepted %q", field, v)
			}
			// The renderer refuses it as well, whatever Validate says.
			s := Site{Domain: "example.org", ACMEWebroot: "/var/www/lcm-acme", TLS: ptr(CertificateFiles("example.org")),
				Routes: []Route{{Service: "web", Prefix: "/web/", Port: 3200, Static: "/srv/web", Options: o}}}
			if out, err := Render(s); err == nil {
				t.Errorf("%s: rendered %q:\n%s", field, v, out)
			}
		}
	}
}

func TestHostValuesRefuseInjections(t *testing.T) {
	for _, v := range injections {
		base := func() Site {
			return Site{Domain: "example.org", ACMEWebroot: "/var/www/lcm-acme", TLS: ptr(CertificateFiles("example.org")),
				Routes: []Route{{Service: "web", Prefix: "/web/", Port: 3200}}}
		}
		for name, mutate := range map[string]func(s *Site){
			"domain":      func(s *Site) { s.Domain = "example.org" + v },
			"listen":      func(s *Site) { s.Listen = []string{"192.0.2.10" + v} },
			"acmeWebroot": func(s *Site) { s.ACMEWebroot = "/var/www/" + v },
			"tls":         func(s *Site) { s.TLS = &TLSFiles{Certificate: "/etc/x/" + v, Key: "/etc/x/key"} },
			"wellKnown":   func(s *Site) { s.WellKnown = []WellKnown{{Name: "security.txt", Path: "/var/www/" + v}} },
			"prefix":      func(s *Site) { s.Routes[0].Prefix = "/" + v + "/" },
			"service":     func(s *Site) { s.Routes[0].Service = "web" + v },
			"static":      func(s *Site) { s.Routes[0].Static = "/srv/" + v },
		} {
			s := base()
			mutate(&s)
			out, err := Render(s)
			if err == nil && v != "" && !harmless(name, v) {
				t.Errorf("%s accepted %q:\n%s", name, v, out)
			}
		}
	}
}

// harmless are injections that stay valid values once appended to a
// valid one: a long but clean path component cannot change the
// configuration.
func harmless(field, v string) bool {
	switch field {
	case "acmeWebroot", "tls", "wellKnown", "static":
		return v == strings.Repeat("a", 300)
	}
	return false
}

// TestCSPStaysQuoted renders a policy that holds nginx syntax as CSP text:
// it stays inside the quoted header value.
func TestCSPStaysQuoted(t *testing.T) {
	s := Site{Domain: "example.org", ACMEWebroot: "/var/www/lcm-acme", TLS: ptr(CertificateFiles("example.org")),
		Routes: []Route{{Service: "web", Prefix: "/web/", Port: 3200, Options: Options{Headers: &Headers{CSP: "default-src 'self' x; return 200 pwned"}}}}}
	out, err := Render(s)
	if err != nil {
		t.Fatal(err)
	}
	want := `add_header Content-Security-Policy "default-src 'self' x; return 200 pwned" always;`
	for _, line := range strings.Split(string(out), "\n") {
		if strings.Contains(line, "pwned") && strings.TrimSpace(line) != want {
			t.Fatalf("unexpected line %q", line)
		}
	}
	if !strings.Contains(string(out), want) {
		t.Fatalf("no CSP header:\n%s", out)
	}
}

func TestReservedWellKnownPrefix(t *testing.T) {
	for _, p := range []string{"/.well-known/", "/.git/", "/a/.hidden/", "/x", "x/", "//", "/a//b/"} {
		if err := ValidPrefix(p); err == nil {
			t.Errorf("ValidPrefix accepted %q", p)
		}
	}
	for _, p := range []string{"/", "/web/", "/a/b-c/d_e.f/"} {
		if err := ValidPrefix(p); err != nil {
			t.Errorf("ValidPrefix refused %q: %v", p, err)
		}
	}
}

func TestParseCSP(t *testing.T) {
	good := map[string]string{
		"default-src 'self'":     "default-src 'self'",
		"default-src 'self';":    "default-src 'self'",
		" a  'self' ;b  data:  ": "a 'self'; b data:",
		"img-src 'self' https://*.example.org:443/p?x=1&y=%20 blob:; report-uri https://example.org/r": "img-src 'self' https://*.example.org:443/p?x=1&y=%20 blob:; report-uri https://example.org/r",
		"script-src 'sha256-AbC+/=' 'nonce-x_y'":                                                       "script-src 'sha256-AbC+/=' 'nonce-x_y'",
		"upgrade-insecure-requests":                                                                    "upgrade-insecure-requests",
	}
	for in, want := range good {
		got, err := ParseCSP(in)
		if err != nil || got != want {
			t.Errorf("ParseCSP(%q) = %q, %v; want %q", in, got, err, want)
		}
	}
	for _, bad := range []string{
		"", ";", "default-src 'self';;", "Default-Src 'self'", "a 'self'; a 'none'", "a \"self\"", "a 'self\"", `a \x`,
		"a $host", "a {x}", "a x\ny", "a\tb", "a 'se lf'", "a ''", "a x,y", "a #x", "a é", "a \x00",
		strings.Repeat("a b;", 70), "a " + strings.Repeat("b", 600), strings.Repeat("a", 5000),
	} {
		if got, err := ParseCSP(bad); err == nil {
			t.Errorf("ParseCSP(%q) accepted: %q", bad, got)
		}
	}
}

func TestLimits(t *testing.T) {
	cases := map[string]Options{
		"bodyLimit over 64m": {Proxy: []Proxy{{Path: "a/", Upstream: "/", BodyLimit: "65m"}}},
		"bodyLimit in bytes": {Proxy: []Proxy{{Path: "a/", Upstream: "/", BodyLimit: "67108865"}}},
		"timeout over 1h":    {Proxy: []Proxy{{Path: "a/", Upstream: "/", Timeout: "61m"}}},
		"timeout in days":    {Proxy: []Proxy{{Path: "a/", Upstream: "/", Timeout: "1d"}}},
		"too many proxies":   {Proxy: make([]Proxy, MaxProxies+1)},
		"redirect code":      {Redirects: []Redirect{{From: "a", To: "/", Code: 200}}},
		"prefix path no dir": {Proxy: []Proxy{{Path: "api", Upstream: "/api/"}}},
		"upstream no dir":    {Proxy: []Proxy{{Path: "api/", Upstream: "/api"}}},
		"immutable file":     {Static: &Static{Immutable: []string{"assets"}}},
		"download dir":       {Static: &Static{Downloads: []Download{{Path: "dl/", Type: "text/plain"}}}},
		"unknown mime":       {Static: &Static{Downloads: []Download{{Path: "a.html", Type: "text/html"}}}},
	}
	for name, o := range cases {
		if is := o.Validate(); len(is) == 0 {
			t.Errorf("%s: accepted", name)
		}
	}
	ok := Options{Proxy: []Proxy{{Path: "a/", Upstream: "/", BodyLimit: "64m", Timeout: "1h"}, {Path: "b", Exact: true, Upstream: "/b", Timeout: "3600"}}}
	if is := ok.Validate(); len(is) > 0 {
		t.Errorf("the limits themselves were refused: %v", is)
	}
}

func TestRenderProbeLocationAndSize(t *testing.T) {
	out, err := Render(liveShape())
	if err != nil {
		t.Fatal(err)
	}
	if n := strings.Count(string(out), "location = "+ProbePath+" {\n        access_log off;\n        return 204;\n    }"); n != 2 {
		t.Fatalf("%d probe locations:\n%s", n, out)
	}
	s := liveShape()
	s.WellKnown = append(s.WellKnown, WellKnown{Name: "lcm-probe", Path: "/var/www/x"})
	if _, err := Render(s); err == nil {
		t.Fatal("the probe path was accepted as a well-known file")
	}
	// A site beyond MaxSite is refused, not truncated.
	big := Site{Domain: "example.org", ACMEWebroot: "/var/www/lcm-acme", TLS: ptr(CertificateFiles("example.org"))}
	csp := "default-src 'self'; img-src " + strings.Repeat("https://a.example.org ", 150)
	for n := range 40 {
		o := Options{Headers: &Headers{CSP: csp}}
		for p := range MaxProxies {
			o.Proxy = append(o.Proxy, Proxy{Path: "p" + strconv.Itoa(p) + "/", Upstream: "/"})
		}
		big.Routes = append(big.Routes, Route{Service: "s" + strconv.Itoa(n), Prefix: "/s" + strconv.Itoa(n) + "/", Port: 3000 + n, Options: o})
	}
	if _, err := Render(big); err == nil || !strings.Contains(err.Error(), "more than the limit") {
		t.Fatalf("oversized site: %v", err)
	}
}
