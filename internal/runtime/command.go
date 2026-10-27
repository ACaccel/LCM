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
// that cannot be run at all yields unknown.
type command struct {
	run  execx.Runner
	opts Options
}

func (c *command) Status(ctx context.Context, svc *model.Service) (Status, error) {
	rt := svc.Spec.Runtime
	if rt.Status == "" {
		return Status{State: StateUnknown, Detail: map[string]string{"reason": "no status command"}}, nil
	}
	res, err := c.run.Run(ctx, execx.Cmd{Shell: rt.Status, Timeout: c.opts.StatusTimeout})
	detail := map[string]string{"exitCode": fmt.Sprint(res.ExitCode)}
	if err == nil {
		return Status{State: StateActive, Detail: detail}, nil
	}
	var ee *execx.ExitError
	if errors.As(err, &ee) {
		return Status{State: StateInactive, Detail: detail}, nil
	}
	return Status{State: StateUnknown}, fmt.Errorf("status command: %w", err)
}

func (c *command) exec(ctx context.Context, name, script string) error {
	_, err := run(ctx, c.run, execx.Cmd{Shell: script, Timeout: c.opts.ActionTimeout})
	if err != nil {
		return fmt.Errorf("%s command: %w", name, err)
	}
	return nil
}

func (c *command) Start(ctx context.Context, svc *model.Service) error {
	return c.exec(ctx, "start", svc.Spec.Runtime.Start)
}

func (c *command) Stop(ctx context.Context, svc *model.Service) error {
	return c.exec(ctx, "stop", svc.Spec.Runtime.Stop)
}

// Restart uses the restart command when configured and stop followed by
// start otherwise.
func (c *command) Restart(ctx context.Context, svc *model.Service) error {
	if r := svc.Spec.Runtime.Restart; r != "" {
		return c.exec(ctx, "restart", r)
	}
	if err := c.Stop(ctx, svc); err != nil {
		return err
	}
	return c.Start(ctx, svc)
}
