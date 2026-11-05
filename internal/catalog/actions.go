package catalog

import (
	"context"
	"errors"
	"fmt"
	"io/fs"
	"os"

	"github.com/ACaccel/lcm/internal/execx"
	"github.com/ACaccel/lcm/internal/model"
	rt "github.com/ACaccel/lcm/internal/runtime"
)

// Start starts a stopped, failed or newly registered service.
func (m *Manager) Start(ctx context.Context, name string, opts ActionOptions) (ActionResult, error) {
	return m.exec(ctx, name, ActionStart, opts, func(ctx context.Context, s *session, _ *ActionResult) error {
		if err := s.adapter.Start(ctx, s.rt); err != nil {
			s.settle(ctx, false)
			return err
		}
		return s.moveDone(ctx, StateRunning)
	})
}

// Stop stops a running or failed service.
func (m *Manager) Stop(ctx context.Context, name string, opts ActionOptions) (ActionResult, error) {
	return m.exec(ctx, name, ActionStop, opts, func(ctx context.Context, s *session, _ *ActionResult) error {
		if err := s.adapter.Stop(ctx, s.rt); err != nil {
			s.settle(ctx, false)
			return err
		}
		return s.moveDone(ctx, StateStopped)
	})
}

// Restart restarts the service; a stopped or failed service is started.
func (m *Manager) Restart(ctx context.Context, name string, opts ActionOptions) (ActionResult, error) {
	return m.exec(ctx, name, ActionRestart, opts, func(ctx context.Context, s *session, _ *ActionResult) error {
		if err := s.restartOrStart(ctx); err != nil {
			s.settle(ctx, false)
			return err
		}
		return s.moveDone(ctx, StateRunning)
	})
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

// Backup runs the service's backup command. The state does not change.
func (m *Manager) Backup(ctx context.Context, name string, opts ActionOptions) (ActionResult, error) {
	return m.exec(ctx, name, ActionBackup, opts, func(ctx context.Context, s *session, res *ActionResult) error {
		b := s.cfg.Backup
		if b == nil {
			return ErrNoBackup
		}
		return s.runCommand(ctx, *b, res)
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
