package security

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"fmt"
	"strconv"
	"strings"
	"time"

	"github.com/ACaccel/lcm/internal/audit"
	"github.com/ACaccel/lcm/internal/machine"
	"github.com/ACaccel/lcm/internal/monitor"
)

// MachineReader preserves immutable Machine identity; observations are never
// converted to Services and this interface cannot invoke machine operations.
type MachineReader interface {
	// Get accepts the Machine manager selector: name, or @ followed by immutable ID.
	Get(context.Context, string) (machine.Record, error)
}

// ServiceReader reuses the monitor's persisted status and configured hysteresis.
type ServiceReader interface {
	Services() []monitor.ServiceStatus
}

// Poll reconciles the latest persisted observations. Progress advances only
// after idempotent ingestion succeeds; replay after a crash is therefore safe.
// It deliberately does not turn journal/history backfill into live alerts.
func (m *Manager) Poll(ctx context.Context, machines MachineReader, services ServiceReader) error {
	if !m.cfg.Enabled {
		return nil
	}
	if err := m.cfg.Audit.Record(ctx, "security.observe", m.cfg.MachineID, audit.OutcomeOK, nil); err != nil {
		return err
	}
	if err := m.retireRemovedServices(ctx); err != nil {
		return err
	}
	r, err := machines.Get(ctx, "@"+m.cfg.MachineID)
	if err != nil {
		_ = m.checkpoint(ctx, "last_error", "Machine observations unavailable")
		return fmt.Errorf("security: read Machine observation: %w", err)
	}
	if r.ID != m.cfg.MachineID {
		return invalidConfig("immutable Machine identity")
	}
	now := m.cfg.Clock.Now()
	gap := !r.Status.Fresh || r.Status.Health == "unknown" || r.Status.Snapshot == nil
	if err = m.pollGap(ctx, gap, now); err != nil {
		return err
	}
	if !gap {
		snap := r.Status.Snapshot
		marker := strconv.FormatInt(snap.ID, 10) + ":" + snap.ObservedAt.UTC().Format(time.RFC3339Nano)
		previous, err := m.Checkpoint(ctx, "machine_snapshot")
		if err != nil {
			return err
		}
		if marker != previous {
			status := "resolved"
			severity := "info"
			summary := "Machine resource pressure recovered"
			if r.Status.Health != "healthy" {
				status = "firing"
				severity = "warning"
				summary = "Machine resource pressure: " + strings.Join(r.Status.Reasons, ", ")
			}
			if len(summary) > 450 {
				summary = summary[:450]
			}
			e := Event{EventID: nativeID(r.ID+":machine", marker), MachineID: r.ID, Source: "machine", Fingerprint: "resource-pressure", Status: status, Severity: severity, Summary: summary, ObservedAt: snap.ObservedAt}
			if _, err = m.ingest(ctx, e, true); err != nil {
				return err
			}
			if err = m.checkpoint(ctx, "machine_snapshot", marker); err != nil {
				return err
			}
		}
	}
	for _, s := range services.Services() {
		var latest time.Time
		fresh := len(s.Checks) > 0
		for _, check := range s.Checks {
			if check.LastResult.After(latest) {
				latest = check.LastResult
			}
			if check.Stale || check.LastResult.IsZero() || check.LastResult.After(now) || now.Sub(check.LastResult) > max(2*check.Interval, time.Minute) {
				fresh = false
			}
		}
		// Service failures retain their own identity. A missing/stale check is
		// a monitoring gap, never evidence that the service is under attack.
		sourceKey := "service:" + s.Service
		status, severity, summary := "resolved", "info", "Service health recovered"
		fingerprint := "health"
		if !fresh || s.Health == monitor.HealthUnknown {
			ready, err := m.serviceGapReady(ctx, sourceKey, now)
			if err != nil {
				return err
			}
			if !ready {
				continue
			}
			fingerprint = "observation-gap"
			status = "firing"
			severity = "warning"
			summary = "Service health observations missing or stale"
			latest = now.Truncate(time.Minute)
		} else {
			if err := m.checkpoint(ctx, sourceKey+":gap_count", "0"); err != nil {
				return err
			}
			if s.Health != monitor.HealthHealthy {
				status = "firing"
				severity = "warning"
				summary = "Service health is " + string(s.Health)
			}
		}
		marker := fingerprint + ":" + string(s.Health) + ":" + latest.UTC().Format(time.RFC3339Nano)
		prev, err := m.Checkpoint(ctx, sourceKey)
		if err != nil {
			return err
		}
		if prev == marker {
			continue
		}
		e := Event{EventID: nativeID(r.ID+":"+sourceKey, marker), MachineID: r.ID, Service: s.Service, Source: "service", Fingerprint: fingerprint, Status: status, Severity: severity, Summary: summary, ObservedAt: latest}
		if _, err = m.ingest(ctx, e, true); err != nil {
			return err
		}
		if fresh && s.Health != monitor.HealthUnknown {
			e.EventID = nativeID(r.ID+":"+sourceKey+":gap-recovery", marker)
			e.Fingerprint = "observation-gap"
			e.Status = "resolved"
			e.Severity = "info"
			e.Summary = "Service health observations restored"
			if _, err = m.ingest(ctx, e, true); err != nil {
				return err
			}
		}
		if err = m.checkpoint(ctx, sourceKey, marker); err != nil {
			return err
		}
	}
	if err := m.checkpoint(ctx, "last_error", ""); err != nil {
		return err
	}
	return m.checkpoint(ctx, "last_poll", now.UTC().Format(time.RFC3339Nano))
}

func (m *Manager) pollGap(ctx context.Context, gap bool, now time.Time) error {
	marker := now.UTC().Truncate(time.Minute).Format(time.RFC3339)
	prev, err := m.Checkpoint(ctx, "gap_marker")
	if err != nil {
		return err
	}
	if prev == marker {
		return nil
	}
	countRaw, err := m.Checkpoint(ctx, "gap_count")
	if err != nil {
		return err
	}
	count, _ := strconv.Atoi(countRaw)
	if gap {
		count = min(count+1, 2)
	} else {
		count = 0
	}
	if !gap || count >= 2 {
		status, sev, summary := "resolved", "info", "Machine observations restored"
		if gap {
			status = "firing"
			sev = "warning"
			summary = "Machine observations unavailable or stale; resource state is unknown"
		}
		e := Event{EventID: nativeID(m.cfg.MachineID+":machine-gap", marker+":"+status), MachineID: m.cfg.MachineID, Source: "machine", Fingerprint: "observation-gap", Status: status, Severity: sev, Summary: summary, ObservedAt: now}
		if _, err = m.ingest(ctx, e, true); err != nil {
			return err
		}
	}
	if err = m.checkpoint(ctx, "gap_count", strconv.Itoa(count)); err != nil {
		return err
	}
	return m.checkpoint(ctx, "gap_marker", marker)
}

func nativeID(source, marker string) string {
	d := sha256.Sum256([]byte(source + "\x00" + marker))
	return "native-" + hex.EncodeToString(d[:])
}

func (m *Manager) serviceGapReady(ctx context.Context, key string, now time.Time) (bool, error) {
	tick := now.UTC().Truncate(time.Minute).Format(time.RFC3339)
	prev, err := m.Checkpoint(ctx, key+":gap_tick")
	if err != nil {
		return false, err
	}
	raw, err := m.Checkpoint(ctx, key+":gap_count")
	if err != nil {
		return false, err
	}
	count, _ := strconv.Atoi(raw)
	if prev != tick {
		count = min(count+1, 2)
		if err = m.checkpoint(ctx, key+":gap_count", strconv.Itoa(count)); err != nil {
			return false, err
		}
		if err = m.checkpoint(ctx, key+":gap_tick", tick); err != nil {
			return false, err
		}
	}
	return count >= 2, nil
}

// retireRemovedServices closes observation scope, not service health. Only the
// configured immutable Machine's existing episodes may be retired; external
// ingestion still requires a currently registered Service.
func (m *Manager) retireRemovedServices(ctx context.Context) error {
	now := m.cfg.Clock.Now().UnixMilli()
	type removedEpisode struct{ id, service string }
	var retired []removedEpisode
	err := m.cfg.DB.Write(ctx, func(tx *sql.Tx) error {
		rows, err := tx.QueryContext(ctx, `SELECT id,service FROM security_incidents WHERE machine_id=? AND service!='' AND state='firing' AND NOT EXISTS(SELECT 1 FROM services WHERE services.name=security_incidents.service) ORDER BY seq LIMIT 500`, m.cfg.MachineID)
		if err != nil {
			return err
		}
		for rows.Next() {
			var item removedEpisode
			if err = rows.Scan(&item.id, &item.service); err != nil {
				_ = rows.Close()
				return err
			}
			retired = append(retired, item)
		}
		if err = rows.Err(); err != nil {
			_ = rows.Close()
			return err
		}
		if err = rows.Close(); err != nil {
			return err
		}
		for _, item := range retired {
			if _, err = tx.ExecContext(ctx, `UPDATE security_incidents SET state='retired',summary='Service registration removed; episode retired without asserting recovery: ' || summary,delivery='disabled',last_error='',revision=revision+1,updated_at=? WHERE id=? AND machine_id=? AND state='firing'`, now, item.id, m.cfg.MachineID); err != nil {
				return err
			}
			if _, err = tx.ExecContext(ctx, `DELETE FROM security_outbox WHERE incident_id=?`, item.id); err != nil {
				return err
			}
			if _, err = tx.ExecContext(ctx, `DELETE FROM security_conditions WHERE incident_id=?`, item.id); err != nil {
				return err
			}
			key := "service:" + item.service
			if _, err = tx.ExecContext(ctx, `DELETE FROM security_progress WHERE key=? OR key LIKE ?`, key, key+":%"); err != nil {
				return err
			}
		}
		return nil
	})
	if err != nil {
		return err
	}
	for _, item := range retired {
		if err = m.cfg.Audit.Record(ctx, "security.incident.retire", item.id, audit.OutcomeOK, map[string]any{"machineId": m.cfg.MachineID, "service": item.service, "reason": "service_removed"}); err != nil {
			return err
		}
	}
	return nil
}
