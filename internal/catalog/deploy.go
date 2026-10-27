package catalog

import (
	"context"
	"errors"
	"fmt"
	"regexp"
	"strings"
	"time"

	"github.com/ACaccel/lcm/internal/execx"
	"github.com/ACaccel/lcm/internal/model"
)

// ErrDeployFailed is matched by the error of a deployment whose pipeline
// failed. The service is left in the failed state and the deployment record
// holds the log; whether to roll back is up to the caller or a policy.
var ErrDeployFailed = errors.New("catalog: deployment failed")

// StepError reports the pipeline step that failed.
type StepError struct {
	Step string
	Err  error
}

func (e *StepError) Error() string { return fmt.Sprintf("deployment failed at %s: %v", e.Step, e.Err) }
func (e *StepError) Unwrap() error { return e.Err }

// Is makes errors.Is(err, ErrDeployFailed) true.
func (e *StepError) Is(target error) bool { return target == ErrDeployFailed }

var shaRe = regexp.MustCompile(`^[0-9a-fA-F]{7,64}$`)

// gitEnv keeps git from prompting for credentials, which would hang an
// unattended run, and from localizing messages that we parse.
var gitEnv = []string{"GIT_TERMINAL_PROMPT=0", "LC_ALL=C"}

// Deploy updates the service to opts.Version, or to the head of the
// configured branch. The source repository must have no uncommitted
// changes to tracked files; it is detached at the target commit, built,
// installed and the service restarted. There is no automatic rollback.
func (m *Manager) Deploy(ctx context.Context, name string, opts ActionOptions) (ActionResult, error) {
	return m.exec(ctx, name, ActionDeploy, opts, func(ctx context.Context, s *session, res *ActionResult) error {
		return s.deploy(ctx, res, opts.Version)
	})
}

// Rollback redeploys the previous successful version. After a failed
// deployment that is the last version that worked; after a successful one
// it is the version that deployment replaced.
func (m *Manager) Rollback(ctx context.Context, name string, opts ActionOptions) (ActionResult, error) {
	return m.exec(ctx, name, ActionRollback, opts, func(ctx context.Context, s *session, res *ActionResult) error {
		deps, err := m.cat.Deployments(ctx, name, 100, 0)
		if err != nil {
			return err
		}
		target, ok := rollbackTarget(deps)
		if !ok {
			return ErrNoRollbackTarget
		}
		return s.deploy(ctx, res, target)
	})
}

// stepLog accumulates the commands run and their output for the deployment
// record.
type stepLog struct{ b strings.Builder }

func (l *stepLog) add(cmd execx.Cmd, out string) {
	fmt.Fprintf(&l.b, "$ %s\n", cmd.Display())
	if out = strings.TrimRight(out, "\n"); out != "" {
		l.b.WriteString(out)
		l.b.WriteByte('\n')
	}
}

func (l *stepLog) note(format string, args ...any) { fmt.Fprintf(&l.b, format+"\n", args...) }

func (s *session) git(dir string, timeout time.Duration, args ...string) execx.Cmd {
	return execx.Cmd{Argv: append([]string{s.m.git}, args...), Dir: dir, Env: gitEnv, Timeout: timeout}
}

// deploy runs the pipeline for deploy (version may be empty) and rollback
// (version is the target).
func (s *session) deploy(ctx context.Context, res *ActionResult, version string) error {
	src := s.svc.Spec.Source
	if src == nil {
		return ErrNoSource
	}
	if version != "" && !shaRe.MatchString(version) {
		return ErrInvalidVersion
	}
	timeout := src.EffectiveTimeout()

	// Refuse before touching anything: a dirty checkout is not a failed
	// deployment, the running service is unaffected.
	status := s.git(src.Repo, timeout, "status", "--porcelain", "--untracked-files=no")
	out, err := s.m.runner.Run(ctx, status)
	if err != nil {
		return fmt.Errorf("check source repository: %w", err)
	}
	if strings.TrimSpace(out.Output) != "" {
		return ErrDirtyRepo
	}

	deps, err := s.m.cat.Deployments(ctx, s.name, 100, 0)
	if err != nil {
		return err
	}
	previous := lastSuccessful(deps)

	transient := StateDeploying
	switch {
	case s.action == ActionRollback:
		transient = StateRollingBack
	case s.cur == StateRunning:
		transient = StateUpdating
	}
	if err := s.move(ctx, transient); err != nil {
		return err
	}
	id, err := s.m.cat.BeginDeployment(ctx, Deployment{
		Service: s.name, Action: s.action, Version: version, PreviousVersion: previous,
		Actor: s.actor, RunID: s.opts.RunID,
	})
	if err != nil {
		s.settle(ctx, true)
		return err
	}
	res.DeploymentID = id

	log := &stepLog{}
	sha, perr := s.pipeline(ctx, src, log, version, &previous, id)
	res.Version, res.PreviousVersion = sha, previous

	// Bookkeeping must complete even when the action context was cancelled.
	fctx, cancel := detached(ctx)
	defer cancel()
	if perr != nil {
		if err := s.m.cat.SetDeploymentVersions(fctx, id, sha, previous); err != nil {
			s.m.log.Error("record deployment versions failed", "service", s.name, "error", err)
		}
		if err := s.m.cat.FinishDeployment(fctx, id, OutcomeFailed, perr.Error(), log.b.String()); err != nil {
			s.m.log.Error("finish deployment failed", "service", s.name, "error", err)
		}
		s.settle(ctx, true)
		return perr
	}
	if err := s.m.cat.SetDeploymentVersions(fctx, id, sha, previous); err != nil {
		s.m.log.Error("record deployment versions failed", "service", s.name, "error", err)
	}
	if err := s.move(fctx, StateRunning); err != nil {
		_ = s.m.cat.FinishDeployment(fctx, id, OutcomeFailed, err.Error(), log.b.String())
		return err
	}
	return s.m.cat.FinishDeployment(fctx, id, OutcomeSucceeded, "", log.b.String())
}

// pipeline fetches, checks out, builds, installs and restarts. It returns
// the commit that was targeted as soon as it is known, even on failure.
func (s *session) pipeline(ctx context.Context, src *model.Source, log *stepLog, version string, previous *string, id int64) (string, error) {
	timeout := src.EffectiveTimeout()
	run := func(step string, c execx.Cmd) (execx.Result, error) {
		r, err := s.m.runner.Run(ctx, c)
		log.add(c, r.Output)
		if err != nil {
			log.note("error: %v", err)
			return r, &StepError{Step: step, Err: err}
		}
		return r, nil
	}

	// A rollback targets a commit that was deployed before, so it works
	// without the remote being reachable.
	if s.action != ActionRollback {
		if _, err := run("fetch", s.git(src.Repo, timeout, "fetch", "--prune", src.EffectiveRemote())); err != nil {
			return "", err
		}
	}

	ref := version
	if ref == "" {
		ref = "refs/remotes/" + src.EffectiveRemote() + "/" + src.Branch
	}
	r, err := run("resolve", s.git(src.Repo, timeout, "rev-parse", "--verify", "--quiet", ref+"^{commit}"))
	if err != nil {
		return "", err
	}
	sha := strings.TrimSpace(r.Output)
	if !shaRe.MatchString(sha) {
		return "", &StepError{Step: "resolve", Err: fmt.Errorf("unexpected output %q", headCap(sha, 80))}
	}

	if *previous == "" {
		if head, err := s.m.runner.Run(ctx, s.git(src.Repo, timeout, "rev-parse", "HEAD")); err == nil {
			*previous = strings.TrimSpace(head.Output)
		}
	}
	if err := s.m.cat.SetDeploymentVersions(ctx, id, sha, *previous); err != nil {
		return sha, err
	}

	if _, err := run("checkout", s.git(src.Repo, timeout, "checkout", "--detach", sha)); err != nil {
		return sha, err
	}
	for _, step := range []struct{ name, script string }{{"build", src.Build}, {"install", src.Install}} {
		if step.script == "" {
			continue
		}
		if _, err := run(step.name, execx.Cmd{Shell: step.script, Dir: src.Repo, Timeout: timeout}); err != nil {
			return sha, err
		}
	}

	log.note("restarting service")
	if err := s.restartOrStart(ctx); err != nil {
		log.note("error: %v", err)
		return sha, &StepError{Step: "restart", Err: err}
	}
	return sha, nil
}
