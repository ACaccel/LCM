package catalog

import (
	"context"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"time"

	"github.com/ACaccel/lcm/internal/execx"
	"github.com/ACaccel/lcm/internal/model"
	rt "github.com/ACaccel/lcm/internal/runtime"
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

// Deploy updates the service to opts.Version, or to the head of the
// configured branch. The build checkout (CheckoutDir) is cloned from
// source.url when missing; otherwise it must have no uncommitted changes to
// tracked files and its origin is set to source.url before fetching. It is
// detached at the target commit, built, installed and the service
// restarted. A runtime that cannot restart the service leaves applying the
// new version to the install step. A managed service is built, installed and
// restarted by the deploy helper instead (see managedPipeline). There is no
// automatic rollback.
func (m *Manager) Deploy(ctx context.Context, name string, opts ActionOptions) (ActionResult, error) {
	return m.exec(ctx, name, ActionDeploy, opts, func(ctx context.Context, s *session, res *ActionResult) error {
		return s.deploy(ctx, res, opts.Version)
	})
}

// Rollback redeploys the previous successful version. After a failed
// deployment that is the last version that worked; after a successful one
// it is the version that deployment replaced. A managed service switches
// back to the release that version installed while the helper still keeps
// it, and is rebuilt otherwise.
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
		if s.rt.Type == model.RuntimeManaged {
			release, err := m.rollbackRelease(ctx, name, deps, target)
			if err != nil {
				return err
			}
			if release != "" {
				return s.activateRelease(ctx, res, target, release, lastSuccessful(deps))
			}
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

// git runs git in dir. The ceiling keeps git from treating a directory
// above the build directory as the repository when a checkout lacks .git.
func (s *session) git(dir string, timeout time.Duration, args ...string) execx.Cmd {
	env := append(execx.GitEnv(), "GIT_CEILING_DIRECTORIES="+s.m.workDir)
	return execx.Cmd{Argv: append([]string{s.m.git}, args...), Dir: dir, Env: env, Timeout: timeout}
}

// needsClone reports whether the build checkout dir must be cloned: it does
// not exist or is an empty directory. Anything else without .git is not
// LCM's checkout and is left alone.
func needsClone(dir string) (bool, error) {
	if _, err := os.Stat(filepath.Join(dir, ".git")); err == nil {
		return false, nil
	} else if !errors.Is(err, fs.ErrNotExist) {
		return false, fmt.Errorf("catalog: build checkout: %w", err)
	}
	entries, err := os.ReadDir(dir)
	switch {
	case errors.Is(err, fs.ErrNotExist):
		return true, nil
	case err != nil:
		return false, fmt.Errorf("catalog: build checkout: %w", err)
	case len(entries) > 0:
		return false, fmt.Errorf("%w (%s)", ErrBadCheckout, dir)
	}
	return true, nil
}

// deploy runs the pipeline for deploy (version may be empty) and rollback
// (version is the target).
func (s *session) deploy(ctx context.Context, res *ActionResult, version string) error {
	src := s.cfg.Source
	if src == nil {
		return ErrNoSource
	}
	if version != "" && !shaRe.MatchString(version) {
		return ErrInvalidVersion
	}
	// Saving refuses such a URL; the build directory moves with the data
	// directory, so a document saved under another one is checked again.
	if s.m.sourceInWorkDir(src) {
		return ErrSourceInWorkDir
	}
	timeout := src.EffectiveTimeout()
	dir := s.m.CheckoutDir(s.name)
	clone, err := needsClone(dir)
	if err != nil {
		return err
	}

	// Refuse before touching anything: a dirty checkout is not a failed
	// deployment, the running service is unaffected.
	if !clone {
		out, err := s.m.runner.Run(ctx, s.git(dir, timeout, "status", "--porcelain", "--untracked-files=no"))
		if err != nil {
			return fmt.Errorf("check build checkout %s: %w", dir, err)
		}
		if strings.TrimSpace(out.Output) != "" {
			return fmt.Errorf("%w (%s)", ErrDirtyRepo, dir)
		}
	}

	deps, err := s.m.cat.Deployments(ctx, s.name, 100, 0)
	if err != nil {
		return err
	}
	return s.record(ctx, res, version, lastSuccessful(deps), func(log *stepLog, id int64, previous *string) (string, error) {
		return s.pipeline(ctx, src, checkout{dir: dir, clone: clone}, log, version, previous, id)
	})
}

// record moves the service to its transient state, records a deployment of
// version, runs body and records its outcome. body returns the commit it
// targeted as soon as that is known, even on failure, and may correct
// previous.
func (s *session) record(ctx context.Context, res *ActionResult, version, previous string,
	body func(log *stepLog, id int64, previous *string) (string, error)) error {
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
		Resource: s.name, Action: s.action, Version: version, PreviousVersion: previous,
		Actor: s.actor, RunID: s.opts.RunID,
	})
	if err != nil {
		s.settle(ctx, true)
		return err
	}
	res.DeploymentID = id

	log := &stepLog{}
	sha, perr := body(log, id, &previous)
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

// checkout is the build checkout of a deployment and whether it must be
// cloned first.
type checkout struct {
	dir   string
	clone bool
}

// pipeline clones or fetches, checks out, builds, installs and restarts. It
// returns the commit that was targeted as soon as it is known, even on
// failure.
func (s *session) pipeline(ctx context.Context, src *model.Source, co checkout, log *stepLog, version string, previous *string, id int64) (string, error) {
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

	switch {
	case co.clone:
		if err := os.MkdirAll(s.m.workDir, 0o700); err != nil {
			log.note("error: %v", err)
			return "", &StepError{Step: "clone", Err: err}
		}
		if _, err := run("clone", s.git(s.m.workDir, timeout, "clone", "--", src.URL, co.dir)); err != nil {
			return "", err
		}
	case s.action != ActionRollback:
		// The document is the authority on where versions come from, so
		// origin follows it before every fetch. A rollback targets a commit
		// that was deployed before and works without the remote.
		if _, err := run("origin", s.git(co.dir, timeout, "remote", "set-url", "origin", src.URL)); err != nil {
			return "", err
		}
		if _, err := run("fetch", s.git(co.dir, timeout, "fetch", "--prune", "origin")); err != nil {
			return "", err
		}
	}

	ref := version
	if ref == "" {
		ref = "refs/remotes/origin/" + src.Branch
	}
	r, err := run("resolve", s.git(co.dir, timeout, "rev-parse", "--verify", "--quiet", ref+"^{commit}"))
	if err != nil {
		return "", err
	}
	sha := strings.TrimSpace(r.Output)
	if !shaRe.MatchString(sha) {
		return "", &StepError{Step: "resolve", Err: fmt.Errorf("unexpected output %q", headCap(sha, 80))}
	}

	// Without a recorded deployment the checkout's HEAD is what was built
	// last, unless the checkout is new and nothing was built from it.
	if *previous == "" && !co.clone {
		if head, err := s.m.runner.Run(ctx, s.git(co.dir, timeout, "rev-parse", "HEAD")); err == nil {
			*previous = strings.TrimSpace(head.Output)
		}
	}
	if err := s.m.cat.SetDeploymentVersions(ctx, id, sha, *previous); err != nil {
		return sha, err
	}

	if _, err := run("checkout", s.git(co.dir, timeout, "checkout", "--detach", sha)); err != nil {
		return sha, err
	}
	if s.rt.Type == model.RuntimeManaged {
		return sha, s.managedPipeline(ctx, sha, log, id)
	}
	for _, step := range []struct{ name, script string }{{"build", src.Build}, {"install", src.Install}} {
		if step.script == "" {
			continue
		}
		if _, err := run(step.name, execx.Cmd{Shell: step.script, Dir: co.dir, Timeout: timeout}); err != nil {
			return sha, err
		}
	}

	if !rt.CapabilitiesOf(s.rt).Restart {
		log.note("skipping restart: the runtime cannot restart this service")
		return sha, nil
	}
	log.note("restarting service")
	if err := s.restartOrStart(ctx); err != nil {
		log.note("error: %v", err)
		return sha, &StepError{Step: "restart", Err: err}
	}
	return sha, nil
}
