package deployhelper

import (
	"bytes"
	"context"
	"errors"
	"io"
	"io/fs"
	"time"

	"github.com/ACaccel/lcm/internal/execx"
	"github.com/ACaccel/lcm/internal/hostsetup"
	"github.com/ACaccel/lcm/internal/ingress"
)

// ingressEnv is the environment of the shared site sequence.
func (h *Helper) ingressEnv() hostsetup.IngressEnv {
	return hostsetup.IngressEnv{
		FS: h.FS, Clock: h.Clock, RootUID: h.RootUID, RootGID: h.RootGID,
		Run: func(ctx context.Context, timeout time.Duration, argv ...string) (string, error) {
			res, err := h.Runner.Run(ctx, execx.Cmd{Argv: argv, Dir: "/", Env: commandEnv, Timeout: timeout, MaxOutput: MaxLog})
			return sanitizeLog(res.Output), err
		},
		Probe: h.Probe, ProbeAttempts: h.ProbeAttempts, ProbeWait: h.ProbeWait,
	}
}

// ingressStatus answers ingress.status.
func (h *Helper) ingressStatus(req Request) Response {
	cfg, err := h.Config()
	if err != nil {
		return refuse(reply(req), CodeRefused, "the host configuration cannot be used: %v", err)
	}
	if cfg.Components.Ingress == nil {
		return refuse(reply(req), CodeDisabled, "components.ingress is not configured in %s", hostsetup.ConfigPath)
	}
	st, err := hostsetup.SiteStatus(h.ingressEnv(), cfg)
	if err != nil {
		return refuse(reply(req), CodeFailed, "the ingress state cannot be read: %v", err)
	}
	resp := reply(req)
	resp.Ingress = &st
	return resp
}

// ingressApply stores a service's route options and applies the whole site
// with them; a site that fails puts the previous options back.
func (h *Helper) ingressApply(ctx context.Context, req Request, cfg hostsetup.Config) Response {
	i := cfg.Components.Ingress
	if _, err := h.trustedPath(hostsetup.Nginx, true); err != nil {
		return refuse(reply(req), CodeRefused, "nginx cannot be used: %v", err)
	}
	if refused := h.siteRefusal(req); refused != nil {
		return *refused
	}
	if info, err := h.trustedPath(hostsetup.IngressRoutes, false); err != nil || info.Mode.Perm() != 0o700 {
		return refuse(reply(req), CodeRefused, "%s is not a root-only directory; run sudo bin/lcm host apply", hostsetup.IngressRoutes)
	}
	unlock, err := h.IngressLock(i.LegacyLock)
	if errors.Is(err, hostsetup.ErrIngressBusy) {
		return refuse(reply(req), CodeBusy, "another change of the nginx site is running")
	}
	if err != nil {
		return refuse(reply(req), CodeFailed, "the ingress lock cannot be taken: %v", err)
	}
	defer unlock()
	// The site may have been reverted or disabled while the lock was
	// taken.
	if refused := h.siteRefusal(req); refused != nil {
		return *refused
	}
	ro, err := hostsetup.ReadRouteOptions(h.FS, h.RootUID)
	if err != nil {
		return refuse(reply(req), CodeFailed, "the accepted route options cannot be read: %v", err)
	}
	file := hostsetup.RouteOptionsFile(req.Service)
	previous, hadPrevious, err := h.readOptionsFile(file)
	if err != nil {
		return refuse(reply(req), CodeFailed, "%v", err)
	}
	if req.Route != nil {
		ro.Options[req.Service] = *req.Route
	} else {
		delete(ro.Options, req.Service)
	}
	env := h.ingressEnv()
	plan, err := hostsetup.PlanSite(env, cfg, ro, req.Service)
	if err != nil {
		return refuse(reply(req), CodeRefused, "the route cannot be applied: %v", err)
	}
	switch {
	case plan.Adopt != "":
		return refuse(reply(req), CodeRefused, "the adopted site %s is enabled again; an administrator reviews it with sudo bin/lcm host apply --check", plan.Adopt)
	case !plan.Enabled:
		return refuse(reply(req), CodeRefused, "%s is not enabled; an administrator enables it again with sudo bin/lcm host apply", hostsetup.SiteName)
	}
	if err := h.writeOptions(file, req.Route); err != nil {
		return refuse(reply(req), CodeFailed, "the route options cannot be stored: %v", err)
	}
	resp := reply(req)
	resp.IngressApply = &IngressApplyResult{SHA256: plan.SHA256, Changes: plan.Changes(), Dropped: plan.Dropped}
	if resp.IngressApply.Changes == nil {
		resp.IngressApply.Changes = []string{}
	}
	if err := hostsetup.ApplySite(ctx, env, plan); err != nil {
		var restore error
		if hadPrevious {
			restore = h.FS.WriteFile(file, bytes.NewReader(previous), 0o600, h.RootUID, h.RootGID)
		} else {
			restore = ignoreNotExist(h.FS.Remove(file))
		}
		if restore != nil {
			err = errors.Join(err, errors.New("the previous route options could not be put back: "+restore.Error()))
		}
		return refuse(resp, CodeFailed, "%v", err)
	}
	return resp
}

// siteRefusal refuses a change of a site that an administrator has not
// installed, has reverted or has disabled.
func (h *Helper) siteRefusal(req Request) *Response {
	var resp Response
	enabled, err := hostsetup.SiteEnabled(h.FS)
	switch {
	case err != nil:
		resp = refuse(reply(req), CodeRefused, "%v", err)
	case hostsetup.Reverted(h.FS):
		// Checked first: revert also removes the site file.
		resp = refuse(reply(req), CodeRefused, "the ingress site was reverted with sudo bin/lcm host ingress revert; sudo bin/lcm host apply installs it again")
	case !hostsetup.SiteInstalled(h.FS):
		resp = refuse(reply(req), CodeRefused, "the ingress site is not installed yet; an administrator installs it (and reviews the cutover) with sudo bin/lcm host apply")
	case !enabled:
		resp = refuse(reply(req), CodeRefused, "%s is not enabled; an administrator enables it again with sudo bin/lcm host apply", hostsetup.SiteName)
	default:
		return nil
	}
	return &resp
}

// readOptionsFile returns the stored options file of a service as it is.
func (h *Helper) readOptionsFile(file string) ([]byte, bool, error) {
	info, err := h.FS.Lstat(file)
	switch {
	case errors.Is(err, fs.ErrNotExist):
		return nil, false, nil
	case err != nil:
		return nil, false, err
	case !info.IsRegular() || info.UID != h.RootUID:
		return nil, false, errors.New(file + " is not a root-owned regular file")
	}
	f, err := h.FS.Open(file)
	if err != nil {
		return nil, false, err
	}
	defer func() { _ = f.Close() }()
	data, err := io.ReadAll(io.LimitReader(f, 256<<10))
	if err != nil {
		return nil, false, err
	}
	return data, true, nil
}

// writeOptions stores a service's accepted options; nil removes them.
func (h *Helper) writeOptions(file string, o *ingress.Options) error {
	if o == nil {
		return ignoreNotExist(h.FS.Remove(file))
	}
	return h.FS.WriteFile(file, bytes.NewReader(hostsetup.EncodeRouteOptions(*o)), 0o600, h.RootUID, h.RootGID)
}

func ignoreNotExist(err error) error {
	if errors.Is(err, fs.ErrNotExist) {
		return nil
	}
	return err
}
