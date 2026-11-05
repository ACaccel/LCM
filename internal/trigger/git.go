package trigger

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"sync"
	"time"

	"github.com/ACaccel/lcm/internal/clock"
	"github.com/ACaccel/lcm/internal/execx"
)

// GitRunner reads the head of a remote branch. It sits behind an interface
// so that tests never touch a real repository or network.
type GitRunner interface {
	// RemoteHead returns the commit id of refs/heads/<branch> in repo, or
	// ErrBranchNotFound.
	RemoteHead(ctx context.Context, repo, branch string) (string, error)
}

// ErrBranchNotFound is returned when the remote has no such branch.
var ErrBranchNotFound = errors.New("trigger: branch not found on remote")

// ExecGit runs `git ls-remote` through an execx.Runner, so that git gets the
// fixed environment of execx.GitEnv and none of LCM's own variables. The
// zero value uses "git" from PATH, a real runner and clock, and a 30 second
// timeout per call.
type ExecGit struct {
	Runner  execx.Runner
	Clock   clock.Clock
	Binary  string
	Timeout time.Duration
}

// ErrGitTimeout is the cause of an ls-remote stopped by ExecGit.Timeout.
var ErrGitTimeout = errors.New("trigger: git ls-remote timed out")

// maxLsRemoteOutput bounds the captured output of one ls-remote; it names a
// single ref, so anything larger is an error message.
const maxLsRemoteOutput = 64 << 10

// RemoteHead implements GitRunner.
func (g ExecGit) RemoteHead(ctx context.Context, repo, branch string) (string, error) {
	bin, timeout, runner, clk := g.Binary, g.Timeout, g.Runner, g.Clock
	if bin == "" {
		bin = "git"
	}
	if timeout <= 0 {
		timeout = 30 * time.Second
	}
	if runner == nil {
		runner = execx.New()
	}
	if clk == nil {
		clk = clock.Real()
	}
	ctx, cancel := clock.WithTimeoutCause(ctx, clk, timeout, ErrGitTimeout)
	defer cancel()

	ref := "refs/heads/" + branch
	// "--" keeps a repo value from being read as an option.
	res, err := runner.Run(ctx, execx.Cmd{
		Argv: []string{bin, "ls-remote", "--", repo, ref}, Env: execx.GitEnv(), MaxOutput: maxLsRemoteOutput,
	})
	if err != nil {
		if cause := context.Cause(ctx); errors.Is(cause, ErrGitTimeout) {
			return "", fmt.Errorf("git ls-remote %s: %w", ref, cause)
		}
		// Both the error (which quotes the command) and git's output may
		// contain the repository URL, so neither is passed on unredacted.
		msg := strings.TrimSpace(execx.RedactURLs(res.Output))
		if len(msg) > 300 {
			msg = msg[len(msg)-300:]
		}
		return "", fmt.Errorf("git ls-remote %s: %s: %s", ref, execx.RedactURLs(err.Error()), msg)
	}
	for line := range strings.SplitSeq(res.Output, "\n") {
		sha, name, ok := strings.Cut(strings.TrimSpace(line), "\t")
		if ok && name == ref && sha != "" {
			return sha, nil
		}
	}
	return "", fmt.Errorf("%w: %s", ErrBranchNotFound, branch)
}

type gitWatcher struct {
	baseWatcher
	props GitProps

	// mu serializes polls and pushes so that one commit starts one run.
	mu       sync.Mutex
	failures int
}

func newGitWatcher(b baseWatcher, p GitProps) *gitWatcher {
	m := b.m
	w := &gitWatcher{baseWatcher: b, props: p}
	m.registerGit(b.policy, b.node, w)
	return w
}

func (w *gitWatcher) run(ctx context.Context) {
	defer w.m.unregisterGit(w.policy, w.node, w)
	for {
		w.poll(ctx)
		if !w.m.sleep(ctx, w.props.Interval.D()) {
			return
		}
	}
}

func (w *gitWatcher) poll(ctx context.Context) {
	sha, err := w.m.git.RemoteHead(ctx, w.props.Repo, w.props.Branch)
	if err != nil {
		if ctx.Err() != nil {
			return
		}
		w.mu.Lock()
		w.failures++
		n := w.failures
		w.mu.Unlock()
		// Log the first failure and then every tenth, so an unreachable
		// remote does not flood the log at every interval.
		if n == 1 || n%10 == 0 {
			w.log.Warn("git poll failed", "consecutive", n, "error", err)
		}
		return
	}
	w.mu.Lock()
	w.failures = 0
	w.mu.Unlock()
	w.observe(ctx, sha)
}

// observe handles a head commit learned by polling or a push notification.
func (w *gitWatcher) observe(ctx context.Context, sha string) {
	w.mu.Lock()
	defer w.mu.Unlock()

	st, err := w.loadState(ctx)
	if err != nil {
		w.log.Warn("cannot read git trigger state", "error", err)
		return
	}
	prev := ""
	// The last seen commit only counts for the repo and branch it was
	// recorded for, because a redeploy may point the trigger elsewhere.
	if st["repo"] == w.props.Repo && st["branch"] == w.props.Branch {
		prev, _ = st["sha"].(string)
	}
	if sha == prev {
		return
	}
	record := func() {
		data := map[string]any{"repo": w.props.Repo, "branch": w.props.Branch, "sha": sha}
		if err := w.saveState(ctx, data); err != nil {
			w.log.Warn("cannot save git trigger state", "error", err)
		}
	}
	if prev == "" && !w.props.FireOnFirst {
		w.log.Info("first sight of branch; recording without a run", "commit", sha)
		record()
		return
	}
	event := map[string]any{"repo": w.props.Repo, "branch": w.props.Branch, "commit": sha, "previous": prev}
	if _, consumed := w.fire(ctx, event); consumed {
		record()
	}
}

// NotifyPush tells the manager that branch of repo now points at sha, as
// reported by a webhook from the git host. Every trigger.git node watching
// that repository and branch handles it exactly like a poll result, and
// repeated notifications for the same commit start nothing. It returns the
// number of matching triggers.
func (m *Manager) NotifyPush(ctx context.Context, repo, branch, sha string) (int, error) {
	if sha == "" {
		return 0, errors.New("trigger: empty commit id")
	}
	if !m.synced.Load() {
		return 0, ErrNotReady
	}
	if err := m.Sync(ctx); err != nil {
		return 0, err
	}
	want := normalizeRepo(repo)
	m.gitMu.Lock()
	var targets []*gitWatcher
	for _, w := range m.gitWatchers {
		if w.props.Branch == branch && normalizeRepo(w.props.Repo) == want {
			targets = append(targets, w)
		}
	}
	m.gitMu.Unlock()
	for _, w := range targets {
		w.observe(ctx, sha)
	}
	return len(targets), nil
}

// SameRepo reports whether a and b name the same repository, ignoring the
// scheme, credentials, case and a trailing ".git".
func SameRepo(a, b string) bool { return normalizeRepo(a) == normalizeRepo(b) }

// normalizeRepo reduces the common spellings of one repository (https and
// ssh URLs, with or without .git) to a comparable form.
func normalizeRepo(s string) string {
	s = strings.ToLower(strings.TrimSpace(s))
	if _, rest, ok := strings.Cut(s, "://"); ok {
		s = rest
	}
	if at := strings.Index(s, "@"); at >= 0 && at < strings.IndexAny(s+"/", "/") {
		s = s[at+1:]
	}
	// scp-like syntax host:owner/name
	if i := strings.Index(s, ":"); i > 0 && !strings.Contains(s[:i], "/") {
		s = s[:i] + "/" + s[i+1:]
	}
	s = strings.TrimSuffix(strings.TrimRight(s, "/"), ".git")
	return s
}

type gitKey struct{ policy, node string }

func (m *Manager) registerGit(policy, nodeID string, w *gitWatcher) {
	m.gitMu.Lock()
	defer m.gitMu.Unlock()
	if m.gitWatchers == nil {
		m.gitWatchers = map[gitKey]*gitWatcher{}
	}
	m.gitWatchers[gitKey{policy, nodeID}] = w
}

func (m *Manager) unregisterGit(policy, nodeID string, w *gitWatcher) {
	m.gitMu.Lock()
	defer m.gitMu.Unlock()
	if m.gitWatchers[gitKey{policy, nodeID}] == w {
		delete(m.gitWatchers, gitKey{policy, nodeID})
	}
}
