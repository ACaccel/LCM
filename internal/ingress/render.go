package ingress

import (
	"cmp"
	"fmt"
	"net/netip"
	"path"
	"regexp"
	"slices"
	"strconv"
	"strings"
)

// Fixed paths of the rendered site.
const (
	// HooksDir holds the security component's per-service hook files;
	// proxy locations include <HooksDir>/<service>/<category>-*.conf, and a
	// glob without matches is valid.
	HooksDir = "/etc/nginx/lcm/hooks"
	// Marker is the first line of the rendered site; a file without it was
	// not written by LCM and is never replaced.
	Marker = "# Written by LCM ingress from /etc/lcm/host.json and the route options of services; do not edit."
)

// Site is everything the site file is rendered from. Values come from the
// host configuration (domain, listen addresses, directories, ports) and the
// accepted route options of services; all are checked again here.
type Site struct {
	Domain string
	// Listen are the addresses the servers bind to; empty binds all.
	Listen []string
	// ACMEWebroot is served at /.well-known/acme-challenge/ on port 80.
	ACMEWebroot string
	// TLS names the certificate; nil renders the port 80 server only.
	TLS  *TLSFiles
	HSTS bool
	// WellKnown are the domain-level files served below /.well-known/.
	WellKnown []WellKnown
	Routes    []Route
}

// TLSFiles are the certificate chain and key of the domain.
type TLSFiles struct {
	Certificate string
	Key         string
}

// WellKnown is one domain-level file: its name below /.well-known/ and the
// path of the copy nginx serves.
type WellKnown struct {
	Name string
	Path string
}

// Route is one service's route.
type Route struct {
	Service string
	Prefix  string
	// Port is the service's approved port; 0 has no proxy locations.
	Port int
	// Static is the directory served at the prefix; empty serves none.
	Static  string
	Options Options
}

// WellKnownTypes are the domain-level files that may be served and the
// fixed media type of each.
var WellKnownTypes = map[string]string{
	"assetlinks.json":            "application/json",
	"security.txt":               "text/plain",
	"apple-app-site-association": "application/json",
}

// CertificateFiles returns where certbot keeps the certificate of domain.
func CertificateFiles(domain string) TLSFiles {
	dir := "/etc/letsencrypt/live/" + domain
	return TLSFiles{Certificate: dir + "/fullchain.pem", Key: dir + "/privkey.pem"}
}

var (
	domainRe  = regexp.MustCompile(`^(?:[a-z0-9](?:[a-z0-9-]{0,61}[a-z0-9])?\.)+[a-z](?:[a-z0-9-]{0,61}[a-z0-9])?$`)
	serviceRe = regexp.MustCompile(`^[a-z][a-z0-9-]{0,26}$`)
	hostPath  = regexp.MustCompile(`^/[A-Za-z0-9._/-]*$`)
)

// ValidDomain checks a domain name: lower-case labels of letters, digits
// and "-", at least two, at most 253 characters.
func ValidDomain(d string) error {
	if len(d) > 253 || !domainRe.MatchString(d) {
		return fmt.Errorf("must be a lower-case domain name such as example.org (got %q)", printable(d))
	}
	return nil
}

// ValidListen checks a listen address: an IP address in canonical form.
func ValidListen(a string) error {
	ip, err := netip.ParseAddr(a)
	if err != nil || ip.Zone() != "" || ip.String() != a {
		return fmt.Errorf("must be an IP address in canonical form such as 192.0.2.10 or 2001:db8::1 (got %q)", printable(a))
	}
	return nil
}

// ValidHostPath checks an absolute, clean host path other than "/" of
// letters, digits and "._-/".
func ValidHostPath(p string) error {
	if !hostPath.MatchString(p) || path.Clean(p) != p || p == "/" {
		return fmt.Errorf("must be an absolute, clean path of letters, digits and ._-/ (got %q)", printable(p))
	}
	return nil
}

// ValidateRoute checks a route against what the host grants it: proxy
// locations need a port and static options a static directory.
func ValidateRoute(r Route) []Issue {
	var is issues
	if !serviceRe.MatchString(r.Service) {
		is.add("service", "is not a service name (got %q)", printable(r.Service))
	}
	if err := ValidPrefix(r.Prefix); err != nil {
		is.add("prefix", "%v", err)
	}
	if r.Port < 0 || r.Port > 65535 {
		is.add("port", "must be a port (got %d)", r.Port)
	}
	if r.Static != "" {
		if err := ValidHostPath(r.Static); err != nil {
			is.add("static", "%v", err)
		}
	}
	if r.Port == 0 && r.Static == "" {
		is.add("", "the route has neither a port nor a static directory")
	}
	is = append(is, r.Options.Validate()...)
	if r.Port == 0 && len(r.Options.Proxy) > 0 {
		is.add("proxy", "needs a port: the host configuration grants the service none")
	}
	if r.Static == "" && r.Options.Static != nil {
		is.add("static", "needs a static directory: the host configuration grants the service none (route.static in host.json)")
	}
	return is
}

// validate checks the whole site.
func (s Site) validate() []Issue {
	var is issues
	if err := ValidDomain(s.Domain); err != nil {
		is.add("domain", "%v", err)
	}
	seenListen := map[string]bool{}
	for _, a := range s.Listen {
		if err := ValidListen(a); err != nil {
			is.add("listen", "%v", err)
		}
		if seenListen[a] {
			is.add("listen", "%s is listed twice", a)
		}
		seenListen[a] = true
	}
	if err := ValidHostPath(s.ACMEWebroot); err != nil {
		is.add("acmeWebroot", "%v", err)
	}
	if t := s.TLS; t != nil {
		for _, p := range []string{t.Certificate, t.Key} {
			if err := ValidHostPath(p); err != nil {
				is.add("tls", "%v", err)
			}
		}
	}
	seenWK := map[string]bool{}
	for _, w := range s.WellKnown {
		if _, ok := WellKnownTypes[w.Name]; !ok || seenWK[w.Name] {
			is.add("wellKnown", "%q is not an allowed well-known file, or is listed twice", printable(w.Name))
		}
		seenWK[w.Name] = true
		if err := ValidHostPath(w.Path); err != nil {
			is.add("wellKnown."+w.Name, "%v", err)
		}
	}
	services, prefixes := map[string]bool{}, map[string]string{}
	for _, r := range s.Routes {
		for _, i := range ValidateRoute(r) {
			i.Path = strings.TrimSuffix("routes."+r.Service+"."+i.Path, ".")
			is = append(is, i)
		}
		if services[r.Service] {
			is.add("routes."+r.Service, "the service has two routes")
		}
		services[r.Service] = true
		if other, dup := prefixes[r.Prefix]; dup {
			is.add("routes."+r.Service+".prefix", "%s is also the prefix of %s", r.Prefix, other)
		}
		prefixes[r.Prefix] = r.Service
	}
	return is
}

// Render returns the site file. The same Site always renders the same
// bytes. It refuses invalid values and locations that two routes would
// share or that lie in another route's prefix.
func Render(s Site) ([]byte, error) {
	if is := s.validate(); len(is) > 0 {
		return nil, Join(is)
	}
	routes := slices.Clone(s.Routes)
	slices.SortFunc(routes, func(a, b Route) int {
		return cmp.Or(cmp.Compare(len(b.Prefix), len(a.Prefix)), cmp.Compare(a.Prefix, b.Prefix))
	})
	var locs []location
	for _, r := range routes {
		rl, err := routeLocations(r)
		if err != nil {
			return nil, err
		}
		locs = append(locs, rl...)
	}
	if err := checkLocations(routes, locs); err != nil {
		return nil, err
	}
	w := &writer{}
	w.line(Marker)
	w.line("# Change it with sudo bin/lcm host apply or through LCM; it is replaced as a whole.")
	w.blank()
	w.open("map $http_upgrade $lcm_connection_upgrade")
	w.line("default upgrade;")
	w.line(`"" close;`)
	w.close()
	w.blank()
	s.httpServer(w)
	if s.TLS != nil {
		w.blank()
		s.httpsServer(w, routes, locs)
	}
	if w.err != nil {
		return nil, w.err
	}
	return []byte(w.b.String()), nil
}

func (s Site) listen(w *writer, port int, ssl string) {
	if len(s.Listen) == 0 {
		w.line(fmt.Sprintf("listen %d%s;", port, ssl))
		w.line(fmt.Sprintf("listen [::]:%d%s;", port, ssl))
		return
	}
	for _, a := range s.Listen {
		host := a
		if strings.Contains(a, ":") {
			host = "[" + a + "]"
		}
		w.line("listen " + w.raw(host, addrToken) + ":" + strconv.Itoa(port) + ssl + ";")
	}
}

func (s Site) serverHead(w *writer, port int, ssl string) {
	s.listen(w, port, ssl)
	w.line("server_name " + w.q(s.Domain) + ";")
	w.line("server_tokens off;")
}

func (s Site) httpServer(w *writer) {
	w.open("server")
	s.serverHead(w, 80, "")
	w.line(`set $lcm_service "";`)
	w.line(`set $lcm_category "";`)
	w.blank()
	w.open("location ^~ /.well-known/acme-challenge/")
	w.line("root " + w.q(s.ACMEWebroot) + ";")
	w.line("default_type text/plain;")
	w.line("try_files $uri =404;")
	w.close()
	w.blank()
	w.open("location /")
	w.line("return 308 " + w.qv(lit("https://"), val(s.Domain), lit("$request_uri")) + ";")
	w.close()
	w.close()
}

func (s Site) httpsServer(w *writer, routes []Route, locs []location) {
	w.open("server")
	s.serverHead(w, 443, " ssl")
	w.line("ssl_certificate " + w.q(s.TLS.Certificate) + ";")
	w.line("ssl_certificate_key " + w.q(s.TLS.Key) + ";")
	w.line("ssl_protocols TLSv1.2 TLSv1.3;")
	w.line(`set $lcm_service "";`)
	w.line(`set $lcm_category "";`)
	wk := slices.Clone(s.WellKnown)
	slices.SortFunc(wk, func(a, b WellKnown) int { return cmp.Compare(a.Name, b.Name) })
	for _, f := range wk {
		w.blank()
		w.open("location = " + w.q("/.well-known/"+f.Name))
		w.line("alias " + w.q(f.Path) + ";")
		w.line("types { }")
		w.line("default_type " + WellKnownTypes[f.Name] + ";")
		s.headers(w, nil, [][2]string{{"Cache-Control", "public, max-age=3600"}})
		w.close()
	}
	w.blank()
	w.line("# The rest of /.well-known/ is reserved for the domain.")
	w.open("location ^~ /.well-known/")
	s.headers(w, nil, nil)
	w.line("return 404;")
	w.close()
	if !slices.ContainsFunc(routes, func(r Route) bool { return r.Prefix == "/" }) {
		w.blank()
		w.open("location /")
		s.headers(w, nil, nil)
		w.line("return 404;")
		w.close()
	}
	service := ""
	for _, l := range locs {
		w.blank()
		if l.service != service {
			service = l.service
			w.line("# Service " + w.raw(service, serviceRe) + ".")
		}
		head := "location " + w.q(l.path)
		if l.exact {
			head = "location = " + w.q(l.path)
		}
		w.open(head)
		w.line("set $lcm_service " + w.q(l.service) + ";")
		w.line("set $lcm_category " + w.q(l.category) + ";")
		for _, d := range l.body {
			w.line(d)
		}
		s.headers(w, l.headers, l.extra)
		if l.final != "" {
			w.line(l.final)
		}
		w.close()
	}
	w.close()
}

// headers writes the complete header set of a location: add_header in a
// location replaces every inherited one, so each location repeats them.
func (s Site) headers(w *writer, h *Headers, extra [][2]string) {
	for _, e := range extra {
		w.line("add_header " + e[0] + " " + w.q(e[1]) + " always;")
	}
	w.line(`add_header X-Content-Type-Options "nosniff" always;`)
	w.line(`add_header Referrer-Policy "strict-origin-when-cross-origin" always;`)
	if s.HSTS {
		w.line(`add_header Strict-Transport-Security "max-age=31536000" always;`)
	}
	if h != nil && h.CSP != "" {
		csp, err := ParseCSP(h.CSP)
		if err != nil {
			w.fail(fmt.Errorf("headers.csp: %w", err))
			return
		}
		name := "Content-Security-Policy"
		if h.CSPReportOnly {
			name += "-Report-Only"
		}
		w.line("add_header " + name + " " + w.q(csp) + " always;")
	}
}

// location is one rendered location of a service.
type location struct {
	service  string
	category string
	exact    bool
	path     string
	// redirectBare marks the redirect of the prefix without its final
	// "/", which lies outside the prefix by construction.
	redirectBare bool
	body         []string
	headers      *Headers
	extra        [][2]string
	// final is the last directive (a return), after the headers.
	final string
}

var gzipLines = []string{
	"gzip on;",
	"gzip_vary on;",
	"gzip_types text/css text/javascript application/javascript application/json image/svg+xml;",
}

// routeLocations builds the locations of one route from the fixed
// templates.
func routeLocations(r Route) ([]location, error) {
	w := &writer{}
	o := r.Options
	var out []location
	add := func(l location) {
		l.service, l.headers = r.Service, o.Headers
		out = append(out, l)
	}
	p := r.Prefix
	if p != "/" {
		bare := strings.TrimSuffix(p, "/")
		add(location{category: CategoryStatic, exact: true, path: bare, redirectBare: true,
			body:  []string{"absolute_redirect off;"},
			final: "return 308 " + w.qv(val(p), lit("$is_args$args")) + ";"})
	}
	upstream := func(u string) string {
		return w.qv(lit("http://127.0.0.1:"+strconv.Itoa(r.Port)), val(u))
	}
	for _, px := range o.Proxy {
		cat := cmp.Or(px.Category, CategoryAPI)
		body := []string{"include " + w.q(HooksDir+"/"+r.Service+"/"+cat+"-*.conf") + ";"}
		if px.BodyLimit != "" {
			body = append(body, "client_max_body_size "+w.raw(px.BodyLimit, sizeRe)+";")
		}
		body = append(body, "proxy_pass "+upstream(px.Upstream)+";")
		body = append(body, proxyHeaders...)
		if px.WebSocket {
			body = append(body,
				"proxy_http_version 1.1;",
				"proxy_set_header Upgrade $http_upgrade;",
				"proxy_set_header Connection $lcm_connection_upgrade;",
				"proxy_buffering off;")
		}
		if px.Timeout != "" {
			secs, _ := ParseTimeout(px.Timeout)
			t := strconv.Itoa(secs) + "s"
			body = append(body, "proxy_read_timeout "+t+";", "proxy_send_timeout "+t+";")
		}
		if px.CookiePath {
			body = append(body, "proxy_cookie_path ~^/$ "+w.q(p)+";")
		}
		add(location{category: cat, exact: px.Exact, path: p + px.Path, body: body})
	}
	docroot := func(rel string) string {
		if p == "/" {
			return "root " + w.q(r.Static) + ";"
		}
		return "alias " + w.q(r.Static+"/"+rel) + ";"
	}
	switch {
	case r.Static != "":
		st := Static{}
		if o.Static != nil {
			st = *o.Static
		}
		fallback := "=404"
		if st.SPA {
			fallback = w.q(p + "index.html")
		}
		body := []string{docroot(""), "try_files $uri $uri/ " + fallback + ";"}
		if st.NotFound != "" {
			body = append(body, "error_page 404 "+w.q(p+st.NotFound)+";")
		}
		body = append(body, "absolute_redirect off;")
		body = append(body, gzipLines...)
		add(location{category: CategoryStatic, path: p, body: body, extra: [][2]string{{"Cache-Control", "no-cache"}}})
		for _, d := range st.Immutable {
			body := append([]string{docroot(d), "try_files $uri =404;"}, gzipLines...)
			add(location{category: CategoryStatic, path: p + d, body: body,
				extra: [][2]string{{"Cache-Control", "public, max-age=31536000, immutable"}}})
		}
		for _, d := range st.Downloads {
			add(location{category: CategoryStatic, exact: true, path: p + d.Path,
				body:  []string{docroot(d.Path), "types { }", "default_type " + w.raw(d.Type, mimeToken) + ";"},
				extra: [][2]string{{"Cache-Control", "no-cache"}, {"Content-Disposition", "attachment"}}})
		}
	case r.Port != 0:
		cat := CategoryAPI
		body := append([]string{
			"include " + w.q(HooksDir+"/"+r.Service+"/"+cat+"-*.conf") + ";",
			"proxy_pass " + upstream("/") + ";",
		}, proxyHeaders...)
		add(location{category: cat, path: p, body: body})
	}
	for _, rd := range o.Redirects {
		code := cmp.Or(rd.Code, 301)
		target := p + strings.TrimPrefix(rd.To, "/")
		final := "return " + strconv.Itoa(code) + " " + w.q(target) + ";"
		froms := []string{rd.From}
		if strings.HasSuffix(rd.From, "/") {
			froms = append(froms, strings.TrimSuffix(rd.From, "/"))
		}
		for _, f := range froms {
			add(location{category: CategoryStatic, exact: true, path: p + f, body: []string{"absolute_redirect off;"}, final: final})
		}
	}
	if w.err != nil {
		return nil, fmt.Errorf("routes.%s: %w", r.Service, w.err)
	}
	slices.SortStableFunc(out, func(a, b location) int {
		return cmp.Or(cmp.Compare(len(b.path), len(a.path)), cmp.Compare(a.path, b.path), boolOrder(a.exact, b.exact))
	})
	return out, nil
}

func boolOrder(a, b bool) int {
	switch {
	case a == b:
		return 0
	case a:
		return -1
	}
	return 1
}

var proxyHeaders = []string{
	"proxy_set_header Host $host;",
	"proxy_set_header X-Real-IP $remote_addr;",
	"proxy_set_header X-Forwarded-For $remote_addr;",
	"proxy_set_header X-Forwarded-Proto $scheme;",
}

// checkLocations refuses a location that two routes (or one route twice)
// would define, and one that lies in a longer prefix of another route,
// which would take its requests.
func checkLocations(routes []Route, locs []location) error {
	var is issues
	seen := map[string]string{}
	for _, l := range locs {
		key := l.path
		if l.exact {
			key = "= " + l.path
		}
		if other, dup := seen[key]; dup {
			is.add("routes."+l.service, "the location %s is also defined by %s", key, other)
			continue
		}
		seen[key] = l.service
		if l.redirectBare {
			continue
		}
		owner, best := "", -1
		for _, r := range routes {
			if strings.HasPrefix(l.path, r.Prefix) && len(r.Prefix) > best {
				owner, best = r.Service, len(r.Prefix)
			}
		}
		if owner != l.service {
			is.add("routes."+l.service, "the location %s lies in the prefix of %s", l.path, owner)
		}
	}
	return Join(is)
}

// writer builds the file with indentation. Values reach it only through
// q, qv and raw, which check them again and quote them.
type writer struct {
	b     strings.Builder
	depth int
	err   error
}

func (w *writer) fail(err error) {
	if w.err == nil {
		w.err = err
	}
}

func (w *writer) line(s string) {
	w.b.WriteString(strings.Repeat("    ", w.depth))
	w.b.WriteString(s)
	w.b.WriteByte('\n')
}

func (w *writer) blank() { w.b.WriteByte('\n') }

func (w *writer) open(head string) {
	w.line(head + " {")
	w.depth++
}

func (w *writer) close() {
	w.depth--
	w.line("}")
}

// safeQuoted reports whether a value can be written inside double quotes
// without nginx interpreting any of it: printable ASCII without '"', '\',
// '$', '{' and '}'. The grammars of every value are narrower; this is the
// last line of defense.
func safeQuoted(s string) bool {
	for i := 0; i < len(s); i++ {
		c := s[i]
		if c < 0x20 || c > 0x7e || c == '"' || c == '\\' || c == '$' || c == '{' || c == '}' {
			return false
		}
	}
	return s != ""
}

// q quotes one value.
func (w *writer) q(s string) string { return w.qv(val(s)) }

// part is a piece of a quoted string: a value (checked) or a literal of the
// renderer (such as an nginx variable).
type part struct {
	s       string
	literal bool
}

func val(s string) part { return part{s: s} }
func lit(s string) part { return part{s: s, literal: true} }

// qv quotes a string made of values and literals.
func (w *writer) qv(parts ...part) string {
	var b strings.Builder
	b.WriteByte('"')
	for _, p := range parts {
		if !p.literal && !safeQuoted(p.s) {
			w.fail(fmt.Errorf("the value %q cannot be written to the nginx configuration", printable(p.s)))
			continue
		}
		b.WriteString(p.s)
	}
	b.WriteByte('"')
	return b.String()
}

var (
	addrToken = regexp.MustCompile(`^(?:[0-9.]+|\[[0-9a-f:.]+\])$`)
	mimeToken = regexp.MustCompile(`^[a-z]+/[a-z0-9.+-]+$`)
)

// raw writes a value unquoted after checking it against re.
func (w *writer) raw(s string, re *regexp.Regexp) string {
	if !re.MatchString(s) {
		w.fail(fmt.Errorf("the value %q cannot be written to the nginx configuration", printable(s)))
		return "invalid"
	}
	return s
}
