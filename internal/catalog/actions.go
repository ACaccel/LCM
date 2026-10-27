package catalog

import (
	"context"
	"errors"
	"fmt"

	"github.com/ACaccel/lcm/internal/execx"
	"github.com/ACaccel/lcm/internal/model"
	rt "github.com/ACaccel/lcm/internal/runtime"
)

// Start starts a stopped, failed or newly registered service.
func (m *Manager) Start(ctx context.Context, name string, opts ActionOptions) (ActionResult, error) {
	return m.exec(ctx, name, ActionStart, opts, func(ctx context.Context, s *session, _ *ActionResult) error {
		if err := s.adapter.Start(ctx, s.svc); err != nil {
			s.settle(ctx, false)
			return err
		}
		return s.move(ctx, StateRunning)
	})
}

// Stop stops a running or failed service.
func (m *Manager) Stop(ctx context.Context, name string, opts ActionOptions) (ActionResult, error) {
	return m.exec(ctx, name, ActionStop, opts, func(ctx context.Context, s *session, _ *ActionResult) error {
		if err := s.adapter.Stop(ctx, s.svc); err != nil {
			s.settle(ctx, false)
			return err
		}
		return s.move(ctx, StateStopped)
	})
}

// Restart restarts the service; a stopped or failed service is started.
func (m *Manager) Restart(ctx context.Context, name string, opts ActionOptions) (ActionResult, error) {
	return m.exec(ctx, name, ActionRestart, opts, func(ctx context.Context, s *session, _ *ActionResult) error {
		if err := s.restartOrStart(ctx); err != nil {
			s.settle(ctx, false)
			return err
		}
		return s.move(ctx, StateRunning)
	})
}

// restartOrStart restarts an active service and starts an inactive one,
// since not every runtime's restart brings up stopped processes.
func (s *session) restartOrStart(ctx context.Context) error {
	if st, err := s.adapter.Status(ctx, s.svc); err == nil && st.State != rt.StateActive && st.State != rt.StateUnknown {
		return s.adapter.Start(ctx, s.svc)
	}
	return s.adapter.Restart(ctx, s.svc)
}

// Retire stops the service and marks it retired, the terminal state. The
// stop is skipped for services that are not running and for external ones,
// which LCM cannot stop.
func (m *Manager) Retire(ctx context.Context, name string, opts ActionOptions) (ActionResult, error) {
	return m.exec(ctx, name, ActionRetire, opts, func(ctx context.Context, s *session, _ *ActionResult) error {
		if s.cur == StateRunning || s.cur == StateFailed {
			if err := s.adapter.Stop(ctx, s.svc); err != nil && !errors.Is(err, rt.ErrUnsupported) {
				s.settle(ctx, false)
				return err
			}
		}
		return s.move(ctx, StateRetired)
	})
}

// Backup runs the service's backup command. The state does not change.
func (m *Manager) Backup(ctx context.Context, name string, opts ActionOptions) (ActionResult, error) {
	return m.exec(ctx, name, ActionBackup, opts, func(ctx context.Context, s *session, res *ActionResult) error {
		b := s.svc.Spec.Backup
		if b == nil {
			return ErrNoBackup
		}
		return s.runCommand(ctx, *b, res)
	})
}

// RunJob runs one of the service's configured jobs. The state does not change.
func (m *Manager) RunJob(ctx context.Context, name string, opts ActionOptions) (ActionResult, error) {
	return m.exec(ctx, name, ActionRunJob, opts, func(ctx context.Context, s *session, res *ActionResult) error {
		job, ok := s.svc.Spec.Jobs[opts.Job]
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
	r, err := s.m.runner.Run(ctx, execx.Cmd{
		Shell: c.Command, Dir: s.workDir(), Timeout: c.EffectiveTimeout(),
	})
	res.Output = tailCap(r.Output, maxJobOutput)
	if err != nil {
		return fmt.Errorf("%s command: %w", s.action, err)
	}
	return nil
}

// workDir is the source checkout when there is one, so that relative paths
// in commands behave as they do for deploys.
func (s *session) workDir() string {
	if src := s.svc.Spec.Source; src != nil {
		return src.Repo
	}
	return ""
}
