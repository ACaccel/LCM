package execx

import (
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"strings"
	"syscall"
	"time"
)

// defaultEnvAllow lists the variables copied from the parent process. It is
// deliberately short so that LCM's own secrets never reach child processes.
// XDG_RUNTIME_DIR and DBUS_SESSION_BUS_ADDRESS are needed by `systemctl
// --user`; DOCKER_HOST and DOCKER_CONFIG locate the Docker daemon and its
// client configuration.
var defaultEnvAllow = []string{
	"PATH", "HOME", "USER", "LOGNAME", "LANG", "LC_ALL", "TZ", "TMPDIR",
	"XDG_RUNTIME_DIR", "DBUS_SESSION_BUS_ADDRESS", "DOCKER_HOST", "DOCKER_CONFIG",
}

const defaultPath = "/usr/local/sbin:/usr/local/bin:/usr/sbin:/usr/bin:/sbin:/bin"

// waitDelay bounds how long Run waits for output pipes after the process
// exits. A start command that daemonizes leaves a child holding the pipe.
const waitDelay = 2 * time.Second

// Real runs commands on the local machine.
type Real struct {
	allow  []string
	lookup func(string) (string, bool)
}

// Option customizes Real.
type Option func(*Real)

// WithEnvAllow replaces the list of variables inherited from the parent.
func WithEnvAllow(names ...string) Option {
	return func(r *Real) { r.allow = names }
}

// WithEnvLookup replaces the source of inherited variables, for tests.
func WithEnvLookup(f func(string) (string, bool)) Option {
	return func(r *Real) { r.lookup = f }
}

// New returns a Runner for the local machine.
func New(opts ...Option) *Real {
	r := &Real{allow: defaultEnvAllow, lookup: os.LookupEnv}
	for _, o := range opts {
		o(r)
	}
	return r
}

func (r *Real) environ(extra []string) []string {
	env := make([]string, 0, len(r.allow)+len(extra))
	hasPath := false
	for _, name := range r.allow {
		if v, ok := r.lookup(name); ok {
			env = append(env, name+"="+v)
			hasPath = hasPath || name == "PATH"
		}
	}
	if !hasPath {
		env = append(env, "PATH="+defaultPath)
	}
	// Later entries win in exec.Cmd.Env.
	return append(env, extra...)
}

// Run implements Runner.
func (r *Real) Run(ctx context.Context, c Cmd) (Result, error) {
	if err := c.validate(); err != nil {
		return Result{ExitCode: -1}, err
	}
	if err := ctx.Err(); err != nil {
		return Result{ExitCode: -1}, err
	}

	runCtx := ctx
	if c.Timeout > 0 {
		var cancel context.CancelFunc
		runCtx, cancel = context.WithTimeoutCause(ctx, c.Timeout, ErrTimeout)
		defer cancel()
	}

	var cmd *exec.Cmd
	if c.Shell != "" {
		cmd = exec.CommandContext(runCtx, "sh", "-c", c.Shell) //nolint:gosec // operator-defined command, run without untrusted input
	} else {
		cmd = exec.CommandContext(runCtx, c.Argv[0], c.Argv[1:]...) //nolint:gosec // argv comes from validated configuration
	}
	cmd.Dir = c.Dir
	cmd.Env = r.environ(c.Env)
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
	cmd.WaitDelay = waitDelay
	cmd.Cancel = func() error {
		// Kill the whole group so that children of a shell do not outlive
		// the command.
		err := syscall.Kill(-cmd.Process.Pid, syscall.SIGKILL)
		if err != nil && !errors.Is(err, syscall.ESRCH) {
			return err
		}
		return nil
	}

	limit := c.MaxOutput
	if limit <= 0 {
		limit = DefaultMaxOutput
	}
	out := &tail{max: limit}
	cmd.Stdout = out
	cmd.Stderr = out

	start := time.Now()
	err := cmd.Run()
	res := Result{ExitCode: -1, Duration: time.Since(start)}
	res.Output, res.Truncated = out.result()

	if cause := context.Cause(runCtx); cause != nil && runCtx.Err() != nil {
		// The context ended the process (or raced with its exit); report
		// why rather than the resulting "signal: killed".
		if errors.Is(cause, ErrTimeout) {
			return res, fmt.Errorf("%w after %s: %s", ErrTimeout, c.Timeout, c.Display())
		}
		return res, fmt.Errorf("%s: %w", c.Display(), cause)
	}
	if err == nil || errors.Is(err, exec.ErrWaitDelay) {
		if cmd.ProcessState != nil {
			res.ExitCode = cmd.ProcessState.ExitCode()
		}
		if res.ExitCode > 0 {
			return res, &ExitError{Code: res.ExitCode}
		}
		return res, nil
	}
	var ee *exec.ExitError
	if errors.As(err, &ee) {
		res.ExitCode = ee.ExitCode()
		if res.ExitCode > 0 {
			return res, &ExitError{Code: res.ExitCode}
		}
		return res, fmt.Errorf("%s: %s", c.Display(), strings.TrimPrefix(ee.Error(), "exit status "))
	}
	return res, fmt.Errorf("%s: %w", c.Display(), err)
}
