package security

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"time"

	"github.com/ACaccel/lcm/internal/audit"
)

const incidentColumns = `seq,id,machine_id,service,source,fingerprint,state,severity,summary,opened_at,observed_at,updated_at,occurrences,revision,delivery,last_error`

type scanner interface{ Scan(...any) error }

func scanIncident(row scanner) (Incident, error) {
	var i Incident
	var opened, observed, updated int64
	err := row.Scan(&i.Seq, &i.ID, &i.MachineID, &i.Service, &i.Source, &i.Fingerprint, &i.State, &i.Severity, &i.Summary, &opened, &observed, &updated, &i.Occurrences, &i.Revision, &i.Delivery, &i.LastError)
	i.OpenedAt = time.UnixMilli(opened).UTC()
	i.ObservedAt = time.UnixMilli(observed).UTC()
	i.UpdatedAt = time.UnixMilli(updated).UTC()
	return i, err
}

// List returns incidents newest first using a stable row cursor.
func (m *Manager) List(ctx context.Context, before int64, limit int) ([]Incident, error) {
	if limit < 1 || limit > 501 {
		return nil, invalidConfig("page size")
	}
	query := `SELECT ` + incidentColumns + ` FROM security_incidents WHERE (?=0 OR seq<?) ORDER BY seq DESC LIMIT ?`
	rows, err := m.cfg.DB.Reader().QueryContext(ctx, query, before, before, limit)
	if err != nil {
		return nil, err
	}
	defer func() { _ = rows.Close() }()
	out := []Incident{}
	for rows.Next() {
		i, err := scanIncident(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, i)
	}
	return out, rows.Err()
}

// Status exposes bounded queue failures without exposing relay credentials.
type Status struct {
	Enabled              bool       `json:"enabled"`
	NotificationsEnabled bool       `json:"notificationsEnabled"`
	MachineID            string     `json:"machineId"`
	Pending              int        `json:"pending"`
	Failed               int        `json:"failed"`
	Overflow             int64      `json:"overflow"`
	Expired              int64      `json:"expired"`
	LastError            string     `json:"lastError"`
	LastPollAt           *time.Time `json:"lastPollAt"`
}

func (m *Manager) Status(ctx context.Context) (Status, error) {
	settings, err := m.Settings(ctx)
	if err != nil {
		return Status{}, err
	}
	s := Status{Enabled: m.cfg.Enabled, NotificationsEnabled: settings.RootEnabled && settings.NotificationConfigured && settings.NotificationsEnabled, MachineID: m.cfg.MachineID}
	if err := m.cfg.DB.Reader().QueryRowContext(ctx, `SELECT COUNT(*) FROM security_outbox`).Scan(&s.Pending); err != nil {
		return s, err
	}
	if err := m.cfg.DB.Reader().QueryRowContext(ctx, `SELECT COUNT(*) FROM security_incidents WHERE delivery IN ('failed','overflow','expired')`).Scan(&s.Failed); err != nil {
		return s, err
	}
	for key, dst := range map[string]*int64{"overflow": &s.Overflow, "expired": &s.Expired} {
		if err := m.cfg.DB.Reader().QueryRowContext(ctx, `SELECT COALESCE((SELECT CAST(value AS INTEGER) FROM security_progress WHERE key=?),0)`, key).Scan(dst); err != nil {
			return s, err
		}
	}
	val, err := m.Checkpoint(ctx, "last_error")
	if err != nil {
		return s, err
	}
	s.LastError = val
	val, err = m.Checkpoint(ctx, "last_poll")
	if err != nil {
		return s, err
	}
	if val != "" {
		parsed, err := time.Parse(time.RFC3339Nano, val)
		if err != nil {
			return s, err
		}
		s.LastPollAt = &parsed
	}
	return s, nil
}

// Deliver makes bounded progress through due items. Updates arriving during a
// send keep their newer revision queued; a crash after the PUT retries the same
// immutable incident key, which edits the existing relay message.
func (m *Manager) Deliver(ctx context.Context) error {
	m.tickMu.Lock()
	defer m.tickMu.Unlock()
	if !m.cfg.Enabled || m.cfg.Send == nil {
		return nil
	}
	for range 20 {
		var id string
		var rev, attempts int
		var created int64
		err := m.cfg.DB.Reader().QueryRowContext(ctx, `SELECT incident_id,revision,attempts,created_at FROM security_outbox WHERE next_at<=? ORDER BY next_at,incident_id LIMIT 1`, m.cfg.Clock.Now().UnixMilli()).Scan(&id, &rev, &attempts, &created)
		if errors.Is(err, sql.ErrNoRows) {
			return nil
		}
		if err != nil {
			return err
		}
		i, err := scanIncident(m.cfg.DB.Reader().QueryRowContext(ctx, `SELECT `+incidentColumns+` FROM security_incidents WHERE id=?`, id))
		if err != nil {
			return err
		}
		if i.State == "retired" || i.Delivery == "disabled" {
			return nil
		}
		state, until, policyErr := m.queuePolicy(ctx, m.cfg.DB.Reader(), id)
		if policyErr != nil {
			return policyErr
		}
		if state != "pending" || until > m.cfg.Clock.Now().UnixMilli() {
			if err = m.cfg.DB.Write(ctx, func(tx *sql.Tx) error {
				if state != "pending" {
					if _, e := tx.ExecContext(ctx, `DELETE FROM security_outbox WHERE incident_id=?`, id); e != nil {
						return e
					}
					_, e := tx.ExecContext(ctx, `UPDATE security_incidents SET delivery=?,last_error='' WHERE id=?`, state, id)
					return e
				}
				_, e := tx.ExecContext(ctx, `UPDATE security_outbox SET next_at=? WHERE incident_id=?`, until, id)
				return e
			}); err != nil {
				return err
			}
			continue
		}
		now := m.cfg.Clock.Now()
		expired := now.Sub(time.UnixMilli(created)) >= m.cfg.DeliveryTTL
		if !expired {
			err = m.cfg.Send(ctx, i)
		}
		if ctx.Err() != nil {
			return ctx.Err()
		}
		outcome := audit.OutcomeOK
		if expired || err != nil {
			outcome = audit.OutcomeError
		}
		if auditErr := m.cfg.Audit.Record(ctx, "security.notify", id, outcome, map[string]any{"revision": i.Revision, "expired": expired}); auditErr != nil {
			return auditErr
		}
		if writeErr := m.cfg.DB.Write(ctx, func(tx *sql.Tx) error {
			// Retirement or an explicit disable may cancel this item while
			// the HTTP request is in flight. Its late result cannot resurrect
			// delivery state or overwrite that durable cancellation.
			var pending int
			if err := tx.QueryRowContext(ctx, `SELECT COUNT(*) FROM security_outbox WHERE incident_id=?`, id).Scan(&pending); err != nil {
				return err
			}
			if pending == 0 {
				return nil
			}
			if expired {
				if _, err := tx.ExecContext(ctx, `UPDATE security_incidents SET state='retired',summary='[TEST] Security notification test expired',revision=revision+1,updated_at=? WHERE id=? AND state='firing' AND id IN (SELECT incident_id FROM security_notification_tests)`, now.UnixMilli(), id); err != nil {
					return err
				}
				if _, err := tx.ExecContext(ctx, `UPDATE security_incidents SET delivery='expired',last_error='notification retention expired' WHERE id=?`, id); err != nil {
					return err
				}
				if _, err := tx.ExecContext(ctx, `DELETE FROM security_outbox WHERE incident_id=? AND revision=?`, id, rev); err != nil {
					return err
				}
				return increment(ctx, tx, "expired")
			}
			if err == nil {
				if _, err := tx.ExecContext(ctx, `DELETE FROM security_outbox WHERE incident_id=? AND revision=?`, id, rev); err != nil {
					return err
				}
				if _, err := tx.ExecContext(ctx, `UPDATE security_incidents SET delivery=CASE WHEN EXISTS(SELECT 1 FROM security_outbox WHERE incident_id=?) THEN 'pending' ELSE 'sent' END,last_error='',last_sent_revision=MAX(last_sent_revision,?) WHERE id=?`, id, i.Revision, id); err != nil {
					return err
				}
				return m.advanceNotificationTest(ctx, tx, id, i.Revision, now)
			}
			// Never persist a transport error that might include a bearer value,
			// URL or remote response. Detailed relay errors remain private.
			const reason = "Konata delivery failed; retry scheduled"
			delay := time.Minute * time.Duration(1<<min(attempts, 6))
			if _, err := tx.ExecContext(ctx, `UPDATE security_outbox SET attempts=attempts+1,next_at=? WHERE incident_id=?`, now.Add(delay).UnixMilli(), id); err != nil {
				return err
			}
			if _, err := tx.ExecContext(ctx, `UPDATE security_incidents SET delivery='failed',last_error=? WHERE id=?`, reason, id); err != nil {
				return err
			}
			_, err := tx.ExecContext(ctx, `INSERT INTO security_progress(key,value) VALUES('last_error',?) ON CONFLICT(key) DO UPDATE SET value=excluded.value`, reason)
			return err
		}); writeErr != nil {
			return writeErr
		}
	}
	return nil
}

// Prune caps retention and removes expired history in bounded batches. Active
// episodes are kept until recovery; intake fails visibly when all capacity is
// active instead of silently forgetting an ongoing incident.
func (m *Manager) Prune(ctx context.Context) error {
	if !m.cfg.Enabled {
		return nil
	}
	cutoff := m.cfg.Clock.Now().Add(-m.cfg.Retention).UnixMilli()
	if err := m.cfg.Audit.Record(ctx, "security.prune", "", audit.OutcomeOK, nil); err != nil {
		return err
	}
	return m.cfg.DB.Write(ctx, func(tx *sql.Tx) error {
		for _, q := range []string{
			`DELETE FROM security_traffic WHERE id IN (SELECT id FROM security_traffic WHERE received_at<? LIMIT 1000)`,
			`DELETE FROM security_events WHERE seq IN (SELECT seq FROM security_events WHERE received_at<? LIMIT 1000)`,
			`DELETE FROM security_incidents WHERE seq IN (SELECT seq FROM security_incidents WHERE state IN ('resolved','retired') AND updated_at<? AND id NOT IN (SELECT incident_id FROM security_outbox) LIMIT 1000)`,
			`DELETE FROM security_conditions WHERE key IN (SELECT key FROM security_conditions WHERE state IN ('resolved','retired') AND updated_at<? LIMIT 1000)`,
		} {
			if _, err := tx.ExecContext(ctx, q, cutoff); err != nil {
				return fmt.Errorf("security: retention: %w", err)
			}
		}
		return nil
	})
}
