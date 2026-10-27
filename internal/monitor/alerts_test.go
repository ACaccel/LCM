package monitor

import (
	"errors"
	"os"
	"testing"
	"time"

	"github.com/ACaccel/lcm/internal/monitor/alert"
	"github.com/ACaccel/lcm/internal/signal"
)

func readFixture(t *testing.T, name string) []byte {
	t.Helper()
	b, err := os.ReadFile("alert/testdata/" + name) //nolint:gosec // fixture names are literals in this package
	if err != nil {
		t.Fatal(err)
	}
	return b
}

func TestIngestAlerts(t *testing.T) {
	h := newHarness(t)
	firing := readFixture(t, "alertmanager_firing.json")

	sigs, err := h.m.IngestAlert(h.ctx, alert.SourceAlertmanager, firing)
	if err != nil {
		t.Fatal(err)
	}
	if len(sigs) != 2 || sigs[0].ID == "" || !sigs[0].Time.Equal(epoch) {
		t.Fatalf("signals = %+v", sigs)
	}
	published := h.drain()
	if len(published) != 2 || published[0].Kind != signal.KindAlert || published[0].Service != "api" {
		t.Fatalf("published = %+v", published)
	}
	api, err := h.m.FiringAlerts(h.ctx, "api")
	if err != nil || len(api) != 1 || api[0].Name != "HighErrorRate" || api[0].Severity != signal.SeverityCritical ||
		api[0].Labels["env"] != "prod" || api[0].Message != "Error rate above 5% for 5m" || !api[0].ResolvedAt.IsZero() {
		t.Fatalf("api alerts = %+v, %v", api, err)
	}
	unmapped, _ := h.m.FiringAlerts(h.ctx, "")
	all, _ := h.m.AllFiringAlerts(h.ctx)
	if len(unmapped) != 1 || len(all) != 2 {
		t.Fatalf("unmapped %d, all %d", len(unmapped), len(all))
	}

	// Alertmanager repeats firing alerts; that is not a new event.
	h.clk.Advance(time.Hour)
	sigs, err = h.m.IngestAlert(h.ctx, alert.SourceAlertmanager, firing)
	if err != nil || len(sigs) != 0 || len(h.drain()) != 0 {
		t.Fatalf("repeat firing published %+v, %v", sigs, err)
	}
	api, _ = h.m.FiringAlerts(h.ctx, "api")
	if len(api) != 1 || !api[0].StartedAt.Equal(epoch) || !api[0].UpdatedAt.Equal(epoch.Add(time.Hour)) {
		t.Fatalf("refreshed alert = %+v", api)
	}

	sigs, err = h.m.IngestAlert(h.ctx, alert.SourceAlertmanager, readFixture(t, "alertmanager_resolved.json"))
	if err != nil || len(sigs) != 1 || sigs[0].Data[alert.DataStatus] != alert.StatusResolved {
		t.Fatalf("resolve signals = %+v, %v", sigs, err)
	}
	if api, _ = h.m.FiringAlerts(h.ctx, "api"); len(api) != 0 {
		t.Fatalf("alerts after resolve = %+v", api)
	}
	sigs, _ = h.m.IngestAlert(h.ctx, alert.SourceAlertmanager, readFixture(t, "alertmanager_resolved.json"))
	if len(sigs) != 0 {
		t.Fatalf("second resolve published %+v", sigs)
	}

	// The same alert firing again is a new event.
	sigs, _ = h.m.IngestAlert(h.ctx, alert.SourceAlertmanager, firing)
	if len(sigs) != 1 {
		t.Fatalf("re-fire after resolve published %d signals", len(sigs))
	}
}

func TestIngestAlertErrors(t *testing.T) {
	h := newHarness(t)
	if _, err := h.m.IngestAlert(h.ctx, "nagios", []byte(`{}`)); !errors.Is(err, alert.ErrUnknownSource) {
		t.Errorf("unknown source error = %v", err)
	}
	if _, err := h.m.IngestAlert(h.ctx, alert.SourceGeneric, []byte(`nope`)); !errors.Is(err, alert.ErrInvalidPayload) {
		t.Errorf("invalid payload error = %v", err)
	}
	if sigs, err := h.m.IngestAlert(h.ctx, alert.SourceUptimeKuma, readFixture(t, "uptimekuma_test.json")); err != nil || sigs != nil {
		t.Errorf("test notification = %+v, %v", sigs, err)
	}
	if h.count("monitor_alerts") != 0 {
		t.Error("rejected payloads stored alerts")
	}
	_ = h.m.Close()
	if _, err := h.m.IngestAlert(h.ctx, alert.SourceGeneric, []byte(`{"alert":"a","status":"firing"}`)); !errors.Is(err, ErrClosed) {
		t.Errorf("after Close = %v", err)
	}
}

func TestIngestAlertServiceLabelOption(t *testing.T) {
	h := newHarness(t, func(c *Config) { c.AlertServiceLabels = []string{"team"} })
	sigs, err := h.m.IngestAlert(h.ctx, alert.SourceGeneric,
		[]byte(`{"alert":"a","status":"firing","labels":{"team":"payments","service":"ignored"}}`))
	if err != nil || len(sigs) != 1 || sigs[0].Service != "payments" {
		t.Fatalf("signals = %+v, %v", sigs, err)
	}
}

func TestPruneRemovesOldResolvedAlerts(t *testing.T) {
	h := newHarness(t, manualPrune)
	_, _ = h.m.IngestAlert(h.ctx, alert.SourceGeneric, []byte(`{"service":"s","alert":"a","status":"firing"}`))
	_, _ = h.m.IngestAlert(h.ctx, alert.SourceGeneric, []byte(`{"service":"s","alert":"a","status":"resolved"}`))
	_, _ = h.m.IngestAlert(h.ctx, alert.SourceGeneric, []byte(`{"service":"s","alert":"b","status":"firing"}`))
	h.clk.Advance(DefaultRollupRetention + time.Hour)
	if err := h.m.Prune(h.ctx); err != nil {
		t.Fatal(err)
	}
	if got := h.count("monitor_alerts"); got != 1 {
		t.Errorf("alerts after prune = %d; the firing one must stay", got)
	}
}
