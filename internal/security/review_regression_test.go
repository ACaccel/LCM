package security

import (
	"context"
	"database/sql"
	"errors"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/ACaccel/lcm/internal/audit"
	"github.com/ACaccel/lcm/internal/auth"
	"github.com/ACaccel/lcm/internal/clock"
	"github.com/ACaccel/lcm/internal/store"
)

func TestShorteningSilenceUsesNewExpiry(t *testing.T) {
	sends := 0
	f := setup(t, func(c *Config) { c.Send = func(context.Context, Incident) error { sends++; return nil } })
	id := f.ingest(t, f.event("shorter-silence")).IncidentID
	long := f.clk.Now().Add(time.Hour)
	if _, err := f.m.UpdateHandling(f.ctx, id, 0, HandlingUpdate{Action: "silence", Reason: "Maintenance", SilenceUntil: &long}); err != nil {
		t.Fatal(err)
	}
	short := f.clk.Now().Add(10 * time.Minute)
	if _, err := f.m.UpdateHandling(f.ctx, id, 1, HandlingUpdate{Action: "silence", Reason: "Maintenance ends sooner", SilenceUntil: &short}); err != nil {
		t.Fatal(err)
	}
	f.clk.Advance(11 * time.Minute)
	if err := f.m.Deliver(f.ctx); err != nil {
		t.Fatal(err)
	}
	if sends != 1 {
		t.Fatalf("new silence expired but queued notification remained delayed: sends=%d", sends)
	}
}

func TestNotificationTestQueueCapacityRollsBackCreation(t *testing.T) {
	f := setup(t, func(c *Config) {
		c.MaxPending = 1
		c.Send = func(context.Context, Incident) error { return nil }
	})
	f.ingest(t, f.event("fill-queue"))
	_, err := f.m.StartNotificationTest(f.ctx, TestRequest{RequestID: "overflow-test", Reason: "Acceptance"})
	if !errors.Is(err, ErrCapacity) {
		t.Fatalf("full queue must reject test atomically instead of creating an unrecoverable firing test: %v", err)
	}
	var count int
	if err = f.cfg.DB.Reader().QueryRowContext(f.ctx, `SELECT COUNT(*) FROM security_notification_tests`).Scan(&count); err != nil || count != 0 {
		t.Fatalf("test request leaked after capacity rejection: count=%d %v", count, err)
	}
	if rows := f.incidents(t); len(rows) != 1 {
		t.Fatalf("synthetic firing incident leaked: %+v", rows)
	}
}

func TestNotificationTestExpiryReleasesActiveCapacity(t *testing.T) {
	f := setup(t, func(c *Config) {
		c.MaxIncidents = 1
		c.DeliveryTTL = time.Minute
		c.Send = func(context.Context, Incident) error { return errors.New("unavailable") }
	})
	if _, err := f.m.StartNotificationTest(f.ctx, TestRequest{RequestID: "expires", Reason: "Acceptance"}); err != nil {
		t.Fatal(err)
	}
	f.clk.Advance(2 * time.Minute)
	if err := f.m.Deliver(f.ctx); err != nil {
		t.Fatal(err)
	}
	if _, err := f.m.Ingest(f.ctx, f.event("real-after-expiry")); err != nil {
		t.Fatalf("expired synthetic test exhausted real incident capacity: %v", err)
	}
}

func TestSettingsAuditFailureRetainsTransactionalHistory(t *testing.T) {
	f := setup(t)
	if err := f.cfg.DB.Write(f.ctx, func(tx *sql.Tx) error {
		_, err := tx.ExecContext(f.ctx, `CREATE TRIGGER reject_settings_audit BEFORE INSERT ON audit_log WHEN NEW.action='security.settings' BEGIN SELECT RAISE(ABORT,'audit unavailable'); END`)
		return err
	}); err != nil {
		t.Fatal(err)
	}
	_, err := f.m.UpdateSettings(f.ctx, 0, SettingsUpdate{NotificationsEnabled: false, CooldownSeconds: 3600, MinimumSeverity: "critical", Reason: "Pause delivery"})
	if err == nil {
		t.Fatal("audit failure was not reported")
	}
	settings, err := f.m.Settings(f.ctx)
	if err != nil {
		t.Fatal(err)
	}
	if settings.Version != 1 || settings.NotificationsEnabled {
		t.Fatalf("expected committed settings with transactional history: %+v", settings)
	}
	var actor, reason, severity string
	var at int64
	var enabled bool
	var cooldown int
	err = f.cfg.DB.Reader().QueryRowContext(f.ctx, `SELECT actor,at,reason,notifications_enabled,cooldown_seconds,minimum_severity FROM security_settings_history WHERE version=?`, settings.Version).Scan(&actor, &at, &reason, &enabled, &cooldown, &severity)
	if err != nil || actor != "system:cli" || at != f.clk.Now().UnixMilli() || reason != "Pause delivery" || enabled || cooldown != 3600 || severity != "critical" {
		t.Fatalf("settings committed without matching durable history: actor=%q at=%d reason=%q enabled=%v cooldown=%d severity=%q err=%v", actor, at, reason, enabled, cooldown, severity, err)
	}
}

func TestLegacySentEpisodeRecoverySurvivesConsoleMigration(t *testing.T) {
	ctx := auth.WithActor(context.Background(), auth.CLI)
	clk := clock.NewFake(time.Date(2026, 11, 1, 1, 0, 0, 0, time.UTC))
	path := filepath.Join(t.TempDir(), "legacy.db")
	legacy, err := sql.Open("sqlite", path)
	if err != nil {
		t.Fatal(err)
	}
	if _, err = legacy.ExecContext(ctx, `CREATE TABLE schema_migrations(version INTEGER PRIMARY KEY,name TEXT NOT NULL,applied_at INTEGER NOT NULL)`); err != nil {
		t.Fatal(err)
	}
	entries, err := os.ReadDir("../store/migrations")
	if err != nil {
		t.Fatal(err)
	}
	for _, entry := range entries {
		prefix, _, ok := strings.Cut(entry.Name(), "_")
		version, conversionErr := strconv.Atoi(prefix)
		if !ok || conversionErr != nil || version > 11 || !strings.HasSuffix(entry.Name(), ".sql") {
			continue
		}
		body, err := os.ReadFile(filepath.Join("../store/migrations", entry.Name()))
		if err != nil {
			t.Fatal(err)
		}
		if _, err = legacy.ExecContext(ctx, string(body)); err != nil {
			t.Fatalf("legacy migration %s: %v", entry.Name(), err)
		}
		if _, err = legacy.ExecContext(ctx, `INSERT INTO schema_migrations VALUES(?,?,0)`, version, strings.TrimSuffix(entry.Name(), ".sql")); err != nil {
			t.Fatal(err)
		}
	}
	e := Event{EventID: "legacy-recovery", MachineID: "machine-a", Source: "nginx", Fingerprint: "abuse.auth", Status: "resolved", Severity: "info", Summary: "Recovered", ObservedAt: clk.Now().Add(time.Minute)}
	stamp := clk.Now().UnixMilli()
	if _, err = legacy.ExecContext(ctx, `INSERT INTO machines(id,name,version,created_at,updated_at) VALUES('machine-a','workstation',1,?,?)`, stamp, stamp); err != nil {
		t.Fatal(err)
	}
	if _, err = legacy.ExecContext(ctx, `INSERT INTO security_incidents(id,condition_key,machine_id,service,source,fingerprint,state,severity,summary,opened_at,observed_at,updated_at,occurrences,revision,last_queued_at,delivery) VALUES('legacy',?,'machine-a','','nginx','abuse.auth','firing','warning','Legacy episode',?,?,?,1,1,?,'sent')`, conditionKey(e), stamp, stamp, stamp, stamp); err != nil {
		t.Fatal(err)
	}
	if _, err = legacy.ExecContext(ctx, `INSERT INTO security_conditions(key,observed_at,state,incident_id,updated_at) VALUES(?,?,'firing','legacy',?)`, conditionKey(e), stamp, stamp); err != nil {
		t.Fatal(err)
	}
	if err = legacy.Close(); err != nil {
		t.Fatal(err)
	}
	db, err := store.Open(ctx, path)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = db.Close() })
	var sent []Incident
	m, err := New(Config{DB: db, Clock: clk, Audit: audit.New(db, audit.WithClock(clk.Now)), Enabled: true, MachineID: "machine-a", Send: func(_ context.Context, i Incident) error { sent = append(sent, i); return nil }})
	if err != nil {
		t.Fatal(err)
	}
	if _, err = m.UpdateSettings(ctx, 0, SettingsUpdate{NotificationsEnabled: true, CooldownSeconds: 60, MinimumSeverity: "critical", Reason: "Critical only"}); err != nil {
		t.Fatal(err)
	}
	clk.Advance(time.Minute)
	if _, err = m.Ingest(ctx, e); err != nil {
		t.Fatal(err)
	}
	if err = m.Deliver(ctx); err != nil {
		t.Fatal(err)
	}
	if len(sent) != 1 || sent[0].ID != "legacy" || sent[0].State != "resolved" {
		t.Fatalf("legacy delivered firing message lost its recovery across schema migration: %+v", sent)
	}
}
