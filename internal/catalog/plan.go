package catalog

import (
	"context"
	"fmt"

	"github.com/ACaccel/lcm/internal/message"
	"github.com/ACaccel/lcm/internal/model"
	rt "github.com/ACaccel/lcm/internal/runtime"
)

// Plan describes what an action would do, without doing it.
type Plan struct {
	Resource string   `json:"resource"`
	Action   string   `json:"action"`
	From     State    `json:"from"`
	To       State    `json:"to"`
	Steps    []string `json:"steps"`
	// StepMessages are Steps with codes for translation, in the same order.
	StepMessages []message.Message `json:"stepMessages"`
}

func (p *Plan) add(ms ...message.Message) {
	for _, m := range ms {
		p.Steps = append(p.Steps, m.Message)
		p.StepMessages = append(p.StepMessages, m)
	}
}

// Plan checks that the action is allowed and returns its steps. It takes
// no lock and changes nothing, which makes it the dry-run of an action.
func (m *Manager) Plan(ctx context.Context, name, action string, opts ActionOptions) (Plan, error) {
	rec, err := m.cat.Get(ctx, name)
	if err != nil {
		return Plan{}, err
	}
	if err := checkAction(action, rec); err != nil {
		return Plan{}, err
	}
	cfg := rec.Config
	p := Plan{Resource: name, Action: action, From: rec.State, To: rec.State}
	p.add(message.New("step.lock", "lock "+LockName(name), "lock", LockName(name)))
	target := runtimeStep(rec.Runtime())

	switch action {
	case ActionStart:
		p.To = StateRunning
		p.add(target("step.start", "start "))
	case ActionStop:
		p.To = StateStopped
		p.add(target("step.stop", "stop "))
	case ActionRestart:
		p.To = StateRunning
		p.add(target("step.restartOrStart", "restart (or start when inactive) "))
	case ActionBackup:
		if cfg.Backup == nil {
			return Plan{}, ErrNoBackup
		}
		p.add(message.New("step.backup", "run backup command: "+cfg.Backup.Command, "command", cfg.Backup.Command))
	case ActionRunJob:
		job, ok := cfg.Jobs[opts.Job]
		if !ok {
			return Plan{}, fmt.Errorf("%w: %q", ErrUnknownJob, opts.Job)
		}
		p.add(message.New("step.job", fmt.Sprintf("run job %s: %s", opts.Job, job.Command), "job", opts.Job, "command", job.Command))
	case ActionDeploy, ActionRollback:
		steps, err := m.deploySteps(ctx, rec, action, opts.Version)
		if err != nil {
			return Plan{}, err
		}
		p.add(steps...)
		p.To = StateRunning
	default:
		return Plan{}, fmt.Errorf("catalog: unknown action %q", action)
	}
	if p.To != p.From {
		p.add(message.New("step.setState", fmt.Sprintf("set state %s", p.To), "state", string(p.To)))
	}
	return p, nil
}

func (m *Manager) deploySteps(ctx context.Context, rec *Record, action, version string) ([]message.Message, error) {
	src := rec.Config.Source
	if src == nil {
		return nil, ErrNoSource
	}
	if version != "" && !shaRe.MatchString(version) {
		return nil, ErrInvalidVersion
	}
	steps := []message.Message{message.New("step.refuseDirty", "refuse if tracked files in "+src.Repo+" have changes", "repo", src.Repo)}
	transient := StateDeploying
	switch {
	case action == ActionRollback:
		transient = StateRollingBack
	case rec.State == StateRunning:
		transient = StateUpdating
	}
	steps = append(steps, message.New("step.recordDeployment", fmt.Sprintf("set state %s and record the deployment", transient), "state", string(transient)))
	remote := src.EffectiveRemote()
	fetch := message.New("step.fetch", "git fetch --prune "+remote, "remote", remote)
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
		steps = append(steps, message.New("step.rollbackTarget", "resolve rollback target "+shortSHA(v), "commit", shortSHA(v)))
	case version != "":
		steps = append(steps, fetch, message.New("step.resolveCommit", "resolve "+shortSHA(version), "commit", shortSHA(version)))
	default:
		steps = append(steps, fetch,
			message.New("step.resolveHead", fmt.Sprintf("resolve head of %s/%s", remote, src.Branch), "remote", remote, "branch", src.Branch))
	}
	steps = append(steps, message.New("step.checkout", "git checkout --detach <commit>"))
	if src.Build != "" {
		steps = append(steps, message.New("step.build", "build: "+src.Build, "command", src.Build))
	}
	if src.Install != "" {
		steps = append(steps, message.New("step.install", "install: "+src.Install, "command", src.Install))
	}
	if !rt.CapabilitiesOf(rec.Runtime()).Restart {
		return append(steps, message.New("step.skipRestart", "skip restart: the runtime cannot restart this service, so the install step must apply the new version")), nil
	}
	return append(steps, runtimeStep(rec.Runtime())("step.restart", "restart ")), nil
}

// runtimeStep returns a constructor of steps that act on the runtime r. The
// parameters name the runtime type, the unit, container or project, and
// the systemd scope.
func runtimeStep(r model.Runtime) func(code, verb string) message.Message {
	var text, name, scope string
	switch r.Type {
	case model.RuntimeSystemd:
		name, scope = r.Unit, r.EffectiveScope()
		text = fmt.Sprintf("systemd unit %s (%s scope)", r.Unit, scope)
	case model.RuntimeDocker:
		name = r.Container
		text = "docker container " + r.Container
	case model.RuntimeCompose:
		name = r.Project
		text = "compose project " + r.Project
	case model.RuntimeCommand:
		text = "service via configured commands"
	default:
		text = string(r.Type) + " service"
	}
	return func(code, verb string) message.Message {
		return message.New(code, verb+text, "runtime", string(r.Type), "name", name, "scope", scope)
	}
}
