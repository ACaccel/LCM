package trigger

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"strings"
	"sync"
	"time"
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

// ExecGit runs `git ls-remote`. The zero value uses "git" from PATH and a
// 30 second timeout per call.
type ExecGit struct {
	Binary  string
	Timeout time.Duration
}

// RemoteHead implements GitRunner.
func (g ExecGit) RemoteHead(ctx context.Context, repo, branch string) (string, error) {
	bin, timeout := g.Binary, g.Timeout
	if bin == "" {
		bin = "git"
	}
	if timeout <= 0 {
		timeout = 30 * time.Second
	}
	ctx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()

	ref := "refs/heads/" + branch
	// "--" keeps a repo value from being read as an option.
	cmd := exec.CommandContext(ctx, bin, "ls-remote", "--", repo, ref) //nolint:gosec // repo and branch are validated policy props passed as arguments, not through a shell
	cmd.Env = gitEnv()
	var stdout, stderr bytes.Buffer
	cmd.Stdout, cmd.Stderr = &stdout, &stderr
	cmd.WaitDelay = 2 * time.Second
	if err := cmd.Run(); err != nil {
		msg := strings.TrimSpace(stderr.String())
		if len(msg) > 300 {
			msg = msg[:300]
		}
		return "", fmt.Errorf("git ls-remote %s: %w: %s", ref, err, msg)
	}
	for line := range strings.SplitSeq(stdout.String(), "\n") {
		sha, name, ok := strings.Cut(strings.TrimSpace(line), "\t")
		if ok && name == ref && sha != "" {
			return sha, nil
		}
	}
	return "", fmt.Errorf("%w: %s", ErrBranchNotFound, branch)
}

// gitEnv never prompts, and allows only protocols that cannot run commands
// (the ext:: transport can).
func gitEnv() []string {
	env := append(os.Environ(), "GIT_TERMINAL_PROMPT=0", "GIT_ALLOW_PROTOCOL=file:git:http:https:ssh")
	if os.Getenv("GIT_SSH_COMMAND") == "" {
		env = append(env, "GIT_SSH_COMMAND=ssh -o BatchMode=yes")
	}
	return env
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
