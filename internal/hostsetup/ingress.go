package hostsetup

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"maps"
	"net"
	"path"
	"slices"
	"strconv"
	"strings"
	"time"

	"github.com/ACaccel/lcm/internal/clock"
	"github.com/ACaccel/lcm/internal/ingress"
)

// The ingress site is changed by "lcm host apply", "lcm host cert issue",
// "lcm host ingress revert" and the deploy helper's ingress.apply, all with
// the sequence below, under IngressLock (and the legacy lock while it
// exists):
//
//  1. Render the whole site from host.json and the accepted route options.
//  2. Replace the site file atomically (the previous content is kept in
//     the state directory), enable it and disable the adopted site link.
//  3. Test the complete configuration with "nginx -t": exactly what a
//     reload would load, the new file in place.
//  4. Reload nginx and probe every route on the listen address.
//  5. On any failure put back the previous file and links, and reload
//     again if nginx was reloaded.
//
// It never touches a file it did not create, except the one adopted link.

// Timeouts of the ingress commands.
const (
	nginxTestTimeout = 30 * time.Second
	reloadTimeout    = 3 * time.Minute
	certbotTimeout   = 5 * time.Minute
	probeTimeout     = 5 * time.Second
	// ProbePhase bounds the probes of all routes after a reload.
	ProbePhase = 2 * time.Minute
	// maxSite bounds the site file that is read back.
	maxSite = 4 << 20
)

// IngressEnv is what changing the site uses. Host setup and the deploy
// helper fill it with their own command runner; tests with fakes.
type IngressEnv struct {
	FS    FS
	Clock clock.Clock
	// RootUID and RootGID own every trusted file and what is written: 0 on
	// a host.
	RootUID, RootGID int
	// Run runs a program by absolute path and returns its output.
	Run func(ctx context.Context, timeout time.Duration, argv ...string) (string, error)
	// Probe sends one request and returns the HTTP status.
	Probe func(ctx context.Context, p Probe) (int, error)
	// ProbeAttempts (at least one) and ProbeWait pace the probes after a
	// reload, which nginx applies asynchronously.
	ProbeAttempts int
	ProbeWait     time.Duration
}

// Probe is one request that checks a route after a reload.
type Probe struct {
	Service string
	// Scheme is "https", or "http" while there is no certificate.
	Scheme string
	// Address is the IP and port to connect to.
	Address string
	// Host is the domain, sent as Host and SNI.
	Host string
	Path string
}

func (p Probe) String() string { return p.Scheme + "://" + p.Host + p.Path }

// LastApply is the result of the last change of the site.
type LastApply struct {
	At      int64  `json:"at"`
	Outcome string `json:"outcome"`
	Message string `json:"message,omitempty"`
	SHA256  string `json:"sha256,omitempty"`
}

// adoptedRecord remembers the hand-made site link that the first
// application disabled, so that revert puts back the same link.
type adoptedRecord struct {
	Name   string `json:"name"`
	Target string `json:"target"`
}

// State files below IngressState.
const (
	lastApplyFile = IngressState + "/last-apply.json"
	previousFile  = IngressState + "/previous.conf"
	adoptedFile   = IngressState + "/adopted.json"
	revertedFile  = IngressState + "/reverted.json"
)

// ownedBy refuses a path that someone other than uid could change.
func ownedBy(p string, info Info, uid int) error {
	switch {
	case info.IsLink():
		return fmt.Errorf("%s is a symbolic link", p)
	case info.UID != uid:
		return fmt.Errorf("%s is not owned by root", p)
	case info.Mode.Perm()&0o022 != 0:
		return fmt.Errorf("%s is writable by group or others", p)
	}
	return nil
}

// trustedAs checks that p and its ancestors exist, are no links and only
// uid can change them; p must be a directory or, with file, a regular file.
func trustedAs(fsys FS, p string, uid int, file bool) (Info, error) {
	var info Info
	for _, a := range ancestry(p) {
		var err error
		if info, err = fsys.Lstat(a); err != nil {
			return info, err
		}
		if err := ownedBy(a, info, uid); err != nil {
			return info, err
		}
		if a != p && !info.IsDir() {
			return info, fmt.Errorf("%s is not a directory", a)
		}
	}
	if file && !info.IsRegular() || !file && !info.IsDir() {
		return info, fmt.Errorf("%s is not a %s", p, map[bool]string{true: "regular file", false: "directory"}[file])
	}
	return info, nil
}

// readLimited reads a regular file of at most max bytes.
func readLimited(fsys FS, p string, max int64) ([]byte, error) {
	f, err := fsys.Open(p)
	if err != nil {
		return nil, err
	}
	defer func() { _ = f.Close() }()
	data, err := io.ReadAll(io.LimitReader(f, max+1))
	if err != nil {
		return nil, err
	}
	if int64(len(data)) > max {
		return nil, fmt.Errorf("%s is larger than %d bytes", p, max)
	}
	return data, nil
}

func digest(b []byte) string {
	sum := sha256.Sum256(b)
	return hex.EncodeToString(sum[:])
}

// RouteOptionsFile is where the accepted route options of a service are
// kept.
func RouteOptionsFile(service string) string { return IngressRoutes + "/" + service + ".json" }

// ReadRouteOptions returns the accepted route options of every service
// that has some. A service without a file has none.
func ReadRouteOptions(fsys FS, rootUID int) (map[string]ingress.Options, error) {
	out := map[string]ingress.Options{}
	if _, err := fsys.Lstat(IngressRoutes); errors.Is(err, fs.ErrNotExist) {
		return out, nil
	}
	if _, err := trustedAs(fsys, IngressRoutes, rootUID, false); err != nil {
		return nil, err
	}
	names, err := fsys.ReadDir(IngressRoutes)
	if err != nil {
		return nil, err
	}
	for _, n := range names {
		service, ok := strings.CutSuffix(n, ".json")
		if !ok || ValidServiceName(service) != nil {
			continue
		}
		info, err := fsys.Lstat(IngressRoutes + "/" + n)
		if err != nil {
			return nil, err
		}
		if err := ownedBy(IngressRoutes+"/"+n, info, rootUID); err != nil || !info.IsRegular() {
			return nil, fmt.Errorf("%s/%s is not a root-owned regular file", IngressRoutes, n)
		}
		data, err := readLimited(fsys, IngressRoutes+"/"+n, 256<<10)
		if err != nil {
			return nil, err
		}
		o, err := DecodeRouteOptions(data)
		if err != nil {
			return nil, fmt.Errorf("%s/%s: %w", IngressRoutes, n, err)
		}
		out[service] = o
	}
	return out, nil
}

// DecodeRouteOptions decodes stored route options strictly and validates
// their form.
func DecodeRouteOptions(data []byte) (ingress.Options, error) {
	var o ingress.Options
	dec := json.NewDecoder(bytes.NewReader(data))
	dec.DisallowUnknownFields()
	if err := dec.Decode(&o); err != nil {
		return o, err
	}
	if dec.More() {
		return o, errors.New("unexpected data after the options")
	}
	return o, ingress.Join(o.Validate())
}

// EncodeRouteOptions returns the stored form of route options.
func EncodeRouteOptions(o ingress.Options) []byte {
	b, _ := json.MarshalIndent(o, "", "  ") // plain structs always marshal
	return append(b, '\n')
}

// SiteFor returns the site of the configuration: the routes of the granted
// services with their accepted options. Options of services without a route
// are ignored. tls includes the certificate.
func SiteFor(cfg Config, options map[string]ingress.Options, tls bool) ingress.Site {
	i := cfg.Components.Ingress
	s := ingress.Site{Domain: i.Domain, Listen: i.Listen, ACMEWebroot: i.webroot(), HSTS: i.HSTS}
	if tls {
		files := ingress.CertificateFiles(i.Domain)
		s.TLS = &files
	}
	for _, name := range slices.Sorted(maps.Keys(i.WellKnown)) {
		s.WellKnown = append(s.WellKnown, ingress.WellKnown{Name: name, Path: IngressWellKnown + "/" + name})
	}
	for _, name := range slices.Sorted(maps.Keys(cfg.Services)) {
		g := cfg.Services[name]
		if g.Route == nil {
			continue
		}
		s.Routes = append(s.Routes, ingress.Route{Service: name, Prefix: g.Route.Prefix, Port: g.Port, Static: g.Route.Static, Options: options[name]})
	}
	return s
}

// probes returns one probe per route, sent to the first listen address (a
// local address) or to loopback.
func probes(s ingress.Site) []Probe {
	scheme, port := "http", 80
	if s.TLS != nil {
		scheme, port = "https", 443
	}
	addr := "127.0.0.1"
	if len(s.Listen) > 0 {
		addr = s.Listen[0]
	}
	var out []Probe
	for _, r := range s.Routes {
		out = append(out, Probe{Service: r.Service, Scheme: scheme, Address: net.JoinHostPort(addr, strconv.Itoa(port)), Host: s.Domain, Path: r.Prefix})
	}
	slices.SortFunc(out, func(a, b Probe) int { return strings.Compare(a.Path, b.Path) })
	return out
}

// wellKnownCopy is one domain-level file to serve.
type wellKnownCopy struct {
	name, src, dst string
	content        []byte
	changed        bool
}

// SitePlan is what applying the site would change.
type SitePlan struct {
	// Content is the rendered site and SHA256 its digest.
	Content []byte
	SHA256  string
	// Installed is the current site file, nil when there is none.
	Installed []byte
	// Enabled reports that the site's link exists.
	Enabled bool
	// Adopt names the adopted site whose link is still enabled.
	Adopt       string
	adoptTarget string
	// Reverted reports that "host ingress revert" removed the site.
	Reverted  bool
	TLS       bool
	wellKnown []wellKnownCopy
	probes    []Probe
}

// Changes describes what applying the plan changes; none means the site is
// up to date.
func (p SitePlan) Changes() []string {
	var out []string
	for _, w := range p.wellKnown {
		if w.changed {
			out = append(out, "copy "+w.src+" to "+w.dst)
		}
	}
	switch {
	case p.Installed == nil:
		out = append(out, "install "+SiteFile)
	case !bytes.Equal(p.Installed, p.Content):
		out = append(out, "update "+SiteFile)
	}
	if !p.Enabled {
		out = append(out, "enable "+SiteName+" in "+SitesEnabled)
	}
	if p.Adopt != "" {
		out = append(out, "disable the adopted site "+SitesEnabled+"/"+p.Adopt+" (its file in "+SitesAvailable+" stays)")
	}
	return out
}

// siteChanges reports whether nginx must load the plan.
func (p SitePlan) siteChanges() bool {
	return p.Installed == nil || !bytes.Equal(p.Installed, p.Content) || !p.Enabled || p.Adopt != ""
}

// nginxDirs must be root's alone: whoever can write them controls nginx.
var nginxDirs = []string{NginxDir, SitesAvailable, SitesEnabled}

// PlanSite renders the site and compares it with the host. It only reads.
func PlanSite(env IngressEnv, cfg Config, options map[string]ingress.Options) (SitePlan, error) {
	i := cfg.Components.Ingress
	if i == nil {
		return SitePlan{}, ErrNoIngress
	}
	fsys := env.FS
	for _, d := range nginxDirs {
		if _, err := trustedAs(fsys, d, env.RootUID, false); err != nil {
			return SitePlan{}, fmt.Errorf("nginx: %w", err)
		}
	}
	var plan SitePlan
	files := ingress.CertificateFiles(i.Domain)
	_, err1 := fsys.Lstat(files.Certificate)
	_, err2 := fsys.Lstat(files.Key)
	plan.TLS = err1 == nil && err2 == nil
	var problems []error
	for _, name := range slices.Sorted(maps.Keys(cfg.Services)) {
		if r := cfg.Services[name].Route; r != nil && r.Static != "" {
			if err := staticRoot(env, cfg, r.Static); err != nil {
				problems = append(problems, fmt.Errorf("services.%s.route.static: %w", name, err))
			}
		}
	}
	for _, name := range slices.Sorted(maps.Keys(i.WellKnown)) {
		w := wellKnownCopy{name: name, src: i.WellKnown[name], dst: IngressWellKnown + "/" + name}
		if _, err := trustedAs(fsys, w.src, env.RootUID, true); err != nil {
			problems = append(problems, fmt.Errorf("components.ingress.wellKnown.%s: %w", name, err))
			continue
		}
		data, err := readLimited(fsys, w.src, maxWellKnown)
		if err != nil {
			problems = append(problems, fmt.Errorf("components.ingress.wellKnown.%s: %w", name, err))
			continue
		}
		w.content = data
		info, err := fsys.Lstat(w.dst)
		switch {
		case errors.Is(err, fs.ErrNotExist):
			w.changed = true
		case err != nil:
			return SitePlan{}, err
		case !info.IsRegular() || info.UID != env.RootUID || info.Mode.Perm() != 0o644:
			w.changed = true
		default:
			have, err := readLimited(fsys, w.dst, maxWellKnown)
			w.changed = err != nil || !bytes.Equal(have, data)
		}
		plan.wellKnown = append(plan.wellKnown, w)
	}
	if len(problems) > 0 {
		return SitePlan{}, errors.Join(problems...)
	}
	site := SiteFor(cfg, options, plan.TLS)
	content, err := ingress.Render(site)
	if err != nil {
		return SitePlan{}, err
	}
	plan.Content, plan.SHA256, plan.probes = content, digest(content), probes(site)
	if plan.Installed, err = installedSite(env); err != nil {
		return SitePlan{}, err
	}
	if plan.Enabled, err = siteEnabled(fsys); err != nil {
		return SitePlan{}, err
	}
	if i.Adopt != "" {
		target, err := adoptedLink(fsys, i.Adopt)
		if err != nil {
			return SitePlan{}, err
		}
		if target != "" {
			plan.Adopt, plan.adoptTarget = i.Adopt, target
		}
	}
	if _, err := fsys.Lstat(revertedFile); err == nil {
		plan.Reverted = true
	}
	return plan, nil
}

// staticRoot checks the static directory p of a route at every render. p
// and every directory above it must be root's alone. p may be a symbolic
// link, such as a "current" link that a release switch replaces: the link
// must be root's (only root can swap it in a directory only root can
// write), and its target, resolved once without ".." components, must
// satisfy every rule of a static directory itself, with no further link in
// its path. The site keeps naming p, so nginx follows the link per request
// and a release switch needs no new site; it is checked again whenever the
// site is rendered.
func staticRoot(env IngressEnv, cfg Config, p string) error {
	parent := path.Dir(p)
	if _, err := trustedAs(env.FS, parent, env.RootUID, false); err != nil {
		return err
	}
	info, err := env.FS.Lstat(p)
	if err != nil {
		return err
	}
	if !info.IsLink() {
		_, err := trustedAs(env.FS, p, env.RootUID, false)
		return err
	}
	if info.UID != env.RootUID {
		return fmt.Errorf("%s is a symbolic link that is not owned by root", p)
	}
	target, err := env.FS.Readlink(p)
	if err != nil {
		return err
	}
	if target == "" || slices.Contains(strings.Split(target, "/"), "..") {
		return fmt.Errorf("%s links to %q; a static link must name its target without \"..\"", p, printable(target, 200))
	}
	resolved := path.Clean(target)
	if !path.IsAbs(target) {
		resolved = path.Join(parent, target)
	}
	if err := cfg.checkPublicDir(resolved); err != nil {
		return fmt.Errorf("%s links to %s, which %w", p, printable(resolved, 200), err)
	}
	if _, err := trustedAs(env.FS, resolved, env.RootUID, false); err != nil {
		return fmt.Errorf("%s links to %s: %w", p, printable(resolved, 200), err)
	}
	return nil
}

// installedSite reads the site file, nil when there is none. A file
// without the marker, or that someone else could have written, is not
// LCM's and is refused.
func installedSite(env IngressEnv) ([]byte, error) {
	info, err := env.FS.Lstat(SiteFile)
	switch {
	case errors.Is(err, fs.ErrNotExist):
		return nil, nil
	case err != nil:
		return nil, err
	case !info.IsRegular() || ownedBy(SiteFile, info, env.RootUID) != nil:
		return nil, fmt.Errorf("%s exists and is not a root-owned regular file; move it away", SiteFile)
	}
	data, err := readLimited(env.FS, SiteFile, maxSite)
	if err != nil {
		return nil, err
	}
	if first, _, _ := strings.Cut(string(data), "\n"); first != ingress.Marker {
		return nil, fmt.Errorf("%s exists and was not written by LCM; move it away", SiteFile)
	}
	return data, nil
}

// siteEnabled reports whether the site's link exists; anything else at its
// place is refused.
func siteEnabled(fsys FS) (bool, error) {
	info, err := fsys.Lstat(SiteLink)
	switch {
	case errors.Is(err, fs.ErrNotExist):
		return false, nil
	case err != nil:
		return false, err
	case !info.IsLink():
		return false, fmt.Errorf("%s exists and is not a link to %s; move it away", SiteLink, SiteFile)
	}
	target, err := fsys.Readlink(SiteLink)
	if err != nil {
		return false, err
	}
	if target != SiteFile {
		return false, fmt.Errorf("%s links to %q, not %s; move it away", SiteLink, printable(target, 200), SiteFile)
	}
	return true, nil
}

// adoptedLink returns the target of the adopted site's link, or "" when it
// is not enabled. Only a link is ever removed: a regular file there is
// refused.
func adoptedLink(fsys FS, name string) (string, error) {
	p := SitesEnabled + "/" + name
	info, err := fsys.Lstat(p)
	switch {
	case errors.Is(err, fs.ErrNotExist):
		return "", nil
	case err != nil:
		return "", err
	case !info.IsLink():
		return "", fmt.Errorf("%s is not a link; make it a link to its file in %s before ingress adopts it", p, SitesAvailable)
	}
	return fsys.Readlink(p)
}

// writeState writes a root-only file of the state directory.
func writeState(env IngressEnv, p string, data []byte) error {
	return env.FS.WriteFile(p, bytes.NewReader(data), 0o600, env.RootUID, env.RootGID)
}

// recordLast writes the result of a change; a failure to record it is
// returned joined with the change's own error.
func recordLast(env IngressEnv, sum string, err error) error {
	last := LastApply{At: env.Clock.Now().UnixMilli(), Outcome: "succeeded", SHA256: sum}
	if err != nil {
		last.Outcome, last.Message = "failed", printable(err.Error(), 2000)
	}
	data, _ := json.Marshal(last) // a struct of strings and ints always marshals
	if werr := writeState(env, lastApplyFile, append(data, '\n')); werr != nil {
		return errors.Join(err, fmt.Errorf("record the result: %w", werr))
	}
	return err
}

// ReadLastApply returns the recorded result of the last change, if any.
func ReadLastApply(env IngressEnv) (*LastApply, error) {
	data, err := readLimited(env.FS, lastApplyFile, 64<<10)
	if errors.Is(err, fs.ErrNotExist) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	var last LastApply
	if err := json.Unmarshal(data, &last); err != nil {
		return nil, err
	}
	return &last, nil
}

// checkState requires the state directory: root's alone, mode 0700.
func checkState(env IngressEnv) error {
	info, err := trustedAs(env.FS, IngressState, env.RootUID, false)
	if err != nil {
		return fmt.Errorf("%w; run sudo bin/lcm host apply", err)
	}
	if info.Mode.Perm() != 0o700 {
		return fmt.Errorf("%s must have mode 0700; run sudo bin/lcm host apply", IngressState)
	}
	return nil
}

// ApplySite carries out a plan; the caller holds the ingress lock. Nothing
// is done when the plan has no changes. A failure puts the previous site
// back, so nginx keeps serving what it served.
func ApplySite(ctx context.Context, env IngressEnv, plan SitePlan) error {
	if len(plan.Changes()) == 0 {
		return nil
	}
	if err := checkState(env); err != nil {
		return err
	}
	if err := copyWellKnown(env, plan); err != nil {
		return recordLast(env, plan.SHA256, err)
	}
	if !plan.siteChanges() {
		return recordLast(env, plan.SHA256, nil)
	}
	err := applySite(ctx, env, plan)
	if err == nil {
		// The administrator applied the site again after a revert.
		if rerr := env.FS.Remove(revertedFile); rerr != nil && !errors.Is(rerr, fs.ErrNotExist) {
			err = rerr
		}
	}
	return recordLast(env, plan.SHA256, err)
}

func copyWellKnown(env IngressEnv, plan SitePlan) error {
	if len(plan.wellKnown) == 0 {
		return nil
	}
	for _, d := range []string{IngressWWW, IngressWellKnown} {
		_, err := env.FS.Lstat(d)
		switch {
		case errors.Is(err, fs.ErrNotExist):
			if _, err := trustedAs(env.FS, path.Dir(d), env.RootUID, false); err != nil {
				return err
			}
			if err := env.FS.Mkdir(d, 0o755, env.RootUID, env.RootGID); err != nil {
				return err
			}
		case err != nil:
			return err
		}
		if _, err := trustedAs(env.FS, d, env.RootUID, false); err != nil {
			return err
		}
	}
	for _, w := range plan.wellKnown {
		if w.changed {
			if err := env.FS.WriteFile(w.dst, bytes.NewReader(w.content), 0o644, env.RootUID, env.RootGID); err != nil {
				return err
			}
		}
	}
	return nil
}

func applySite(ctx context.Context, env IngressEnv, plan SitePlan) error {
	fsys := env.FS
	if plan.Installed != nil && !bytes.Equal(plan.Installed, plan.Content) {
		if err := writeState(env, previousFile, plan.Installed); err != nil {
			return err
		}
	}
	if plan.Adopt != "" {
		if _, err := fsys.Lstat(adoptedFile); errors.Is(err, fs.ErrNotExist) {
			data, _ := json.Marshal(adoptedRecord{Name: plan.Adopt, Target: plan.adoptTarget}) // plain struct
			if err := writeState(env, adoptedFile, append(data, '\n')); err != nil {
				return err
			}
		}
	}
	undo := func() error {
		var errs []error
		if plan.Installed == nil {
			errs = append(errs, ignoreMissing(fsys.Remove(SiteFile)))
		} else {
			errs = append(errs, fsys.WriteFile(SiteFile, bytes.NewReader(plan.Installed), 0o644, env.RootUID, env.RootGID))
		}
		if !plan.Enabled {
			errs = append(errs, ignoreMissing(fsys.Remove(SiteLink)))
		}
		if plan.Adopt != "" {
			if _, err := fsys.Lstat(SitesEnabled + "/" + plan.Adopt); errors.Is(err, fs.ErrNotExist) {
				errs = append(errs, fsys.Symlink(plan.adoptTarget, SitesEnabled+"/"+plan.Adopt, env.RootUID, env.RootGID))
			}
		}
		return errors.Join(errs...)
	}
	change := func() error {
		if err := fsys.WriteFile(SiteFile, bytes.NewReader(plan.Content), 0o644, env.RootUID, env.RootGID); err != nil {
			return err
		}
		if !plan.Enabled {
			if err := fsys.Symlink(SiteFile, SiteLink, env.RootUID, env.RootGID); err != nil {
				return err
			}
		}
		if plan.Adopt != "" {
			// Only the link that was looked at is removed.
			target, err := adoptedLink(fsys, plan.Adopt)
			if err != nil {
				return err
			}
			if target != plan.adoptTarget {
				return fmt.Errorf("%s/%s changed since it was checked", SitesEnabled, plan.Adopt)
			}
			if err := fsys.Remove(SitesEnabled + "/" + plan.Adopt); err != nil {
				return err
			}
		}
		return nil
	}
	return transact(ctx, env, change, undo, plan.probes)
}

func ignoreMissing(err error) error {
	if errors.Is(err, fs.ErrNotExist) {
		return nil
	}
	return err
}

// transact makes a change of nginx's files, tests and reloads the
// configuration and probes it; any failure is undone, and nginx reloaded
// again when it had loaded the change.
func transact(ctx context.Context, env IngressEnv, change, undo func() error, probes []Probe) error {
	if err := change(); err != nil {
		return errors.Join(fmt.Errorf("change the site: %w", err), undoing(undo()))
	}
	if err := testNginx(ctx, env); err != nil {
		return errors.Join(err, undoing(undo()))
	}
	if err := reloadNginx(ctx, env); err != nil {
		return errors.Join(err, undoing(undo()), reloadAfterUndo(ctx, env))
	}
	if err := probeAll(ctx, env, probes); err != nil {
		return errors.Join(err, undoing(undo()), reloadAfterUndo(ctx, env))
	}
	return nil
}

func undoing(err error) error {
	if err == nil {
		return errors.New("the previous site was put back")
	}
	return fmt.Errorf("putting the previous site back failed, inspect %s and %s: %w", SiteFile, SitesEnabled, err)
}

func reloadAfterUndo(ctx context.Context, env IngressEnv) error {
	if err := testNginx(ctx, env); err != nil {
		return fmt.Errorf("after putting the previous site back: %w", err)
	}
	if err := reloadNginx(ctx, env); err != nil {
		return fmt.Errorf("after putting the previous site back: %w", err)
	}
	return nil
}

func testNginx(ctx context.Context, env IngressEnv) error {
	out, err := env.Run(ctx, nginxTestTimeout, Nginx, "-t", "-q")
	if err != nil {
		return fmt.Errorf("nginx -t refused the configuration: %s", commandMessage(out, err))
	}
	return nil
}

func reloadNginx(ctx context.Context, env IngressEnv) error {
	out, err := env.Run(ctx, reloadTimeout, Systemctl, "reload", "nginx.service")
	if err != nil {
		return fmt.Errorf("reloading nginx failed: %s", commandMessage(out, err))
	}
	return nil
}

// commandMessage is the error of a command with the last line it printed.
func commandMessage(out string, err error) string {
	if line := lastLine(out); line != "" {
		return line + " (" + err.Error() + ")"
	}
	return err.Error()
}

// probeOK reports whether a status shows that nginx serves the route:
// anything but a server error of nginx itself. 502, 503 and 504 mean the
// route reached a service that is not answering yet, which is not the
// site's fault.
func probeOK(status int) bool {
	return status < 500 || status == 502 || status == 503 || status == 504
}

// errProbePhase ends probes that together took longer than ProbePhase.
var errProbePhase = errors.New("the routes did not answer within " + ProbePhase.String())

func probeAll(ctx context.Context, env IngressEnv, probes []Probe) error {
	if len(probes) == 0 {
		return nil
	}
	ctx, cancel := clock.WithTimeoutCause(ctx, env.Clock, ProbePhase, errProbePhase)
	defer cancel()
	attempts := max(env.ProbeAttempts, 1)
	for _, p := range probes {
		var last error
		for n := range attempts {
			if n > 0 {
				if err := sleep(ctx, env.Clock, env.ProbeWait); err != nil {
					return context.Cause(ctx)
				}
			}
			status, err := env.Probe(ctx, p)
			if err == nil && probeOK(status) {
				last = nil
				break
			}
			if err == nil {
				err = fmt.Errorf("answered %d", status)
			}
			last = err
		}
		if last != nil {
			return fmt.Errorf("the route of %s (%s via %s) does not answer: %w", p.Service, p, p.Address, last)
		}
	}
	return nil
}

// IngressStatus is the state of the site for status reports.
type IngressStatus struct {
	// Rendered is the digest of the site the current configuration
	// renders; empty when it cannot be rendered (RenderError says why).
	Rendered    string     `json:"rendered,omitempty"`
	RenderError string     `json:"renderError,omitempty"`
	Installed   string     `json:"installed,omitempty"`
	Enabled     bool       `json:"enabled"`
	TLS         bool       `json:"tls"`
	Adopted     string     `json:"adopted,omitempty"`
	Reverted    bool       `json:"reverted,omitempty"`
	Last        *LastApply `json:"last,omitempty"`
	// Routes are the services with a route and whether options were
	// accepted for them.
	Routes []RouteStatus `json:"routes"`
}

// RouteStatus is one service's route in a status report.
type RouteStatus struct {
	Service string `json:"service"`
	Prefix  string `json:"prefix"`
	Options bool   `json:"options"`
}

// SiteStatus reports the site: what the configuration renders against what
// is installed. It only reads.
func SiteStatus(env IngressEnv, cfg Config) (IngressStatus, error) {
	st := IngressStatus{Routes: []RouteStatus{}}
	if cfg.Components.Ingress == nil {
		return st, ErrNoIngress
	}
	options, err := ReadRouteOptions(env.FS, env.RootUID)
	if err != nil {
		return st, err
	}
	for _, name := range slices.Sorted(maps.Keys(cfg.Services)) {
		if r := cfg.Services[name].Route; r != nil {
			_, has := options[name]
			st.Routes = append(st.Routes, RouteStatus{Service: name, Prefix: r.Prefix, Options: has})
		}
	}
	plan, err := PlanSite(env, cfg, options)
	if err != nil {
		st.RenderError = printable(err.Error(), 2000)
	} else {
		st.Rendered, st.TLS = plan.SHA256, plan.TLS
	}
	if installed, err := installedSite(env); err == nil && installed != nil {
		st.Installed = digest(installed)
	}
	st.Enabled, _ = siteEnabled(env.FS)
	if data, err := readLimited(env.FS, adoptedFile, 64<<10); err == nil {
		var rec adoptedRecord
		if json.Unmarshal(data, &rec) == nil {
			st.Adopted = rec.Name
		}
	}
	if _, err := env.FS.Lstat(revertedFile); err == nil {
		st.Reverted = true
	}
	if st.Last, err = ReadLastApply(env); err != nil {
		return st, err
	}
	return st, nil
}

// RevertSite removes LCM's site and enables the adopted one again, from
// what the first application recorded or, without a record, from the
// configuration's adopt. The caller holds the ingress lock. It returns what
// it changed.
func RevertSite(ctx context.Context, env IngressEnv, cfg Config) ([]string, error) {
	fsys := env.FS
	for _, d := range nginxDirs {
		if _, err := trustedAs(fsys, d, env.RootUID, false); err != nil {
			return nil, fmt.Errorf("nginx: %w", err)
		}
	}
	if err := checkState(env); err != nil {
		return nil, err
	}
	installed, err := installedSite(env)
	if err != nil {
		return nil, err
	}
	enabled, err := siteEnabled(fsys)
	if err != nil {
		return nil, err
	}
	rec := adoptedRecord{}
	if data, err := readLimited(fsys, adoptedFile, 64<<10); err == nil {
		if err := json.Unmarshal(data, &rec); err != nil || !siteFileName.MatchString(rec.Name) {
			return nil, fmt.Errorf("%s cannot be used; restore the link by hand", adoptedFile)
		}
	} else if !errors.Is(err, fs.ErrNotExist) {
		return nil, err
	} else if i := cfg.Components.Ingress; i != nil && i.Adopt != "" {
		rec = adoptedRecord{Name: i.Adopt, Target: SitesAvailable + "/" + i.Adopt}
	}
	restoreAdopted := false
	if rec.Name != "" {
		target, err := adoptedLink(fsys, rec.Name)
		if err != nil {
			return nil, err
		}
		if target == "" {
			if !within(rec.Target, SitesAvailable) || path.Clean(rec.Target) != rec.Target {
				return nil, fmt.Errorf("the recorded link target %q of %s is not in %s; restore the link by hand", printable(rec.Target, 200), rec.Name, SitesAvailable)
			}
			if _, err := trustedAs(fsys, rec.Target, env.RootUID, true); err != nil {
				return nil, fmt.Errorf("the adopted site cannot be enabled again: %w", err)
			}
			restoreAdopted = true
		}
	}
	var did []string
	if enabled {
		did = append(did, "disable "+SiteName)
	}
	if restoreAdopted {
		did = append(did, "enable the adopted site "+rec.Name+" again")
	}
	if len(did) > 0 {
		change := func() error {
			if enabled {
				if err := fsys.Remove(SiteLink); err != nil {
					return err
				}
			}
			if restoreAdopted {
				return fsys.Symlink(rec.Target, SitesEnabled+"/"+rec.Name, env.RootUID, env.RootGID)
			}
			return nil
		}
		undo := func() error {
			var errs []error
			if restoreAdopted {
				errs = append(errs, ignoreMissing(fsys.Remove(SitesEnabled+"/"+rec.Name)))
			}
			if enabled {
				if _, err := fsys.Lstat(SiteLink); errors.Is(err, fs.ErrNotExist) {
					errs = append(errs, fsys.Symlink(SiteFile, SiteLink, env.RootUID, env.RootGID))
				}
			}
			return errors.Join(errs...)
		}
		if err := transact(ctx, env, change, undo, nil); err != nil {
			return nil, recordLast(env, "", fmt.Errorf("revert: %w", err))
		}
	}
	if installed != nil {
		if err := writeState(env, previousFile, installed); err != nil {
			return did, err
		}
		if err := fsys.Remove(SiteFile); err != nil {
			return did, err
		}
		did = append(did, "remove "+SiteFile+" (kept as "+previousFile+")")
	}
	if err := ignoreMissing(fsys.Remove(adoptedFile)); err != nil {
		return did, err
	}
	data, _ := json.Marshal(map[string]int64{"at": env.Clock.Now().UnixMilli()}) // a map of ints always marshals
	if err := writeState(env, revertedFile, append(data, '\n')); err != nil {
		return did, err
	}
	return did, recordLast(env, "", nil)
}

// Reverted reports whether the site was reverted and not applied since.
func Reverted(fsys FS) bool {
	_, err := fsys.Lstat(revertedFile)
	return err == nil
}

// SiteInstalled reports whether LCM's site file exists.
func SiteInstalled(fsys FS) bool {
	_, err := fsys.Lstat(SiteFile)
	return err == nil
}
