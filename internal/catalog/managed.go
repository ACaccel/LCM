package catalog

import (
	"context"
	"errors"
	"fmt"
	"math"
	"path"
	"regexp"
	"slices"
	"strings"

	"github.com/ACaccel/lcm/internal/deployhelper"
	"github.com/ACaccel/lcm/internal/message"
	"github.com/ACaccel/lcm/internal/model"
	rt "github.com/ACaccel/lcm/internal/runtime"
)

// A managed service is deployed by the deploy helper: after LCM has
// detached its own checkout at the target commit, the helper builds it in
// its sandbox, installs the artifact as a release and activates it. Each
// helper call gets a fresh operation ID that is written to the deployment
// log before the call, so that the outcome can be looked up in the
// helper's ledger after a lost reply or a restart of LCM. The release a
// successful deployment activated is the log's last line, which a rollback
// reads to switch back without rebuilding.

// Lines of the deployment log that the catalog reads back. The helper's
// own output is indented (see helperOutput), so it can never start one.
var (
	pendingOpRe = regexp.MustCompile(`(?m)^deploy helper (build|install|activate) operation ([0-9a-f-]{36})$`)
	releaseRe   = regexp.MustCompile(`(?m)^installed release ([0-9]{8}T[0-9]{6}Z-[0-9a-f]{8})$`)
)

// maxHelperLog bounds the helper output kept per step in the deployment
// log, so that one build's output does not push the operation IDs out.
const maxHelperLog = 4 << 10

// helperOutput adds the helper's log tail, indented.
func (l *stepLog) helperOutput(out string) {
	out = strings.TrimRight(tailCap(out, maxHelperLog), "\n")
	if out == "" {
		return
	}
	for line := range strings.SplitSeq(out, "\n") {
		l.b.WriteString("  | ")
		l.b.WriteString(line)
		l.b.WriteByte('\n')
	}
}

// installedRelease returns the release a deployment's log records as
// activated.
func installedRelease(log string) string {
	m := releaseRe.FindAllStringSubmatch(log, -1)
	if len(m) == 0 {
		return ""
	}
	return m[len(m)-1][1]
}

// lastOperation returns the last helper operation a deployment's log
// records.
func lastOperation(log string) (op, id string, ok bool) {
	m := pendingOpRe.FindAllStringSubmatch(log, -1)
	if len(m) == 0 {
		return "", "", false
	}
	last := m[len(m)-1]
	return last[1], last[2], true
}

// activation records how far the activate request of a deployment got, to
// tell a failure that left the running service alone from one that did not.
type activation struct {
	// sent is set once the request may have reached the helper.
	sent bool
	// switched is set when the helper reported that the links moved.
	switched bool
}

// leavesServiceAlone reports whether a failed managed deployment certainly
// changed nothing about the running service: the failure came before the
// activate request, a build or install failed (they only add files to the
// helper's directories), or activate was answered with an error before it
// switched the release. An unknown outcome, and a failure after the
// switch, may have changed it.
func (s *session) leavesServiceAlone(err error) bool {
	var he *rt.HelperError
	if errors.As(err, &he) {
		switch {
		case he.Unknown:
			return false
		case he.Op != deployhelper.OpActivate:
			return true
		}
		return !s.act.switched
	}
	return !s.act.sent
}

// helperStep calls the helper for one pipeline step. The operation ID is
// in the stored log before the request is sent; without that record the
// request is not sent.
func (s *session) helperStep(ctx context.Context, step string, req deployhelper.Request, log *stepLog, id int64) (deployhelper.Response, error) {
	req.Service, req.OperationID = s.name, rt.NewOperationID()
	log.note("deploy helper %s operation %s", req.Op, req.OperationID)
	if err := s.m.cat.NoteDeployment(ctx, id, log.b.String()); err != nil {
		log.note("error: %v", err)
		return deployhelper.Response{}, &StepError{Step: step, Err: err}
	}
	if req.Op == deployhelper.OpActivate {
		s.act.sent = true
	}
	resp, err := rt.CallHelper(ctx, s.m.deploy, req)
	if a := resp.Activate; a != nil && a.Switched {
		s.act.switched = true
	}
	log.helperOutput(resp.Log)
	if err != nil {
		log.note("error: %v", err)
		return resp, &StepError{Step: step, Err: err}
	}
	return resp, nil
}

// buildTimeoutSec is source.timeout in whole seconds within the helper's
// bounds.
func buildTimeoutSec(src *model.Source) int {
	sec := int(math.Ceil(src.EffectiveTimeout().Seconds()))
	return min(max(sec, 1), deployhelper.MaxTimeoutSec)
}

// activateRequest is the activate request for release. Rollbacks run no
// migration: the migration of the release being left may already have
// changed the data, and running an older one again cannot undo that.
func (s *session) activateRequest(release string) deployhelper.Request {
	cfg := s.cfg
	req := deployhelper.Request{
		Op: deployhelper.OpActivate, ReleaseID: release,
		Start:   append([]string{cfg.Start}, cfg.StartArgs...),
		Runtime: cfg.RuntimeName, KeepReleases: cfg.KeepReleases,
	}
	if s.action != ActionRollback {
		req.Migrate = cfg.Migrate
	}
	return req
}

// managedPipeline builds, installs and activates commit sha, which LCM's
// checkout is detached at.
func (s *session) managedPipeline(ctx context.Context, sha string, log *stepLog, id int64) error {
	cfg := s.cfg
	build := deployhelper.Request{Op: deployhelper.OpBuild, Commit: strings.ToLower(sha), Command: cfg.Source.Build, TimeoutSec: buildTimeoutSec(cfg.Source)}
	if cfg.RuntimeName != "" {
		build.Runtimes = []string{cfg.RuntimeName}
	}
	resp, err := s.helperStep(ctx, "build", build, log, id)
	if err != nil {
		return err
	}
	if resp.Build == nil {
		return &StepError{Step: "build", Err: errors.New("the deploy helper's reply has no build")}
	}
	log.note("built %s", resp.Build.BuildID)

	resp, err = s.helperStep(ctx, "install", deployhelper.Request{Op: deployhelper.OpInstall, BuildID: resp.Build.BuildID, Artifact: cfg.Artifact}, log, id)
	if err != nil {
		return err
	}
	if resp.Install == nil {
		return &StepError{Step: "install", Err: errors.New("the deploy helper's reply has no release")}
	}
	log.note("installed %s as release %s", cfg.Artifact, resp.Install.ReleaseID)
	if s.action == ActionRollback && cfg.Migrate != nil {
		log.note("rollback: the migration is not run")
	}
	return s.activate(ctx, resp.Install.ReleaseID, log, id)
}

// activate activates an installed release and records it as the
// deployment's last log line.
func (s *session) activate(ctx context.Context, release string, log *stepLog, id int64) error {
	resp, err := s.helperStep(ctx, "activate", s.activateRequest(release), log, id)
	if a := resp.Activate; err != nil && a != nil && a.Switched {
		// The links moved before a later step failed: the release is
		// current, and a rollback returns to the one it replaced.
		log.note("release %s is current although the activation failed (previous: %s)", release, a.Previous)
	}
	if err != nil {
		return err
	}
	if resp.Activate == nil || resp.Activate.ReleaseID != release {
		return &StepError{Step: "activate", Err: errors.New("the deploy helper's reply does not confirm the release")}
	}
	if resp.Activate.Previous != "" {
		log.note("previous release %s", resp.Activate.Previous)
	}
	log.note("installed release %s", release)
	return nil
}

// activateRelease rolls back to target by activating its installed release
// without rebuilding it.
func (s *session) activateRelease(ctx context.Context, res *ActionResult, target, release, previous string) error {
	return s.record(ctx, res, target, previous, func(log *stepLog, id int64, _ *string) (string, error) {
		log.note("rolling back to release %s of %s", release, shortSHA(target))
		if s.cfg.Migrate != nil {
			log.note("rollback: the migration is not run")
		}
		return target, s.activate(ctx, release, log, id)
	})
}

// rollbackRelease returns the installed release of the rollback target: the
// release recorded by the newest successful deployment of target, else the
// newest release built from target. It is empty when the helper keeps
// neither, and the rollback rebuilds the commit.
func (m *Manager) rollbackRelease(ctx context.Context, name string, cfg *model.ServiceConfig, deps []Deployment, target string) (string, error) {
	resp, err := rt.CallHelper(ctx, m.deploy, deployhelper.Request{Op: deployhelper.OpReleases, Service: name, OperationID: rt.NewOperationID()})
	var he *rt.HelperError
	if errors.As(err, &he) && he.Code == deployhelper.CodeNotFound {
		return "", nil
	}
	if err != nil {
		return "", fmt.Errorf("list the releases of %s: %w", name, err)
	}
	// The old release is started with the current start settings, so it
	// must have been installed the way the current document installs.
	built := func(r deployhelper.Release) bool {
		return releaseFits(r.Manifest, cfg) && strings.EqualFold(r.Manifest.Commit, target)
	}
	recorded := ""
	for _, d := range deps {
		if d.Outcome == OutcomeSucceeded && d.Version == target {
			recorded = installedRelease(d.LogExcerpt)
			break
		}
	}
	for _, r := range resp.Releases {
		if r.ReleaseID == recorded && releaseFits(r.Manifest, cfg) {
			return r.ReleaseID, nil
		}
	}
	for _, r := range resp.Releases {
		if built(r) {
			return r.ReleaseID, nil
		}
	}
	return "", nil
}

// releaseFits reports whether an installed release was built for what the
// document says now: the same artifact directory and the same runtime.
func releaseFits(m *deployhelper.Manifest, cfg *model.ServiceConfig) bool {
	if m == nil || path.Clean(m.Artifact) != path.Clean(cfg.Artifact) {
		return false
	}
	if cfg.RuntimeName == "" {
		return len(m.Runtimes) == 0
	}
	return slices.Equal(m.Runtimes, []string{cfg.RuntimeName})
}

// interrupted is what the ledger says about the last helper operation of a
// deployment that was running when LCM stopped.
type interrupted struct {
	// text is the explanation for the deployment's error; empty when the
	// log names no helper operation.
	text string
	// activated is set when the ledger confirms that the operation was a
	// successful activate of the deployment's release.
	activated *deployhelper.ActivateResult
}

// inspectInterrupted looks up the last helper operation of d.
func (m *Manager) inspectInterrupted(ctx context.Context, d Deployment) interrupted {
	op, opID, ok := lastOperation(d.LogExcerpt)
	if !ok {
		return interrupted{}
	}
	resp, err := rt.CallHelper(ctx, m.deploy, deployhelper.Request{Op: deployhelper.OpQuery, OperationID: opID})
	var he *rt.HelperError
	switch {
	case errors.As(err, &he) && he.Code == deployhelper.CodeNotFound:
		return interrupted{text: fmt.Sprintf("deploy helper %s operation %s was not carried out: the helper has no record of it", op, opID)}
	case err != nil:
		return interrupted{text: fmt.Sprintf("deploy helper %s operation %s: outcome unknown, query %s (%v)", op, opID, opID, err)}
	case resp.State == deployhelper.StateSucceeded && resp.Op == deployhelper.OpActivate && op == string(deployhelper.OpActivate) && resp.Activate != nil:
		return interrupted{
			text:      fmt.Sprintf("deploy helper %s operation %s succeeded: it activated release %s", op, opID, resp.Activate.ReleaseID),
			activated: resp.Activate,
		}
	case resp.State == deployhelper.StateSucceeded:
		return interrupted{text: fmt.Sprintf("deploy helper %s operation %s succeeded", op, opID)}
	case resp.State == deployhelper.StateFailed:
		return interrupted{text: fmt.Sprintf("deploy helper %s operation %s failed: %s", op, opID, resp.Message)}
	}
	return interrupted{text: fmt.Sprintf("deploy helper %s operation %s: outcome unknown (%s), query %s", op, opID, resp.State, opID)}
}

// confirmedLog is the log of a deployment that the helper's ledger proves
// succeeded after LCM lost track of it, ending in the release line a
// rollback reads.
func confirmedLog(log string, a *deployhelper.ActivateResult) string {
	var l stepLog
	l.b.WriteString(strings.TrimRight(log, "\n"))
	l.b.WriteByte('\n')
	l.note("outcome confirmed from the helper ledger after a restart of LCM")
	if a.Previous != "" {
		l.note("previous release %s", a.Previous)
	}
	l.note("installed release %s", a.ReleaseID)
	return l.b.String()
}

// managedRollbackSteps returns the steps of a rollback that activates an
// installed release, or nothing when the rollback rebuilds.
func (m *Manager) managedRollbackSteps(ctx context.Context, rec *Record) ([]message.Message, error) {
	deps, err := m.cat.Deployments(ctx, rec.Name, 100, 0)
	if err != nil {
		return nil, err
	}
	target, ok := rollbackTarget(deps)
	if !ok {
		return nil, ErrNoRollbackTarget
	}
	release, err := m.rollbackRelease(ctx, rec.Name, rec.Config, deps, target)
	if err != nil || release == "" {
		return nil, err
	}
	return []message.Message{
		message.New("step.recordDeployment", fmt.Sprintf("set state %s and record the deployment", StateRollingBack), "state", string(StateRollingBack)),
		message.New("step.rollbackTarget", "resolve rollback target "+shortSHA(target), "commit", shortSHA(target)),
		message.New("step.helperActivatePrevious",
			fmt.Sprintf("switch %s back to its installed release %s and restart it, without running the migration", rec.Name, release),
			"service", rec.Name, "release", release, "commit", shortSHA(target)),
	}, nil
}

// managedDeploySteps are the helper steps that follow the checkout.
func managedDeploySteps(rec *Record, action string) []message.Message {
	cfg := rec.Config
	start := strings.Join(append([]string{cfg.Start}, cfg.StartArgs...), " ")
	migrate := ""
	if cfg.Migrate != nil && action != ActionRollback {
		migrate = strings.Join(cfg.Migrate, " ")
	}
	activate := fmt.Sprintf("switch %s to the new release and restart it with %s", rec.Name, start)
	if migrate != "" {
		activate = fmt.Sprintf("run the migration %s, then %s", migrate, activate)
	}
	return []message.Message{
		message.New("step.helperBuild", "build in the deploy helper's sandbox as lcm-build: "+cfg.Source.Build,
			"command", cfg.Source.Build, "runtime", cfg.RuntimeName),
		message.New("step.helperInstall", fmt.Sprintf("install %s of the build as a new release of %s", cfg.Artifact, rec.Name),
			"artifact", cfg.Artifact, "service", rec.Name),
		message.New("step.helperActivate", activate, "service", rec.Name, "start", start, "migrate", migrate),
	}
}
