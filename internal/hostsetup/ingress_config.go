package hostsetup

import (
	"errors"
	"fmt"
	"maps"
	"regexp"
	"slices"
	"strings"

	"github.com/ACaccel/lcm/internal/ingress"
)

// Ingress configures the ingress component: one nginx site for the domain,
// rendered from this section and the services' routes.
type Ingress struct {
	Domain string `json:"domain"`
	// Listen are the addresses nginx binds the site to; empty binds all.
	Listen []string `json:"listen,omitempty"`
	// ACMEWebroot is where certbot's webroot authenticator writes its
	// challenges; empty is DefaultACMEWebroot.
	ACMEWebroot string     `json:"acmeWebroot,omitempty"`
	TLS         IngressTLS `json:"tls"`
	// HSTS sends Strict-Transport-Security from every location.
	HSTS bool `json:"hsts,omitempty"`
	// WellKnown maps the allowed domain-level file names to root-owned
	// files whose content is served below /.well-known/.
	WellKnown map[string]string `json:"wellKnown,omitempty"`
	// Adopt names the hand-made site in /etc/nginx/sites-enabled that the
	// first application disables (the file in sites-available stays).
	Adopt string `json:"adopt,omitempty"`
	// LegacyLock is a lock file that hand-made scripts still take while
	// they edit nginx; while it exists, applying the site takes it too.
	LegacyLock string `json:"legacyLock,omitempty"`
}

// IngressTLS configures certbot for the domain.
type IngressTLS struct {
	Email string `json:"email"`
	// KeyType is "ecdsa" (the default) or "rsa".
	KeyType string `json:"keyType,omitempty"`
}

// Ingress paths. The site and the hooks follow Debian's nginx layout.
const (
	DefaultACMEWebroot = "/var/www/lcm-acme"
	NginxDir           = "/etc/nginx"
	SitesAvailable     = NginxDir + "/sites-available"
	SitesEnabled       = NginxDir + "/sites-enabled"
	SiteName           = "lcm-ingress.conf"
	SiteFile           = SitesAvailable + "/" + SiteName
	SiteLink           = SitesEnabled + "/" + SiteName
	// IngressState is the root-only state of the component: the accepted
	// route options of services, the last result and what cutover
	// disabled.
	IngressState  = "/var/lib/lcm-ingress"
	IngressRoutes = IngressState + "/routes"
	// IngressWWW holds the copies of the well-known files nginx serves.
	IngressWWW       = "/var/www/lcm-ingress"
	IngressWellKnown = IngressWWW + "/well-known"
	// IngressLock serializes every change of the site.
	IngressLock = "/run/lcm-ingress.lock"
	// RenewalHook is the certbot deploy hook that reloads nginx.
	RenewalHooksDir = "/etc/letsencrypt/renewal-hooks/deploy"
	RenewalHook     = RenewalHooksDir + "/lcm-ingress-reload"
	RenewalDir      = "/etc/letsencrypt/renewal"
	// Nginx is the nginx program, run by absolute path.
	Nginx     = "/usr/sbin/nginx"
	Systemctl = "/usr/bin/systemctl"

	ComponentIngress = "ingress"
)

// RenewalConf returns certbot's renewal configuration of a domain.
func RenewalConf(domain string) string { return RenewalDir + "/" + domain + ".conf" }

// maxWellKnown bounds a well-known file.
const maxWellKnown = 64 << 10

var (
	siteFileName = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9._-]{0,99}$`)
	emailRe      = regexp.MustCompile(`^[A-Za-z0-9_%+][A-Za-z0-9._%+-]{0,63}@[a-z0-9](?:[a-z0-9.-]{0,251}[a-z0-9])?$`)
)

// webroot returns the ACME webroot with its default.
func (i *Ingress) webroot() string {
	if i.ACMEWebroot == "" {
		return DefaultACMEWebroot
	}
	return i.ACMEWebroot
}

func (i *Ingress) keyType() string {
	if i.TLS.KeyType == "" {
		return "ecdsa"
	}
	return i.TLS.KeyType
}

// publicTrees are where a directory nginx serves to everyone must not be:
// the system's own trees, home directories and the data of LCM and its
// services.
var publicTrees = []string{
	"/boot", "/bin", "/sbin", "/lib", "/lib32", "/lib64", "/usr", "/etc", "/dev", "/proc", "/sys", "/run", "/tmp",
	"/snap", "/root", "/home", "/var/lib", "/var/log", "/var/cache", "/var/backups", "/var/spool", "/var/tmp", "/var/mail",
}

// checkPublicDir refuses a directory that would publish what is not meant
// to be public.
func (c Config) checkPublicDir(dir string) error {
	if err := checkHostPath(dir); err != nil {
		return err
	}
	for _, t := range publicTrees {
		if within(dir, t) {
			return fmt.Errorf("must not be in %s", t)
		}
	}
	if slices.Contains(systemDirs, dir) {
		return fmt.Errorf("must be a directory of its own, not %s", dir)
	}
	protected := []string{OptDir, IngressWWW}
	if c.Backup != nil {
		protected = append(protected, c.Backup.Dir)
	}
	for _, r := range slices.Sorted(maps.Keys(c.Runtimes)) {
		protected = append(protected, c.Runtimes[r])
	}
	for _, d := range protected {
		if within(dir, d) || within(d, dir) {
			return fmt.Errorf("must not overlap %s", d)
		}
	}
	return nil
}

func (c Config) validateIngress() []error {
	var errs []error
	prefixes := map[string]string{}
	for _, name := range slices.Sorted(maps.Keys(c.Services)) {
		r := c.Services[name].Route
		if r == nil {
			continue
		}
		at := "services." + name + ".route"
		if err := ingress.ValidPrefix(r.Prefix); err != nil {
			errs = append(errs, fmt.Errorf("%s.prefix %w", at, err))
		} else if other, dup := prefixes[r.Prefix]; dup {
			errs = append(errs, fmt.Errorf("%s.prefix %s is also the prefix of services.%s", at, r.Prefix, other))
		} else {
			prefixes[r.Prefix] = name
		}
		if r.Static != "" {
			if err := c.checkPublicDir(r.Static); err != nil {
				errs = append(errs, fmt.Errorf("%s.static %w", at, err))
			}
		}
		if r.Static == "" && c.Services[name].Port == 0 {
			errs = append(errs, fmt.Errorf("%s needs a port or a static directory", at))
		}
	}
	i := c.Components.Ingress
	if i == nil {
		return errs
	}
	at := "components.ingress"
	if err := ingress.ValidDomain(i.Domain); err != nil {
		errs = append(errs, fmt.Errorf("%s.domain %w", at, err))
	}
	seen := map[string]bool{}
	for _, a := range i.Listen {
		if err := ingress.ValidListen(a); err != nil {
			errs = append(errs, fmt.Errorf("%s.listen %w", at, err))
		} else if seen[a] {
			errs = append(errs, fmt.Errorf("%s.listen lists %s twice", at, a))
		}
		seen[a] = true
	}
	if len(i.Listen) > 8 {
		errs = append(errs, fmt.Errorf("%s.listen has more than 8 addresses", at))
	}
	if err := c.checkPublicDir(i.webroot()); err != nil {
		errs = append(errs, fmt.Errorf("%s.acmeWebroot %w", at, err))
	}
	for _, name := range slices.Sorted(maps.Keys(c.Services)) {
		if r := c.Services[name]; r.Route != nil && r.Route.Static != "" && (within(r.Route.Static, i.webroot()) || within(i.webroot(), r.Route.Static)) {
			errs = append(errs, fmt.Errorf("%s.acmeWebroot must not overlap services.%s.route.static", at, name))
		}
	}
	if !emailRe.MatchString(i.TLS.Email) || len(i.TLS.Email) > 254 {
		errs = append(errs, fmt.Errorf("%s.tls.email must be an email address for certbot (got %q)", at, printable(i.TLS.Email, 100)))
	}
	switch i.TLS.KeyType {
	case "", "ecdsa", "rsa":
	default:
		errs = append(errs, fmt.Errorf("%s.tls.keyType must be ecdsa or rsa (got %q)", at, printable(i.TLS.KeyType, 32)))
	}
	for _, name := range slices.Sorted(maps.Keys(i.WellKnown)) {
		if _, ok := ingress.WellKnownTypes[name]; !ok {
			errs = append(errs, fmt.Errorf("%s.wellKnown: %q is not one of %s", at, printable(name, 64), strings.Join(slices.Sorted(maps.Keys(ingress.WellKnownTypes)), ", ")))
			continue
		}
		if err := checkHostPath(i.WellKnown[name]); err != nil {
			errs = append(errs, fmt.Errorf("%s.wellKnown.%s %w", at, name, err))
		}
	}
	if i.Adopt != "" && (!siteFileName.MatchString(i.Adopt) || i.Adopt == SiteName || strings.Contains(i.Adopt, "..")) {
		errs = append(errs, fmt.Errorf("%s.adopt must name a site file in %s other than %s (got %q)", at, SitesEnabled, SiteName, printable(i.Adopt, 100)))
	}
	if i.LegacyLock != "" {
		if err := checkHostPath(i.LegacyLock); err != nil || !strings.HasPrefix(i.LegacyLock, "/run/") {
			errs = append(errs, fmt.Errorf("%s.legacyLock must be a lock file below /run (got %q)", at, printable(i.LegacyLock, 100)))
		}
	}
	return errs
}

// ErrNoIngress reports a host configuration without the ingress component.
var ErrNoIngress = errors.New("components.ingress is not configured in " + ConfigPath)
