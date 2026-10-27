package catalog

import (
	"context"
	"fmt"

	"github.com/ACaccel/lcm/internal/model"
)

// Plan describes what an action would do, without doing it.
type Plan struct {
	Service string   `json:"service"`
	Action  string   `json:"action"`
	From    State    `json:"from"`
	To      State    `json:"to"`
	Steps   []string `json:"steps"`
}

// Plan checks that the action is allowed and returns its steps. It takes
// no lock and changes nothing, which makes it the dry-run of an action.
func (m *Manager) Plan(ctx context.Context, name, action string, opts ActionOptions) (Plan, error) {
	rec, err := m.cat.Get(ctx, name)
	if err != nil {
		return Plan{}, err
	}
	if !CanAct(action, rec.State) {
		return Plan{}, &StateError{Service: name, Action: action, State: rec.State}
	}
	svc := rec.Spec
	p := Plan{Service: name, Action: action, From: rec.State, To: rec.State}
	p.Steps = append(p.Steps, "lock "+LockName(name))
	target := describeRuntime(svc.Spec.Runtime)

	switch action {
	case ActionStart:
		p.To = StateRunning
		p.Steps = append(p.Steps, "start "+target)
	case ActionStop:
		p.To = StateStopped
		p.Steps = append(p.Steps, "stop "+target)
	case ActionRestart:
		p.To = StateRunning
		p.Steps = append(p.Steps, "restart (or start when inactive) "+target)
	case ActionRetire:
		p.To = StateRetired
		if rec.State == StateRunning || rec.State == StateFailed {
			p.Steps = append(p.Steps, "stop "+target)
		}
	case ActionBackup:
		if svc.Spec.Backup == nil {
			return Plan{}, ErrNoBackup
		}
		p.Steps = append(p.Steps, "run backup command: "+svc.Spec.Backup.Command)
	case ActionRunJob:
		job, ok := svc.Spec.Jobs[opts.Job]
		if !ok {
			return Plan{}, fmt.Errorf("%w: %q", ErrUnknownJob, opts.Job)
		}
		p.Steps = append(p.Steps, fmt.Sprintf("run job %s: %s", opts.Job, job.Command))
	case ActionDeploy, ActionRollback:
		steps, err := m.deploySteps(ctx, rec, action, opts.Version)
		if err != nil {
			return Plan{}, err
		}
		p.Steps = append(p.Steps, steps...)
		p.To = StateRunning
	default:
		return Plan{}, fmt.Errorf("catalog: unknown action %q", action)
	}
	if p.To != p.From {
		p.Steps = append(p.Steps, fmt.Sprintf("set state %s", p.To))
	}
	return p, nil
}

func (m *Manager) deploySteps(ctx context.Context, rec *Record, action, version string) ([]string, error) {
	src := rec.Spec.Spec.Source
	if src == nil {
		return nil, ErrNoSource
	}
	if version != "" && !shaRe.MatchString(version) {
		return nil, ErrInvalidVersion
	}
	steps := []string{"refuse if tracked files in " + src.Repo + " have changes"}
	transient := StateDeploying
	switch {
	case action == ActionRollback:
		transient = StateRollingBack
	case rec.State == StateRunning:
		transient = StateUpdating
	}
	steps = append(steps, fmt.Sprintf("set state %s and record the deployment", transient))
	switch {
	case action == ActionRollback:
		deps, err := m.cat.Deployments(ctx, rec.Name, 100, 0)
		if err != nil {
			return nil, err
		}
		v, ok := rollbackTarget(deps)
		if !ok {
			return nil, ErrNoRollbackTarget
		}
		steps = append(steps, "resolve rollback target "+shortSHA(v))
	case version != "":
		steps = append(steps, "git fetch --prune "+src.EffectiveRemote(), "resolve "+shortSHA(version))
	default:
		steps = append(steps, "git fetch --prune "+src.EffectiveRemote(),
			fmt.Sprintf("resolve head of %s/%s", src.EffectiveRemote(), src.Branch))
	}
	steps = append(steps, "git checkout --detach <commit>")
	if src.Build != "" {
		steps = append(steps, "build: "+src.Build)
	}
	if src.Install != "" {
		steps = append(steps, "install: "+src.Install)
	}
	return append(steps, "restart "+describeRuntime(rec.Spec.Spec.Runtime)), nil
}

func describeRuntime(r model.Runtime) string {
	switch r.Type {
	case model.RuntimeSystemd:
		return fmt.Sprintf("systemd unit %s (%s scope)", r.Unit, r.EffectiveScope())
	case model.RuntimeDocker:
		return "docker container " + r.Container
	case model.RuntimeCompose:
		return fmt.Sprintf("compose project %s", r.Project)
	case model.RuntimeCommand:
		return "service via configured commands"
	}
	return string(r.Type) + " service"
}
