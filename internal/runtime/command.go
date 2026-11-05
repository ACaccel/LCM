package runtime

import (
	"context"
	"errors"
	"fmt"

	"github.com/ACaccel/lcm/internal/execx"
	"github.com/ACaccel/lcm/internal/model"
)

// command runs the operator's shell commands. The status command reports
// through its exit code: 0 is active, any other code is inactive; a command
// that cannot be run at all, including the shell's 126 (not executable) and
// 127 (not found), yields unknown. An action without a command is
// unsupported (see CapabilitiesOf).
type command struct {
	run  execx.Runner
	opts Options
}

// Exit codes sh uses when it cannot run the command it was given.
const (
	shellCannotExecute = 126
	shellNotFound      = 127
)

func (c *command) Status(ctx context.Context, rt model.Runtime) (Status, error) {
	if rt.Status == "" {
		return Status{State: StateUnknown, Detail: map[string]string{"reason": "no status command"}}, nil
	}
	res, err := c.run.Run(ctx, execx.Cmd{Shell: rt.Status, Timeout: c.opts.StatusTimeout})
	detail := map[string]string{"exitCode": fmt.Sprint(res.ExitCode)}
	if err == nil {
		return Status{State: StateActive, Detail: detail}, nil
	}
	var ee *execx.ExitError
	if errors.As(err, &ee) && ee.Code != shellCannotExecute && ee.Code != shellNotFound {
		return Status{State: StateInactive, Detail: detail}, nil
	}
	return Status{State: StateUnknown}, fmt.Errorf("status command: %w", err)
}

func (c *command) exec(ctx context.Context, name, script string) error {
	if script == "" {
		return fmt.Errorf("%w: %s (the service has no %s command)", ErrUnsupported, name, name)
	}
	_, err := run(ctx, c.run, execx.Cmd{Shell: script, Timeout: c.opts.ActionTimeout})
	if err != nil {
		return fmt.Errorf("%s command: %w", name, err)
	}
	return nil
}

func (c *command) Start(ctx context.Context, rt model.Runtime) error {
	return c.exec(ctx, "start", rt.Start)
}

func (c *command) Stop(ctx context.Context, rt model.Runtime) error {
	return c.exec(ctx, "stop", rt.Stop)
}

// Restart uses the restart command when configured and stop followed by
// start otherwise.
func (c *command) Restart(ctx context.Context, rt model.Runtime) error {
	if rt.Restart != "" {
		return c.exec(ctx, "restart", rt.Restart)
	}
	if rt.Start == "" || rt.Stop == "" {
		return c.exec(ctx, "restart", "")
	}
	if err := c.Stop(ctx, rt); err != nil {
		return err
	}
	return c.Start(ctx, rt)
}
