package monitor

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"time"

	"github.com/ACaccel/lcm/internal/monitor/alert"
	"github.com/ACaccel/lcm/internal/signal"
)

// maxAlertMessage caps stored alert messages.
const maxAlertMessage = 1024

// Alert is an external alert as stored.
type Alert struct {
	Source      string            `json:"source"`
	Fingerprint string            `json:"fingerprint"`
	Service     string            `json:"service,omitempty"`
	Name        string            `json:"name"`
	Severity    string            `json:"severity"`
	Labels      map[string]string `json:"labels,omitempty"`
	Message     string            `json:"message,omitempty"`
	StartedAt   time.Time         `json:"startedAt"`
	UpdatedAt   time.Time         `json:"updatedAt"`
	// ResolvedAt is zero while the alert is firing.
	ResolvedAt time.Time `json:"resolvedAt,omitzero"`
}

// IngestAlert parses a webhook body from source (see package alert for the
// supported sources), stores the resulting alert state and publishes one
// signal per alert that started or stopped firing. A repeated notification
// for an alert that is already firing refreshes it silently, so Alertmanager
// re-sends do not retrigger policies. It returns the published signals.
func (m *Monitor) IngestAlert(ctx context.Context, source string, body []byte) ([]signal.Signal, error) {
	parsed, err := alert.Parse(source, body, alert.Options{ServiceLabels: m.alertLabels})
	if err != nil {
		return nil, err
	}
	if len(parsed) == 0 {
		return nil, nil
	}
	m.mu.Lock()
	closed := m.closed
	m.mu.Unlock()
	if closed {
		return nil, ErrClosed
	}
	now := m.clock.Now()
	var out []signal.Signal
	err = m.db.Write(ctx, func(tx *sql.Tx) error {
		out = out[:0]
		for _, sig := range parsed {
			publish, err := recordAlert(ctx, tx, sig, now)
			if err != nil {
				return err
			}
			if publish {
				sig.Time = now
				out = append(out, sig)
			}
		}
		return nil
	})
	if err != nil {
		return nil, fmt.Errorf("monitor: store alert: %w", err)
	}
	for i := range out {
		out[i] = m.bus.Publish(out[i])
	}
	return out, nil
}

// recordAlert applies one alert signal to the alert table and reports
// whether it is a state change worth publishing.
func recordAlert(ctx context.Context, tx *sql.Tx, sig signal.Signal, now time.Time) (bool, error) {
	fp, _ := sig.Data[alert.DataFingerprint].(string)
	name, _ := sig.Data[alert.DataAlert].(string)
	status, _ := sig.Data[alert.DataStatus].(string)
	msg, _ := sig.Data[alert.DataMessage].(string)
	msg = truncate(msg, maxAlertMessage)

	var resolved sql.NullInt64
	err := tx.QueryRowContext(ctx,
		`SELECT resolved_at FROM monitor_alerts WHERE source = ? AND fingerprint = ?`, sig.Source, fp).Scan(&resolved)
	exists := err == nil
	if err != nil && !errors.Is(err, sql.ErrNoRows) {
		return false, err
	}
	firing := exists && !resolved.Valid

	switch status {
	case alert.StatusFiring:
		labels, err := json.Marshal(nonNilLabels(sig.Labels))
		if err != nil {
			return false, err
		}
		if firing {
			_, err = tx.ExecContext(ctx,
				`UPDATE monitor_alerts SET service = ?, name = ?, severity = ?, labels = ?, message = ?, updated_at = ?
                 WHERE source = ? AND fingerprint = ?`,
				sig.Service, name, sig.Severity, string(labels), msg, now.UnixMilli(), sig.Source, fp)
			return false, err
		}
		_, err = tx.ExecContext(ctx,
			`INSERT INTO monitor_alerts (source, fingerprint, service, name, severity, labels, message, started_at, updated_at, resolved_at)
             VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, NULL)
             ON CONFLICT (source, fingerprint) DO UPDATE SET service = excluded.service, name = excluded.name,
                 severity = excluded.severity, labels = excluded.labels, message = excluded.message,
                 started_at = excluded.started_at, updated_at = excluded.updated_at, resolved_at = NULL`,
			sig.Source, fp, sig.Service, name, sig.Severity, string(labels), msg, now.UnixMilli(), now.UnixMilli())
		return true, err
	default:
		if !firing {
			// Nothing to resolve: a resolve for an alert LCM never saw or
			// already closed carries no state change.
			return false, nil
		}
		_, err := tx.ExecContext(ctx,
			`UPDATE monitor_alerts SET resolved_at = ?, updated_at = ?, message = ? WHERE source = ? AND fingerprint = ?`,
			now.UnixMilli(), now.UnixMilli(), msg, sig.Source, fp)
		return true, err
	}
}

// FiringAlerts returns the alerts currently firing for service, oldest
// first. The empty service selects alerts that could not be mapped to one.
func (m *Monitor) FiringAlerts(ctx context.Context, service string) ([]Alert, error) {
	return m.queryAlerts(ctx, `service = ? AND resolved_at IS NULL`, service)
}

// AllFiringAlerts returns every alert currently firing, oldest first.
func (m *Monitor) AllFiringAlerts(ctx context.Context) ([]Alert, error) {
	return m.queryAlerts(ctx, `resolved_at IS NULL`)
}

func (m *Monitor) queryAlerts(ctx context.Context, where string, args ...any) ([]Alert, error) {
	rows, err := m.db.Reader().QueryContext(ctx,
		`SELECT source, fingerprint, service, name, severity, labels, message, started_at, updated_at
         FROM monitor_alerts WHERE `+where+` ORDER BY started_at, source, fingerprint`, args...)
	if err != nil {
		return nil, fmt.Errorf("monitor: alerts: %w", err)
	}
	out := []Alert{}
	for rows.Next() {
		var a Alert
		var labels string
		var started, updated int64
		if err := rows.Scan(&a.Source, &a.Fingerprint, &a.Service, &a.Name, &a.Severity, &labels, &a.Message, &started, &updated); err != nil {
			_ = rows.Close()
			return nil, fmt.Errorf("monitor: alerts: %w", err)
		}
		a.StartedAt, a.UpdatedAt = fromMillis(started), fromMillis(updated)
		if err := json.Unmarshal([]byte(labels), &a.Labels); err != nil {
			a.Labels = nil
		}
		out = append(out, a)
	}
	if err := closeRows(rows); err != nil {
		return nil, fmt.Errorf("monitor: alerts: %w", err)
	}
	return out, nil
}
