// Package lifecycle provides the policy nodes that act on service
// resources: lifecycle.deploy, rollback, restart, start, stop, backup and
// run-job.
//
// The nodes are thin: the catalog manager owns locking, the state machine,
// auditing and the timeline. A node adds the run's identity (lock owner,
// audit actor, idempotency key) and translates errors into the engine's
// retry model: failures that a retry cannot fix are permanent, while busy
// resources and failed actions are retried according to the node's policy.
package lifecycle

import (
	"context"
	"errors"
	"fmt"
	"regexp"

	"github.com/ACaccel/lcm/internal/auth"
	"github.com/ACaccel/lcm/internal/catalog"
	"github.com/ACaccel/lcm/internal/model"
	"github.com/ACaccel/lcm/internal/node"
	"github.com/ACaccel/lcm/internal/resource"
	rt "github.com/ACaccel/lcm/internal/runtime"
)

// Category groups the nodes in the editor.
const Category = "lifecycle"

// Actions is what the nodes need from the catalog manager;
// *catalog.Manager implements it.
type Actions interface {
	Deploy(ctx context.Context, name string, opts catalog.ActionOptions) (catalog.ActionResult, error)
	Rollback(ctx context.Context, name string, opts catalog.ActionOptions) (catalog.ActionResult, error)
	Restart(ctx context.Context, name string, opts catalog.ActionOptions) (catalog.ActionResult, error)
	Start(ctx context.Context, name string, opts catalog.ActionOptions) (catalog.ActionResult, error)
	Stop(ctx context.Context, name string, opts catalog.ActionOptions) (catalog.ActionResult, error)
	Backup(ctx context.Context, name string, opts catalog.ActionOptions) (catalog.ActionResult, error)
	RunJob(ctx context.Context, name string, opts catalog.ActionOptions) (catalog.ActionResult, error)
	Plan(ctx context.Context, name, action string, opts catalog.ActionOptions) (catalog.Plan, error)
	CheckImpact(ctx context.Context, name, action string, acknowledged bool) (resource.Impact, error)
}

var _ Actions = (*catalog.Manager)(nil)

// ResourceProps selects the service resource for restart, start, stop,
// rollback and backup.
type ResourceProps struct {
	Resource          string `json:"resource" lcm:"required" desc:"name of a service resource, usually an expression such as {{ trigger.resource }}"`
	AcknowledgeImpact bool   `json:"acknowledgeImpact" desc:"allow a disruptive action on a service other managed resources depend on or are controlled through; without it such a node fails"`
}

// DeployProps configures lifecycle.deploy.
type DeployProps struct {
	Resource          string `json:"resource" lcm:"required" desc:"name of a service resource"`
	Version           string `json:"version" desc:"commit sha to deploy; empty deploys the head of the configured branch"`
	AcknowledgeImpact bool   `json:"acknowledgeImpact" desc:"allow a disruptive action on a service other managed resources depend on or are controlled through; without it such a node fails"`
}

// RunJobProps configures lifecycle.run-job.
type RunJobProps struct {
	Resource          string `json:"resource" lcm:"required" desc:"name of a service resource"`
	Job               string `json:"job" lcm:"required" desc:"name of a job defined in the service config"`
	AcknowledgeImpact bool   `json:"acknowledgeImpact" desc:"allow a disruptive action on a service other managed resources depend on or are controlled through; without it such a node fails"`
}

var resourceRe = regexp.MustCompile(`^[a-z]([a-z0-9-]*[a-z0-9])?$`)

func checkResource(name string) error {
	if len(name) > 63 || !resourceRe.MatchString(name) {
		return fmt.Errorf("resource: %q is not a valid resource name", name)
	}
	return nil
}

// Register adds the lifecycle node types to r.
func Register(r *node.Registry, a Actions) {
	simple := func(typ, action, desc string, run func(context.Context, string, catalog.ActionOptions) (catalog.ActionResult, error)) {
		node.Register(r, node.Spec[ResourceProps]{
			Type: typ, Category: Category, Effect: node.EffectWrite, Description: desc, ResourceProp: "resource",
			Validate: func(p ResourceProps) error { return checkResource(p.Resource) },
			Run: func(ctx context.Context, rc *node.RunContext, p ResourceProps) (node.Result, error) {
				return execute(ctx, rc, a, p.Resource, action, p.AcknowledgeImpact, catalog.ActionOptions{}, run)
			},
			DryRun: func(ctx context.Context, rc *node.RunContext, p ResourceProps) (node.Result, error) {
				return plan(ctx, a, p.Resource, action, p.AcknowledgeImpact, catalog.ActionOptions{})
			},
		})
	}
	simple("lifecycle.rollback", catalog.ActionRollback,
		"Redeploys the previous successful version of a service.", a.Rollback)
	simple("lifecycle.restart", catalog.ActionRestart,
		"Restarts a service; a stopped service is started.", a.Restart)
	simple("lifecycle.start", catalog.ActionStart, "Starts a stopped or failed service.", a.Start)
	simple("lifecycle.stop", catalog.ActionStop, "Stops a service.", a.Stop)
	simple("lifecycle.backup", catalog.ActionBackup, "Runs the service's backup: its command, or the typed backup of a managed service.", a.Backup)

	node.Register(r, node.Spec[DeployProps]{
		Type: "lifecycle.deploy", Category: Category, Effect: node.EffectWrite, ResourceProp: "resource",
		Description: "Updates a service from its source repository: fetch, check out, build, install, restart.",
		Validate:    func(p DeployProps) error { return checkResource(p.Resource) },
		Run: func(ctx context.Context, rc *node.RunContext, p DeployProps) (node.Result, error) {
			return execute(ctx, rc, a, p.Resource, catalog.ActionDeploy, p.AcknowledgeImpact, catalog.ActionOptions{Version: p.Version}, a.Deploy)
		},
		DryRun: func(ctx context.Context, rc *node.RunContext, p DeployProps) (node.Result, error) {
			return plan(ctx, a, p.Resource, catalog.ActionDeploy, p.AcknowledgeImpact, catalog.ActionOptions{Version: p.Version})
		},
	})
	node.Register(r, node.Spec[RunJobProps]{
		Type: "lifecycle.run-job", Category: Category, Effect: node.EffectWrite, ResourceProp: "resource",
		Description: "Runs a job defined in the service config.",
		Validate:    func(p RunJobProps) error { return checkResource(p.Resource) },
		Run: func(ctx context.Context, rc *node.RunContext, p RunJobProps) (node.Result, error) {
			return execute(ctx, rc, a, p.Resource, catalog.ActionRunJob, p.AcknowledgeImpact, catalog.ActionOptions{Job: p.Job}, a.RunJob)
		},
		DryRun: func(ctx context.Context, rc *node.RunContext, p RunJobProps) (node.Result, error) {
			return plan(ctx, a, p.Resource, catalog.ActionRunJob, p.AcknowledgeImpact, catalog.ActionOptions{Job: p.Job})
		},
	})
}

// withRunActor attributes the action to the policy run unless the caller
// already supplied an actor.
func withRunActor(ctx context.Context, rc *node.RunContext) context.Context {
	if _, ok := auth.ActorFrom(ctx); ok {
		return ctx
	}
	return auth.WithActor(ctx, auth.Actor{Kind: auth.KindSystem, ID: "run:" + rc.RunID, Name: "policy " + rc.Policy})
}

// checkImpact applies the resource impact guard. An unacknowledged impact
// fails the node permanently: retrying cannot change the relations, and the
// policy author has to opt in with the acknowledgeImpact prop.
func checkImpact(ctx context.Context, a Actions, name, action string, acknowledged bool) (resource.Impact, error) {
	imp, err := a.CheckImpact(ctx, name, action, acknowledged)
	if err != nil {
		var ie *resource.ImpactError
		if errors.As(err, &ie) {
			return imp, node.Permanent(fmt.Errorf("%w: %s on %s affects dependents %v and control dependents %v; set acknowledgeImpact: true on the node to allow it",
				resource.ErrImpactUnacknowledged, action, name, ie.Impact.Names()["dependents"], ie.Impact.Names()["controlDependents"]))
		}
		return imp, classify(err)
	}
	return imp, nil
}

func execute(ctx context.Context, rc *node.RunContext, a Actions, name, action string, acknowledged bool, opts catalog.ActionOptions,
	run func(context.Context, string, catalog.ActionOptions) (catalog.ActionResult, error)) (node.Result, error) {

	imp, err := checkImpact(ctx, a, name, action, acknowledged)
	if err != nil {
		return node.Result{}, err
	}
	ctx = withRunActor(ctx, rc)
	if acknowledged {
		ctx = resource.WithAcknowledgedImpact(ctx, imp)
	}
	opts.RunID = rc.RunID
	opts.IdempotencyKey = rc.IdempotencyKey
	startedMs := rc.Now().UnixMilli()
	res, err := run(ctx, name, opts)
	if err != nil {
		return node.Result{}, classify(err)
	}
	out := map[string]any{
		"resource":   res.Resource,
		"action":     res.Action,
		"from":       string(res.From),
		"to":         string(res.To),
		"durationMs": res.DurationMs,
		"replayed":   res.Replayed,
		"startedMs":  startedMs,
	}
	if res.Version != "" {
		out["version"] = res.Version
	}
	if res.PreviousVersion != "" {
		out["previousVersion"] = res.PreviousVersion
	}
	if res.DeploymentID != 0 {
		out["deploymentId"] = res.DeploymentID
	}
	if res.Output != "" {
		out["output"] = res.Output
	}
	return node.Result{Output: out}, nil
}

// plan is the dry run of a node: the action's steps and impact. It fails as
// the run would when the impact is not acknowledged.
func plan(ctx context.Context, a Actions, name, action string, acknowledged bool, opts catalog.ActionOptions) (node.Result, error) {
	p, err := a.Plan(ctx, name, action, opts)
	if err != nil {
		return node.Result{}, classify(err)
	}
	imp, err := checkImpact(ctx, a, name, action, acknowledged)
	if err != nil {
		return node.Result{}, err
	}
	steps := make([]any, len(p.Steps))
	for i, s := range p.Steps {
		steps[i] = s
	}
	return node.Result{Output: map[string]any{
		"planned": true, "resource": p.Resource, "action": p.Action,
		"from": string(p.From), "to": string(p.To), "steps": steps, "impact": impactOutput(imp),
	}}, nil
}

// impactOutput is the impact in node output form: names by relation kind.
func impactOutput(imp resource.Impact) map[string]any {
	out := map[string]any{}
	for k, names := range imp.Names() {
		list := make([]any, len(names))
		for i, n := range names {
			list[i] = n
		}
		out[k] = list
	}
	return out
}

// classify marks errors that a retry cannot fix as permanent, including a
// candidate that needs the operator's upgrade. A busy resource and a failed
// action stay retryable.
func classify(err error) error {
	var issues model.Issues
	switch {
	case errors.Is(err, catalog.ErrPrecondition),
		errors.Is(err, catalog.ErrNotFound),
		errors.Is(err, catalog.ErrNeedsUpgrade),
		errors.Is(err, catalog.ErrInvalidTransition),
		errors.Is(err, rt.ErrUnsupported),
		errors.As(err, &issues):
		return node.Permanent(err)
	}
	return err
}
