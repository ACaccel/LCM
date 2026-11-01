package machine

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"sort"
	"time"

	"github.com/ACaccel/lcm/internal/model"
)

// Observe samples once; background and administrator requests cannot overlap.
func (m *Manager) Observe(ctx context.Context, name string) (Record, error) {
	m.pollMu.Lock()
	defer m.pollMu.Unlock()
	r, err := m.Get(ctx, name)
	if err != nil {
		return Record{}, err
	}
	if r.Phase != "managed" {
		return Record{}, precondition("machine is retired")
	}
	if m.observer == nil {
		return Record{}, ErrUnsupported
	}
	// A slow probe must never re-date pre-maintenance evidence as a new sample.
	startedAt := m.clk.Now().UTC()
	snap, probeErr := m.observer.Observe(ctx, r.Document.Spec.Monitoring)
	snap.ObservedAt = startedAt
	if snap.Coverage == nil {
		snap.Coverage = map[string]Coverage{}
	}
	if probeErr != nil {
		snap.Coverage["probe"] = Coverage{State: "unknown", Reason: "probe failed"}
	}
	var cursor string
	var cursorAt int64
	err = m.db.Reader().QueryRowContext(ctx, `SELECT cursor,updated_at FROM machine_cursors WHERE machine_id=? AND source='journal'`, r.ID).Scan(&cursor, &cursorAt)
	if err != nil && !errors.Is(err, sql.ErrNoRows) {
		return Record{}, err
	}
	batch := JournalBatch{Coverage: Coverage{State: "disabled", Reason: "not configured"}}
	if r.Document.Spec.Monitoring.Journal {
		batch, err = m.observer.ReadJournal(ctx, r.Document.Spec.Monitoring, cursor, time.UnixMilli(cursorAt))
		if err != nil {
			batch = JournalBatch{Coverage: Coverage{State: "unknown", Reason: "journal unavailable"}}
		}
	}
	if len(batch.Events) > 1000 || len(batch.Cursor) > 4096 {
		return Record{}, precondition("journal batch exceeds bounds")
	}
	events, err := m.commitSample(ctx, r, snap, batch)
	if err != nil {
		return Record{}, err
	}
	m.publish(events)
	return m.Get(ctx, name)
}

func (m *Manager) commitSample(ctx context.Context, r Record, snap Snapshot, batch JournalBatch) ([]Event, error) {
	snap.CPUPercent = nil
	var published []Event
	err := m.db.Write(ctx, func(tx *sql.Tx) error {
		var raw, identity, phase string
		var version int
		if err := tx.QueryRowContext(ctx, `SELECT status,identity_hash,phase,version FROM machines WHERE id=?`, r.ID).Scan(&raw, &identity, &phase, &version); err != nil {
			return err
		}
		if version != r.Version || phase != "managed" {
			return ErrConflict
		}
		var old storedStatus
		if err := json.Unmarshal([]byte(raw), &old); err != nil {
			return err
		}
		if old.Baseline == nil || old.DocumentVersion != version {
			old.Baseline = map[string]transition{}
		}
		next := storedStatus{Status: Status{Snapshot: &snap, Journal: batch.Coverage, Fresh: true}, Baseline: old.Baseline, DocumentVersion: version}
		next.Identity = "verified"
		if snap.DeviceHash == "" || snap.BootID == "" {
			next.Identity = "verification-required"
		} else if identity == "" {
			identity = snap.DeviceHash
		} else if identity != snap.DeviceHash {
			next.Identity = "changed"
		}
		continuous := old.Snapshot != nil && old.Snapshot.BootID == snap.BootID && snap.ObservedAt.After(old.Snapshot.ObservedAt) && snap.ObservedAt.Sub(old.Snapshot.ObservedAt) <= r.Document.Spec.Monitoring.StaleAfter.D()
		if continuous && old.Snapshot.CPUTotal > 0 && old.Snapshot.CPUCount > 0 && snap.CPUCount > 0 && cpuAvailable(old.Snapshot.Coverage) && cpuAvailable(snap.Coverage) && snap.CPUTotal > old.Snapshot.CPUTotal && snap.CPUIdle >= old.Snapshot.CPUIdle {
			delta, idle := snap.CPUTotal-old.Snapshot.CPUTotal, snap.CPUIdle-old.Snapshot.CPUIdle
			if idle <= delta {
				pct := float64(delta-idle) / float64(delta) * 100
				snap.CPUPercent = &pct
			}
		}
		if !continuous {
			for key, state := range next.Baseline {
				state.Count = 0
				next.Baseline[key] = state
			}
		}
		var events []Event
		if old.Snapshot != nil && old.Snapshot.BootID != "" && snap.BootID != "" && old.Snapshot.BootID != snap.BootID {
			events = append(events, Event{Kind: "machine.boot.changed", Severity: "info", Reason: "boot identity changed"})
		}
		changes := evaluateHealth(&next, r.Document.Spec.Monitoring)
		events = append(events, changes...)
		if old.Health != "" && old.Health != next.Health {
			events = append(events, Event{Kind: "machine.health.changed", Severity: severity(next.Health), Reason: next.Health})
		}
		if old.Journal.State != "" && (old.Journal.State != next.Journal.State || old.Journal.Reason != next.Journal.Reason) {
			events = append(events, Event{Kind: "machine.collector.changed", Severity: severity(next.Journal.State), Reason: next.Journal.State})
		}
		body, err := encode(snap)
		if err != nil {
			return err
		}
		res, err := tx.ExecContext(ctx, `INSERT INTO machine_snapshots(machine_id,observed_at,body) VALUES(?,?,?)`, r.ID, snap.ObservedAt.UnixMilli(), body)
		if err != nil {
			return err
		}
		if snap.ID, err = res.LastInsertId(); err != nil {
			return err
		}
		var cpuSum float64
		var cpuCount int
		if snap.CPUPercent != nil {
			cpuSum = *snap.CPUPercent
			cpuCount = 1
		}
		if _, err = tx.ExecContext(ctx, `INSERT INTO machine_rollups(machine_id,bucket_at,boot_id,samples,cpu_sum,cpu_count,body) VALUES(?,?,?,1,?,?,?) ON CONFLICT(machine_id,bucket_at,boot_id) DO UPDATE SET samples=samples+1,cpu_sum=cpu_sum+excluded.cpu_sum,cpu_count=cpu_count+excluded.cpu_count,body=excluded.body`, r.ID, snap.ObservedAt.Truncate(time.Minute).UnixMilli(), snap.BootID, cpuSum, cpuCount, body); err != nil {
			return err
		}
		for i := range events {
			e := &events[i]
			e.MachineID = r.ID
			e.Source = "poll"
			e.Key = fmt.Sprintf("%d:%d", snap.ID, i)
			e.OccurredAt = snap.ObservedAt
			e.IngestedAt = snap.ObservedAt
			e.BootID = snap.BootID
			if err := insertEvent(ctx, tx, e); err != nil {
				return err
			}
			published = append(published, *e)
		}
		for i := range batch.Events {
			e := batch.Events[i]
			e.MachineID = r.ID
			e.Source = "journal"
			e.IngestedAt = snap.ObservedAt
			if e.Key == "" || e.OccurredAt.IsZero() {
				return precondition("invalid journal event")
			}
			if err := insertEvent(ctx, tx, &e); err != nil {
				return err
			}
			if e.ID != 0 {
				published = append(published, e)
			}
		}
		if batch.ResetCursor {
			batch.Cursor = ""
		}
		if batch.Cursor != "" || batch.ResetCursor || (r.Document.Spec.Monitoring.Journal && (batch.Coverage.State == "available" || batch.Coverage.State == "partial")) {
			if _, err := tx.ExecContext(ctx, `INSERT INTO machine_cursors(machine_id,source,cursor,updated_at) VALUES(?,'journal',?,?) ON CONFLICT(machine_id,source) DO UPDATE SET cursor=excluded.cursor,updated_at=excluded.updated_at`, r.ID, batch.Cursor, snap.ObservedAt.UnixMilli()); err != nil {
				return err
			}
		}
		// Receipts and completion evidence live in operations, independently of event retention.
		if err = trimEvents(ctx, tx); err != nil {
			return err
		}
		status, err := encode(next)
		if err != nil {
			return err
		}
		_, err = tx.ExecContext(ctx, `UPDATE machines SET status=?,identity_hash=? WHERE id=?`, status, identity, r.ID)
		return err
	})
	if err != nil {
		return nil, err
	}
	return published, nil
}

func severity(state string) string {
	if state == "healthy" || state == "available" || state == "disabled" {
		return "info"
	}
	return "warning"
}

func cpuAvailable(sources map[string]Coverage) bool {
	c, ok := sources["cpu"]
	return !ok || c.State == "available"
}

func evaluateHealth(s *storedStatus, c model.MachineMonitoring) []Event {
	t := c.Thresholds
	s.Health = "healthy"
	s.Reasons = []string{}
	values := map[string]*float64{}
	values["cpu"] = s.Snapshot.CPUPercent
	percent := func(total, available uint64) *float64 {
		if total == 0 || available > total {
			return nil
		}
		v := float64(total-available) / float64(total) * 100
		return &v
	}
	values["memory"] = percent(s.Snapshot.MemoryTotal, s.Snapshot.MemoryAvailable)
	for _, fs := range s.Snapshot.Filesystems {
		values["disk:"+fs.Path] = percent(fs.Total, fs.Available)
		if fs.Inodes > 0 {
			values["inode:"+fs.Path] = percent(fs.Inodes, fs.FreeInodes)
		}
	}
	for _, p := range c.Filesystems {
		if _, ok := values["disk:"+p]; !ok {
			values["disk:"+p] = nil
		}
	}
	for _, unit := range c.Units {
		var v *float64
		if state, ok := s.Snapshot.Units[unit]; ok && state != "unknown" {
			n := float64(100)
			if state == "active" {
				n = 0
			}
			v = &n
		}
		values["unit:"+unit] = v
	}
	keys := make([]string, 0, len(values))
	for key := range values {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	var events []Event
	unknown := s.Identity != "verified"
	for _, key := range keys {
		value := values[key]
		st := s.Baseline[key]
		threshold := t.DiskPercent
		switch key {
		case "cpu":
			threshold = t.CPUPercent
		case "memory":
			threshold = t.MemoryPercent
		}
		if value == nil {
			st.Count = 0
			unknown = true
		} else {
			if *value >= threshold {
				st.Count = min(st.Count+1, t.Consecutive)
				if st.Count >= t.Consecutive && !st.Active {
					st.Active = true
					events = append(events, Event{Kind: "machine.threshold.firing", Severity: "warning", Reason: key})
				}
			} else {
				st.Count = 0
				if st.Active && *value <= threshold-t.Hysteresis {
					st.Active = false
					events = append(events, Event{Kind: "machine.threshold.recovered", Severity: "info", Reason: key})
				}
			}
		}
		s.Baseline[key] = st
		if st.Active {
			s.Health = "degraded"
			s.Reasons = append(s.Reasons, key)
		}
	}
	for _, source := range []string{"probe", "identity", "cpu", "memory", "filesystems", "units"} {
		cov := s.Snapshot.Coverage[source]
		if cov.State == "unknown" || cov.State == "unavailable" || cov.State == "error" {
			unknown = true
		}
	}
	if unknown {
		s.Health = "unknown"
		s.Reasons = append(s.Reasons, "required observation unavailable")
	}
	return events
}

// Events returns durable events after an opaque API cursor has been decoded.
func (m *Manager) Events(ctx context.Context, name string, after int64, limit int) ([]Event, error) {
	r, err := m.Get(ctx, name)
	if err != nil {
		return nil, err
	}
	limit = max(1, min(limit, 200))
	rows, err := m.db.Reader().QueryContext(ctx, `SELECT id,body FROM machine_events WHERE machine_id=? AND id>? ORDER BY id LIMIT ?`, r.ID, after, limit)
	if err != nil {
		return nil, err
	}
	defer func() { _ = rows.Close() }()
	out := []Event{}
	for rows.Next() {
		var e Event
		var id int64
		var body string
		if err = rows.Scan(&id, &body); err != nil {
			return nil, err
		}
		if err = json.Unmarshal([]byte(body), &e); err != nil {
			return nil, err
		}
		e.ID = id
		out = append(out, e)
	}
	return out, rows.Err()
}

func (m *Manager) History(ctx context.Context, name string, after int64, limit int) ([]Snapshot, error) {
	return m.HistoryResolution(ctx, name, after, limit, false)
}

// HistoryResolution reads raw samples or the longer-lived minute points.
func (m *Manager) HistoryResolution(ctx context.Context, name string, after int64, limit int, minute bool) ([]Snapshot, error) {
	r, err := m.Get(ctx, name)
	if err != nil {
		return nil, err
	}
	limit = max(1, min(limit, 200))
	query := `SELECT id,body,0,0 FROM machine_snapshots WHERE machine_id=? AND id>? ORDER BY id LIMIT ?`
	if minute {
		query = `SELECT id,body,cpu_sum,cpu_count FROM machine_rollups WHERE machine_id=? AND id>? ORDER BY id LIMIT ?`
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
			average := sum / float64(count)
			s.CPUPercent = &average
		}
		out = append(out, s)
	}
	return out, rows.Err()
}

// Run polls only registered managed machines; errors leave the last data visibly stale.
func (m *Manager) Run(ctx context.Context) {
	for ctx.Err() == nil {
		wait := 30 * time.Second
		rows, err := m.List(ctx)
		if err == nil {
			for _, r := range rows {
				if r.Phase == "managed" {
					wait = r.Document.Spec.Monitoring.Interval.D()
					if _, err := m.Observe(ctx, r.Name); err != nil && ctx.Err() == nil {
						m.log.Warn("machine observation failed", "machine", r.Name, "error", err)
					}
				}
			}
		}
		ready := make(chan struct{}, 1)
		timer := m.clk.AfterFunc(wait, func() { ready <- struct{}{} })
		select {
		case <-ctx.Done():
			timer.Stop()
			return
		case <-ready:
		}
	}
}

// Prune bounds storage while retaining the current baseline and operation evidence.
func (m *Manager) Prune(ctx context.Context) error {
	now := m.clk.Now()
	jobs := []struct {
		query string
		args  []any
	}{
		{`DELETE FROM machine_snapshots WHERE id IN (SELECT id FROM machine_snapshots WHERE observed_at<? AND id NOT IN (SELECT MAX(id) FROM machine_snapshots GROUP BY machine_id) LIMIT 5000)`, []any{now.Add(-m.rawRetention).UnixMilli()}},
		{`DELETE FROM machine_rollups WHERE id IN (SELECT id FROM machine_rollups WHERE bucket_at<? LIMIT 5000)`, []any{now.Add(-m.rollupRetention).UnixMilli()}},
		{`DELETE FROM machine_events WHERE id IN (SELECT id FROM machine_events WHERE ingested_at<? LIMIT 5000)`, []any{now.Add(-m.eventRetention).UnixMilli()}},
		{`DELETE FROM machine_snapshots WHERE id IN (SELECT id FROM machine_snapshots WHERE id < (SELECT id FROM machine_snapshots ORDER BY id DESC LIMIT 1 OFFSET 199999) AND id NOT IN (SELECT MAX(id) FROM machine_snapshots GROUP BY machine_id) LIMIT 5000)`, nil},
		{`DELETE FROM machine_rollups WHERE id IN (SELECT id FROM machine_rollups WHERE id < (SELECT id FROM machine_rollups ORDER BY id DESC LIMIT 1 OFFSET 199999) LIMIT 5000)`, nil},
		{`DELETE FROM machine_events WHERE id IN (SELECT id FROM machine_events WHERE id <= (SELECT MAX(id)-200000 FROM machine_events) LIMIT 5000)`, nil},
	}
	for _, job := range jobs {
		for {
			var n int64
			err := m.db.Write(ctx, func(tx *sql.Tx) error {
				result, err := tx.ExecContext(ctx, job.query, job.args...)
				if err != nil {
					return err
				}
				n, err = result.RowsAffected()
				return err
			})
			if err != nil {
				return err
			}
			if n < 5000 {
				break
			}
		}
	}
	return nil
}
