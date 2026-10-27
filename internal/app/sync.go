package app

import (
	"context"
	"errors"
	"log/slog"
	"strings"
	"sync"
	"time"

	"github.com/ACaccel/lcm/internal/catalog"
	"github.com/ACaccel/lcm/internal/model"
	"github.com/ACaccel/lcm/internal/monitor"
)

// monitorSyncInterval is the safety net behind the catalog change hook: a
// missed kick (or a change made by another process, such as a restore from
// backup) is picked up within this time.
const monitorSyncInterval = time.Minute

// monitorSyncer keeps the monitor's check set equal to the catalog.
type monitorSyncer struct {
	cat *catalog.Catalog
	mon *monitor.Monitor
	log *slog.Logger

	// mu serializes syncs so that an older catalog snapshot never replaces
	// a newer one.
	mu   sync.Mutex
	kick chan struct{}
}

func newMonitorSyncer(cat *catalog.Catalog, mon *monitor.Monitor, log *slog.Logger) *monitorSyncer {
	return &monitorSyncer{cat: cat, mon: mon, log: log, kick: make(chan struct{}, 1)}
}

// Kick requests a sync without blocking. It is the catalog change hook,
// which runs inside catalog writes and so must be cheap.
func (s *monitorSyncer) Kick() {
	select {
	case s.kick <- struct{}{}:
	default:
	}
}

// monitorDefs lists the checks of every service that is not retired.
func monitorDefs(recs []catalog.Record) []monitor.Def {
	var defs []monitor.Def
	for _, rec := range recs {
		if rec.State == catalog.StateRetired || rec.Spec == nil {
			continue
		}
		for _, c := range rec.Spec.Spec.Checks {
			defs = append(defs, monitor.Def{Service: rec.Name, Labels: rec.Spec.Metadata.Labels, Check: c})
		}
	}
	return defs
}

// Sync reads the catalog and applies its checks to the monitor. Invalid
// check definitions are logged and do not fail the sync, because the rest
// of the checks were applied.
func (s *monitorSyncer) Sync(ctx context.Context) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	recs, err := s.cat.List(ctx)
	if err != nil {
		return err
	}
	err = s.mon.Sync(ctx, monitorDefs(recs))
	var issues model.Issues
	if errors.As(err, &issues) {
		for _, is := range issues {
			// Paths start with "service/check".
			service, _, _ := strings.Cut(is.Path, "/")
			s.log.Warn("invalid check definition", "service", service, "path", is.Path, "error", is.Message)
		}
		return nil
	}
	return err
}

// syncQuietly runs Sync and logs failures; a closed monitor or a cancelled
// context is the normal end of the process.
func (s *monitorSyncer) syncQuietly(ctx context.Context) {
	if err := s.Sync(ctx); err != nil && ctx.Err() == nil && !errors.Is(err, monitor.ErrClosed) {
		s.log.Warn("monitor sync failed", "error", err)
	}
}
