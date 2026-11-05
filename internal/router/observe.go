package router

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"slices"
	"sort"
	"time"

	"github.com/ACaccel/lcm/internal/model"
	"github.com/ACaccel/lcm/internal/resource"
	"github.com/ACaccel/lcm/internal/router/xiaomi"
)

// Observe samples a managed router once and stores the attempt, whatever
// its outcome. Observations of one router never overlap, and all routers
// share the concurrency limit.
func (m *Manager) Observe(ctx context.Context, ref string) (Record, error) {
	r, err := m.Get(ctx, ref)
	if err != nil {
		return Record{}, err
	}
	if r.Phase != resource.PhaseManaged {
		return Record{}, precondition("router is not managed")
	}
	locks := m.locksFor(r.ID)
	locks.observe.Lock()
	defer locks.observe.Unlock()
	if err := m.acquire(ctx); err != nil {
		return Record{}, err
	}
	snap, err := m.sample(ctx, r)
	m.release()
	if err != nil {
		return Record{}, err
	}
	// The document may have changed while the router answered; the sample
	// belongs to the version it was taken for.
	events, err := m.commitSample(ctx, r, snap)
	if err != nil {
		return Record{}, err
	}
	m.publish(events)
	m.settle(ctx, r.ID)
	latest, err := m.Get(ctx, "@"+r.ID)
	if err != nil {
		return Record{}, err
	}
	m.feedNative(latest)
	return latest, nil
}

// sample asks the router for its status. A failed attempt is a snapshot
// with a fixed reason; only the end of ctx is an error.
func (m *Manager) sample(ctx context.Context, r Record) (Snapshot, error) {
	snap := Snapshot{ObservedAt: m.clk.Now().UTC(), Sources: map[string]Coverage{}}
	c, err := m.client(r)
	if err != nil {
		var nc errNotConfigured
		if !errors.As(err, &nc) {
			return Snapshot{}, err
		}
		snap.Outcome, snap.Reason = OutcomeNotConfigured, nc.Error()
		return snap, nil
	}
	st, err := c.Status(ctx)
	if ctx.Err() != nil {
		return Snapshot{}, context.Cause(ctx)
	}
	fill(&snap, st)
	switch {
	case err == nil:
		snap.Outcome = OutcomeOK
	case errors.Is(err, xiaomi.ErrAuth):
		snap.Outcome, snap.Reason = OutcomeAuthFailed, resource.ReasonAuthRejected
		m.dropClient(r.Connection)
	case errors.Is(err, xiaomi.ErrUnreachable):
		snap.Outcome, snap.Reason = OutcomeUnreachable, resource.ReasonUnreachable
	default:
		snap.Outcome, snap.Reason = OutcomeUnavailable, resource.ReasonNoStatus
	}
	if err != nil {
		// A router that stays down is logged once per change of outcome,
		// not on every attempt. Client errors never contain the password or
		// session token.
		level := slog.LevelWarn
		if prev := r.Status.Snapshot; prev != nil && prev.Outcome == snap.Outcome {
			level = slog.LevelDebug
		}
		m.log.Log(ctx, level, "router observation failed", "router", r.Name, "outcome", snap.Outcome, "error", err)
	}
	return snap, nil
}

func fill(s *Snapshot, st xiaomi.Status) {
	s.CPUPercent, s.CPUCores, s.MemoryPercent = st.CPUPercent, st.CPUCores, st.MemoryPercent
	s.UptimeSeconds, s.TemperatureC, s.Clients = st.UptimeSeconds, st.TemperatureC, st.Clients
	s.Model, s.Firmware = st.Model, st.Firmware
	s.WAN = WAN{
		Link: st.WAN.Link, Status: st.WAN.Status, UptimeSeconds: st.WAN.UptimeSeconds, Type: st.WAN.Type,
		UpBytesPerSec: st.WAN.UpBytesPerSec, DownBytesPerSec: st.WAN.DownBytesPerSec,
		UploadedBytes: st.WAN.UploadedBytes, DownloadedBytes: st.WAN.DownloadedBytes,
	}
	for name, c := range st.Sources {
		s.Sources[name] = Coverage{State: string(c.State), Reason: c.Reason}
	}
}

func reachabilityOf(outcome string) string {
	switch outcome {
	case OutcomeOK, OutcomeAuthFailed, OutcomeUnavailable:
		return reachable
	case OutcomeUnreachable:
		return unreachable
	}
	return unknown
}

func stateOf(outcome string) string {
	switch outcome {
	case OutcomeOK:
		return resource.StateOnline
	case OutcomeUnreachable:
		return resource.StateOffline
	}
	return resource.StateUnknown
}

func bootAt(s *Snapshot) (time.Time, bool) {
	if s == nil || s.Outcome != OutcomeOK || s.UptimeSeconds == nil || *s.UptimeSeconds < 0 {
		return time.Time{}, false
	}
	return s.ObservedAt.Add(-time.Duration(*s.UptimeSeconds * float64(time.Second))), true
}

// commitSample stores a snapshot, its minute point, the events it causes
// and the new status in one transaction.
func (m *Manager) commitSample(ctx context.Context, r Record, snap Snapshot) ([]resource.Event, error) {
	var published []resource.Event
	err := m.db.Write(ctx, func(tx *sql.Tx) error {
		published = nil
		cur, err := readCurrent(ctx, tx, r.ID)
		if err != nil {
			return err
		}
		if cur.version != r.Version || cur.phase != resource.PhaseManaged {
			return ErrConflict
		}
		old := cur.status
		if old.Baseline == nil || old.DocumentVersion != cur.version {
			old.Baseline = map[string]transition{}
		}
		staleAfter := r.Config.StaleAfter.D()
		continuous := old.Snapshot != nil && old.DocumentVersion == cur.version && snap.ObservedAt.After(old.Snapshot.ObservedAt) &&
			snap.ObservedAt.Sub(old.Snapshot.ObservedAt) <= staleAfter
		next := storedStatus{
			Status:   Status{Snapshot: &snap, Fresh: true, Reachability: reachabilityOf(snap.Outcome), BootAt: old.BootAt},
			Baseline: old.Baseline, DocumentVersion: cur.version, State: stateOf(snap.Outcome), Since: snap.ObservedAt,
			LastKnown: old.LastKnown, Sources: old.Sources,
		}
		if continuous && old.State == next.State && !old.Since.IsZero() {
			next.Since = old.Since
		}
		if !continuous {
			for key, st := range next.Baseline {
				st.Count = 0
				next.Baseline[key] = st
			}
		}

		var events []resource.Event
		if next.Reachability != unknown {
			if old.LastKnown != "" && old.LastKnown != next.Reachability {
				events = append(events, resource.Event{Kind: "router.reachability.changed", Severity: severity(next.Reachability), Reason: next.Reachability})
			}
			next.LastKnown = next.Reachability
		}
		if at, ok := bootAt(&snap); ok {
			if old.BootAt != nil && at.After(old.BootAt.Add(bootTolerance)) {
				events = append(events, resource.Event{Kind: "router.boot.changed", Severity: "info", Reason: resource.ReasonUptimeRestarted})
			}
			next.BootAt = &at
		}
		if snap.Outcome == OutcomeOK {
			if old.Sources != nil {
				events = append(events, collectorChanges(old.Sources, snap.Sources)...)
			}
			next.Sources = snap.Sources
		}
		events = append(events, evaluate(&next, r.Config.Thresholds)...)
		if old.Health != "" && old.Health != next.Health {
			events = append(events, resource.Event{Kind: "router.health.changed", Severity: severity(next.Health), Reason: next.Health})
		}

		body, err := encode(snap)
		if err != nil {
			return err
		}
		res, err := tx.ExecContext(ctx, `INSERT INTO resource_snapshots(resource_id,observed_at,body) VALUES(?,?,?)`, r.ID, snap.ObservedAt.UnixMilli(), body)
		if err != nil {
			return err
		}
		if snap.ID, err = res.LastInsertId(); err != nil {
			return err
		}
		var cpuSum float64
		var cpuCount int
		if snap.CPUPercent != nil {
			cpuSum, cpuCount = *snap.CPUPercent, 1
		}
		if _, err = tx.ExecContext(ctx, `INSERT INTO resource_points(resource_id,bucket_at,boot_id,samples,cpu_sum,cpu_count,body) VALUES(?,?,'',1,?,?,?)
			ON CONFLICT(resource_id,bucket_at,boot_id) DO UPDATE SET samples=samples+1,cpu_sum=cpu_sum+excluded.cpu_sum,cpu_count=cpu_count+excluded.cpu_count,body=excluded.body`,
			r.ID, snap.ObservedAt.Truncate(time.Minute).UnixMilli(), cpuSum, cpuCount, body); err != nil {
			return err
		}
		for i := range events {
			e := &events[i]
			e.ResourceID, e.Source, e.Key = r.ID, "poll", fmt.Sprintf("%d:%d", snap.ID, i)
			e.OccurredAt, e.IngestedAt = snap.ObservedAt, snap.ObservedAt
			if err := resource.InsertEvent(ctx, tx, e); err != nil {
				return err
			}
			published = append(published, *e)
		}
		if err := resource.TrimEvents(ctx, tx); err != nil {
			return err
		}
		return saveStatus(ctx, tx, r.ID, next, snap.ObservedAt)
	})
	if err != nil {
		return nil, err
	}
	return published, nil
}

func severity(state string) string {
	switch state {
	case healthHealthy, reachable, string(xiaomi.CoverageAvailable), "succeeded", "observing", "pending", "submitting":
		return "info"
	}
	return "warning"
}

func collectorChanges(old, next map[string]Coverage) []resource.Event {
	keys := map[string]bool{}
	for k := range old {
		keys[k] = true
	}
	for k := range next {
		keys[k] = true
	}
	names := make([]string, 0, len(keys))
	for k := range keys {
		names = append(names, k)
	}
	sort.Strings(names)
	var events []resource.Event
	for _, k := range names {
		before, after := old[k].State, next[k].State
		if after == "" {
			after = unknown
		}
		if before != "" && before != after {
			events = append(events, resource.Event{Kind: "router.collector.changed", Severity: severity(after), Reason: k + " " + after})
		}
	}
	return events
}

// evaluate sets the native health of the latest attempt and returns the
// threshold transitions it causes. A threshold fires after the configured
// number of consecutive samples at or above it and recovers once a sample
// falls to the threshold minus the hysteresis.
func evaluate(s *storedStatus, t model.RouterThresholds) []resource.Event {
	snap := s.Snapshot
	s.Reasons = []string{}
	switch snap.Outcome {
	case OutcomeOK:
	case OutcomeUnreachable:
		for key, st := range s.Baseline {
			st.Count = 0
			s.Baseline[key] = st
		}
		s.Health, s.Reasons = healthDown, []string{resource.ReasonUnreachable}
		return nil
	default:
		for key, st := range s.Baseline {
			st.Count = 0
			s.Baseline[key] = st
		}
		s.Health, s.Reasons = healthUnknown, []string{snap.Reason}
		return nil
	}
	s.Health = healthHealthy
	var events []resource.Event
	missing := false
	for _, key := range []string{"cpu", "memory"} {
		value, threshold := snap.CPUPercent, t.CPUPercent
		if key == "memory" {
			value, threshold = snap.MemoryPercent, t.MemoryPercent
		}
		st := s.Baseline[key]
		switch {
		case value == nil:
			st.Count = 0
			missing = true
		case *value >= threshold:
			st.Count = min(st.Count+1, t.Consecutive)
			if st.Count >= t.Consecutive && !st.Active {
				st.Active = true
				events = append(events, resource.Event{Kind: "router.threshold.firing", Severity: "warning", Reason: key})
			}
		default:
			st.Count = 0
			if st.Active && *value <= threshold-t.Hysteresis {
				st.Active = false
				events = append(events, resource.Event{Kind: "router.threshold.recovered", Severity: "info", Reason: key})
			}
		}
		s.Baseline[key] = st
		if st.Active {
			s.Health = healthDegraded
			s.Reasons = append(s.Reasons, key)
		}
		if value == nil {
			s.Reasons = append(s.Reasons, resource.NotReported(key))
		}
	}
	if missing {
		s.Health = healthUnknown
	}
	return events
}

// Events returns stored events with an ID above after, oldest first.
func (m *Manager) Events(ctx context.Context, ref string, after int64, limit int) ([]resource.Event, error) {
	r, err := m.Get(ctx, ref)
	if err != nil {
		return nil, err
	}
	limit = max(1, min(limit, 200))
	rows, err := m.db.Reader().QueryContext(ctx, `SELECT id,body FROM resource_events WHERE resource_id=? AND id>? ORDER BY id LIMIT ?`, r.ID, after, limit)
	if err != nil {
		return nil, err
	}
	defer func() { _ = rows.Close() }()
	out := []resource.Event{}
	for rows.Next() {
		var e resource.Event
		var body string
		if err = rows.Scan(&e.ID, &body); err != nil {
			return nil, err
		}
		id := e.ID
		if err = json.Unmarshal([]byte(body), &e); err != nil {
			return nil, err
		}
		e.ID = id
		out = append(out, e)
	}
	return out, rows.Err()
}

// History returns raw snapshots, or minute points with the mean CPU of the
// minute, with an ID above after, oldest first.
func (m *Manager) History(ctx context.Context, ref string, after int64, limit int, minute bool) ([]Snapshot, error) {
	r, err := m.Get(ctx, ref)
	if err != nil {
		return nil, err
	}
	limit = max(1, min(limit, 200))
	query := `SELECT id,body,0,0 FROM resource_snapshots WHERE resource_id=? AND id>? ORDER BY id LIMIT ?`
	if minute {
		query = `SELECT id,body,cpu_sum,cpu_count FROM resource_points WHERE resource_id=? AND id>? ORDER BY id LIMIT ?`
	}
	rows, err := m.db.Reader().QueryContext(ctx, query, r.ID, after, limit)
	if err != nil {
		return nil, err
	}
	defer func() { _ = rows.Close() }()
	out := []Snapshot{}
	for rows.Next() {
		var s Snapshot
		var id int64
		var body string
		var sum float64
		var count int
		if err = rows.Scan(&id, &body, &sum, &count); err != nil {
			return nil, err
		}
		if err = json.Unmarshal([]byte(body), &s); err != nil {
			return nil, err
		}
		s.ID = id
		if minute && count > 0 && s.CPUPercent != nil {
			avg := sum / float64(count)
			s.CPUPercent = &avg
		}
		if s.Sources == nil {
			s.Sources = map[string]Coverage{}
		}
		out = append(out, s)
	}
	return out, rows.Err()
}

// reasonsOf copies health reasons for the monitor.
func reasonsOf(r Record) []string { return slices.Clone(r.Status.Reasons) }
