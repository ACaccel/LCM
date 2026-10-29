// Package lifecycle provides the policy nodes that act on services:
// lifecycle.deploy, rollback, restart, start, stop, backup and run-job.
//
// The nodes are thin: the catalog manager owns locking, the state machine,
// auditing and the timeline. A node adds the run's identity (lock owner,
// audit actor, idempotency key) and translates errors into the engine's
// retry model: failures that a retry cannot fix are permanent, while busy
// services and failed actions are retried according to the node's policy.
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
}

var _ Actions = (*catalog.Manager)(nil)

// ServiceProps selects the service for restart, start, stop, rollback and backup.
type ServiceProps struct {
	Service string `json:"service" lcm:"required" desc:"service name, usually an expression such as {{ trigger.service }}"`
}

// DeployProps configures lifecycle.deploy.
type DeployProps struct {
	Service string `json:"service" lcm:"required" desc:"service name"`
	Version string `json:"version" desc:"commit sha to deploy; empty deploys the head of the configured branch"`
}

// RunJobProps configures lifecycle.run-job.
type RunJobProps struct {
	Service string `json:"service" lcm:"required" desc:"service name"`
	Job     string `json:"job" lcm:"required" desc:"name of a job defined in the service document"`
}

var serviceRe = regexp.MustCompile(`^[a-z]([a-z0-9-]*[a-z0-9])?$`)

func checkService(name string) error {
	if len(name) > 63 || !serviceRe.MatchString(name) {
		return fmt.Errorf("service: %q is not a valid service name", name)
	}
	return nil
}

// Register adds the lifecycle node types to r.
func Register(r *node.Registry, a Actions) {
	simple := func(typ, action, desc string, run func(context.Context, string, catalog.ActionOptions) (catalog.ActionResult, error)) {
		node.Register(r, node.Spec[ServiceProps]{
			Type: typ, Category: Category, Effect: node.EffectWrite, Description: desc, ServiceProp: "service",
			Validate: func(p ServiceProps) error { return checkService(p.Service) },
			Run: func(ctx context.Context, rc *node.RunContext, p ServiceProps) (node.Result, error) {
				return execute(ctx, rc, p.Service, catalog.ActionOptions{}, run)
			},
			DryRun: func(ctx context.Context, rc *node.RunContext, p ServiceProps) (node.Result, error) {
				return plan(ctx, a, p.Service, action, catalog.ActionOptions{})
			},
		})
	}
	simple("lifecycle.rollback", catalog.ActionRollback,
		"Redeploys the previous successful version of a service.", a.Rollback)
	simple("lifecycle.restart", catalog.ActionRestart,
		"Restarts a service; a stopped service is started.", a.Restart)
	simple("lifecycle.start", catalog.ActionStart, "Starts a stopped or failed service.", a.Start)
	simple("lifecycle.stop", catalog.ActionStop, "Stops a service.", a.Stop)
	simple("lifecycle.backup", catalog.ActionBackup, "Runs the service's backup command.", a.Backup)

	node.Register(r, node.Spec[DeployProps]{
		Type: "lifecycle.deploy", Category: Category, Effect: node.EffectWrite, ServiceProp: "service",
		Description: "Updates a service from its source repository: fetch, check out, build, install, restart.",
		Validate:    func(p DeployProps) error { return checkService(p.Service) },
		Run: func(ctx context.Context, rc *node.RunContext, p DeployProps) (node.Result, error) {
			return execute(ctx, rc, p.Service, catalog.ActionOptions{Version: p.Version}, a.Deploy)
		},
		DryRun: func(ctx context.Context, rc *node.RunContext, p DeployProps) (node.Result, error) {
			return plan(ctx, a, p.Service, catalog.ActionDeploy, catalog.ActionOptions{Version: p.Version})
		},
	})
	node.Register(r, node.Spec[RunJobProps]{
		Type: "lifecycle.run-job", Category: Category, Effect: node.EffectWrite, ServiceProp: "service",
		Description: "Runs a job defined in the service document.",
		Validate:    func(p RunJobProps) error { return checkService(p.Service) },
		Run: func(ctx context.Context, rc *node.RunContext, p RunJobProps) (node.Result, error) {
			return execute(ctx, rc, p.Service, catalog.ActionOptions{Job: p.Job}, a.RunJob)
		},
		DryRun: func(ctx context.Context, rc *node.RunContext, p RunJobProps) (node.Result, error) {
			return plan(ctx, a, p.Service, catalog.ActionRunJob, catalog.ActionOptions{Job: p.Job})
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

func execute(ctx context.Context, rc *node.RunContext, service string, opts catalog.ActionOptions,
	run func(context.Context, string, catalog.ActionOptions) (catalog.ActionResult, error)) (node.Result, error) {

	opts.RunID = rc.RunID
	opts.IdempotencyKey = rc.IdempotencyKey
	startedMs := rc.Now().UnixMilli()
	res, err := run(withRunActor(ctx, rc), service, opts)
	if err != nil {
		return node.Result{}, classify(err)
	}
	out := map[string]any{
		"service":    res.Service,
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

func plan(ctx context.Context, a Actions, service, action string, opts catalog.ActionOptions) (node.Result, error) {
	p, err := a.Plan(ctx, service, action, opts)
	if err != nil {
		return node.Result{}, classify(err)
	}
	steps := make([]any, len(p.Steps))
	for i, s := range p.Steps {
		steps[i] = s
	}
	return node.Result{Output: map[string]any{
		"planned": true, "service": p.Service, "action": p.Action,
		"from": string(p.From), "to": string(p.To), "steps": steps,
	}}, nil
}

// classify marks errors that a retry cannot fix as permanent. A busy
// service and a failed action stay retryable.
func classify(err error) error {
	var issues model.Issues
	switch {
	case errors.Is(err, catalog.ErrPrecondition),
		errors.Is(err, catalog.ErrNotFound),
		errors.Is(err, catalog.ErrInvalidTransition),
		errors.Is(err, rt.ErrUnsupported),
		errors.As(err, &issues):
		return node.Permanent(err)
	}
	return err
}
