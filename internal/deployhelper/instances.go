package deployhelper

import (
	"context"
	"errors"
	"fmt"
	"io/fs"
	"maps"
	"slices"
	"strconv"
	"strings"

	"github.com/ACaccel/lcm/internal/hostsetup"
)

// helperUnits are the units the helper wrote for one service: its single
// unit and its instance units, read from the unit directory. Only files
// that start with the marker count; another file named like an instance
// unit is a conflict.
type helperUnits struct {
	single    bool
	instances []string
}

// all returns the unit names, the single unit first.
func (u helperUnits) all(svc string) []string {
	var out []string
	if u.single {
		out = append(out, unitName(svc))
	}
	for _, in := range u.instances {
		out = append(out, hostsetup.InstanceUnitName(svc, in))
	}
	return out
}

// scanUnits reads every unit file of svc in the unit directory and returns
// the ones the helper wrote, and a conflict naming every file that looks
// like an instance unit of svc but was not written by the helper.
func (h *Helper) scanUnits(svc string) (helperUnits, string, error) {
	var u helperUnits
	var foreign []string
	names, err := h.FS.ReadDir(hostsetup.UnitDir)
	if err != nil {
		return u, "", err
	}
	for _, n := range names {
		inst, isInstance := strings.CutPrefix(n, svc+"@")
		inst, isUnit := strings.CutSuffix(inst, ".service")
		single := n == unitName(svc)
		if !single && (!isInstance || !isUnit || inst == "") {
			continue
		}
		p := hostsetup.UnitDir + "/" + n
		info, err := h.FS.Lstat(p)
		if err != nil {
			return u, "", err
		}
		owned := false
		if info.IsRegular() && info.UID == h.RootUID {
			data, err := h.readFile(p)
			if err != nil {
				return u, "", err
			}
			owned = strings.HasPrefix(string(data), hostsetup.ServiceUnitMarker+"\n")
		}
		switch {
		case single && owned:
			u.single = true
		case single:
			// A foreign single unit does not take part in an instance's
			// life; UnitConflict guards the single-unit operations.
		case !owned || !hostsetup.ValidInstanceName(inst):
			foreign = append(foreign, p)
		default:
			u.instances = append(u.instances, inst)
		}
	}
	slices.Sort(u.instances)
	if len(foreign) > 0 {
		slices.Sort(foreign)
		return u, fmt.Sprintf("%s exists and was not written by the deploy helper", strings.Join(foreign, ", ")), nil
	}
	return u, "", nil
}

// instanceConflict checks the units of a service granted instances: a
// template, drop-ins or foreign units would change or provide them.
func (h *Helper) instanceConflict(svc string, g hostsetup.ServiceGrant) (helperUnits, *opErr) {
	conflict, err := hostsetup.InstanceConflicts(h.FS, svc, slices.Sorted(maps.Keys(g.Instances)), h.RootUID)
	if err != nil {
		return helperUnits{}, failf(CodeFailed, "%v", err)
	}
	if conflict != "" {
		return helperUnits{}, failf(CodeRefused, "%s", conflict)
	}
	units, conflict, err := h.scanUnits(svc)
	if err != nil {
		return units, failf(CodeFailed, "%v", err)
	}
	if conflict != "" {
		return units, failf(CodeRefused, "%s", conflict)
	}
	return units, nil
}

// checkGroups checks the supplementary groups a grant gives a unit against
// the host, as host setup did when it was granted: each must still exist
// and pass hostsetup.GroupProblem.
func (h *Helper) checkGroups(ctx context.Context, cfg hostsetup.Config, svc string, groups []string) error {
	if len(groups) == 0 {
		return nil
	}
	if h.LookupGroup == nil {
		return errors.New("groups cannot be looked up")
	}
	primary := map[int]string{}
	for _, n := range hostsetup.ProtectedAccounts(cfg, svc) {
		acc, ok, err := h.Lookup(ctx, n)
		if err != nil {
			return err
		}
		if ok {
			primary[acc.GID] = n
		}
	}
	min := hostsetup.GIDMin(h.FS)
	for _, name := range groups {
		g, ok, err := h.LookupGroup(ctx, name)
		if err != nil {
			return err
		}
		if !ok {
			return fmt.Errorf("the group %s that %s grants does not exist; run sudo bin/lcm host apply", name, hostsetup.ConfigPath)
		}
		if p := hostsetup.GroupProblem(g, min, primary); p != "" {
			return errors.New(p)
		}
	}
	return nil
}

// checkInstanceEnv checks the environment file of an instance like the
// service's: root's, and not setting what host.json or the unit decides.
func (h *Helper) checkInstanceEnv(svc, inst string) error {
	return h.checkEnvKeys(hostsetup.ServiceInstanceEnvFile(svc, inst), "PORT", "HOST", "LCM_SERVICE", "LCM_INSTANCE")
}

// checkEnvKeys refuses an env file that is not root's or sets one of keys.
// systemd lets EnvironmentFile= override Environment= whatever their
// order, so what the unit sets itself must not be in the files.
func (h *Helper) checkEnvKeys(env string, keys ...string) error {
	if _, err := h.trustedPath(env, true); err != nil {
		return err
	}
	data, err := h.readFile(env)
	if err != nil {
		return err
	}
	if key := envSets(string(data), keys...); key != "" {
		return fmt.Errorf("%s sets %s, which comes from host.json or the unit; remove it", env, key)
	}
	return nil
}

// activateInstances activates a release of a service granted instances:
// every check first, then the migration and publishing once, then one unit
// per instance and one switch of the release links (switchInstances).
func (h *Helper) activateInstances(ctx context.Context, req Request, resp Response, cfg hostsetup.Config, g hostsetup.ServiceGrant, relDir string) Response {
	svc := req.Service
	for _, in := range req.Instances {
		if _, ok := g.Instances[in.Name]; !ok {
			return refuse(resp, CodeRefused, "the instance %s is not granted to %s in %s; an administrator grants it with sudo bin/lcm host allow %s --instance %s",
				in.Name, svc, hostsetup.ConfigPath, svc, in.Name)
		}
	}
	if req.Publish != nil && (g.Route == nil || g.Route.Static != hostsetup.ServicePublishDir(svc)) {
		return refuse(resp, CodeRefused, "publish needs services.%s.route.static to be %s in %s", svc, hostsetup.ServicePublishDir(svc), hostsetup.ConfigPath)
	}
	if req.Runtime != "" && !slices.Contains(g.Runtimes, req.Runtime) {
		return refuse(resp, CodeRefused, "the runtime %s is not granted to %s in %s", req.Runtime, svc, hostsetup.ConfigPath)
	}
	acc, err := h.account(ctx, svc, hostsetup.ServiceDataDir(svc), hostsetup.ServiceComment(svc))
	if err != nil {
		return refuse(resp, CodeRefused, "%v", err)
	}
	units, e := h.instanceConflict(svc, g)
	if e != nil {
		return respond(resp, e)
	}
	if err := h.checkServiceFiles(svc, acc); err != nil {
		return refuse(resp, CodeRefused, "%v; run sudo bin/lcm host allow", err)
	}
	if err := h.checkEnvKeys(hostsetup.ServiceEnvFile(svc), "LCM_SERVICE", "LCM_INSTANCE"); err != nil {
		return refuse(resp, CodeRefused, "%v", err)
	}
	for _, in := range req.Instances {
		if err := h.checkInstanceEnv(svc, in.Name); err != nil {
			return refuse(resp, CodeRefused, "%v; run sudo bin/lcm host allow", err)
		}
		if err := h.checkGroups(ctx, cfg, svc, g.GrantedGroups(in.Name)); err != nil {
			return refuse(resp, CodeRefused, "services.%s.instances.%s: %v", svc, in.Name, err)
		}
	}
	rtBin := ""
	if req.Runtime != "" {
		rt := hostsetup.RuntimesDir + "/" + req.Runtime
		if _, err := h.trustedPath(rt, false); err != nil {
			return refuse(resp, CodeRefused, "the runtime %s is not installed (%v); run sudo bin/lcm host apply", req.Runtime, err)
		}
		rtBin = rt + "/bin"
	}
	specs := make([]unitSpec, 0, len(req.Instances))
	for _, in := range req.Instances {
		execStart, err := h.startCommand(svc, relDir, in.Start, rtBin)
		if err != nil {
			return refuse(resp, CodeRefused, "instance %s: %v", in.Name, err)
		}
		specs = append(specs, unitSpec{svc: svc, instance: in.Name, port: g.Instances[in.Name].Port, execStart: execStart, rtBin: rtBin,
			backup: backupDir(cfg), limits: in.Limits, after: g.After, wants: g.Wants, groups: g.GrantedGroups(in.Name)})
	}
	if req.Migrate != nil {
		// Once per activation, with the shared env file and no port: no
		// instance owns the migration.
		argv, err := h.migrateCommand(relDir, req.Migrate, rtBin)
		if err != nil {
			return refuse(resp, CodeRefused, "%v", err)
		}
		unit := "lcm-migrate-" + svc + "-" + req.OperationID[:8]
		res, err := h.run(ctx, migrateTimeout+migrateRunMargin, migrateArgs(unit, svc, acc, relDir, 0, rtBin, backupDir(cfg), argv)...)
		resp.Log = sanitizeLog(res.Output)
		if err != nil {
			h.stopUnit(ctx, unit, err)
			return refuse(resp, CodeFailed, "%s; nothing was switched", commandFailure("the migration", res.Output, err))
		}
	}
	var published *PublishResult
	if req.Publish != nil {
		var notes []string
		if published, notes, err = h.publish(svc, relDir, *req.Publish); err != nil {
			return refuse(resp, CodeFailed, "%v; nothing was switched", err)
		}
		if len(notes) > 0 {
			resp.Log = sanitizeLog(strings.TrimRight(resp.Log, "\n") + "\n" + strings.Join(notes, "\n"))
		}
	}
	return h.switchInstances(ctx, req, resp, specs, units, published)
}

// instanceUnit is one unit file activate writes.
type instanceUnit struct {
	name, path, content string
	old                 []byte
	existed, changed    bool
}

// switchInstances writes the instance units, switches the release links and
// reloads systemd, putting everything back when a unit does not verify or
// load. After the switch it stops and disables the units of instances no
// longer requested (the orphans, the single unit of a converted service
// included) before anything starts, enables the new units, restarts every
// enabled instance in one call so that all run the same release, and then
// removes the orphans' files. An instance an operator stopped (disabled)
// gets its new unit but stays stopped.
func (h *Helper) switchInstances(ctx context.Context, req Request, resp Response, specs []unitSpec, existing helperUnits, published *PublishResult) Response {
	svc, id := req.Service, req.ReleaseID
	opt := hostsetup.ServiceOptDir(svc)
	cur, prev := opt+"/current", opt+"/previous"
	oldCur, err := h.linkTarget(cur)
	if err != nil {
		return refuse(resp, CodeFailed, "%v", err)
	}
	oldPrev, err := h.linkTarget(prev)
	if err != nil {
		return refuse(resp, CodeFailed, "%v", err)
	}
	units := make([]instanceUnit, 0, len(specs))
	for _, s := range specs {
		u := instanceUnit{name: s.instance, path: hostsetup.InstanceUnit(svc, s.instance), content: renderSpec(s)}
		if _, err := h.FS.Lstat(u.path); err == nil {
			if u.old, err = h.readFile(u.path); err != nil {
				return refuse(resp, CodeFailed, "%v", err)
			}
			u.existed = true
		}
		u.changed = !u.existed || string(u.old) != u.content
		units = append(units, u)
	}
	result := &ActivateResult{ReleaseID: id, Previous: oldCur, Published: published}
	if oldCur == id {
		result.Previous = oldPrev
	}
	for _, u := range units {
		result.UnitChanged = result.UnitChanged || u.changed
		result.Instances = append(result.Instances, InstanceResult{Name: u.name, UnitChanged: u.changed})
	}
	resp.Activate = result
	written := 0
	restore := func() error {
		var errs []error
		for _, u := range units[:written] {
			if !u.changed {
				continue
			}
			if u.existed {
				errs = append(errs, h.FS.WriteFile(u.path, strings.NewReader(string(u.old)), 0o644, h.RootUID, h.RootGID))
			} else {
				errs = append(errs, h.FS.Remove(u.path))
			}
		}
		if result.Switched && oldCur != id {
			errs = append(errs, h.restoreLink(cur, oldCur), h.restoreLink(prev, oldPrev))
			result.Switched = false
		}
		return errors.Join(errs...)
	}
	for _, u := range units {
		if u.changed {
			if err := h.FS.WriteFile(u.path, strings.NewReader(u.content), 0o644, h.RootUID, h.RootGID); err != nil {
				return refuse(resp, CodeFailed, "write %s: %v", u.path, errors.Join(err, restore()))
			}
		}
		written++
	}
	if oldCur != id {
		if err := h.setLink(cur, "releases/"+id); err != nil {
			return refuse(resp, CodeFailed, "switch %s: %v", cur, errors.Join(err, restore()))
		}
		result.Switched = true
		if oldCur != "" {
			if err := h.setLink(prev, "releases/"+oldCur); err != nil {
				return refuse(resp, CodeFailed, "switch %s: %v", prev, errors.Join(err, restore()))
			}
		}
	}
	result.Switched = true
	var changedPaths, names []string
	for _, u := range units {
		names = append(names, hostsetup.InstanceUnitName(svc, u.name))
		if u.changed {
			changedPaths = append(changedPaths, u.path)
		}
	}
	if len(changedPaths) > 0 {
		if res, err := h.run(ctx, analyzeTimeout, append([]string{systemdAnalyze, "verify"}, changedPaths...)...); err != nil {
			resp.Log = sanitizeLog(res.Output)
			if rerr := restore(); rerr != nil {
				return refuse(resp, CodeFailed, "a unit does not verify, and restoring the previous units and release failed: %v", rerr)
			}
			return refuse(resp, CodeFailed, "a unit does not verify; the previous units and release were restored")
		}
	}
	reload := len(changedPaths) > 0
	if !reload {
		res, err := h.run(ctx, systemctlTimeout, append([]string{systemctl, "show", "--property=NeedDaemonReload", "--value"}, names...)...)
		// systemctl show separates the units with empty lines.
		lines := strings.Fields(res.Output)
		reload = err != nil || len(lines) != len(names) || slices.ContainsFunc(lines, func(l string) bool { return l != "no" })
	}
	if reload {
		if res, err := h.run(ctx, systemctlTimeout, systemctl, "daemon-reload"); err != nil {
			resp.Log = sanitizeLog(res.Output)
			rerr := restore()
			if rerr == nil {
				_, rerr = h.run(ctx, systemctlTimeout, systemctl, "daemon-reload")
			}
			if rerr != nil {
				return refuse(resp, CodeFailed, "%s, and restoring the previous units and release failed: %v", commandFailure("systemctl daemon-reload", res.Output, err), rerr)
			}
			return refuse(resp, CodeFailed, "%s; the previous units and release were restored", commandFailure("systemctl daemon-reload", res.Output, err))
		}
	}

	// The point of no return: the release is current.
	var orphans []string

	if existing.single {
		orphans = append(orphans, unitName(svc))
		result.Removed = append(result.Removed, "")
	}
	for _, in := range existing.instances {
		if !slices.ContainsFunc(units, func(u instanceUnit) bool { return u.name == in }) {
			orphans = append(orphans, hostsetup.InstanceUnitName(svc, in))
			result.Removed = append(result.Removed, in)
		}
	}
	if len(orphans) > 0 {
		// Before anything starts, so that an old process never holds a
		// port or a session while its replacement starts.
		if res, err := h.run(ctx, systemctlTimeout, append([]string{systemctl, "disable", "--now", "--quiet"}, orphans...)...); err != nil {
			resp.Log = sanitizeLog(res.Output)
			result.Removed = nil
			return refuse(resp, CodeFailed, "%s; the release is current", commandFailure("systemctl disable --now", res.Output, err))
		}
	}
	var fresh []string
	for _, u := range units {
		if !u.existed {
			fresh = append(fresh, hostsetup.InstanceUnitName(svc, u.name))
		}
	}
	// From here on the orphans are stopped and disabled, so their files go
	// whatever happens next, as with a single unit.
	if len(fresh) > 0 {
		if res, err := h.run(ctx, systemctlTimeout, append([]string{systemctl, "enable", "--quiet"}, fresh...)...); err != nil {
			resp.Log = sanitizeLog(res.Output)
			h.removeUnits(ctx, &resp, orphans)
			return refuse(resp, CodeFailed, "%s; the release is current", commandFailure("systemctl enable", res.Output, err))
		}
	}
	enabled, err := h.enabledUnits(ctx, names)
	if err != nil {
		h.removeUnits(ctx, &resp, orphans)
		return refuse(resp, CodeFailed, "%v; the release is current", err)
	}
	var restart []string
	for i, n := range names {
		result.Instances[i].Enabled = enabled[n]
		if enabled[n] {
			restart = append(restart, n)
		}
	}
	if len(restart) > 0 {
		if res, err := h.run(ctx, systemctlTimeout, append([]string{systemctl, "restart"}, restart...)...); err != nil {
			resp.Log = sanitizeLog(res.Output)
			h.fillActiveStates(ctx, svc, result)
			h.removeUnits(ctx, &resp, orphans)
			return refuse(resp, CodeFailed, "%s; the release is current", commandFailure("systemctl restart", res.Output, err))
		}
		for i := range result.Instances {
			result.Instances[i].Restarted = result.Instances[i].Enabled
		}
	}
	h.fillActiveStates(ctx, svc, result)
	h.removeUnits(ctx, &resp, orphans)
	keep := req.KeepReleases
	if keep == 0 {
		keep = DefaultKeepReleases
	}
	h.pruneReleases(svc, keep, id, result.Previous)
	return resp
}

// enabledUnits asks systemd which of names are enabled, in one call.
func (h *Helper) enabledUnits(ctx context.Context, names []string) (map[string]bool, error) {
	// is-enabled exits non-zero when one is not enabled; its lines tell.
	res, _ := h.run(ctx, systemctlTimeout, append([]string{systemctl, "is-enabled"}, names...)...)
	lines := strings.Split(strings.TrimSpace(res.Output), "\n")
	if len(lines) != len(names) {
		return nil, errors.New("systemctl is-enabled printed an unexpected answer")
	}
	out := map[string]bool{}
	for i, n := range names {
		state := strings.TrimSpace(lines[i])
		if !unitValue.MatchString(state) {
			return nil, errors.New("systemctl is-enabled printed an unexpected value")
		}
		out[n] = state == "enabled"
	}
	return out, nil
}

// fillActiveStates records the state of each instance in an activate
// result, best effort.
func (h *Helper) fillActiveStates(ctx context.Context, svc string, result *ActivateResult) {
	names := make([]string, 0, len(result.Instances))
	for _, in := range result.Instances {
		names = append(names, hostsetup.InstanceUnitName(svc, in.Name))
	}
	states, err := h.showUnits(ctx, names)
	if err != nil {
		return
	}
	for i, in := range result.Instances {
		result.Instances[i].ActiveState = states[hostsetup.InstanceUnitName(svc, in.Name)].ActiveState
	}
}

// showUnits reads the state of several units in one systemctl show call,
// keyed by unit name.
func (h *Helper) showUnits(ctx context.Context, names []string) (map[string]UnitStatus, error) {
	args := append([]string{systemctl, "show", "--property=Id,LoadState,ActiveState,SubState,UnitFileState,MainPID"}, names...)
	res, err := h.run(ctx, systemctlTimeout, args...)
	if err != nil {
		return nil, errors.New(commandFailure("systemctl show", res.Output, err))
	}
	out := map[string]UnitStatus{}
	for block := range strings.SplitSeq(strings.TrimSpace(res.Output), "\n\n") {
		var id string
		var st UnitStatus
		for line := range strings.SplitSeq(strings.TrimSpace(block), "\n") {
			key, value, _ := strings.Cut(line, "=")
			if key == "Id" {
				if !slices.Contains(names, value) {
					return nil, errors.New("systemctl show printed an unexpected value")
				}
				id = value
				continue
			}
			if !unitValue.MatchString(value) {
				return nil, errors.New("systemctl show printed an unexpected value")
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
					return nil, errors.New("systemctl show printed an unexpected value")
				}
			default:
				return nil, errors.New("systemctl show printed an unexpected value")
			}
		}
		if id == "" {
			return nil, errors.New("systemctl show printed a unit without its name")
		}
		out[id] = st
	}
	if len(out) != len(names) {
		return nil, errors.New("systemctl show did not report every unit")
	}
	return out, nil
}

// grantedUnits returns the granted instances that have a unit, as unit
// names by instance.
func grantedUnits(svc string, g hostsetup.ServiceGrant, units helperUnits) []string {
	var out []string
	for _, in := range units.instances {
		if _, ok := g.Instances[in]; ok {
			out = append(out, hostsetup.InstanceUnitName(svc, in))
		}
	}
	return out
}

// instanceAction starts, stops or restarts the units of a service granted
// instances. Without an instance, start and restart act on the enabled
// instances (an instance an operator stopped stays stopped) and stop on
// every unit the helper wrote for the service. With one, start and restart
// enable it first, and stop disables it, which is how it stays stopped;
// the last enabled instance cannot be stopped that way.
func (h *Helper) instanceAction(ctx context.Context, req Request, g hostsetup.ServiceGrant) Response {
	resp, svc := reply(req), req.Service
	units, e := h.instanceConflict(svc, g)
	if e != nil {
		return respond(resp, e)
	}
	granted := grantedUnits(svc, g, units)
	run := func(args ...string) *Response {
		if res, err := h.run(ctx, systemctlTimeout, append([]string{systemctl}, args...)...); err != nil {
			resp.Log = sanitizeLog(res.Output)
			r := refuse(resp, CodeFailed, "%s", commandFailure("systemctl "+args[0], res.Output, err))
			return &r
		}
		return nil
	}
	if req.Instance == "" {
		if req.Op == OpStop {
			all := units.all(svc)
			if len(all) == 0 {
				return refuse(resp, CodeNotFound, "%s has no unit yet; deploy the service first", svc)
			}
			if r := run(append([]string{"stop"}, all...)...); r != nil {
				return *r
			}
			return resp
		}
		if len(granted) == 0 {
			return refuse(resp, CodeNotFound, "%s has no instance unit yet; deploy the service first", svc)
		}
		enabled, err := h.enabledUnits(ctx, granted)
		if err != nil {
			return refuse(resp, CodeFailed, "%v", err)
		}
		var act []string
		for _, n := range granted {
			if enabled[n] {
				act = append(act, n)
			}
		}
		if len(act) == 0 {
			return refuse(resp, CodeRefused, "no instance of %s is enabled; start one with its instance named", svc)
		}
		if r := run(append([]string{string(req.Op)}, act...)...); r != nil {
			return *r
		}
		return resp
	}
	if _, ok := g.Instances[req.Instance]; !ok {
		return refuse(resp, CodeRefused, "the instance %s is not granted to %s in %s", req.Instance, svc, hostsetup.ConfigPath)
	}
	unit := hostsetup.InstanceUnitName(svc, req.Instance)
	if !slices.Contains(units.instances, req.Instance) {
		return refuse(resp, CodeNotFound, "the instance %s of %s has no unit yet; deploy the service first", req.Instance, svc)
	}
	switch req.Op {
	case OpStart:
		if r := run("enable", "--now", "--quiet", unit); r != nil {
			return *r
		}
	case OpRestart:
		if r := run("enable", "--quiet", unit); r != nil {
			return *r
		}
		if r := run("restart", unit); r != nil {
			return *r
		}
	case OpStop:
		enabled, err := h.enabledUnits(ctx, granted)
		if err != nil {
			return refuse(resp, CodeFailed, "%v", err)
		}
		others := 0
		for _, n := range granted {
			if enabled[n] && n != unit {
				others++
			}
		}
		if enabled[unit] && others == 0 {
			return refuse(resp, CodeRefused, "%s is the last enabled instance of %s; stop the service instead", req.Instance, svc)
		}
		if r := run("disable", "--now", "--quiet", unit); r != nil {
			return *r
		}
	}
	return resp
}

// instanceStatus reports the units of a service granted instances: the
// aggregate of the enabled granted instances as unit, and each instance,
// or with an instance named only that one's unit.
func (h *Helper) instanceStatus(ctx context.Context, req Request, g hostsetup.ServiceGrant) Response {
	resp, svc := reply(req), req.Service
	units, e := h.instanceConflict(svc, g)
	if e != nil {
		return respond(resp, e)
	}
	release, err := h.linkTarget(hostsetup.ServiceOptDir(svc) + "/current")
	if err != nil {
		return refuse(resp, CodeFailed, "%v", err)
	}
	if req.Instance != "" {
		if _, ok := g.Instances[req.Instance]; !ok {
			return refuse(resp, CodeRefused, "the instance %s is not granted to %s in %s", req.Instance, svc, hostsetup.ConfigPath)
		}
		if !slices.Contains(units.instances, req.Instance) {
			return refuse(resp, CodeNotFound, "the instance %s of %s has no unit yet; deploy the service first", req.Instance, svc)
		}
		unit := hostsetup.InstanceUnitName(svc, req.Instance)
		states, err := h.showUnits(ctx, []string{unit})
		if err != nil {
			return refuse(resp, CodeFailed, "%v", err)
		}
		st := states[unit]
		st.Release = release
		resp.Unit = &st
		return resp
	}
	if len(units.instances) == 0 {
		return refuse(resp, CodeNotFound, "%s has no instance unit yet; deploy the service first", svc)
	}
	var names []string
	for _, in := range units.instances {
		names = append(names, hostsetup.InstanceUnitName(svc, in))
	}
	states, err := h.showUnits(ctx, names)
	if err != nil {
		return refuse(resp, CodeFailed, "%v", err)
	}
	list := []InstanceStatus{}
	agg := UnitStatus{LoadState: "loaded", Release: release}
	var enabled []string
	for _, in := range slices.Sorted(maps.Keys(g.Instances)) {
		st, ok := states[hostsetup.InstanceUnitName(svc, in)]
		if !ok {
			list = append(list, InstanceStatus{Name: in, Port: g.Instances[in].Port, LoadState: "not-found", ActiveState: "inactive", SubState: "dead", Granted: true})
			continue
		}
		list = append(list, InstanceStatus{Name: in, Port: g.Instances[in].Port, LoadState: st.LoadState, ActiveState: st.ActiveState,
			SubState: st.SubState, UnitFileState: st.UnitFileState, MainPID: st.MainPID, Granted: true})
		if st.UnitFileState == "enabled" {
			enabled = append(enabled, st.ActiveState)
		}
	}
	for _, in := range units.instances {
		if _, ok := g.Instances[in]; ok {
			continue
		}
		st := states[hostsetup.InstanceUnitName(svc, in)]
		list = append(list, InstanceStatus{Name: in, LoadState: st.LoadState, ActiveState: st.ActiveState,
			SubState: st.SubState, UnitFileState: st.UnitFileState, MainPID: st.MainPID})
	}
	agg.ActiveState, agg.SubState = aggregateState(enabled), "instances"
	agg.UnitFileState = "enabled"
	if len(enabled) == 0 {
		agg.UnitFileState = "disabled"
	}
	resp.Unit, resp.Instances = &agg, list
	return resp
}

// aggregateState is the state of a service from its enabled instances:
// active when all are, failed when one is, inactive when all are (or none
// is enabled), and activating, which LCM reads as unknown, otherwise.
func aggregateState(states []string) string {
	switch {
	case len(states) == 0:
		return "inactive"
	case slices.Contains(states, "failed"):
		return "failed"
	}
	all := func(want ...string) bool {
		for _, s := range states {
			if !slices.Contains(want, s) {
				return false
			}
		}
		return true
	}
	switch {
	case all("active", "reloading"):
		return "active"
	case all("inactive"):
		return "inactive"
	}
	return "activating"
}

// serviceUnits returns the units the helper wrote for a service, its single
// unit or those of its instances, for the operations that stop it for its
// data.
func (h *Helper) serviceUnits(svc string, g hostsetup.ServiceGrant) ([]string, *opErr) {
	if !g.MultiInstance() {
		conflict, err := hostsetup.UnitConflict(h.FS, svc, h.RootUID)
		switch {
		case err != nil:
			return nil, failf(CodeFailed, "%v", err)
		case conflict != "":
			return nil, failf(CodeRefused, "%s", conflict)
		}
		if _, err := h.FS.Lstat(hostsetup.ServiceUnit(svc)); errors.Is(err, fs.ErrNotExist) {
			return nil, nil
		} else if err != nil {
			return nil, failf(CodeFailed, "%v", err)
		}
		return []string{unitName(svc)}, nil
	}
	units, e := h.instanceConflict(svc, g)
	if e != nil {
		return nil, e
	}
	return units.all(svc), nil
}
