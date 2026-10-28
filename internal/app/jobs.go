package app

import (
	"context"
	"log/slog"
	"time"

	"github.com/ACaccel/lcm/internal/clock"
)

// every runs fn immediately and then once per interval on clk until ctx
// ends. The next wait starts after fn returns, so a slow job never overlaps
// itself. kick, when non-nil, runs fn early.
func every(ctx context.Context, clk clock.Clock, interval time.Duration, kick <-chan struct{}, fn func(context.Context)) {
	for {
		if ctx.Err() != nil {
			return
		}
		fn(ctx)
		timer := make(chan struct{})
		t := clk.AfterFunc(interval, func() { close(timer) })
		select {
		case <-ctx.Done():
			t.Stop()
			return
		case <-kick:
			t.Stop()
		case <-timer:
		}
	}
}

// retentionJobs deletes expired data. Each job reports how many rows it
// removed; failures are logged and retried at the next interval.
func (a *App) retentionJobs(ctx context.Context) {
	jobs := []struct {
		name string
		run  func(context.Context) (int64, error)
	}{
		{"engine runs", func(ctx context.Context) (int64, error) {
			return a.Engine.Prune(ctx, a.Config.RunRetentionKeep, a.Config.RunRetentionAge)
		}},
		{"timeline", func(ctx context.Context) (int64, error) {
			return a.Catalog.PruneTimeline(ctx, a.Config.TimelineRetention)
		}},
		{"deployments", func(ctx context.Context) (int64, error) {
			return a.Catalog.PruneDeployments(ctx, a.Config.DeploymentKeep)
		}},
		{"action completions", func(ctx context.Context) (int64, error) {
			return a.Catalog.PruneCompletions(ctx, a.Config.RunRetentionAge)
		}},
		{"expired locks", func(ctx context.Context) (int64, error) {
			return a.Locks.PurgeExpired(ctx)
		}},
	}
	for _, j := range jobs {
		n, err := j.run(ctx)
		switch {
		case err != nil:
			if ctx.Err() == nil {
				a.log.Warn("retention job failed", "job", j.name, "error", err)
			}
		case n > 0:
			a.log.Info("retention job deleted rows", "job", j.name, "rows", n)
		default:
			a.log.Log(ctx, slog.LevelDebug, "retention job found nothing to delete", "job", j.name)
		}
	}
}
