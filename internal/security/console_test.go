package security

import (
	"context"
	"errors"
	"sync"
	"testing"
	"time"

	"github.com/ACaccel/lcm/internal/store/storetest"
)

func TestHandlingVersionAndSilenceSurviveRestart(t *testing.T) {
	var sent []Incident
	f := setup(t, func(c *Config) {
		c.Send = func(_ context.Context, i Incident) error { sent = append(sent, i); return nil }
	})
	id := f.ingest(t, f.event("initial")).IncidentID
	until := f.clk.Now().Add(time.Hour)
	h, err := f.m.UpdateHandling(f.ctx, id, 0, HandlingUpdate{Action: "silence", Reason: "Maintenance", SilenceUntil: &until})
	if err != nil || h.Version != 1 {
		t.Fatalf("silence: %+v %v", h, err)
	}
	if _, err = f.m.UpdateHandling(f.ctx, id, 0, HandlingUpdate{Action: "acknowledge", Reason: "Read"}); !errors.Is(err, ErrVersionConflict) {
		t.Fatalf("stale write: %v", err)
	}
	h, err = f.m.UpdateHandling(f.ctx, id, 1, HandlingUpdate{Action: "acknowledge", Reason: "Read"})
	if err != nil || !h.Acknowledged {
		t.Fatalf("ack: %+v %v", h, err)
	}
	f.m, err = New(f.cfg)
	if err != nil {
		t.Fatal(err)
	}
	f.clk.Advance(time.Minute)
	f.ingest(t, f.event("during-silence"))
	if err = f.m.Deliver(f.ctx); err != nil {
		t.Fatal(err)
	}
	if len(sent) != 0 {
		t.Fatal("silence sent notification")
	}
	if rows := f.incidents(t); rows[0].State != "firing" || rows[0].Occurrences != 2 {
		t.Fatalf("handling changed detector: %+v", rows)
	}
	history, err := f.m.HandlingHistory(f.ctx, id)
	if err != nil || len(history) != 2 {
		t.Fatalf("history: %+v %v", history, err)
	}
	f.clk.Advance(time.Hour)
	if err = f.m.Deliver(f.ctx); err != nil {
		t.Fatal(err)
	}
	if len(sent) != 1 || sent[0].Occurrences != 2 {
		t.Fatalf("expiry did not send latest: %+v", sent)
	}
}
func TestConcurrentHandlingWritesOnlyOneVersion(t *testing.T) {
	f := setup(t)
	id := f.ingest(t, f.event("event")).IncidentID
	var wg sync.WaitGroup
	results := make(chan error, 2)
	for range 2 {
		wg.Go(func() {
			_, err := f.m.UpdateHandling(f.ctx, id, 0, HandlingUpdate{Action: "note", Reason: "Reviewed"})
			results <- err
		})
	}
	wg.Wait()
	close(results)
	ok, conflicts := 0, 0
	for err := range results {
		if err == nil {
			ok++
		} else if errors.Is(err, ErrVersionConflict) {
			conflicts++
		} else {
			t.Fatal(err)
		}
	}
	if ok != 1 || conflicts != 1 {
		t.Fatalf("success=%d conflict=%d", ok, conflicts)
	}
}
func TestSettingsFilteringRecoveryAndDisable(t *testing.T) {
	var sent []Incident
	f := setup(t, func(c *Config) {
		c.Send = func(_ context.Context, i Incident) error { sent = append(sent, i); return nil }
	})
	_, err := f.m.UpdateSettings(f.ctx, 0, SettingsUpdate{NotificationsEnabled: true, CooldownSeconds: 60, MinimumSeverity: "critical", Reason: "Critical only"})
	if err != nil {
		t.Fatal(err)
	}
	id := f.ingest(t, f.event("warning")).IncidentID
	if err = f.m.Deliver(f.ctx); err != nil {
		t.Fatal(err)
	}
	if len(sent) != 0 || f.incidents(t)[0].Delivery != "filtered" {
		t.Fatal("minimum severity bypass")
	}
	f.clk.Advance(time.Minute)
	e := f.event("critical")
	e.Severity = "critical"
	f.ingest(t, e)
	if err = f.m.Deliver(f.ctx); err != nil {
		t.Fatal(err)
	}
	f.clk.Advance(time.Minute)
	e = f.event("recovery")
	e.Status = "resolved"
	e.Severity = "info"
	f.ingest(t, e)
	if err = f.m.Deliver(f.ctx); err != nil {
		t.Fatal(err)
	}
	if len(sent) != 2 || sent[1].ID != id || sent[1].State != "resolved" {
		t.Fatalf("recovery filtered: %+v", sent)
	}
	f.clk.Advance(time.Minute)
	e = f.event("disable-pending")
	e.Severity = "critical"
	f.ingest(t, e)
	_, err = f.m.UpdateSettings(f.ctx, 1, SettingsUpdate{NotificationsEnabled: false, CooldownSeconds: 60, MinimumSeverity: "info", Reason: "Pause relay"})
	if err != nil {
		t.Fatal(err)
	}
	f.m, err = New(f.cfg)
	if err != nil {
		t.Fatal(err)
	}
	st, err := f.m.Status(f.ctx)
	if err != nil || st.NotificationsEnabled || st.Pending != 0 {
		t.Fatalf("persistent disable: %+v %v", st, err)
	}
	_, err = f.m.UpdateSettings(f.ctx, 2, SettingsUpdate{NotificationsEnabled: true, CooldownSeconds: 60, MinimumSeverity: "info", Reason: "Resume relay"})
	if err != nil {
		t.Fatal(err)
	}
	if err = f.m.Deliver(f.ctx); err != nil {
		t.Fatal(err)
	}
	if len(sent) != 2 {
		t.Fatal("reenable replayed cancelled queue")
	}
}
func TestRootDisableCannotBeOverridden(t *testing.T) {
	f := setup(t, func(c *Config) {
		c.Enabled = false
		c.Send = func(context.Context, Incident) error { t.Fatal("root disabled sent"); return nil }
	})
	st, err := f.m.UpdateSettings(f.ctx, 0, SettingsUpdate{NotificationsEnabled: true, CooldownSeconds: 60, MinimumSeverity: "info", Reason: "Enable preferences"})
	if err != nil || st.RootEnabled {
		t.Fatalf("root preference: %+v %v", st, err)
	}
	if _, err = f.m.StartNotificationTest(f.ctx, TestRequest{RequestID: "root-test", Reason: "Check"}); !errors.Is(err, ErrDisabled) {
		t.Fatalf("root disable bypass: %v", err)
	}
}
func TestNotificationTestDurableTwoRevisionFlow(t *testing.T) {
	var sent []Incident
	f := setup(t, func(c *Config) {
		c.Send = func(_ context.Context, i Incident) error { sent = append(sent, i); return nil }
	})
	if _, err := f.m.UpdateSettings(f.ctx, 0, SettingsUpdate{NotificationsEnabled: true, CooldownSeconds: 60, MinimumSeverity: "critical", Reason: "Filter"}); err != nil {
		t.Fatal(err)
	}
	request := TestRequest{RequestID: "test-request", Reason: "Operator acceptance"}
	first, err := f.m.StartNotificationTest(f.ctx, request)
	if err != nil {
		t.Fatal(err)
	}
	repeated, err := f.m.StartNotificationTest(f.ctx, request)
	if err != nil || first.IncidentID != repeated.IncidentID {
		t.Fatalf("replay: %+v %v", repeated, err)
	}
	if _, err = f.m.StartNotificationTest(f.ctx, TestRequest{RequestID: "test-request", Reason: "Changed"}); !errors.Is(err, ErrConflict) {
		t.Fatalf("conflict: %v", err)
	}
	f.m, err = New(f.cfg)
	if err != nil {
		t.Fatal(err)
	}
	if err = f.m.Deliver(f.ctx); err != nil {
		t.Fatal(err)
	}
	if len(sent) != 2 || sent[0].State != "firing" || sent[1].State != "resolved" || sent[0].ID != sent[1].ID {
		t.Fatalf("test revisions: %+v", sent)
	}
	status, err := f.m.NotificationTest(f.ctx, request.RequestID)
	if err != nil || status.State != "resolved-sent" || status.Pending {
		t.Fatalf("test status: %+v %v", status, err)
	}
	if _, err = f.m.StartNotificationTest(f.ctx, TestRequest{RequestID: "other", Reason: "Check"}); !errors.Is(err, ErrTestRate) {
		t.Fatalf("rate limit: %v", err)
	}
	// Collector-controlled identifiers never authorize a severity bypass.
	e := f.event("spoof-test")
	e.Fingerprint = "notification-test:spoof"
	e.Severity = "info"
	f.ingest(t, e)
	if err = f.m.Deliver(f.ctx); err != nil {
		t.Fatal(err)
	}
	if len(sent) != 2 {
		t.Fatal("collector spoofed trusted test")
	}
}
func TestHandlingRollsBackWithoutAudit(t *testing.T) {
	f := setup(t)
	id := f.ingest(t, f.event("event")).IncidentID
	storetest.FailInserts(t, f.cfg.DB, "audit_log")
	if _, err := f.m.UpdateHandling(f.ctx, id, 0, HandlingUpdate{Action: "acknowledge", Reason: "Read"}); err == nil {
		t.Fatal("handling changed without an audit entry")
	}
	if h, err := f.m.Handling(f.ctx, id); err != nil || h.Version != 0 || h.Acknowledged {
		t.Fatalf("handling = %+v, %v", h, err)
	}
}
