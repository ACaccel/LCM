package deployhelper

import (
	"context"
	"errors"
	"io/fs"
	"regexp"
	"slices"
	"strconv"
	"strings"

	"github.com/ACaccel/lcm/internal/hostsetup"
)

// ownUnit refuses a service without a unit the helper wrote.
func (h *Helper) ownUnit(resp Response, svc string) *Response {
	conflict, err := hostsetup.UnitConflict(h.FS, svc, h.RootUID)
	switch {
	case err != nil:
		r := refuse(resp, CodeFailed, "%v", err)
		return &r
	case conflict != "":
		r := refuse(resp, CodeRefused, "%s", conflict)
		return &r
	}
	if _, err := h.FS.Lstat(hostsetup.ServiceUnit(svc)); errors.Is(err, fs.ErrNotExist) {
		r := refuse(resp, CodeNotFound, "%s has no unit yet; activate a release first", svc)
		return &r
	}
	return nil
}

// unitAction starts, stops or restarts the service's unit.
func (h *Helper) unitAction(ctx context.Context, req Request) Response {
	resp := reply(req)
	if r := h.ownUnit(resp, req.Service); r != nil {
		return *r
	}
	res, err := h.run(ctx, systemctlTimeout, systemctl, string(req.Op), unitName(req.Service))
	if err != nil {
		resp.Log = sanitizeLog(res.Output)
		return refuse(resp, CodeFailed, "%s", commandFailure("systemctl "+string(req.Op), res.Output, err))
	}
	return resp
}

var unitValue = regexp.MustCompile(`^[a-z0-9-]{0,64}$`)

// status reports the unit's state and the current release.
func (h *Helper) status(ctx context.Context, req Request) Response {
	resp := reply(req)
	if r := h.ownUnit(resp, req.Service); r != nil {
		return *r
	}
	res, err := h.run(ctx, systemctlTimeout, systemctl, "show", "--property=LoadState,ActiveState,SubState,UnitFileState,MainPID", unitName(req.Service))
	if err != nil {
		resp.Log = sanitizeLog(res.Output)
		return refuse(resp, CodeFailed, "%s", commandFailure("systemctl show", res.Output, err))
	}
	st := &UnitStatus{}
	for line := range strings.SplitSeq(strings.TrimSpace(res.Output), "\n") {
		key, value, _ := strings.Cut(line, "=")
		if !unitValue.MatchString(value) {
			return refuse(resp, CodeFailed, "systemctl show printed an unexpected value")
		}
		switch key {
		case "LoadState":
			st.LoadState = value
		case "ActiveState":
			st.ActiveState = value
		case "SubState":
			st.SubState = value
		case "UnitFileState":
			st.UnitFileState = value
		case "MainPID":
			if st.MainPID, err = strconv.Atoi(value); err != nil {
				return refuse(resp, CodeFailed, "systemctl show printed an unexpected value")
			}
		default:
			return refuse(resp, CodeFailed, "systemctl show printed an unexpected value")
		}
	}
	if st.Release, err = h.linkTarget(hostsetup.ServiceOptDir(req.Service) + "/current"); err != nil {
		return refuse(resp, CodeFailed, "%v", err)
	}
	resp.Unit = st
	return resp
}

// releases lists the installed releases, newest first.
func (h *Helper) releases(req Request) Response {
	resp, svc := reply(req), req.Service
	releases := hostsetup.ServiceReleases(svc)
	if _, err := h.trustedPath(releases, false); err != nil {
		return refuse(resp, CodeNotFound, "%v", err)
	}
	opt := hostsetup.ServiceOptDir(svc)
	current, err := h.linkTarget(opt + "/current")
	if err != nil {
		return refuse(resp, CodeFailed, "%v", err)
	}
	previous, err := h.linkTarget(opt + "/previous")
	if err != nil {
		return refuse(resp, CodeFailed, "%v", err)
	}
	names, err := h.FS.ReadDir(releases)
	if err != nil {
		return refuse(resp, CodeFailed, "%v", err)
	}
	slices.Reverse(names)
	list := []Release{}
	for _, id := range names {
		if !idPattern.MatchString(id) {
			continue
		}
		if info, err := h.FS.Lstat(releases + "/" + id); err != nil || !info.IsDir() {
			continue
		}
		r := Release{ReleaseID: id, Current: id == current, Previous: id == previous}
		var m Manifest
		if err := h.readJSON(manifestPath(svc, id), &m); err == nil && m.ReleaseID == id {
			r.Manifest = &m
		}
		list = append(list, r)
	}
	resp.Releases = list
	return resp
}
