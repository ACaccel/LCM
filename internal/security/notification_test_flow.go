package security

import (
	"context"
	"database/sql"
	"errors"
	"time"

	"github.com/ACaccel/lcm/internal/audit"
)

// NotificationTest tracks the two durable revisions of one labelled test episode.
type NotificationTest struct {
	RequestID  string `json:"requestId"`
	IncidentID string `json:"incidentId"`
	State      string `json:"state"`
	Pending    bool   `json:"pending"`
}
type TestRequest struct {
	RequestID string `json:"requestId"`
	Reason    string `json:"reason"`
}

func (m *Manager) NotificationTest(ctx context.Context, id string) (NotificationTest, error) {
	var t NotificationTest
	var state, delivery string
	err := m.cfg.DB.Reader().QueryRowContext(ctx, `SELECT t.request_id,t.incident_id,i.state,i.delivery FROM security_notification_tests t JOIN security_incidents i ON i.id=t.incident_id WHERE request_id=?`, id).Scan(&t.RequestID, &t.IncidentID, &state, &delivery)
	if err != nil {
		return t, err
	}
	t.State = state + "-" + delivery
	t.Pending = delivery == "pending" || delivery == "failed"
	return t, nil
}
func (m *Manager) StartNotificationTest(ctx context.Context, u TestRequest) (NotificationTest, error) {
	if !safeID.MatchString(u.RequestID) || len(u.RequestID) > 64 {
		return NotificationTest{}, invalidField("requestId", "safe stable request id is required")
	}
	if !validReason(u.Reason) {
		return NotificationTest{}, invalidField("reason", "use 1 to 500 printable characters")
	}
	actor, err := operator(ctx)
	if err != nil {
		return NotificationTest{}, err
	}
	m.tickMu.Lock()
	defer m.tickMu.Unlock()
	err = m.cfg.DB.Write(ctx, func(tx *sql.Tx) error {
		var reason, owner string
		err := tx.QueryRowContext(ctx, `SELECT reason,actor FROM security_notification_tests WHERE request_id=?`, u.RequestID).Scan(&reason, &owner)
		if err == nil {
			if reason != u.Reason || owner != actor {
				return ErrConflict
			}
			return nil
		}
		if !errors.Is(err, sql.ErrNoRows) {
			return err
		}
		settings, err := m.settings(ctx, tx)
		if err != nil {
			return err
		}
		if !settings.RootEnabled || !settings.NotificationConfigured || !settings.NotificationsEnabled {
			return ErrDisabled
		}
		var count int
		now := m.cfg.Clock.Now()
		if err = tx.QueryRowContext(ctx, `SELECT COUNT(*) FROM security_notification_tests t JOIN security_incidents i ON i.id=t.incident_id WHERE t.created_at>? OR (i.state='firing' AND i.delivery IN ('pending','failed'))`, now.Add(-5*time.Minute).UnixMilli()).Scan(&count); err != nil {
			return err
		}
		if count > 0 {
			return ErrTestRate
		}
		if err = tx.QueryRowContext(ctx, `SELECT COUNT(*) FROM machines WHERE id=? AND phase='managed'`, m.cfg.MachineID).Scan(&count); err != nil {
			return err
		}
		if count != 1 {
			return invalidField("machineId", "configured managed Machine is required")
		}
		if err = makeIncidentRoom(ctx, tx, m.cfg.MaxIncidents); err != nil {
			return err
		}
		id := newID()
		stamp := now.UnixMilli()
		_, err = tx.ExecContext(ctx, `INSERT INTO security_incidents(id,condition_key,machine_id,service,source,fingerprint,state,severity,summary,opened_at,observed_at,updated_at,occurrences,revision) VALUES(?,?,?,'','collector',?,'firing','info',?,?,?, ?,1,1)`, id, "notification-test:"+u.RequestID, m.cfg.MachineID, "notification-test:"+u.RequestID, "[TEST] Operator-requested security notification acceptance test", stamp, stamp, stamp)
		if err != nil {
			return err
		}
		if _, err = tx.ExecContext(ctx, `INSERT INTO security_notification_tests(request_id,incident_id,reason,actor,created_at) VALUES(?,?,?,?,?)`, u.RequestID, id, u.Reason, actor, stamp); err != nil {
			return err
		}
		if err = m.enqueue(ctx, tx, id, 1, now); err != nil {
			return err
		}
		var delivery string
		if err = tx.QueryRowContext(ctx, `SELECT delivery FROM security_incidents WHERE id=?`, id).Scan(&delivery); err != nil {
			return err
		}
		if delivery != "pending" {
			return ErrCapacity
		}
		return nil
	})
	if err != nil {
		return NotificationTest{}, err
	}
	if err = m.cfg.Audit.Record(ctx, "security.notification.test", u.RequestID, audit.OutcomeOK, nil); err != nil {
		return NotificationTest{}, err
	}
	return m.NotificationTest(ctx, u.RequestID)
}

// advanceNotificationTest queues recovery only after firing delivery succeeded.
// Both records commit together, so a crash cannot lose the second revision.
func (m *Manager) advanceNotificationTest(ctx context.Context, tx *sql.Tx, id string, rev int, now time.Time) error {
	var recovered int
	err := tx.QueryRowContext(ctx, `SELECT recovered FROM security_notification_tests WHERE incident_id=?`, id).Scan(&recovered)
	if errors.Is(err, sql.ErrNoRows) {
		return nil
	}
	if err != nil {
		return err
	}
	if recovered != 0 || rev != 1 {
		return nil
	}
	if _, err = tx.ExecContext(ctx, `UPDATE security_notification_tests SET recovered=1 WHERE incident_id=?`, id); err != nil {
		return err
	}
	if _, err = tx.ExecContext(ctx, `UPDATE security_incidents SET state='resolved',summary='[TEST] Security notification acceptance test recovered',revision=2,observed_at=?,updated_at=? WHERE id=?`, now.UnixMilli(), now.UnixMilli(), id); err != nil {
		return err
	}
	return m.enqueue(ctx, tx, id, 2, now)
}
