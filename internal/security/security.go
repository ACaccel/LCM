// Package security integrates persisted Machine and Service observations with
// bounded security incidents and durable Konata delivery. It has no lifecycle
// or privileged execution capability.
package security

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"regexp"
	"strings"
	"sync"
	"time"
	"unicode"
	"unicode/utf8"

	"github.com/ACaccel/lcm/internal/audit"
	"github.com/ACaccel/lcm/internal/auth"
	"github.com/ACaccel/lcm/internal/clock"
	"github.com/ACaccel/lcm/internal/model"
	"github.com/ACaccel/lcm/internal/store"
)

const (
	// MaxBodyBytes caps sanitized collector envelopes, not raw log uploads.
	MaxBodyBytes        = 16 << 10
	DefaultRetention    = 30 * 24 * time.Hour
	DefaultDeliveryTTL  = 7 * 24 * time.Hour
	DefaultFreshness    = 10 * time.Minute
	DefaultCooldown     = 15 * time.Minute
	DefaultMaxEvents    = 10000
	DefaultMaxIncidents = 2000
	DefaultMaxPending   = 1000
)

var (
	ErrDisabled = errors.New("security: ingestion is disabled")
	ErrConflict = errors.New("security: eventId already has a different payload")
	ErrCapacity = errors.New("security: active condition capacity reached")
	safeID      = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9._:-]{0,99}$`)
)

var serviceName = regexp.MustCompile(`^[a-z0-9][a-z0-9-]*$`)

// Event is the sanitized collector contract. EventId must remain stable across
// retries. A fingerprint names a condition; a later firing after recovery opens
// a new incident. Historical events never modify live incident state.
type Event struct {
	EventID     string    `json:"eventId"`
	MachineID   string    `json:"machineId"`
	Service     string    `json:"service,omitempty"`
	Source      string    `json:"source"`
	Fingerprint string    `json:"fingerprint"`
	Status      string    `json:"status"`
	Severity    string    `json:"severity"`
	Summary     string    `json:"summary"`
	ObservedAt  time.Time `json:"observedAt"`
	Historical  bool      `json:"historical"`
}

// Receipt is safe to retry without opening duplicate incidents.
type Receipt struct {
	Accepted   bool   `json:"accepted"`
	Duplicate  bool   `json:"duplicate"`
	IncidentID string `json:"incidentId"`
	Historical bool   `json:"historical"`
}

// Incident is a persisted episode and its latest delivery state.
type Incident struct {
	Seq         int64     `json:"seq"`
	ID          string    `json:"id"`
	MachineID   string    `json:"machineId"`
	Service     string    `json:"service"`
	Source      string    `json:"source"`
	Fingerprint string    `json:"fingerprint"`
	State       string    `json:"state"`
	Severity    string    `json:"severity"`
	Summary     string    `json:"summary"`
	OpenedAt    time.Time `json:"openedAt"`
	ObservedAt  time.Time `json:"observedAt"`
	UpdatedAt   time.Time `json:"updatedAt"`
	Occurrences int       `json:"occurrences"`
	Revision    int       `json:"revision"`
	Delivery    string    `json:"delivery"`
	LastError   string    `json:"lastError"`
}

// SendFunc must use Incident.ID as the stable relay key, not a run id.
type SendFunc func(context.Context, Incident) error

// Config supplies bounded storage, observation identity and collaborators.
// Zero durations and capacities select documented defaults.
type Config struct {
	DB           *store.DB
	Audit        *audit.Log
	Clock        clock.Clock
	Enabled      bool
	MachineID    string
	Send         SendFunc
	Retention    time.Duration
	DeliveryTTL  time.Duration
	Freshness    time.Duration
	Cooldown     time.Duration
	MaxEvents    int
	MaxIncidents int
	MaxPending   int
}

// Manager serializes polling and delivery, with all durable state in SQLite.
type Manager struct {
	cfg    Config
	tickMu sync.Mutex
}

func New(cfg Config) (*Manager, error) {
	if cfg.DB == nil || cfg.Audit == nil {
		return nil, errors.New("security: DB and Audit are required")
	}
	if cfg.Clock == nil {
		cfg.Clock = clock.Real()
	}
	if cfg.Retention == 0 {
		cfg.Retention = DefaultRetention
	}
	if cfg.DeliveryTTL == 0 {
		cfg.DeliveryTTL = DefaultDeliveryTTL
	}
	if cfg.Freshness == 0 {
		cfg.Freshness = DefaultFreshness
	}
	if cfg.Cooldown == 0 {
		cfg.Cooldown = DefaultCooldown
	}
	if cfg.MaxEvents == 0 {
		cfg.MaxEvents = DefaultMaxEvents
	}
	if cfg.MaxIncidents == 0 {
		cfg.MaxIncidents = DefaultMaxIncidents
	}
	if cfg.MaxPending == 0 {
		cfg.MaxPending = DefaultMaxPending
	}
	if cfg.Retention <= 0 || cfg.DeliveryTTL <= 0 || cfg.Freshness <= 0 || cfg.Cooldown <= 0 || cfg.MaxEvents < 1 || cfg.MaxIncidents < 1 || cfg.MaxPending < 1 {
		return nil, errors.New("security: durations and capacities must be positive")
	}
	m := &Manager{cfg: cfg}
	if !cfg.Enabled || cfg.Send == nil {
		ctx := auth.WithActor(context.Background(), auth.Actor{Kind: auth.KindSystem, ID: "security", Name: "security"})
		if err := m.cancelDisabledDelivery(ctx); err != nil {
			return nil, err
		}
	}
	return m, nil
}

func (m *Manager) validate(e Event, native bool) error {
	var issues model.Issues
	issue := func(p, msg string) { issues = append(issues, model.Issue{Path: p, Message: msg}) }
	if !safeID.MatchString(e.EventID) {
		issue("eventId", "use 1 to 100 safe identifier characters")
	}
	if !safeID.MatchString(e.Fingerprint) {
		issue("fingerprint", "use 1 to 100 safe identifier characters")
	}
	if e.MachineID == "" || len(e.MachineID) > 100 {
		issue("machineId", "an immutable registered Machine id is required")
	}
	if e.Status != "firing" && e.Status != "resolved" {
		issue("status", "must be firing or resolved")
	}
	if e.Severity != "info" && e.Severity != "warning" && e.Severity != "critical" {
		issue("severity", "must be info, warning or critical")
	}
	if e.Source != "nginx" && e.Source != "auditd" && e.Source != "aide" && e.Source != "collector" && (!native || (e.Source != "machine" && e.Source != "service")) {
		issue("source", "unsupported security source")
	}
	if strings.TrimSpace(e.Summary) == "" || utf8.RuneCountInString(e.Summary) > 500 || strings.ContainsFunc(e.Summary, unicode.IsControl) {
		issue("summary", "use 1 to 500 printable characters without raw logs or secrets")
	}
	now := m.cfg.Clock.Now()
	if e.ObservedAt.IsZero() || e.ObservedAt.After(now.Add(5*time.Minute)) || e.ObservedAt.Before(now.Add(-m.cfg.Retention)) {
		issue("observedAt", "outside the retention or future clock-skew window")
	}
	if e.Service != "" && (len(e.Service) > 63 || !serviceName.MatchString(e.Service)) {
		issue("service", "invalid Service name")
	}
	return issues.Err()
}

// Ingest accepts a collector event. It never grants lifecycle privileges.
func (m *Manager) Ingest(ctx context.Context, e Event) (Receipt, error) {
	return m.ingest(ctx, e, false)
}

func (m *Manager) ingest(ctx context.Context, e Event, native bool) (Receipt, error) {
	if !m.cfg.Enabled {
		return Receipt{}, ErrDisabled
	}
	e.ObservedAt = e.ObservedAt.UTC()
	if err := m.validate(e, native); err != nil {
		return Receipt{}, err
	}
	raw, err := json.Marshal(e)
	if err != nil {
		return Receipt{}, err
	}
	digest := sha256.Sum256(raw)
	hash := hex.EncodeToString(digest[:])
	now := m.cfg.Clock.Now()
	historical := e.Historical || now.Sub(e.ObservedAt) > m.cfg.Freshness
	receipt := Receipt{Accepted: true, Historical: historical}
	// The audit stores identity only; collector summaries may contain sensitive
	// operational details and belong solely to the restricted incident API.
	err = m.cfg.DB.Write(ctx, func(tx *sql.Tx) error {
		var existing string
		err := tx.QueryRowContext(ctx, `SELECT digest,incident_id,historical FROM security_events WHERE event_id=?`, e.EventID).Scan(&existing, &receipt.IncidentID, &receipt.Historical)
		if err == nil {
			if existing != hash {
				return ErrConflict
			}
			receipt.Duplicate = true
			return nil
		}
		if !errors.Is(err, sql.ErrNoRows) {
			return err
		}
		var count int
		if err = tx.QueryRowContext(ctx, `SELECT COUNT(*) FROM resources WHERE id=? AND type='machine' AND phase='managed'`, e.MachineID).Scan(&count); err != nil {
			return err
		}
		if count != 1 {
			return model.Issues{{Path: "machineId", Message: "registered managed Machine id is required"}}
		}
		if e.Service != "" {
			if err = tx.QueryRowContext(ctx, `SELECT COUNT(*) FROM resources WHERE name=? AND type='service' AND phase!='unmanaged'`, e.Service).Scan(&count); err != nil {
				return err
			}
			if count != 1 {
				return model.Issues{{Path: "service", Message: "Service does not exist"}}
			}
		}
		key := conditionKey(e)
		var at int64
		var active, state string
		err = tx.QueryRowContext(ctx, `SELECT observed_at,incident_id,state FROM security_conditions WHERE key=?`, key).Scan(&at, &active, &state)
		if err != nil && !errors.Is(err, sql.ErrNoRows) {
			return err
		}
		if e.ObservedAt.UnixMilli() < at {
			historical = true
			receipt.Historical = true
		}
		if !historical {
			if errors.Is(err, sql.ErrNoRows) {
				if _, err = tx.ExecContext(ctx, `DELETE FROM security_conditions WHERE key IN (SELECT key FROM security_conditions WHERE state IN ('resolved','retired') ORDER BY updated_at LIMIT MAX(0,(SELECT COUNT(*)+1 FROM security_conditions)-?))`, m.cfg.MaxIncidents); err != nil {
					return err
				}
				if err = tx.QueryRowContext(ctx, `SELECT COUNT(*) FROM security_conditions`).Scan(&count); err != nil {
					return err
				}
				if count >= m.cfg.MaxIncidents {
					return ErrCapacity
				}
			}
			if state != "firing" {
				active = ""
			}
			if e.Status == "firing" && active == "" {
				if err = makeIncidentRoom(ctx, tx, m.cfg.MaxIncidents); err != nil {
					return err
				}
				active = newID()
				_, err = tx.ExecContext(ctx, `INSERT INTO security_incidents(id,condition_key,machine_id,service,source,fingerprint,state,severity,summary,opened_at,observed_at,updated_at,occurrences,revision) VALUES(?,?,?,?,?,?,?,?,?,?,?,?,1,1)`, active, key, e.MachineID, e.Service, e.Source, e.Fingerprint, e.Status, e.Severity, e.Summary, e.ObservedAt.UnixMilli(), e.ObservedAt.UnixMilli(), now.UnixMilli())
				if err != nil {
					return err
				}
				if err = m.enqueue(ctx, tx, active, 1, now); err != nil {
					return err
				}
			} else if active != "" {
				var rev int
				var last int64
				var sev string
				if err = tx.QueryRowContext(ctx, `SELECT revision,last_queued_at,severity FROM security_incidents WHERE id=?`, active).Scan(&rev, &last, &sev); err != nil {
					return err
				}
				rev++
				_, err = tx.ExecContext(ctx, `UPDATE security_incidents SET state=?,severity=?,summary=?,observed_at=?,updated_at=?,occurrences=occurrences+1,revision=? WHERE id=?`, e.Status, e.Severity, e.Summary, e.ObservedAt.UnixMilli(), now.UnixMilli(), rev, active)
				if err != nil {
					return err
				}
				var pending int
				if err = tx.QueryRowContext(ctx, `SELECT COUNT(*) FROM security_outbox WHERE incident_id=?`, active).Scan(&pending); err != nil {
					return err
				}
				settings, err := m.settings(ctx, tx)
				if err != nil {
					return err
				}
				if e.Status == "resolved" || severityRank(e.Severity) > severityRank(sev) || pending > 0 || now.Sub(time.UnixMilli(last)) >= time.Duration(settings.CooldownSeconds)*time.Second {
					if err = m.enqueue(ctx, tx, active, rev, now); err != nil {
						return err
					}
				}
			}
			receipt.IncidentID = active
			_, err = tx.ExecContext(ctx, `INSERT INTO security_conditions(key,observed_at,state,incident_id,updated_at) VALUES(?,?,?,?,?) ON CONFLICT(key) DO UPDATE SET observed_at=excluded.observed_at,state=excluded.state,incident_id=excluded.incident_id,updated_at=excluded.updated_at`, key, e.ObservedAt.UnixMilli(), e.Status, active, now.UnixMilli())
			if err != nil {
				return err
			}
		}
		_, err = tx.ExecContext(ctx, `INSERT INTO security_events(event_id,digest,received_at,observed_at,historical,incident_id,body) VALUES(?,?,?,?,?,?,?)`, e.EventID, hash, now.UnixMilli(), e.ObservedAt.UnixMilli(), receipt.Historical, receipt.IncidentID, string(raw))
		if err != nil {
			return err
		}
		_, err = tx.ExecContext(ctx, `DELETE FROM security_events WHERE seq IN (SELECT seq FROM security_events ORDER BY seq DESC LIMIT -1 OFFSET ?)`, m.cfg.MaxEvents)
		return err
	})
	outcome := audit.OutcomeOK
	if err != nil {
		outcome = audit.OutcomeError
	}
	if auditErr := m.cfg.Audit.Record(ctx, "security.ingest", e.EventID, outcome, map[string]any{"source": e.Source, "machineId": e.MachineID, "duplicate": receipt.Duplicate, "historical": receipt.Historical}); auditErr != nil {
		return receipt, auditErr
	}
	return receipt, err
}

func conditionKey(e Event) string {
	d := sha256.Sum256([]byte(e.MachineID + "\x00" + e.Service + "\x00" + e.Source + "\x00" + e.Fingerprint))
	return hex.EncodeToString(d[:])
}
func newID() string {
	var b [16]byte
	_, err := rand.Read(b[:])
	if err != nil {
		panic("security: random identity unavailable")
	}
	return "sec-" + hex.EncodeToString(b[:])
}
func severityRank(s string) int {
	switch s {
	case "critical":
		return 3
	case "warning":
		return 2
	default:
		return 1
	}
}

func makeIncidentRoom(ctx context.Context, tx *sql.Tx, limit int) error {
	_, err := tx.ExecContext(ctx, `DELETE FROM security_incidents WHERE seq IN (SELECT seq FROM security_incidents WHERE state IN ('resolved','retired') AND id NOT IN (SELECT incident_id FROM security_outbox) ORDER BY seq LIMIT MAX(0,(SELECT COUNT(*)+1 FROM security_incidents)-?))`, limit)
	if err != nil {
		return err
	}
	var n int
	if err = tx.QueryRowContext(ctx, `SELECT COUNT(*) FROM security_incidents`).Scan(&n); err != nil {
		return err
	}
	if n >= limit {
		return ErrCapacity
	}
	return nil
}

func increment(ctx context.Context, tx *sql.Tx, key string) error {
	_, err := tx.ExecContext(ctx, `INSERT INTO security_progress(key,value) VALUES(?,'1') ON CONFLICT(key) DO UPDATE SET value=CAST(value AS INTEGER)+1`, key)
	return err
}

func (m *Manager) enqueue(ctx context.Context, tx *sql.Tx, id string, rev int, now time.Time) error {
	state, until, err := m.queuePolicy(ctx, tx, id)
	if err != nil {
		return err
	}
	if state != "pending" {
		if _, err = tx.ExecContext(ctx, `DELETE FROM security_outbox WHERE incident_id=?`, id); err != nil {
			return err
		}
		_, err = tx.ExecContext(ctx, `UPDATE security_incidents SET delivery=?,last_error='' WHERE id=?`, state, id)
		return err
	}
	var exists, count int
	if err := tx.QueryRowContext(ctx, `SELECT COUNT(*) FROM security_outbox WHERE incident_id=?`, id).Scan(&exists); err != nil {
		return err
	}
	if exists == 0 {
		if err := tx.QueryRowContext(ctx, `SELECT COUNT(*) FROM security_outbox`).Scan(&count); err != nil {
			return err
		}
		if count >= m.cfg.MaxPending {
			if err := increment(ctx, tx, "overflow"); err != nil {
				return err
			}
			_, err := tx.ExecContext(ctx, `UPDATE security_incidents SET delivery='overflow',last_error='notification queue capacity exceeded' WHERE id=?`, id)
			return err
		}
	}
	_, err = tx.ExecContext(ctx, `INSERT INTO security_outbox(incident_id,revision,created_at,next_at) VALUES(?,?,?,?) ON CONFLICT(incident_id) DO UPDATE SET revision=excluded.revision,next_at=MAX(security_outbox.next_at,excluded.next_at)`, id, rev, now.UnixMilli(), max(now.UnixMilli(), until))
	if err != nil {
		return err
	}
	_, err = tx.ExecContext(ctx, `UPDATE security_incidents SET delivery='pending',last_queued_at=?,last_error='' WHERE id=?`, now.UnixMilli(), id)
	return err
}

// Checkpoint reads private progress markers used by the durable observation poller.
func (m *Manager) Checkpoint(ctx context.Context, key string) (string, error) {
	var value string
	err := m.cfg.DB.Reader().QueryRowContext(ctx, `SELECT value FROM security_progress WHERE key=?`, key).Scan(&value)
	if errors.Is(err, sql.ErrNoRows) {
		return "", nil
	}
	return value, err
}

func (m *Manager) checkpoint(ctx context.Context, key, value string) error {
	return m.cfg.DB.Write(ctx, func(tx *sql.Tx) error {
		_, err := tx.ExecContext(ctx, `INSERT INTO security_progress(key,value) VALUES(?,?) ON CONFLICT(key) DO UPDATE SET value=excluded.value`, key, value)
		return err
	})
}

func invalidConfig(field string) error { return fmt.Errorf("security: invalid %s", field) }

// cancelDisabledDelivery makes disabling notifications an explicit durable
// cancellation. Re-enabling never replays a queue from an older destination.
func (m *Manager) cancelDisabledDelivery(ctx context.Context) error {
	var pending int
	if err := m.cfg.DB.Reader().QueryRowContext(ctx, `SELECT COUNT(*) FROM security_outbox`).Scan(&pending); err != nil {
		return err
	}
	if pending == 0 {
		return nil
	}
	err := m.cfg.DB.Write(ctx, func(tx *sql.Tx) error {
		return cancelQueue(ctx, tx, m.cfg.Clock.Now())
	})
	if err != nil {
		return err
	}
	return m.cfg.Audit.Record(ctx, "security.delivery.disable", "", audit.OutcomeOK, map[string]any{"cancelled": pending})
}
