package catalog

import (
	"context"
	"fmt"
	"maps"
	"slices"
	"strings"

	"github.com/ACaccel/lcm/internal/audit"
	"github.com/ACaccel/lcm/internal/model"
	"github.com/ACaccel/lcm/internal/resource"
)

// ActionInfo describes a service lifecycle action for the impact guard.
func ActionInfo(action string) resource.Action {
	return resource.Action{Name: action, Risk: actionRisks[action], Policy: true, Kind: resource.KindAction}
}

// CheckImpact applies the resource impact guard to a lifecycle action on
// the service name: a disruptive action on a service other managed
// resources depend on, or are controlled through, needs acknowledged set.
func (m *Manager) CheckImpact(ctx context.Context, name, action string, acknowledged bool) (resource.Impact, error) {
	return m.cat.res.CheckImpact(ctx, name, ActionInfo(action), acknowledged)
}

// Do runs one lifecycle action by name.
func (m *Manager) Do(ctx context.Context, name, action string, opts ActionOptions) (ActionResult, error) {
	switch action {
	case ActionDeploy:
		return m.Deploy(ctx, name, opts)
	case ActionRollback:
		return m.Rollback(ctx, name, opts)
	case ActionStart:
		return m.Start(ctx, name, opts)
	case ActionStop:
		return m.Stop(ctx, name, opts)
	case ActionRestart:
		return m.Restart(ctx, name, opts)
	case ActionBackup:
		return m.Backup(ctx, name, opts)
	case ActionRunJob:
		return m.RunJob(ctx, name, opts)
	}
	return ActionResult{}, fmt.Errorf("%w: unknown action %q", ErrPrecondition, action)
}

// GroupRequest selects managed services by labels and names one lifecycle
// action to run on all of them.
type GroupRequest struct {
	Labels map[string]string
	// Names, when not empty, narrows the services the labels match to these;
	// each must be a managed service that matches the labels. Without
	// labels, the names alone select the group.
	Names  []string
	Action string
	// Options carries Version (deploy) and Job (run-job) for every step.
	Options           ActionOptions
	AcknowledgeImpact bool
}

// GroupStep is one service of a group action, in execution order.
type GroupStep struct {
	Resource string
	Plan     Plan
	Impact   resource.Impact
	// AcknowledgementRequired reports that the step's impact needs
	// AcknowledgeImpact.
	AcknowledgementRequired bool
	// PlanErr explains why the step could not run as things stand.
	PlanErr error
}

// Group is a planned group action.
type Group struct {
	Request GroupRequest
	Steps   []GroupStep
}

// ErrEmptyGroup refuses a group action whose selector matches no managed
// service.
var ErrEmptyGroup = precondition("catalog: no managed service matches the selector")

// PlanGroup selects the services of a group action and orders them: the
// services a service depends on come first, except for stop, which stops
// dependents before what they depend on. Every step is planned and its
// impact computed; the result is the dry run of the group. Only lifecycle
// actions are accepted: machine and router operations are never run in
// groups.
func (m *Manager) PlanGroup(ctx context.Context, req GroupRequest) (Group, error) {
	if !slices.Contains(Actions, req.Action) {
		return Group{}, fmt.Errorf("%w: group actions run lifecycle actions only, not %q", ErrPrecondition, req.Action)
	}
	recs, err := m.cat.res.List(ctx, resource.Filter{Type: model.ResourceService, Labels: req.Labels})
	if err != nil {
		return Group{}, err
	}
	if len(req.Names) > 0 {
		if recs, err = narrowToNames(recs, req.Names); err != nil {
			return Group{}, err
		}
	}
	if len(recs) == 0 {
		return Group{}, ErrEmptyGroup
	}
	names := make([]string, len(recs))
	for i, r := range recs {
		names[i] = r.Name
	}
	order, err := m.cat.res.DependencyOrder(ctx, names)
	if err != nil {
		return Group{}, err
	}
	if req.Action == ActionStop {
		slices.Reverse(order)
	}
	info := ActionInfo(req.Action)
	g := Group{Request: req, Steps: make([]GroupStep, len(order))}
	for i, name := range order {
		st := GroupStep{Resource: name}
		st.Plan, st.PlanErr = m.Plan(ctx, name, req.Action, req.Options)
		st.Impact, err = m.cat.res.Impact(ctx, name)
		if err != nil {
			return Group{}, err
		}
		st.AcknowledgementRequired = resource.RequiresAcknowledgement(info, st.Impact)
		g.Steps[i] = st
	}
	return g, nil
}

// narrowToNames keeps the records named in names. A name that no record
// carries is refused rather than ignored, so a stale selection never runs a
// smaller group than the one that was reviewed.
func narrowToNames(recs []resource.Record, names []string) ([]resource.Record, error) {
	byName := make(map[string]resource.Record, len(recs))
	for _, r := range recs {
		byName[r.Name] = r
	}
	out := make([]resource.Record, 0, len(names))
	seen := make(map[string]bool, len(names))
	for _, n := range names {
		if seen[n] {
			continue
		}
		seen[n] = true
		r, ok := byName[n]
		if !ok {
			return nil, precondition(fmt.Sprintf("catalog: %q is not a managed service matching the selector", n))
		}
		out = append(out, r)
	}
	return out, nil
}

// Check refuses to start the group when a step cannot run as planned or
// needs an impact acknowledgement the request did not give. It returns the
// first offending step's error, so nothing runs unless every step could.
func (g Group) Check() error {
	for _, st := range g.Steps {
		if st.PlanErr != nil {
			return &GroupStepError{Resource: st.Resource, Err: st.PlanErr}
		}
	}
	if g.Request.AcknowledgeImpact {
		return nil
	}
	for _, st := range g.Steps {
		if st.AcknowledgementRequired {
			return &resource.ImpactError{Action: g.Request.Action, Impact: st.Impact}
		}
	}
	return nil
}

// GroupStepError names the step of a group that cannot run; it unwraps to
// the step's own error, so callers map it like a single action's.
type GroupStepError struct {
	Resource string
	Err      error
}

func (e *GroupStepError) Error() string {
	return "catalog: group step " + e.Resource + " cannot run: " + strings.TrimPrefix(e.Err.Error(), "catalog: ")
}

func (e *GroupStepError) Unwrap() error { return e.Err }

// GroupObserver follows the steps of a running group. Started is called
// before a step runs and Finished after it; both get the step's index.
type GroupObserver struct {
	Started  func(i int)
	Finished func(i int, res ActionResult, err error)
}

// RunGroup runs the steps of a checked group one after another and stops at
// the first failure; the steps after it are not run. The impact guard is
// applied again before each step. Every action records
// its own audit entry (with the step's acknowledged impact); the group adds
// one entry of its own. It returns the index of the failed step, or -1.
func (m *Manager) RunGroup(ctx context.Context, g Group, obs GroupObserver) int {
	failed := -1
	var failure error
	// The steps get the impact current when they run; the caller's plan is
	// left as it was.
	g.Steps = slices.Clone(g.Steps)
	for i, st := range g.Steps {
		if obs.Started != nil {
			obs.Started(i)
		}
		// Relations may have changed since the group was checked, and the
		// steps before this one take time: a step whose impact now needs
		// an acknowledgement the request did not give fails the group.
		imp, err := m.CheckImpact(ctx, st.Resource, g.Request.Action, g.Request.AcknowledgeImpact)
		var res ActionResult
		if err == nil {
			g.Steps[i].Impact = imp
			sctx := ctx
			if g.Request.AcknowledgeImpact {
				sctx = resource.WithAcknowledgedImpact(ctx, imp)
			}
			res, err = m.doRecovered(sctx, st.Resource, g.Request.Action, g.Request.Options)
		}
		if obs.Finished != nil {
			obs.Finished(i, res, err)
		}
		if err != nil {
			failed, failure = i, err
			break
		}
	}
	m.recordGroup(ctx, g, failed, failure)
	return failed
}

// doRecovered runs one step and turns a panic in a runtime adapter into a
// failure of the step, so the group still ends and reports.
func (m *Manager) doRecovered(ctx context.Context, name, action string, opts ActionOptions) (res ActionResult, err error) {
	defer func() {
		if p := recover(); p != nil {
			m.log.Error("panic in group action", "action", action, "resource", name, "panic", p)
			err = fmt.Errorf("internal error while running %s", action)
		}
	}()
	return m.Do(ctx, name, action, opts)
}

func (m *Manager) recordGroup(ctx context.Context, g Group, failed int, failure error) {
	ctx, cancel := detached(ctx)
	defer cancel()
	order := make([]string, len(g.Steps))
	acknowledged := map[string]map[string][]string{}
	for i, st := range g.Steps {
		order[i] = st.Resource
		if g.Request.AcknowledgeImpact && !st.Impact.Empty() {
			acknowledged[st.Resource] = st.Impact.Names()
		}
	}
	details := map[string]any{"action": g.Request.Action, "labels": g.Request.Labels, "order": order}
	if len(g.Request.Names) > 0 {
		details["names"] = g.Request.Names
	}
	if g.Request.Options.Version != "" {
		details["version"] = g.Request.Options.Version
	}
	if g.Request.Options.Job != "" {
		details["job"] = g.Request.Options.Job
	}
	if len(acknowledged) > 0 {
		details["acknowledgedImpact"] = acknowledged
	}
	outcome := audit.OutcomeOK
	if failed >= 0 {
		outcome = audit.OutcomeError
		details["failed"] = g.Steps[failed].Resource
		details["notRun"] = order[failed+1:]
		details["error"] = headCap(failure.Error(), 1<<10)
	}
	if err := m.cat.audit.Record(ctx, "service.group-action", selectorTarget(g.Request.Labels, g.Request.Names), outcome, details); err != nil {
		m.log.Error("write audit entry failed", "action", "service.group-action", "error", err)
	}
}

// selectorTarget names a group in the audit log by its selector.
func selectorTarget(labels map[string]string, names []string) string {
	parts := make([]string, 0, len(labels)+len(names))
	for _, k := range slices.Sorted(maps.Keys(labels)) {
		parts = append(parts, k+"="+labels[k])
	}
	parts = append(parts, slices.Sorted(slices.Values(names))...)
	return "services:" + strings.Join(parts, ",")
}
