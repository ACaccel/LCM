// Package ingress holds the presentation options of a service's route and
// the deterministic renderer of LCM's nginx site. It depends on the standard
// library only, so that the document model (internal/model), host setup
// (internal/hostsetup) and the deploy helper (internal/deployhelper) can all
// validate the same values the same way.
//
// Nothing a document says reaches nginx as configuration syntax: every value
// is checked against a strict grammar here, checked again by the renderer,
// and emitted inside double quotes; locations are built from fixed templates
// only. Whatever a document holds, the host configuration alone decides the
// domain, the prefix, the port and the static directory of a route.
package ingress

import (
	"errors"
	"fmt"
	"regexp"
	"slices"
	"strconv"
	"strings"
)

// Options are the presentation options of one service's route: the
// optional "route" section of a service document. They carry no
// privileges; all paths are relative to the route's prefix.
type Options struct {
	// Proxy adds locations below the prefix that go to the service's
	// approved port.
	Proxy []Proxy `yaml:"proxy,omitempty" json:"proxy,omitempty"`
	// Static configures the static directory the host configuration grants.
	Static *Static `yaml:"static,omitempty" json:"static,omitempty"`
	// Redirects answer paths below the prefix with a redirect to another
	// path below the prefix.
	Redirects []Redirect `yaml:"redirects,omitempty" json:"redirects,omitempty"`
	// Headers are the response headers of the service's locations.
	Headers *Headers `yaml:"headers,omitempty" json:"headers,omitempty"`
}

// Proxy is one proxied location.
type Proxy struct {
	// Path is below the prefix; a prefix location ends with "/".
	Path string `yaml:"path" json:"path"`
	// Exact matches Path alone instead of everything below it.
	Exact bool `yaml:"exact,omitempty" json:"exact,omitempty"`
	// Upstream is the absolute path on the service that Path maps to.
	Upstream string `yaml:"upstream" json:"upstream"`
	// BodyLimit is the largest request body, as nginx sizes (3m, 512k).
	BodyLimit string `yaml:"bodyLimit,omitempty" json:"bodyLimit,omitempty"`
	// CookiePath rewrites the cookie path "/" of the service's responses
	// to the prefix.
	CookiePath bool `yaml:"cookiePath,omitempty" json:"cookiePath,omitempty"`
	// WebSocket passes connection upgrades through and does not buffer.
	WebSocket bool `yaml:"websocket,omitempty" json:"websocket,omitempty"`
	// Timeout bounds reading from and writing to the service (75s, 5m).
	Timeout string `yaml:"timeout,omitempty" json:"timeout,omitempty"`
	// Category names the location for the security component's hooks;
	// empty is "api".
	Category string `yaml:"category,omitempty" json:"category,omitempty"`
}

// Static configures how the static directory is served.
type Static struct {
	// SPA answers unknown paths with index.html instead of 404.
	SPA bool `yaml:"spa,omitempty" json:"spa,omitempty"`
	// NotFound is the page served with a 404, relative to the directory.
	NotFound string `yaml:"notFound,omitempty" json:"notFound,omitempty"`
	// Immutable are directories whose files never change in place; they
	// are cached for a year and never fall back to index.html.
	Immutable []string `yaml:"immutable,omitempty" json:"immutable,omitempty"`
	// Downloads are files served as attachments with a fixed type.
	Downloads []Download `yaml:"downloads,omitempty" json:"downloads,omitempty"`
}

// Download is one file served as an attachment.
type Download struct {
	Path string `yaml:"path" json:"path"`
	Type string `yaml:"type" json:"type"`
}

// Redirect answers From with a redirect to To. Both stay below the
// prefix: From is relative to it and To is written from the prefix's root
// ("/" is the prefix itself).
type Redirect struct {
	From string `yaml:"from" json:"from"`
	To   string `yaml:"to" json:"to"`
	Code int    `yaml:"code,omitempty" json:"code,omitempty"`
}

// Headers are the response headers a service chooses.
type Headers struct {
	// CSP is the Content-Security-Policy, in the grammar of ParseCSP.
	CSP string `yaml:"csp,omitempty" json:"csp,omitempty"`
	// CSPReportOnly sends it as Content-Security-Policy-Report-Only.
	CSPReportOnly bool `yaml:"cspReportOnly,omitempty" json:"cspReportOnly,omitempty"`
}

// Location categories, the fixed vocabulary of the security component's
// hooks.
const (
	CategoryAPI    = "api"
	CategoryAuth   = "auth"
	CategoryUpload = "upload"
	CategorySocket = "socket"
	CategoryHealth = "health"
	CategoryStatic = "static"
)

// Categories lists the categories in a fixed order.
var Categories = []string{CategoryAPI, CategoryAuth, CategoryUpload, CategorySocket, CategoryHealth, CategoryStatic}

// DownloadTypes are the media types a download may be served with.
var DownloadTypes = []string{
	"application/gzip",
	"application/json",
	"application/octet-stream",
	"application/pdf",
	"application/vnd.android.package-archive",
	"application/x-tar",
	"application/zip",
	"text/csv",
	"text/plain",
}

// RedirectCodes are the status codes a redirect may use; empty is 301.
var RedirectCodes = []int{301, 302, 307, 308}

// Limits of the options.
const (
	MaxProxies   = 32
	MaxImmutable = 16
	MaxDownloads = 16
	MaxRedirects = 32
	// MaxBodyLimit is the largest bodyLimit in bytes (64m).
	MaxBodyLimit = 64 << 20
	// MaxTimeoutSeconds bounds timeout (1h).
	MaxTimeoutSeconds = 3600
	maxPath           = 200
	maxSegment        = 100
)

// Issue is one problem of a value, with the path of the field it concerns
// (such as "proxy[0].path"), relative to the options.
type Issue struct {
	Path    string
	Message string
}

func (i Issue) String() string {
	if i.Path == "" {
		return i.Message
	}
	return i.Path + ": " + i.Message
}

// Join returns the issues as one error, or nil.
func Join(issues []Issue) error {
	if len(issues) == 0 {
		return nil
	}
	errs := make([]error, len(issues))
	for i, is := range issues {
		errs[i] = errors.New(is.String())
	}
	return errors.Join(errs...)
}

type issues []Issue

func (is *issues) add(path, format string, args ...any) {
	*is = append(*is, Issue{Path: path, Message: fmt.Sprintf(format, args...)})
}

var (
	// segment is one path component: letters, digits, "_", "-" and ".",
	// never starting with "." (so no ".", "..", dot files or .well-known).
	segment = regexp.MustCompile(`^[A-Za-z0-9_-][A-Za-z0-9._-]*$`)
	sizeRe  = regexp.MustCompile(`^([1-9][0-9]{0,8})([km]?)$`)
	timeRe  = regexp.MustCompile(`^([1-9][0-9]{0,4})([smh]?)$`)
)

// RelPathRule describes the paths below a prefix, for messages.
const RelPathRule = `a relative path of letters, digits and ._- components separated by "/", none starting with "."`

// validRel accepts a relative path below a prefix: components of letters,
// digits and "._-", none starting with ".", with an optional final "/".
// dir requires (true) or forbids (false) the final "/".
func validRel(p string, dir bool) bool {
	if p == "" || len(p) > maxPath || strings.HasSuffix(p, "/") != dir {
		return false
	}
	for _, s := range strings.Split(strings.TrimSuffix(p, "/"), "/") {
		if len(s) > maxSegment || !segment.MatchString(s) {
			return false
		}
	}
	return true
}

// validRelAny accepts a relative path in either form.
func validRelAny(p string) bool { return validRel(p, true) || validRel(p, false) }

// ValidPrefix checks the prefix of a route: "/" or an absolute path of
// components ending with "/", none starting with "." (so "/.well-known/",
// which the site reserves, is never one).
func ValidPrefix(p string) error {
	if p == "/" || strings.HasPrefix(p, "/") && validRel(p[1:], true) {
		return nil
	}
	return fmt.Errorf(`must be "/" or an absolute path ending with "/" of letters, digits and ._- components, none starting with "." (got %q)`, printable(p))
}

// validTarget accepts the target of a redirect: "/" (the prefix itself) or
// "/" followed by a relative path.
func validTarget(p string) bool { return p == "/" || strings.HasPrefix(p, "/") && validRelAny(p[1:]) }

// validUpstream accepts an absolute path on the service.
func validUpstream(p string) bool { return validTarget(p) }

// ParseSize returns the bytes of an nginx size such as "3m" or "512k".
func ParseSize(s string) (int64, bool) {
	m := sizeRe.FindStringSubmatch(s)
	if m == nil {
		return 0, false
	}
	n, err := strconv.ParseInt(m[1], 10, 64)
	if err != nil {
		return 0, false
	}
	switch m[2] {
	case "k":
		n <<= 10
	case "m":
		n <<= 20
	}
	return n, true
}

// ParseTimeout returns the seconds of a timeout such as "75s", "5m" or
// "1h"; a bare number is seconds.
func ParseTimeout(s string) (int, bool) {
	m := timeRe.FindStringSubmatch(s)
	if m == nil {
		return 0, false
	}
	n, err := strconv.Atoi(m[1])
	if err != nil {
		return 0, false
	}
	switch m[2] {
	case "m":
		n *= 60
	case "h":
		n *= 3600
	}
	return n, true
}

// Validate checks the form of the options. Whether the host grants what
// they need (a port for proxy locations, a static directory for static)
// and whether their locations collide with other routes is checked by
// ValidateRoute and Render.
func (o Options) Validate() []Issue {
	var is issues
	if len(o.Proxy) > MaxProxies {
		is.add("proxy", "at most %d locations are allowed", MaxProxies)
	} else {
		for i, p := range o.Proxy {
			p.validate(fmt.Sprintf("proxy[%d]", i), &is)
		}
	}
	if s := o.Static; s != nil {
		s.validate("static", &is)
	}
	if len(o.Redirects) > MaxRedirects {
		is.add("redirects", "at most %d redirects are allowed", MaxRedirects)
	} else {
		for i, r := range o.Redirects {
			at := fmt.Sprintf("redirects[%d]", i)
			if !validRelAny(r.From) {
				is.add(at+".from", "must be %s (got %q)", RelPathRule, printable(r.From))
			}
			if !validTarget(r.To) {
				is.add(at+".to", `must be "/" (the prefix) or "/" followed by %s (got %q)`, RelPathRule, printable(r.To))
			}
			if r.Code != 0 && !slices.Contains(RedirectCodes, r.Code) {
				is.add(at+".code", "must be one of %v (got %d)", RedirectCodes, r.Code)
			}
		}
	}
	if h := o.Headers; h != nil && h.CSP != "" {
		if _, err := ParseCSP(h.CSP); err != nil {
			is.add("headers.csp", "%v", err)
		}
	}
	return is
}

func (p Proxy) validate(at string, is *issues) {
	switch {
	case p.Exact && !validRelAny(p.Path):
		is.add(at+".path", "must be %s (got %q)", RelPathRule, printable(p.Path))
	case !p.Exact && !validRel(p.Path, true):
		is.add(at+".path", `must be %s ending with "/" unless exact is set (got %q)`, RelPathRule, printable(p.Path))
	}
	switch {
	case !validUpstream(p.Upstream):
		is.add(at+".upstream", `must be "/" or "/" followed by %s (got %q)`, RelPathRule, printable(p.Upstream))
	case !p.Exact && !strings.HasSuffix(p.Upstream, "/"):
		is.add(at+".upstream", `must end with "/" like path, so that the rest of the request path is kept`)
	}
	if p.BodyLimit != "" {
		if n, ok := ParseSize(p.BodyLimit); !ok || n > MaxBodyLimit {
			is.add(at+".bodyLimit", "must be a size such as 512k or 3m, at most 64m (got %q)", printable(p.BodyLimit))
		}
	}
	if p.Timeout != "" {
		if n, ok := ParseTimeout(p.Timeout); !ok || n > MaxTimeoutSeconds {
			is.add(at+".timeout", "must be a duration such as 75s or 5m, at most 1h (got %q)", printable(p.Timeout))
		}
	}
	if p.Category != "" && !slices.Contains(Categories, p.Category) {
		is.add(at+".category", "must be one of %s (got %q)", strings.Join(Categories, ", "), printable(p.Category))
	}
}

func (s Static) validate(at string, is *issues) {
	if s.NotFound != "" && !validRel(s.NotFound, false) {
		is.add(at+".notFound", "must be a file, %s (got %q)", RelPathRule, printable(s.NotFound))
	}
	if len(s.Immutable) > MaxImmutable {
		is.add(at+".immutable", "at most %d directories are allowed", MaxImmutable)
	} else {
		for i, d := range s.Immutable {
			if !validRel(d, true) {
				is.add(fmt.Sprintf("%s.immutable[%d]", at, i), `must be a directory, %s ending with "/" (got %q)`, RelPathRule, printable(d))
			}
		}
	}
	if len(s.Downloads) > MaxDownloads {
		is.add(at+".downloads", "at most %d downloads are allowed", MaxDownloads)
		return
	}
	for i, d := range s.Downloads {
		dat := fmt.Sprintf("%s.downloads[%d]", at, i)
		if !validRel(d.Path, false) {
			is.add(dat+".path", "must be a file, %s (got %q)", RelPathRule, printable(d.Path))
		}
		if !slices.Contains(DownloadTypes, d.Type) {
			is.add(dat+".type", "must be one of %s (got %q)", strings.Join(DownloadTypes, ", "), printable(d.Type))
		}
	}
}

// printable shortens a value for a message and replaces what a terminal or
// log would interpret.
func printable(s string) string {
	const max = 80
	var b strings.Builder
	for _, r := range s {
		if b.Len() >= max {
			b.WriteString("...")
			break
		}
		if r < 0x20 || r == 0x7f || r > 0x7e {
			b.WriteByte('?')
			continue
		}
		b.WriteRune(r)
	}
	return b.String()
}
