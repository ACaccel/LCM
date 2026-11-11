package hostsetup

import (
	"context"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"strings"
	"time"

	"github.com/ACaccel/lcm/internal/ingress"
)

// ingressEnv is the ingress environment of a host.
func (h Host) ingressEnv() IngressEnv {
	return IngressEnv{
		FS: h.FS, Clock: h.Clock,
		Run: func(ctx context.Context, timeout time.Duration, argv ...string) (string, error) {
			out, err := h.Runner.Run(ctx, Command{Path: argv[0], Args: argv[1:], Timeout: timeout})
			return string(out), err
		},
		Probe: h.Probe, ProbeAttempts: h.ProbeAttempts, ProbeWait: h.ProbeWait,
	}
}

func (h Host) lockIngress(legacy string) (func(), error) {
	if h.LockIngress == nil {
		return nil, errors.New("the ingress lock is not available")
	}
	return h.LockIngress(legacy)
}

// ensureRootDir creates a missing directory (root:root, perm) and checks an
// existing one, which may belong to another program, without changing it.
func (a *applier) ensureRootDir(s *step, p string, perm fs.FileMode) error {
	info, err := a.h.FS.Lstat(p)
	switch {
	case errors.Is(err, fs.ErrNotExist):
		return a.ensureDir(s, p, perm, 0, 0, "root:root")
	case err != nil:
		return err
	case !info.IsDir():
		return fmt.Errorf("%s is not a directory; move it away", p)
	}
	_, err = trustedAs(a.h.FS, p, 0, false)
	return err
}

// ingressStep converges the ingress component: its directories, the ACME
// webroot, certbot's deploy hook and renewal configuration and the site.
func (a *applier) ingressStep(ctx context.Context, s *step) error {
	i := a.cfg.Components.Ingress
	fsys := a.h.FS
	if i == nil {
		if SiteInstalled(fsys) {
			s.note("installed but disabled; components.ingress is null and nothing is removed (sudo bin/lcm host ingress revert removes the site)")
		}
		return nil
	}
	if info, err := fsys.Lstat(Nginx); err != nil || !info.IsRegular() {
		s.manual("nginx is not installed (" + Nginx + "); install it (apt install nginx) and apply again")
		a.ingressHeld = "nginx is not installed"
		return nil
	}
	if _, err := a.h.Runner.LookPath("certbot"); err != nil {
		s.manual("certbot is not installed; install it (apt install certbot) for the domain's certificate")
	}
	for _, d := range []struct {
		path string
		perm fs.FileMode
	}{{IngressState, 0o700}, {IngressRoutes, 0o700}} {
		if err := a.ensureDir(s, d.path, d.perm, 0, 0, "root:root"); err != nil {
			return err
		}
	}
	if err := a.removeStaleRoutes(s); err != nil {
		return err
	}
	if err := a.ensureRootDir(s, i.webroot(), 0o755); err != nil {
		return err
	}
	if err := a.renewalHook(s); err != nil {
		return err
	}
	env := a.h.ingressEnv()
	plan, err := a.planIngress(env)
	if err != nil {
		s.manual(err.Error())
		s.note("the renewal configuration is changed only after the site applies")
		a.ingressHeld = "the ingress site needs manual action"
		return nil
	}
	for _, d := range plan.Dropped {
		s.manual(d)
	}
	if !plan.TLS {
		s.note("there is no certificate for " + i.Domain + " yet, so only port 80 is served; issue one with: sudo bin/lcm host cert issue")
	}
	if changes := plan.Changes(); len(changes) > 0 {
		what := strings.Join(changes, "; ")
		if plan.Reverted {
			what += " (the site was reverted; this applies it again)"
		}
		if a.o.Check && plan.siteChanges() {
			_, _ = fmt.Fprintf(a.o.Out, "--- rendered %s (sha256 %s) ---\n%s--- end of %s ---\n", SiteFile, plan.SHA256, plan.Content, SiteFile)
		}
		if err := s.change(what, func() error {
			unlock, err := a.h.lockIngress(i.LegacyLock)
			if err != nil {
				return err
			}
			defer unlock()
			// Another change may have come first: plan again under the lock.
			plan, err := a.planIngress(env)
			if err != nil {
				return err
			}
			return ApplySite(ctx, env, plan)
		}); err != nil {
			return err
		}
	}
	// The site serves the ACME webroot now, so renewals may use it.
	return a.renewalConf(s, i)
}

// removeStaleRoutes removes the stored route options of services that are
// no longer in services: the helper refuses every request for them, so
// nothing else ever would, and a service granted again under the name
// starts without them.
func (a *applier) removeStaleRoutes(s *step) error {
	fsys := a.h.FS
	names, err := fsys.ReadDir(IngressRoutes)
	if errors.Is(err, fs.ErrNotExist) && a.o.Check {
		return nil
	}
	if err != nil {
		return err
	}
	for _, n := range names {
		service, ok := strings.CutSuffix(n, ".json")
		if _, granted := a.cfg.Services[service]; !ok || granted || ValidServiceName(service) != nil {
			continue
		}
		p := IngressRoutes + "/" + n
		if err := s.change("remove "+p+": "+service+" is no longer in services", func() error {
			return ignoreMissing(fsys.Remove(p))
		}); err != nil {
			return err
		}
	}
	return nil
}

func (a *applier) planIngress(env IngressEnv) (SitePlan, error) {
	ro, err := ReadRouteOptions(a.h.FS, 0)
	if err != nil {
		return SitePlan{}, err
	}
	return PlanSite(env, a.cfg, ro, "")
}

// renewalHook installs certbot's deploy hook; other hooks, such as a
// hand-made reload, are reported and kept.
func (a *applier) renewalHook(s *step) error {
	if err := a.ensureRootDir(s, RenewalHooksDir, 0o755); err != nil {
		return err
	}
	if _, err := a.ensureFile(s, RenewalHook, bytesContent(asset("lcm-ingress-reload")), 0o755); err != nil {
		return err
	}
	names, err := a.h.FS.ReadDir(RenewalHooksDir)
	if errors.Is(err, fs.ErrNotExist) {
		return nil
	}
	if err != nil {
		return err
	}
	var others []string
	for _, n := range names {
		if n != "lcm-ingress-reload" {
			others = append(others, printable(n, 100))
		}
	}
	if len(others) > 0 {
		s.note(fmt.Sprintf("other certbot deploy hooks in %s are kept (%s); remove a hand-made nginx reload hook once %s is in place", RenewalHooksDir, strings.Join(others, ", "), RenewalHook))
	}
	return nil
}

// renewalConf makes certbot renew the domain with the webroot
// authenticator into the ACME webroot; it changes only those keys.
func (a *applier) renewalConf(s *step, i *Ingress) error {
	fsys, p := a.h.FS, RenewalConf(i.Domain)
	info, err := fsys.Lstat(p)
	switch {
	case errors.Is(err, fs.ErrNotExist):
		return nil
	case err != nil:
		return err
	}
	if _, err := trustedAs(fsys, p, 0, true); err != nil {
		s.manual(p + ": " + err.Error() + "; review it by hand")
		return nil
	}
	data, err := readLimited(fsys, p, maxConfigSize)
	if err != nil {
		return err
	}
	fixed, drift, problems, err := FixRenewal(string(data), i.Domain, i.webroot())
	if err != nil {
		s.manual(p + ": " + err.Error())
		return nil
	}
	for _, pr := range problems {
		s.manual(p + ": " + pr)
	}
	if len(drift) == 0 {
		return nil
	}
	return s.change("set "+strings.Join(drift, "; ")+" in "+p+" (other keys are kept)", func() error {
		now, err := readLimited(fsys, p, maxConfigSize)
		if err != nil {
			return err
		}
		if string(now) != string(data) {
			return fmt.Errorf("%s changed since it was read; apply again", p)
		}
		// host ingress revert puts the first version back.
		if err := saveRenewal(a.h.ingressEnv(), p, info.Mode, data); err != nil {
			return err
		}
		return fsys.WriteFile(p, strings.NewReader(fixed), info.Mode.Perm(), 0, 0)
	})
}

// FixRenewal returns certbot's renewal configuration of domain with
// "authenticator = webroot", "webroot_path = <webroot>," and the domain's
// [[webroot_map]] entry set, every other line unchanged. drift names the
// keys it changed; problems are settings it does not change but that
// conflict with the ingress component.
func FixRenewal(conf, domain, webroot string) (fixed string, drift, problems []string, err error) {
	lines := strings.SplitAfter(conf, "\n")
	if len(lines) > 0 && lines[len(lines)-1] == "" {
		lines = lines[:len(lines)-1]
	}
	if len(lines) > 0 && !strings.HasSuffix(lines[len(lines)-1], "\n") {
		lines[len(lines)-1] += "\n"
	}
	section := ""
	params, mapHeader := -1, -1
	keys := map[string]int{} // "section/key" -> line
	for n, l := range lines {
		t := strings.TrimSpace(l)
		switch {
		case strings.HasPrefix(t, "[["):
			section = strings.TrimSpace(strings.Trim(t, "[]"))
			if section == "webroot_map" {
				mapHeader = n
			}
			continue
		case strings.HasPrefix(t, "["):
			section = strings.TrimSpace(strings.Trim(t, "[]"))
			if section == "renewalparams" {
				params = n
			}
			continue
		case t == "" || strings.HasPrefix(t, "#"):
			continue
		}
		k, _, ok := strings.Cut(t, "=")
		if ok {
			keys[section+"/"+strings.TrimSpace(k)] = n
		}
	}
	if params < 0 {
		return "", nil, nil, errors.New("there is no [renewalparams] section; certbot did not write this file, review it by hand")
	}
	value := func(key string) (string, bool) {
		n, ok := keys[key]
		if !ok {
			return "", false
		}
		_, v, _ := strings.Cut(lines[n], "=")
		return strings.TrimSpace(v), true
	}
	if v, ok := value("renewalparams/installer"); ok && v != "None" && v != "" {
		problems = append(problems, fmt.Sprintf("installer = %s lets certbot edit nginx's files on renewal; set installer = None (the deploy hook reloads nginx)", printable(v, 40)))
	}
	inserts := map[int][]string{} // after line n
	set := func(key, line string, after int) {
		if n, ok := keys[key]; ok {
			lines[n] = line + "\n"
			return
		}
		inserts[after] = append(inserts[after], line+"\n")
	}
	if v, _ := value("renewalparams/authenticator"); v != "webroot" {
		drift = append(drift, "authenticator = webroot")
		set("renewalparams/authenticator", "authenticator = webroot", params)
	}
	v, _ := value("renewalparams/webroot_path")
	if paths := strings.Split(strings.TrimSuffix(v, ","), ","); len(paths) != 1 || strings.TrimSpace(paths[0]) != webroot {
		drift = append(drift, "webroot_path = "+webroot+",")
		set("renewalparams/webroot_path", "webroot_path = "+webroot+",", params)
	}
	appendMap := ""
	if mapHeader < 0 {
		drift = append(drift, "[[webroot_map]] "+domain+" = "+webroot)
		appendMap = "[[webroot_map]]\n" + domain + " = " + webroot + "\n"
	} else if v, _ := value("webroot_map/" + domain); v != webroot {
		drift = append(drift, "[[webroot_map]] "+domain+" = "+webroot)
		set("webroot_map/"+domain, domain+" = "+webroot, mapHeader)
	}
	var b strings.Builder
	for n, l := range lines {
		b.WriteString(l)
		for _, ins := range inserts[n] {
			b.WriteString(ins)
		}
	}
	b.WriteString(appendMap)
	return b.String(), drift, problems, nil
}

// CertOptions control IssueCertificate.
type CertOptions struct {
	ConfigPath string
	Out        io.Writer
}

// IssueCertificate obtains the domain's first certificate: it applies the
// site without TLS (the ACME location on port 80), runs certbot with the
// webroot authenticator, then applies the full site. It refuses when a
// certificate exists; certbot's timer renews it.
func IssueCertificate(ctx context.Context, h Host, o CertOptions) error {
	if o.ConfigPath == "" {
		o.ConfigPath = ConfigPath
	}
	out := safeWriter{w: o.Out}
	if o.Out == nil {
		out = safeWriter{w: io.Discard}
	}
	cfg, err := privilegedConfig(h, o.ConfigPath, "host cert issue")
	if err != nil {
		return safeError{err}
	}
	i := cfg.Components.Ingress
	files := ingress.CertificateFiles(i.Domain)
	if _, err := h.FS.Lstat(files.Certificate); err == nil {
		return fmt.Errorf("a certificate for %s exists (%s); certbot's timer renews it", i.Domain, files.Certificate)
	}
	if _, err := h.Runner.LookPath("certbot"); err != nil {
		return errors.New("certbot is not installed; install it (apt install certbot)")
	}
	if info, err := h.FS.Lstat(i.webroot()); err != nil || !info.IsDir() {
		return fmt.Errorf("the ACME webroot %s does not exist; run sudo bin/lcm host apply first", i.webroot())
	}
	unlock, err := h.lockIngress(i.LegacyLock)
	if err != nil {
		return err
	}
	defer unlock()
	env := h.ingressEnv()
	apply := func(stage string) error {
		ro, err := ReadRouteOptions(h.FS, 0)
		if err != nil {
			return err
		}
		plan, err := PlanSite(env, cfg, ro, "")
		if err != nil {
			return err
		}
		if changes := plan.Changes(); len(changes) > 0 {
			_, _ = fmt.Fprintf(out, "%s: %s\n", stage, strings.Join(changes, "; "))
		}
		return ApplySite(ctx, env, plan)
	}
	if err := apply("site without TLS"); err != nil {
		return safeError{err}
	}
	_, _ = fmt.Fprintf(out, "certbot: requesting a certificate for %s\n", i.Domain)
	if _, err := h.Runner.Run(ctx, Command{Path: "certbot", Args: []string{
		"certonly", "--webroot", "-w", i.webroot(), "-d", i.Domain, "--email", i.TLS.Email,
		"--key-type", i.keyType(), "--agree-tos", "-n",
	}, Timeout: certbotTimeout}); err != nil {
		return safeError{fmt.Errorf("certbot: %w", err)}
	}
	if _, err := h.FS.Lstat(files.Certificate); err != nil {
		return fmt.Errorf("certbot finished but %s does not exist", files.Certificate)
	}
	if err := apply("site with TLS"); err != nil {
		return safeError{err}
	}
	_, _ = fmt.Fprintf(out, "The certificate for %s is in place; certbot's timer renews it and %s reloads nginx.\n", i.Domain, RenewalHook)
	return nil
}

// RevertOptions control RevertIngress.
type RevertOptions struct {
	ConfigPath string
	Out        io.Writer
}

// RevertIngress removes LCM's site and enables the adopted hand-made site
// again. The deploy helper refuses ingress.apply until "host apply" applies
// the site again; to stay reverted, set components.ingress to null first.
func RevertIngress(ctx context.Context, h Host, o RevertOptions) error {
	if o.ConfigPath == "" {
		o.ConfigPath = ConfigPath
	}
	out := safeWriter{w: io.Discard}
	if o.Out != nil {
		out = safeWriter{w: o.Out}
	}
	if !h.Privileged {
		return errors.New("must run as root, from a reviewed build: sudo bin/lcm host ingress revert")
	}
	if err := CheckExecutable(h.Executable); err != nil {
		return err
	}
	cfg, err := LoadConfig(h.FS, o.ConfigPath)
	if err != nil {
		return safeError{err}
	}
	// Revert never waits for the legacy lock, which anyone who can read
	// the lock file can hold: going back must stay possible.
	unlock, err := h.lockIngress("")
	if err != nil {
		return err
	}
	defer unlock()
	did, err := RevertSite(ctx, h.ingressEnv(), cfg)
	for _, d := range did {
		_, _ = fmt.Fprintln(out, "changed: "+d)
	}
	if err != nil {
		return safeError{err}
	}
	if len(did) == 0 {
		_, _ = fmt.Fprintln(out, "unchanged: the ingress site is not installed")
	}
	if cfg.Components.Ingress != nil {
		_, _ = fmt.Fprintf(out, "components.ingress is still set in %s: the next sudo bin/lcm host apply installs the site again; set it to null to stay reverted.\n", o.ConfigPath)
	}
	return nil
}

// privilegedConfig checks that the command runs as root from a reviewed
// build and returns a configuration with the ingress component.
func privilegedConfig(h Host, path, command string) (Config, error) {
	if !h.Privileged {
		return Config{}, fmt.Errorf("must run as root, from a reviewed build: sudo bin/lcm %s", command)
	}
	if err := CheckExecutable(h.Executable); err != nil {
		return Config{}, err
	}
	cfg, err := LoadConfig(h.FS, path)
	if err != nil {
		return Config{}, err
	}
	if cfg.Components.Ingress == nil {
		return Config{}, ErrNoIngress
	}
	if info, err := h.FS.Lstat(Nginx); err != nil || !info.IsRegular() {
		return Config{}, fmt.Errorf("nginx is not installed (%s)", Nginx)
	}
	return cfg, nil
}

// ingressStatus reports the ingress component's files. The rendered site
// needs root (the accepted options and the certificate are root's); other
// users see whether the installed site is LCM's.
func ingressStatus(h Host) ComponentStatus {
	cs := ComponentStatus{Name: ComponentIngress}
	cs.Files = append(cs.Files, fileStatus(h.FS, rootFile{path: RenewalHook, perm: 0o755, asset: "lcm-ingress-reload"}, bytesContent(asset("lcm-ingress-reload"))))
	st := FileStatus{Path: SiteFile}
	info, err := h.FS.Lstat(SiteFile)
	switch {
	case errors.Is(err, fs.ErrNotExist):
		st.State = StateMissing
		cs.Files = append(cs.Files, st)
		cs.State = componentState(cs.Files)
		return cs
	case err != nil:
		cs.Files = append(cs.Files, unknown(st, err))
		cs.State = componentState(cs.Files)
		return cs
	}
	env := h.ingressEnv()
	installed, err := installedSite(env)
	switch {
	case err != nil:
		st.State, st.Detail = StateOutdated, err.Error()
	case info.Mode.Perm() != 0o644:
		st.State, st.Detail = StateOutdated, fmt.Sprintf("mode %04o, want 0644", info.Mode.Perm())
	default:
		st.Installed = digest(installed)
		st.State, st.Detail = StateUpToDate, "rendered from host.json; sudo bin/lcm host status compares it"
		if h.Privileged {
			st.Detail = ""
			if err := renderedStatus(h, env, &st); err != nil {
				st = unknown(st, err)
			}
		}
	}
	cs.Files = append(cs.Files, st)
	cs.State = componentState(cs.Files)
	return cs
}

func renderedStatus(h Host, env IngressEnv, st *FileStatus) error {
	cfg, err := LoadConfig(h.FS, ConfigPath)
	if err != nil {
		return err
	}
	if cfg.Components.Ingress == nil {
		st.State, st.Detail = StateOutdated, "components.ingress is null; sudo bin/lcm host ingress revert removes the site"
		return nil
	}
	ro, err := ReadRouteOptions(h.FS, 0)
	if err != nil {
		return err
	}
	plan, err := PlanSite(env, cfg, ro, "")
	if err != nil {
		return err
	}
	st.Embedded = plan.SHA256
	switch {
	case st.Installed != plan.SHA256:
		st.State, st.Detail = StateOutdated, "differs from the site host.json renders"
	case !plan.Enabled:
		st.State, st.Detail = StateOutdated, "not enabled in "+SitesEnabled
	case plan.Adopt != "":
		st.State, st.Detail = StateOutdated, "the adopted site "+plan.Adopt+" is still enabled"
	case len(plan.Dropped) > 0:
		st.State, st.Detail = StateOutdated, strings.Join(plan.Dropped, "; ")
	default:
		st.State = StateUpToDate
	}
	return nil
}
