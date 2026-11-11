package catalog

import (
	"context"
	"fmt"
	"slices"
	"time"

	"github.com/ACaccel/lcm/internal/deployhelper"
	"github.com/ACaccel/lcm/internal/model"
	"github.com/ACaccel/lcm/internal/resource"
	rt "github.com/ACaccel/lcm/internal/runtime"
)

// A managed service with instances has one lifecycle state; each
// instance's desired state is its unit's enablement on the host, which an
// instance stop turns off and an instance start or restart turns on, so
// that an instance an operator stopped stays stopped across deployments
// and service-wide actions. The observed state of the instances is kept in
// memory, refreshed by reconciliation and after every instance action.

// ReasonLastInstance: stopping the instance would stop the last enabled
// one; stop the service instead.
const ReasonLastInstance = "lastInstance"

// ReasonInstanceRunning: the instance is enabled and active, so there is
// nothing to start; restart it instead.
const ReasonInstanceRunning = "instanceRunning"

// ErrUnknownInstance refuses an instance the service's document does not
// name.
var ErrUnknownInstance = precondition("catalog: the service has no such instance")

// ErrLastInstance refuses to stop the last enabled instance by name.
var ErrLastInstance = precondition("catalog: this is the last running instance of the service; stop the service instead")

// InstanceSnapshot is the last observed state of a service's instances.
type InstanceSnapshot struct {
	At        time.Time
	Instances []rt.InstanceStatus
}

// Instance returns the observed state of one instance.
func (s InstanceSnapshot) Instance(name string) (rt.InstanceStatus, bool) {
	i := slices.IndexFunc(s.Instances, func(in rt.InstanceStatus) bool { return in.Name == name })
	if i < 0 {
		return rt.InstanceStatus{}, false
	}
	return s.Instances[i], true
}

// Instances returns the last observed state of the instances of service
// name, if there is one.
func (m *Manager) Instances(name string) (InstanceSnapshot, bool) {
	m.instMu.Lock()
	defer m.instMu.Unlock()
	s, ok := m.instances[name]
	return s, ok
}

// StoppedInstances returns the instances of service name that the last
// observation found with a disabled unit: an operator stopped them, so
// their checks are paused. An instance without a unit yet (not deployed)
// is not stopped: its checks run and report it down.
func (m *Manager) StoppedInstances(name string) map[string]bool {
	snap, ok := m.Instances(name)
	if !ok {
		return nil
	}
	out := map[string]bool{}
	for _, in := range snap.Instances {
		if in.Granted && in.Disabled {
			out[in.Name] = true
		}
	}
	return out
}

// noteInstances records an observation of a service's instances and tells
// OnInstances when the set of stopped instances changed.
func (m *Manager) noteInstances(name string, st rt.Status) {
	if st.Instances == nil {
		return
	}
	before := m.StoppedInstances(name)
	m.instMu.Lock()
	m.instances[name] = InstanceSnapshot{At: m.cat.now(), Instances: slices.Clone(st.Instances)}
	m.instMu.Unlock()
	after := m.StoppedInstances(name)
	if m.onInstances != nil && !sameSet(before, after) {
		m.onInstances(name)
	}
}

func sameSet(a, b map[string]bool) bool {
	if len(a) != len(b) {
		return false
	}
	for k := range a {
		if !b[k] {
			return false
		}
	}
	return true
}

// forgetInstances drops the observations of a service.
func (m *Manager) forgetInstances(name string) {
	m.instMu.Lock()
	delete(m.instances, name)
	m.instMu.Unlock()
}

// CanActInstance reports whether an instance action may start in state:
// the service runs, or failed and the instance may be what brings it back.
// A stopped service is started as a whole.
func CanActInstance(state State) bool { return state == StateRunning || state == StateFailed }

// isInstanceAction reports whether action may name an instance.
func isInstanceAction(action string) bool {
	return action == ActionStart || action == ActionStop || action == ActionRestart
}

// checkInstanceAction refuses an instance action that the phase, the
// document, the state or the helper does not allow.
func (m *Manager) checkInstanceAction(action, instance string, rec *Record) error {
	if rec.Phase != resource.PhaseManaged {
		return ErrNotManaged
	}
	if !isInstanceAction(action) {
		return precondition(fmt.Sprintf("catalog: %s does not take an instance", action))
	}
	if _, ok := rec.Config.Instances[instance]; !ok {
		return fmt.Errorf("%w: %s has no instance %q", ErrUnknownInstance, rec.Name, instance)
	}
	if !CanActInstance(rec.State) {
		return &StateError{Resource: rec.Name, Action: action, State: rec.State}
	}
	if err := checkHelper(action, rec.Runtime(), m.helperReady()); err != nil {
		return err
	}
	if action == ActionStop && m.lastEnabled(rec.Name, instance) {
		return ErrLastInstance
	}
	return nil
}

// lastEnabled reports whether the last observation found instance the only
// enabled instance of service name.
func (m *Manager) lastEnabled(name, instance string) bool {
	snap, ok := m.Instances(name)
	if !ok {
		return false
	}
	self, ok := snap.Instance(instance)
	if !ok || !self.Enabled {
		return false
	}
	for _, in := range snap.Instances {
		if in.Granted && in.Enabled && in.Name != instance {
			return false
		}
	}
	return true
}

// instanceAction acts on one instance and settles the service's state from
// the aggregate the helper reports afterwards: running when the enabled
// instances are active, failed when one failed, and unchanged otherwise.
func (s *session) instanceAction(ctx context.Context, action string, res *ActionResult) error {
	res.Instance = s.opts.Instance
	ic, ok := s.adapter.(rt.InstanceController)
	if !ok {
		return fmt.Errorf("%w: %s of an instance on a %s runtime", ErrUnsupported, action, s.rt.Type)
	}
	err := ic.InstanceAction(ctx, s.rt, action, s.opts.Instance)
	sctx, cancel := detached(ctx)
	defer cancel()
	st, serr := s.adapter.Status(sctx, s.rt)
	if serr == nil {
		s.m.noteInstances(s.name, st)
	}
	if err != nil && refusedByHelper(err) {
		// Refused before anything changed, such as the last enabled
		// instance: the service's state stays.
		return err
	}
	to := s.cur
	switch {
	case serr != nil:
	case st.State == rt.StateActive:
		to = StateRunning
	case st.State == rt.StateFailed || st.State == rt.StateInactive:
		to = StateFailed
	}
	if to != s.cur {
		if merr := s.moveDone(ctx, to); merr != nil && err == nil {
			return merr
		}
	}
	return err
}

// InstanceActions reports for start, stop and restart whether they can
// start on one instance of rec now, from the helper's readiness, the last
// observation and the busy mark.
func (m *Manager) InstanceActions(rec *Record, instance string) []InstanceAvailability {
	var snap *InstanceSnapshot
	if s, ok := m.Instances(rec.Name); ok {
		snap = &s
	}
	out := AvailableInstanceActions(rec, m.helperReady(), instance, snap)
	if m.isBusy(rec.Name) {
		for i := range out {
			if out[i].Allowed {
				out[i].Allowed, out[i].Reason = false, ReasonBusy
			}
		}
	}
	return out
}

// InstanceAvailability tells whether an action can start on one instance.
type InstanceAvailability struct {
	Action  string
	Allowed bool
	Reason  string
}

// AvailableInstanceActions reports for start, stop and restart whether
// they can start on instance of rec now; helper is nil when the deploy
// helper can be called, and snap is the last observation of the
// instances, if any.
func AvailableInstanceActions(rec *Record, helper error, instance string, snap *InstanceSnapshot) []InstanceAvailability {
	var out []InstanceAvailability
	for _, a := range []string{ActionStart, ActionStop, ActionRestart} {
		reason := ""
		switch {
		case rec.Phase != resource.PhaseManaged:
			reason = ReasonNotManaged
		case checkHelper(a, rec.Runtime(), helper) != nil:
			reason = ReasonHelperUnavailable
		case !CanActInstance(rec.State):
			reason = ReasonInvalidState
		case a == ActionStart && snap != nil && runningIn(*snap, instance):
			reason = ReasonInstanceRunning
		case a == ActionStop && snap != nil && lastEnabledIn(*snap, instance):
			reason = ReasonLastInstance
		}
		out = append(out, InstanceAvailability{Action: a, Allowed: reason == "", Reason: reason})
	}
	return out
}

// runningIn reports whether the observation found instance enabled and
// active.
func runningIn(snap InstanceSnapshot, instance string) bool {
	in, ok := snap.Instance(instance)
	return ok && in.Enabled && in.State == rt.StateActive
}

func lastEnabledIn(snap InstanceSnapshot, instance string) bool {
	self, ok := snap.Instance(instance)
	if !ok || !self.Enabled {
		return false
	}
	for _, in := range snap.Instances {
		if in.Granted && in.Enabled && in.Name != instance {
			return false
		}
	}
	return true
}

// instanceRequests resolves the instances of a document for an activate
// request.
func instanceRequests(cfg *model.ServiceConfig) []deployhelper.InstanceSpec {
	var out []deployhelper.InstanceSpec
	for _, in := range cfg.ResolvedInstances() {
		out = append(out, deployhelper.InstanceSpec{
			Name: in.Name, Start: append([]string{in.Start}, in.StartArgs...), Limits: limitsRequest(in.Limits),
		})
	}
	return out
}
