package app

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/ACaccel/lcm/internal/catalog"
	"github.com/ACaccel/lcm/internal/execx"
	"github.com/ACaccel/lcm/internal/lock"
	"github.com/ACaccel/lcm/internal/monitor"
)

// lockAdapter lets the catalog manager take service locks from the durable
// lock manager.
type lockAdapter struct{ m *lock.Manager }

var _ catalog.Locker = lockAdapter{}

// Acquire never waits. A lock held by someone else is reported as
// catalog.ErrLockHeld so the manager and the lifecycle nodes can tell a
// busy service from a failure; the lock error stays in the chain for its
// holder details.
func (l lockAdapter) Acquire(ctx context.Context, name, owner string, ttl time.Duration) (func(context.Context) error, error) {
	release, err := l.m.Acquire(ctx, name, owner, ttl)
	if err != nil {
		if errors.Is(err, lock.ErrHeld) {
			return nil, fmt.Errorf("%w: %w", catalog.ErrLockHeld, err)
		}
		return nil, err
	}
	return release, nil
}

// monitorRunner runs the monitor's check commands through execx, so that
// checks share the environment allowlist, process-group handling and test
// fake of the lifecycle actions.
type monitorRunner struct {
	run execx.Runner
	// binaries maps a program name used by built-in checks to the
	// configured path, so a custom systemctl or docker is honoured.
	binaries map[string]string
}

var _ monitor.Runner = monitorRunner{}

// Run reports a non-zero exit as a normal result and any other failure
// (not started, timed out, cancelled) as an error, as monitor.Runner
// requires. execx merges stdout and stderr; the combined output is
// offered as both streams because probes read the stream that suits the
// outcome (state from stdout, failure reasons from stderr).
func (r monitorRunner) Run(ctx context.Context, c monitor.Command) (monitor.CommandResult, error) {
	name := c.Name
	if path, ok := r.binaries[name]; ok {
		name = path
	}
	argv := make([]string, 0, 1+len(c.Args))
	argv = append(argv, name)
	argv = append(argv, c.Args...)

	res, err := r.run.Run(ctx, execx.Cmd{Argv: argv})
	out := monitor.CommandResult{ExitCode: res.ExitCode, Stdout: res.Output, Stderr: res.Output}
	var exit *execx.ExitError
	switch {
	case err == nil:
		return out, nil
	case errors.As(err, &exit):
		out.ExitCode = exit.Code
		return out, nil
	default:
		return out, err
	}
}
