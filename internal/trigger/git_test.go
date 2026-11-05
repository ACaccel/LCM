package trigger_test

import (
	"context"
	"errors"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/ACaccel/lcm/internal/clock"
	"github.com/ACaccel/lcm/internal/execx"
	"github.com/ACaccel/lcm/internal/trigger"
)

func TestExecGitUsesFixedEnvironment(t *testing.T) {
	t.Setenv("LCM_SECRET_FOR_TEST", "hunter2")
	f := (&execx.Fake{}).Reply("ls-remote", "0123abc\trefs/heads/main\n", 0)
	g := trigger.ExecGit{Runner: f, Binary: "/usr/bin/git"}
	sha, err := g.RemoteHead(context.Background(), "https://example.com/x.git", "main")
	if err != nil || sha != "0123abc" {
		t.Fatalf("RemoteHead = %q, %v", sha, err)
	}
	calls := f.Calls()
	if len(calls) != 1 {
		t.Fatalf("calls = %v", f.Displays())
	}
	c := calls[0]
	want := []string{"/usr/bin/git", "ls-remote", "--", "https://example.com/x.git", "refs/heads/main"}
	if !slices.Equal(c.Argv, want) {
		t.Fatalf("argv = %q, want %q", c.Argv, want)
	}
	for _, e := range c.Env {
		if strings.HasPrefix(e, "LCM_") {
			t.Fatalf("LCM variable passed to git: %q", e)
		}
	}
	if !slices.Contains(c.Env, "GIT_ALLOW_PROTOCOL="+execx.GitAllowProtocol) || !slices.Contains(c.Env, "GIT_TERMINAL_PROMPT=0") {
		t.Fatalf("env = %q", c.Env)
	}
}

func TestExecGitRedactsCredentials(t *testing.T) {
	f := (&execx.Fake{}).Reply("ls-remote", "fatal: unable to access 'https://user:s3cret@example.com/x.git/': denied\n", 128)
	g := trigger.ExecGit{Runner: f}
	_, err := g.RemoteHead(context.Background(), "https://user:s3cret@example.com/x.git", "main")
	if err == nil {
		t.Fatal("want error")
	}
	if strings.Contains(err.Error(), "s3cret") || !strings.Contains(err.Error(), "denied") {
		t.Fatalf("error = %v", err)
	}
}

func TestExecGitTimeoutUsesClock(t *testing.T) {
	clk := clock.NewFake(time.Unix(1_700_000_000, 0))
	f := (&execx.Fake{}).On(func(execx.Cmd) bool { return true }, func(execx.Cmd) (execx.Result, error) {
		return execx.Result{ExitCode: -1}, errors.New("killed")
	})
	g := trigger.ExecGit{Runner: blockingRunner{f: f}, Clock: clk, Timeout: time.Minute}
	errc := make(chan error, 1)
	go func() {
		_, err := g.RemoteHead(context.Background(), "https://example.com/x.git", "main")
		errc <- err
	}()
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	if err := clk.WaitForTimers(ctx, 1); err != nil {
		t.Fatal(err)
	}
	clk.Advance(time.Minute)
	if err := <-errc; !errors.Is(err, trigger.ErrGitTimeout) {
		t.Fatalf("want ErrGitTimeout, got %v", err)
	}
}

// blockingRunner waits for the context to end before answering through f,
// like a git process that hangs until it is killed.
type blockingRunner struct{ f *execx.Fake }

func (b blockingRunner) Run(ctx context.Context, c execx.Cmd) (execx.Result, error) {
	<-ctx.Done()
	return b.f.Run(context.Background(), c)
}
