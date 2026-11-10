package hostsetup

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"maps"
	"regexp"
	"slices"
	"strconv"
	"strings"
	"time"
)

// The security component is installed by deploy/security/install.py, which
// runs as root with python3 -I from a root-only staging directory next to
// the runtime and helper of the same build. Apply hands it the wanted state
// on stdin and reports the changes it lists; the sudoers rule comes last,
// from here, like the rules of the other components.

// securityInput is the installer's stdin.
type securityInput struct {
	Version   int                 `json:"version"`
	MachineID string              `json:"machineId,omitempty"`
	Endpoint  string              `json:"endpoint,omitempty"`
	Services  []string            `json:"services,omitempty"`
	Watch     map[string][]string `json:"watch,omitempty"`
	// AIDEIgnore are subtrees of watched paths the integrity check skips.
	AIDEIgnore []string `json:"aideIgnore,omitempty"`
	Token      string   `json:"token,omitempty"`
}

// securityReply is the installer's stdout.
type securityReply struct {
	OK           bool     `json:"ok"`
	Error        string   `json:"error"`
	Changes      []string `json:"changes"`
	Manual       []string `json:"manual"`
	Notes        []string `json:"notes"`
	TokenMissing bool     `json:"tokenMissing"`
	Transaction  string   `json:"transaction"`
}

// securityAssets are the installer and the files it installs, staged
// together.
var securityAssets = []string{"install.py", "helper.py", "runtime.py"}

// securityDropIn adds the component's LCM settings to lcm.service.
const securityDropIn = "[Service]\nEnvironmentFile=" + SecurityEnvFile + "\n"

// maxSecurityServices is the installer's limit of observed services.
const maxSecurityServices = 64

// securityTokenName names the ingest token apply creates for the collector.
const securityTokenName = "security-collector"

// transactionID is the form of the installer's transaction IDs.
var transactionID = regexp.MustCompile(`^[0-9]{8}T[0-9]{6}Z-[0-9a-f]{8}$`)

// runSecurityInstaller runs one installer action. A reply that is not the
// installer's JSON is an error; the installer's own refusal is returned
// with its message.
func runSecurityInstaller(ctx context.Context, h Host, action string, in securityInput, args ...string) (securityReply, error) {
	if info, err := h.FS.Lstat(python3); err != nil || info.IsDir() {
		return securityReply{}, fmt.Errorf("the security installer needs %s", python3)
	}
	dir, err := h.FS.MkdirTemp("/tmp", "lcm-security-")
	if err != nil {
		return securityReply{}, err
	}
	defer func() { _ = h.FS.RemoveAll(dir) }()
	for _, name := range securityAssets {
		if err := h.FS.WriteFile(dir+"/"+name, bytes.NewReader(asset("security/"+name)), 0o600, 0, 0); err != nil {
			return securityReply{}, err
		}
	}
	input, err := json.Marshal(in)
	if err != nil {
		return securityReply{}, err
	}
	timeout := 3 * time.Minute
	if !strings.HasSuffix(action, "-plan") && action != "plan" {
		// Installing the packages may take a while.
		timeout = 20 * time.Minute
	}
	out, runErr := h.Runner.Run(ctx, Command{
		Path: python3, Args: append([]string{"-I", dir + "/install.py", action}, args...),
		Env: []string{"LANG=C.UTF-8"}, Stdin: bytes.NewReader(input), MaxOutput: 256 << 10, Timeout: timeout,
	})
	var reply securityReply
	if err := json.Unmarshal(bytes.TrimSpace(out), &reply); err != nil {
		if runErr != nil {
			return securityReply{}, runErr
		}
		return securityReply{}, errors.New("install.py did not answer with its report")
	}
	if !reply.OK {
		return securityReply{}, fmt.Errorf("install.py %s: %s", action, printable(reply.Error, 1000))
	}
	if runErr != nil {
		return securityReply{}, runErr
	}
	return reply, nil
}

func (a *applier) preflightSecurity() error {
	if a.cfg.Components.Security == nil && !a.securityInstalled() {
		return nil
	}
	for _, p := range []string{python3, visudo} {
		if info, err := a.h.FS.Lstat(p); err != nil || info.IsDir() {
			return fmt.Errorf("the security component needs %s", p)
		}
	}
	return nil
}

// securityInstalled reports whether any file of the component exists.
func (a *applier) securityInstalled() bool {
	return a.present(append(slices.Clone(securityFiles), rootFile{path: SecurityCollector}))
}

// securityInput describes the wanted component: the Machine, the services
// whose locations get observation hooks and every watched path. The
// directories of the services LCM deploys are deploy watches. AIDE skips
// /opt/<service>: every deploy changes it, its releases are root-owned and
// LCM records each deploy; the audit watch stays. An external grant has a
// route, and so hooks, but no directories of LCM's.
func (a *applier) securityInput(sec *Security) securityInput {
	watch := map[string][]string{}
	for category, paths := range sec.Watch {
		watch[category] = slices.Clone(paths)
	}
	services := slices.Sorted(maps.Keys(a.cfg.Services))
	var ignore []string
	for _, name := range services {
		if !a.cfg.Services[name].External {
			watch["deploy"] = append(watch["deploy"], ServiceOptDir(name), ServiceEtcDir(name))
			ignore = append(ignore, ServiceOptDir(name))
		}
	}
	for category, paths := range watch {
		slices.Sort(paths)
		watch[category] = slices.Compact(paths)
	}
	return securityInput{Version: 1, MachineID: sec.MachineID, Endpoint: SecurityEndpoint, Services: services, Watch: watch, AIDEIgnore: ignore}
}

func (s *step) installerReport(r securityReply) {
	for _, m := range r.Manual {
		s.manual(printable(m, 500))
	}
	for _, n := range r.Notes {
		s.note(printable(n, 500))
	}
}

// done counts and prints a change the installer has already made.
func (s *step) done(what string) {
	_ = s.change(printable(what, 500), func() error { return nil })
}

func (a *applier) securityComponent(ctx context.Context, s *step) error {
	sec := a.cfg.Components.Security
	if sec == nil {
		if !a.securityInstalled() {
			return nil
		}
		return a.removeSecurity(ctx, s)
	}
	in := a.securityInput(sec)
	if len(in.Services) > maxSecurityServices {
		return fmt.Errorf("the security component observes at most %d services; services has %d", maxSecurityServices, len(in.Services))
	}
	plan, err := runSecurityInstaller(ctx, a.h, "plan", in)
	if err != nil {
		return err
	}
	if len(plan.Manual) > 0 {
		// The installer changes nothing while a difference needs review.
		s.installerReport(plan)
		return nil
	}
	if a.o.Check {
		for _, c := range plan.Changes {
			_ = s.change(printable(c, 500), nil)
		}
		if plan.TokenMissing {
			s.note("apply creates the ingest token with: lcm token create --scope ingest --name " + securityTokenName)
		}
		s.installerReport(securityReply{Notes: plan.Notes})
	} else if len(plan.Changes) > 0 {
		var tokenID int64
		if plan.TokenMissing {
			if in.Token, tokenID, err = a.securityToken(ctx); err != nil {
				return fmt.Errorf("create the collector's ingest token: %w", err)
			}
		}
		applied, err := runSecurityInstaller(ctx, a.h, "apply", in)
		if err != nil {
			// The installer undid its changes; the token it would have kept
			// must not stay valid.
			if tokenID > 0 {
				if rerr := a.revokeSecurityToken(ctx, tokenID); rerr != nil {
					return fmt.Errorf("%w; revoking the new ingest token %d failed too, revoke it by hand: %w", err, tokenID, rerr)
				}
				return fmt.Errorf("%w; the new ingest token %d was revoked", err, tokenID)
			}
			return err
		}
		for _, c := range applied.Changes {
			s.done(c)
		}
		s.installerReport(applied)
		if applied.Transaction != "" {
			s.note("recorded as transaction " + printable(applied.Transaction, 64) + "; sudo bin/lcm host security revert undoes it")
		}
	} else {
		s.installerReport(securityReply{Notes: plan.Notes})
	}
	// The grant comes last: it is useless before the helper and its policy
	// are in place and verified.
	if err := a.ensureGrant(ctx, s, SecuritySudoers, asset("security/sudoers")); err != nil {
		return err
	}
	return a.securitySettings(ctx, s, sec.MachineID)
}

// securityToken creates an ingest-scope API token with the installed lcm
// binary, as lcm, the account that owns the database. The token reaches
// the installer on stdin only.
func (a *applier) securityToken(ctx context.Context) (string, int64, error) {
	if a.lcm == nil {
		return "", 0, errors.New("the account lcm does not exist")
	}
	before, err := a.securityTokenIDs(ctx)
	if err != nil {
		return "", 0, err
	}
	out, err := a.run(ctx, Command{
		Path: Binary, Args: []string{"token", "create", "--name", securityTokenName, "--scope", "ingest", "--data-dir", DataDir},
		As: a.lcm, MaxOutput: 4096, Timeout: time.Minute,
	})
	if err != nil {
		return "", 0, err
	}
	token := strings.TrimSpace(string(out))
	if token == "" || strings.ContainsAny(token, " \t\n") {
		return "", 0, errors.New("lcm token create printed no token")
	}
	// The new token is the one active collector token that was not there
	// before; without it a failed apply cannot revoke the token.
	after, err := a.securityTokenIDs(ctx)
	if err != nil {
		return "", 0, err
	}
	var id int64
	for candidate := range after {
		if !before[candidate] && candidate > id {
			id = candidate
		}
	}
	if id == 0 {
		return "", 0, errors.New("the new ingest token is not in lcm token list")
	}
	return token, id, nil
}

// securityTokenIDs lists the active tokens named securityTokenName.
func (a *applier) securityTokenIDs(ctx context.Context) (map[int64]bool, error) {
	out, err := a.run(ctx, Command{
		Path: Binary, Args: []string{"token", "list", "--data-dir", DataDir}, As: a.lcm, MaxOutput: 1 << 20, Timeout: time.Minute,
	})
	if err != nil {
		return nil, err
	}
	ids := map[int64]bool{}
	for _, line := range strings.Split(string(out), "\n") {
		f := strings.Fields(line)
		if len(f) < 3 || f[1] != securityTokenName || f[len(f)-1] != "active" {
			continue
		}
		if id, err := strconv.ParseInt(f[0], 10, 64); err == nil && id > 0 {
			ids[id] = true
		}
	}
	return ids, nil
}

func (a *applier) revokeSecurityToken(ctx context.Context, id int64) error {
	_, err := a.run(ctx, Command{
		Path: Binary, Args: []string{"token", "revoke", "--data-dir", DataDir, strconv.FormatInt(id, 10)}, As: a.lcm, Timeout: time.Minute,
	})
	return err
}

// securitySettings makes LCM itself read the security settings: a drop-in
// adds /etc/lcm/workstation-security.env to lcm.service. An existing file
// is kept with its other settings (such as the optional Konata delivery);
// it must name the configured Machine.
func (a *applier) securitySettings(ctx context.Context, s *step, machine string) error {
	fsys := a.h.FS
	if err := a.ensureDir(s, SecurityDropInDir, 0o755, 0, 0, "root:root"); err != nil {
		return err
	}
	changed, err := a.ensureFile(s, SecurityDropIn, bytesContent([]byte(securityDropIn)), 0o644)
	if err != nil {
		return err
	}
	_, gid := a.lcmIDs()
	info, err := fsys.Lstat(SecurityEnvFile)
	switch {
	case errors.Is(err, fs.ErrNotExist):
		content := "LCM_SECURITY_ENABLED=true\nLCM_SECURITY_MACHINE_ID=" + machine + "\n"
		changed = true
		if err := s.change("write "+SecurityEnvFile+" (root:lcm 0640)", func() error {
			return a.writeContent(SecurityEnvFile, bytesContent([]byte(content)), 0o640, 0, gid)
		}); err != nil {
			return err
		}
	case err != nil:
		return err
	case !info.IsRegular() || info.UID != 0:
		s.manual(SecurityEnvFile + " is not a root-owned regular file; review it, then make it root:lcm 0640")
		return nil
	default:
		data, err := readAll(fsys, SecurityEnvFile)
		if err != nil {
			return err
		}
		settings := envSettings(string(data))
		if settings["LCM_SECURITY_MACHINE_ID"] != machine || settings["LCM_SECURITY_ENABLED"] != "true" {
			s.manual(fmt.Sprintf("%s must set LCM_SECURITY_ENABLED=true and LCM_SECURITY_MACHINE_ID=%s; edit it, then restart lcm.service", SecurityEnvFile, machine))
		}
		if info.GID != gid || info.Mode.Perm() != 0o640 {
			if err := s.change("set "+SecurityEnvFile+" to root:lcm 0640", func() error {
				if err := fsys.Chown(SecurityEnvFile, info, 0, gid); err != nil {
					return err
				}
				return fsys.Chmod(SecurityEnvFile, info, 0o640)
			}); err != nil {
				return err
			}
		}
	}
	if !changed {
		return nil
	}
	return s.change("restart "+UnitName+" for the security settings", func() error {
		if _, err := a.systemctl(ctx, "daemon-reload"); err != nil {
			return err
		}
		if _, err := a.systemctl(ctx, "restart", UnitName); err != nil {
			return err
		}
		return waitHealthy(ctx, a.h, healthWait)
	})
}

// envSettings reads KEY=VALUE lines; the last assignment of a key wins, as
// in systemd.
func envSettings(content string) map[string]string {
	out := map[string]string{}
	for _, line := range strings.Split(content, "\n") {
		line = strings.TrimSpace(line)
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		if k, v, ok := strings.Cut(line, "="); ok {
			out[strings.TrimSpace(k)] = strings.Trim(strings.TrimSpace(v), `"`)
		}
	}
	return out
}

// removeSecurity removes an installed component the configuration turns
// off, but only with --remove; otherwise it lists what would go. The grant
// goes first, so that nothing can call the helper while it is removed.
func (a *applier) removeSecurity(ctx context.Context, s *step) error {
	plan, err := runSecurityInstaller(ctx, a.h, "remove-plan", securityInput{Version: 1})
	if err != nil {
		return err
	}
	_, grantErr := a.h.FS.Lstat(SecuritySudoers)
	grant := grantErr == nil
	if !grant && len(plan.Changes) == 0 {
		return nil
	}
	if !a.o.Remove {
		s.manual("components.security is null but the security component is installed; sudo bin/lcm host apply --remove removes it (the AIDE baseline, reports and helper journal are kept)")
		if grant {
			s.note("would remove " + SecuritySudoers)
		}
		for _, c := range plan.Changes {
			s.note("would " + printable(c, 500))
		}
		return nil
	}
	if grant {
		if err := s.change("remove "+SecuritySudoers, func() error { return a.h.FS.Remove(SecuritySudoers) }); err != nil {
			return err
		}
	}
	if a.o.Check {
		for _, c := range plan.Changes {
			_ = s.change(printable(c, 500), nil)
		}
		s.installerReport(securityReply{Notes: plan.Notes})
		return nil
	}
	removed, err := runSecurityInstaller(ctx, a.h, "remove", securityInput{Version: 1})
	if err != nil {
		return err
	}
	for _, c := range removed.Changes {
		s.done(c)
	}
	s.installerReport(removed)
	return nil
}

// SecurityOptions control SecurityCleanup and SecurityRevert.
type SecurityOptions struct {
	// Check lists what would change without changing anything.
	Check bool
	// Transaction selects the transaction SecurityRevert undoes; empty
	// selects the last one.
	Transaction string
	Out         io.Writer
}

// SecurityCleanup removes the known leftovers of the installers the
// security component replaced; anything else is only reported.
func SecurityCleanup(ctx context.Context, h Host, o SecurityOptions) (Result, error) {
	action := "cleanup"
	if o.Check {
		action = "cleanup-plan"
	}
	return securityMaintenance(ctx, h, o, "security cleanup", action)
}

// SecurityRevert undoes the last recorded transaction of the security
// installer, or the one named, when no file it changed was changed since.
func SecurityRevert(ctx context.Context, h Host, o SecurityOptions) (Result, error) {
	action := "revert"
	if o.Check {
		action = "revert-plan"
	}
	var args []string
	if o.Transaction != "" {
		if !transactionID.MatchString(o.Transaction) {
			return Result{}, fmt.Errorf("transaction %q is not a transaction ID", o.Transaction)
		}
		args = []string{"--transaction", o.Transaction}
	}
	return securityMaintenance(ctx, h, o, "security revert", action, args...)
}

func securityMaintenance(ctx context.Context, h Host, o SecurityOptions, name, action string, args ...string) (Result, error) {
	if !h.Privileged {
		return Result{}, errors.New("must run as root, from a reviewed build: sudo bin/lcm host security")
	}
	if err := CheckExecutable(h.Executable); err != nil {
		return Result{}, err
	}
	if o.Out == nil {
		o.Out = io.Discard
	}
	a := &applier{h: h, o: Options{Check: o.Check, Out: safeWriter{w: o.Out}}}
	s := &step{a: a, name: name}
	reply, err := runSecurityInstaller(ctx, h, action, securityInput{Version: 1}, args...)
	if err != nil {
		return a.res, safeError{err}
	}
	for _, c := range reply.Changes {
		if o.Check {
			_ = s.change(printable(c, 500), nil)
		} else {
			s.done(c)
		}
	}
	s.installerReport(reply)
	s.finish()
	return a.res, nil
}
