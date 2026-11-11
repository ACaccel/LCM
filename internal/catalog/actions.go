package catalog

import (
	"context"
	"errors"
	"fmt"
	"io/fs"
	"os"

	"github.com/ACaccel/lcm/internal/deployhelper"
	"github.com/ACaccel/lcm/internal/execx"
	"github.com/ACaccel/lcm/internal/model"
	rt "github.com/ACaccel/lcm/internal/runtime"
)

// Start starts a stopped, failed or newly registered service. With an
// instance, it starts that instance of a running or failed service.
func (m *Manager) Start(ctx context.Context, name string, opts ActionOptions) (ActionResult, error) {
	return m.exec(ctx, name, ActionStart, opts, func(ctx context.Context, s *session, res *ActionResult) error {
		if opts.Instance != "" {
			return s.instanceAction(ctx, rt.InstanceStart, res)
		}
		if err := s.adapter.Start(ctx, s.rt); err != nil {
			return s.settleAfter(ctx, err)
		}
		return s.moveDone(ctx, StateRunning)
	})
}

// Stop stops a running or failed service. With an instance, it stops that
// instance and keeps it stopped until it is started by name again; the
// service stays running on its other instances.
func (m *Manager) Stop(ctx context.Context, name string, opts ActionOptions) (ActionResult, error) {
	return m.exec(ctx, name, ActionStop, opts, func(ctx context.Context, s *session, res *ActionResult) error {
		if opts.Instance != "" {
			return s.instanceAction(ctx, rt.InstanceStop, res)
		}
		if err := s.adapter.Stop(ctx, s.rt); err != nil {
			return s.settleAfter(ctx, err)
		}
		return s.moveDone(ctx, StateStopped)
	})
}

// Restart restarts the service; a stopped or failed service is started.
// With an instance, it restarts (and so starts) that instance.
func (m *Manager) Restart(ctx context.Context, name string, opts ActionOptions) (ActionResult, error) {
	return m.exec(ctx, name, ActionRestart, opts, func(ctx context.Context, s *session, res *ActionResult) error {
		if opts.Instance != "" {
			return s.instanceAction(ctx, rt.InstanceRestart, res)
		}
		if err := s.restartOrStart(ctx); err != nil {
			return s.settleAfter(ctx, err)
		}
		return s.moveDone(ctx, StateRunning)
	})
}

// settleAfter records the state a failed start, stop or restart leaves,
// unless the deploy helper refused it before changing anything, such as a
// service with instances that was never deployed: its state then stays.
func (s *session) settleAfter(ctx context.Context, err error) error {
	if refusedByHelper(err) {
		return err
	}
	s.settle(ctx, false)
	return err
}

// refusedByHelper reports a helper answer that proves nothing was done.
func refusedByHelper(err error) bool {
	var he *rt.HelperError
	return errors.As(err, &he) && !he.Unknown && (he.Code == deployhelper.CodeRefused || he.Code == deployhelper.CodeNotFound || he.Code == deployhelper.CodeBusy)
}

// restartOrStart restarts an active service and starts an inactive one,
// since not every runtime's restart brings up stopped processes.
func (s *session) restartOrStart(ctx context.Context) error {
	if !rt.CapabilitiesOf(s.rt).Start {
		return s.adapter.Restart(ctx, s.rt)
	}
	if st, err := s.adapter.Status(ctx, s.rt); err == nil && st.State != rt.StateActive && st.State != rt.StateUnknown {
		return s.adapter.Start(ctx, s.rt)
	}
	return s.adapter.Restart(ctx, s.rt)
}

// Backup runs the service's backup: its command, or for a managed service
// the typed backup that the deploy helper performs. The state does not
// change.
func (m *Manager) Backup(ctx context.Context, name string, opts ActionOptions) (ActionResult, error) {
	return m.exec(ctx, name, ActionBackup, opts, func(ctx context.Context, s *session, res *ActionResult) error {
		b := s.cfg.Backup
		if b == nil {
			return ErrNoBackup
		}
		if b.Typed() {
			return s.typedBackup(ctx, res)
		}
		return s.runCommand(ctx, model.Command{Command: b.Command, Timeout: b.Timeout}, res)
	})
}

// RunJob runs one of the service's configured jobs. The state does not change.
func (m *Manager) RunJob(ctx context.Context, name string, opts ActionOptions) (ActionResult, error) {
	return m.exec(ctx, name, ActionRunJob, opts, func(ctx context.Context, s *session, res *ActionResult) error {
		job, ok := s.cfg.Jobs[opts.Job]
		if !ok {
			return fmt.Errorf("%w: %q", ErrUnknownJob, opts.Job)
		}
		return s.runCommand(ctx, job, res)
	})
}

// maxJobOutput bounds the command output kept on the timeline and in the
// result.
const maxJobOutput = 2 << 10

func (s *session) runCommand(ctx context.Context, c model.Command, res *ActionResult) error {
	dir, err := s.workDir()
	if err != nil {
		return err
	}
	r, err := s.m.runner.Run(ctx, execx.Cmd{
		Shell: c.Command, Dir: dir, Timeout: c.EffectiveTimeout(),
	})
	res.Output = tailCap(r.Output, maxJobOutput)
	if err != nil {
		return fmt.Errorf("%s command: %w", s.action, err)
	}
	return nil
}

// workDir is the build checkout of a service with a source, so that
// relative paths in commands behave as they do for deploys. Until the first
// deploy has cloned it there is no such directory, and running the command
// elsewhere would change what its relative paths mean.
func (s *session) workDir() (string, error) {
	if s.cfg.Source == nil {
		return "", nil
	}
	dir := s.m.CheckoutDir(s.name)
	if _, err := os.Stat(dir); err != nil {
		if errors.Is(err, fs.ErrNotExist) {
			return "", fmt.Errorf("%w (%s)", ErrNoCheckout, dir)
		}
		return "", fmt.Errorf("catalog: build checkout: %w", err)
	}
	return dir, nil
}
