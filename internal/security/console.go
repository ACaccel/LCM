package security

import (
	"context"
	"database/sql"
	"errors"
	"strings"
	"time"
	"unicode"
	"unicode/utf8"

	"github.com/ACaccel/lcm/internal/audit"
	"github.com/ACaccel/lcm/internal/auth"
	"github.com/ACaccel/lcm/internal/model"
)

// ErrVersionConflict protects concurrent operator updates.
var ErrVersionConflict = errors.New("security: stored version changed")
var ErrTestRate = errors.New("security: notification test cooldown is active")

const MaxSilence = 24 * time.Hour

// Settings combines persisted operator preferences and immutable bootstrap limits.
type Settings struct {
	Version                int64  `json:"version"`
	NotificationsEnabled   bool   `json:"notificationsEnabled"`
	CooldownSeconds        int    `json:"cooldownSeconds"`
	MinimumSeverity        string `json:"minimumSeverity"`
	RootEnabled            bool   `json:"rootEnabled"`
	NotificationConfigured bool   `json:"notificationConfigured"`
	MaxSilenceSeconds      int    `json:"maxSilenceSeconds"`
}

type rowQuerier interface {
	QueryRowContext(context.Context, string, ...any) *sql.Row
}

func (m *Manager) settings(ctx context.Context, db rowQuerier) (Settings, error) {
	s := Settings{NotificationsEnabled: true, CooldownSeconds: int(m.cfg.Cooldown / time.Second), MinimumSeverity: "info", RootEnabled: m.cfg.Enabled, NotificationConfigured: m.cfg.Send != nil, MaxSilenceSeconds: int(MaxSilence / time.Second)}
	err := db.QueryRowContext(ctx, `SELECT version,notifications_enabled,cooldown_seconds,minimum_severity FROM security_settings WHERE singleton=1`).Scan(&s.Version, &s.NotificationsEnabled, &s.CooldownSeconds, &s.MinimumSeverity)
	if errors.Is(err, sql.ErrNoRows) {
		err = nil
	}
	return s, err
}
func (m *Manager) Settings(ctx context.Context) (Settings, error) {
	return m.settings(ctx, m.cfg.DB.Reader())
}

// SettingsUpdate contains no relay destination or credential fields.
type SettingsUpdate struct {
	NotificationsEnabled bool   `json:"notificationsEnabled"`
	CooldownSeconds      int    `json:"cooldownSeconds"`
	MinimumSeverity      string `json:"minimumSeverity"`
	Reason               string `json:"reason"`
}

func validReason(s string) bool {
	return strings.TrimSpace(s) != "" && utf8.RuneCountInString(s) <= 500 && !strings.ContainsFunc(s, unicode.IsControl)
}
func invalidField(field, message string) error { return model.Issues{{Path: field, Message: message}} }
func operator(ctx context.Context) (string, error) {
	a, ok := auth.ActorFrom(ctx)
	if !ok {
		return "", auth.ErrUnauthenticated
	}
	return string(a.Kind) + ":" + a.ID, nil
}
func (m *Manager) UpdateSettings(ctx context.Context, version int64, u SettingsUpdate) (Settings, error) {
	if !validReason(u.Reason) {
		return Settings{}, invalidField("reason", "use 1 to 500 printable characters")
	}
	if u.CooldownSeconds < 60 || u.CooldownSeconds > 86400 {
		return Settings{}, invalidField("cooldownSeconds", "must be between 60 and 86400")
	}
	if u.MinimumSeverity != "info" && u.MinimumSeverity != "warning" && u.MinimumSeverity != "critical" {
		return Settings{}, invalidField("minimumSeverity", "unsupported severity")
	}
	if _, err := operator(ctx); err != nil {
		return Settings{}, err
	}
	m.tickMu.Lock()
	defer m.tickMu.Unlock()
	var out Settings
	err := m.cfg.DB.Write(ctx, func(tx *sql.Tx) error {
		current, err := m.settings(ctx, tx)
		if err != nil {
			return err
		}
		if version != current.Version {
			return ErrVersionConflict
		}
		_, err = tx.ExecContext(ctx, `INSERT INTO security_settings(singleton,version,notifications_enabled,cooldown_seconds,minimum_severity) VALUES(1,?,?,?,?) ON CONFLICT(singleton) DO UPDATE SET version=excluded.version,notifications_enabled=excluded.notifications_enabled,cooldown_seconds=excluded.cooldown_seconds,minimum_severity=excluded.minimum_severity`, version+1, u.NotificationsEnabled, u.CooldownSeconds, u.MinimumSeverity)
		if err != nil {
			return err
		}
		actor, _ := operator(ctx)
		if _, err = tx.ExecContext(ctx, `INSERT INTO security_settings_history(version,actor,at,reason,notifications_enabled,cooldown_seconds,minimum_severity) VALUES(?,?,?,?,?,?,?)`, version+1, actor, m.cfg.Clock.Now().UnixMilli(), u.Reason, u.NotificationsEnabled, u.CooldownSeconds, u.MinimumSeverity); err != nil {
			return err
		}
		if _, err = tx.ExecContext(ctx, `DELETE FROM security_settings_history WHERE version IN (SELECT version FROM security_settings_history ORDER BY version DESC LIMIT -1 OFFSET 100)`); err != nil {
			return err
		}
		if !u.NotificationsEnabled {
			if err = cancelQueue(ctx, tx, m.cfg.Clock.Now()); err != nil {
				return err
			}
		}
		if out, err = m.settings(ctx, tx); err != nil {
			return err
		}
		return m.cfg.Audit.RecordTx(ctx, tx, "security.settings", "", audit.OutcomeOK, map[string]any{"version": out.Version, "reason": u.Reason})
	})
	if err != nil {
		return Settings{}, err
	}
	return out, nil
}
func cancelQueue(ctx context.Context, tx *sql.Tx, now time.Time) error {
	if _, err := tx.ExecContext(ctx, `UPDATE security_incidents SET state='retired',summary='[TEST] Security notification test cancelled',revision=revision+1,updated_at=? WHERE state='firing' AND id IN (SELECT incident_id FROM security_notification_tests)`, now.UnixMilli()); err != nil {
		return err
	}
	if _, err := tx.ExecContext(ctx, `UPDATE security_incidents SET delivery='disabled',last_error='' WHERE id IN (SELECT incident_id FROM security_outbox)`); err != nil {
		return err
	}
	_, err := tx.ExecContext(ctx, `DELETE FROM security_outbox`)
	return err
}

// Handling never changes the detector's state or event history.
type Handling struct {
	Version      int64      `json:"version"`
	Acknowledged bool       `json:"acknowledged"`
	Note         string     `json:"note"`
	SilenceUntil *time.Time `json:"silenceUntil"`
	UpdatedAt    *time.Time `json:"updatedAt"`
	UpdatedBy    string     `json:"updatedBy"`
}
type HandlingUpdate struct {
	Action       string     `json:"action"`
	Reason       string     `json:"reason"`
	SilenceUntil *time.Time `json:"silenceUntil,omitempty"`
}
type HandlingHistory struct {
	ID           int64      `json:"id"`
	Action       string     `json:"action"`
	Reason       string     `json:"reason"`
	Actor        string     `json:"actor"`
	At           time.Time  `json:"at"`
	SilenceUntil *time.Time `json:"silenceUntil"`
}

func scanHandling(row scanner) (Handling, error) {
	var h Handling
	var until sql.NullInt64
	var updated int64
	err := row.Scan(&h.Version, &h.Acknowledged, &h.Note, &until, &updated, &h.UpdatedBy)
	if until.Valid {
		v := time.UnixMilli(until.Int64).UTC()
		h.SilenceUntil = &v
	}
	if err == nil {
		v := time.UnixMilli(updated).UTC()
		h.UpdatedAt = &v
	}
	return h, err
}
func (m *Manager) Handling(ctx context.Context, id string) (Handling, error) {
	h, err := scanHandling(m.cfg.DB.Reader().QueryRowContext(ctx, `SELECT version,acknowledged,note,silence_until,updated_at,updated_by FROM security_handling WHERE incident_id=?`, id))
	if errors.Is(err, sql.ErrNoRows) {
		return Handling{}, nil
	}
	return h, err
}
func (m *Manager) HandlingHistory(ctx context.Context, id string) ([]HandlingHistory, error) {
	rows, err := m.cfg.DB.Reader().QueryContext(ctx, `SELECT id,action,reason,actor,at,silence_until FROM security_handling_history WHERE incident_id=? ORDER BY id DESC LIMIT 100`, id)
	if err != nil {
		return nil, err
	}
	defer func() { _ = rows.Close() }()
	out := []HandlingHistory{}
	for rows.Next() {
		var v HandlingHistory
		var at int64
		var until sql.NullInt64
		if err = rows.Scan(&v.ID, &v.Action, &v.Reason, &v.Actor, &at, &until); err != nil {
			return nil, err
		}
		v.At = time.UnixMilli(at).UTC()
		if until.Valid {
			u := time.UnixMilli(until.Int64).UTC()
			v.SilenceUntil = &u
		}
		out = append(out, v)
	}
	return out, rows.Err()
}
func (m *Manager) UpdateHandling(ctx context.Context, id string, version int64, u HandlingUpdate) (Handling, error) {
	if !validReason(u.Reason) {
		return Handling{}, invalidField("reason", "use 1 to 500 printable characters")
	}
	if u.Action != "acknowledge" && u.Action != "note" && u.Action != "silence" && u.Action != "unsilence" {
		return Handling{}, invalidField("action", "unsupported handling action")
	}
	now := m.cfg.Clock.Now()
	if u.Action == "silence" {
		if u.SilenceUntil == nil || !u.SilenceUntil.After(now) || u.SilenceUntil.After(now.Add(MaxSilence)) {
			return Handling{}, invalidField("silenceUntil", "must be in the next 24 hours")
		}
	} else if u.SilenceUntil != nil {
		return Handling{}, invalidField("silenceUntil", "only permitted for silence")
	}
	actor, err := operator(ctx)
	if err != nil {
		return Handling{}, err
	}
	m.tickMu.Lock()
	defer m.tickMu.Unlock()
	err = m.cfg.DB.Write(ctx, func(tx *sql.Tx) error {
		var exists int
		if err := tx.QueryRowContext(ctx, `SELECT 1 FROM security_incidents WHERE id=?`, id).Scan(&exists); err != nil {
			return err
		}
		h, err := scanHandling(tx.QueryRowContext(ctx, `SELECT version,acknowledged,note,silence_until,updated_at,updated_by FROM security_handling WHERE incident_id=?`, id))
		if err != nil && !errors.Is(err, sql.ErrNoRows) {
			return err
		}
		if h.Version != version {
			return ErrVersionConflict
		}
		switch u.Action {
		case "acknowledge":
			h.Acknowledged = true
		case "note":
			h.Note = u.Reason
		case "silence":
			h.SilenceUntil = u.SilenceUntil
		case "unsilence":
			h.SilenceUntil = nil
		}
		var until any
		if h.SilenceUntil != nil {
			until = h.SilenceUntil.UnixMilli()
		}
		_, err = tx.ExecContext(ctx, `INSERT INTO security_handling(incident_id,version,acknowledged,note,silence_until,updated_at,updated_by) VALUES(?,?,?,?,?,?,?) ON CONFLICT(incident_id) DO UPDATE SET version=excluded.version,acknowledged=excluded.acknowledged,note=excluded.note,silence_until=excluded.silence_until,updated_at=excluded.updated_at,updated_by=excluded.updated_by`, id, version+1, h.Acknowledged, h.Note, until, now.UnixMilli(), actor)
		if err != nil {
			return err
		}
		switch u.Action {
		case "silence":
			_, err = tx.ExecContext(ctx, `UPDATE security_outbox SET next_at=? WHERE incident_id=?`, until, id)
		case "unsilence":
			_, err = tx.ExecContext(ctx, `UPDATE security_outbox SET next_at=? WHERE incident_id=?`, now.UnixMilli(), id)
		}
		if err != nil {
			return err
		}
		if _, err = tx.ExecContext(ctx, `INSERT INTO security_handling_history(incident_id,action,reason,actor,at,silence_until) VALUES(?,?,?,?,?,?)`, id, u.Action, u.Reason, actor, now.UnixMilli(), until); err != nil {
			return err
		}
		if _, err = tx.ExecContext(ctx, `DELETE FROM security_handling_history WHERE id IN (SELECT id FROM security_handling_history WHERE incident_id=? ORDER BY id DESC LIMIT -1 OFFSET 100)`, id); err != nil {
			return err
		}
		return m.cfg.Audit.RecordTx(ctx, tx, "security.handling."+u.Action, id, audit.OutcomeOK, map[string]any{"version": version + 1})
	})
	if err != nil {
		return Handling{}, err
	}
	return m.Handling(ctx, id)
}

// queuePolicy is re-evaluated both on intake and immediately before delivery.
func (m *Manager) queuePolicy(ctx context.Context, db rowQuerier, id string) (state string, until int64, err error) {
	settings, err := m.settings(ctx, db)
	if err != nil {
		return "", 0, err
	}
	if !m.cfg.Enabled || m.cfg.Send == nil || !settings.NotificationsEnabled {
		return "disabled", 0, nil
	}
	var severity, status string
	var sent, synthetic int
	err = db.QueryRowContext(ctx, `SELECT severity,state,last_sent_revision,EXISTS(SELECT 1 FROM security_notification_tests WHERE incident_id=security_incidents.id),COALESCE((SELECT silence_until FROM security_handling WHERE incident_id=security_incidents.id),0) FROM security_incidents WHERE id=?`, id).Scan(&severity, &status, &sent, &synthetic, &until)
	if err != nil {
		return "", 0, err
	}
	if synthetic == 0 && severityRank(severity) < severityRank(settings.MinimumSeverity) && (status != "resolved" || sent <= 0) {
		return "filtered", 0, nil
	}
	return "pending", until, nil
}

// SettingsHistory retains the latest hundred effective operator versions.
type SettingsHistory struct {
	Version              int64     `json:"version"`
	Actor                string    `json:"actor"`
	At                   time.Time `json:"at"`
	Reason               string    `json:"reason"`
	NotificationsEnabled bool      `json:"notificationsEnabled"`
	CooldownSeconds      int       `json:"cooldownSeconds"`
	MinimumSeverity      string    `json:"minimumSeverity"`
}

func (m *Manager) SettingsHistory(ctx context.Context) ([]SettingsHistory, error) {
	rows, err := m.cfg.DB.Reader().QueryContext(ctx, `SELECT version,actor,at,reason,notifications_enabled,cooldown_seconds,minimum_severity FROM security_settings_history ORDER BY version DESC LIMIT 100`)
	if err != nil {
		return nil, err
	}
	defer func() { _ = rows.Close() }()
	out := []SettingsHistory{}
	for rows.Next() {
		var h SettingsHistory
		var at int64
		if err = rows.Scan(&h.Version, &h.Actor, &at, &h.Reason, &h.NotificationsEnabled, &h.CooldownSeconds, &h.MinimumSeverity); err != nil {
			return nil, err
		}
		h.At = time.UnixMilli(at).UTC()
		out = append(out, h)
	}
	return out, rows.Err()
}
