package app

import (
	"context"
	"errors"
	"log/slog"
	"strings"
	"sync"
	"time"

	"github.com/ACaccel/lcm/internal/model"
	"github.com/ACaccel/lcm/internal/monitor"
	"github.com/ACaccel/lcm/internal/resource"
)

// monitorSyncInterval is the safety net behind the resource change
// subscription: a missed kick (or a change made by another process, such
// as a restore from backup) is picked up within this time.
const monitorSyncInterval = time.Minute

// monitorSyncer keeps the monitor's check set equal to the checks of the
// managed resources.
type monitorSyncer struct {
	res *resource.Store
	mon *monitor.Monitor
	log *slog.Logger

	// mu serializes syncs so that an older snapshot never replaces a newer
	// one.
	mu   sync.Mutex
	kick chan struct{}
}

func newMonitorSyncer(res *resource.Store, mon *monitor.Monitor, log *slog.Logger) *monitorSyncer {
	return &monitorSyncer{res: res, mon: mon, log: log, kick: make(chan struct{}, 1)}
}

// monitorDefs lists the checks of every managed resource of any type.
// Retired resources are not observed, and List never returns unmanaged
// ones.
func monitorDefs(recs []resource.Record) []monitor.Def {
	var defs []monitor.Def
	for _, rec := range recs {
		if rec.Phase != resource.PhaseManaged || rec.Document == nil {
			continue
		}
		for _, c := range rec.Document.Spec.Checks {
			defs = append(defs, monitor.Def{Resource: rec.Name, ResourceType: rec.Type, Labels: rec.Document.Metadata.Labels, Check: c})
		}
	}
	return defs
}

// Sync reads the managed resources and applies their checks to the monitor.
// Invalid check definitions are logged and do not fail the sync, because
// the rest of the checks were applied.
func (s *monitorSyncer) Sync(ctx context.Context) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	// The generation is read before the snapshot, so that the monitor can
	// ignore a resource forgotten while this sync was reading.
	gen := s.mon.Generation()
	recs, err := s.res.List(ctx, resource.Filter{})
	if err != nil {
		return err
	}
	err = s.mon.SyncAt(ctx, gen, monitorDefs(recs))
	var issues model.Issues
	if errors.As(err, &issues) {
		for _, is := range issues {
			// Paths start with "resource/check".
			name, _, _ := strings.Cut(is.Path, "/")
			s.log.Warn("invalid check definition", "resource", name, "path", is.Path, "error", is.Message)
		}
		return nil
	}
	return err
}

// Changed makes the monitor forget a resource that left observation and
// then requests a sync: a sync that removed the checks of a retired machine
// or router first would recompute its health from its native input alone
// and announce a change. It is a resource store subscriber.
func (s *monitorSyncer) Changed(c resource.Change) {
	if s.leftObservation(c) {
		s.mon.Forget(c.Name)
	}
	select {
	case s.kick <- struct{}{}:
	default:
	}
}

func (s *monitorSyncer) leftObservation(c resource.Change) bool {
	switch c.Change {
	case resource.ChangeUnmanaged:
		return true
	case resource.ChangePhase:
		rec, err := s.res.Get(context.Background(), "@"+c.ID)
		if err != nil {
			s.log.Warn("resource phase not read", "resource", c.Name, "error", err)
			return false
		}
		return rec.Phase != resource.PhaseManaged
	default:
		return false
	}
}

// syncQuietly runs Sync and logs failures; a closed monitor or a cancelled
// context is the normal end of the process.
func (s *monitorSyncer) syncQuietly(ctx context.Context) {
	if err := s.Sync(ctx); err != nil && ctx.Err() == nil && !errors.Is(err, monitor.ErrClosed) {
		s.log.Warn("monitor sync failed", "error", err)
	}
}
