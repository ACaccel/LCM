package monitor

import (
	"slices"
	"testing"

	"github.com/ACaccel/lcm/internal/signal"
)

func (h *harness) flush() {
	h.t.Helper()
	if err := h.m.Flush(h.ctx); err != nil {
		h.t.Fatal(err)
	}
}

func storedHealth(h *harness, name string) (Health, bool) {
	h.t.Helper()
	var health string
	err := h.db.Reader().QueryRow(`SELECT health FROM monitor_resources WHERE resource = ?`, name).Scan(&health)
	if err != nil {
		return "", false
	}
	return Health(health), true
}

func TestNativeOnlyHealth(t *testing.T) {
	h := newHarness(t)
	labels := map[string]string{"room": "rack"}
	h.m.SetNative("ws", "machine", labels, HealthHealthy, nil)
	h.flush()
	s, ok := h.m.Resource("ws")
	if !ok || s.Health != HealthHealthy || s.Type != "machine" || s.Native == nil || s.Native.Health != HealthHealthy || len(s.Checks) != 0 {
		t.Fatalf("status = %+v", s)
	}
	hs := kinds(h.drain(), signal.KindHealth)
	if len(hs) != 1 || hs[0].Resource != "ws" || hs[0].ResourceType != "machine" || hs[0].Labels["room"] != "rack" ||
		hs[0].Data["health"] != "healthy" || hs[0].Data["previous"] != "unknown" {
		t.Fatalf("health signals = %+v", hs)
	}

	// The same value again, even with other reasons, is not a change.
	h.m.SetNative("ws", "machine", labels, HealthHealthy, nil)
	h.flush()
	if sigs := h.drain(); len(sigs) != 0 {
		t.Fatalf("repeated native health published %+v", sigs)
	}

	h.m.SetNative("ws", "machine", labels, HealthDegraded, []string{"cpu"})
	h.flush()
	hs = kinds(h.drain(), signal.KindHealth)
	if len(hs) != 1 || hs[0].Severity != signal.SeverityWarning || hs[0].Data["health"] != "degraded" ||
		!slices.Equal(hs[0].Data["reasons"].([]string), []string{"cpu"}) {
		t.Fatalf("degraded signals = %+v", hs)
	}
	if got, ok := storedHealth(h, "ws"); !ok || got != HealthDegraded {
		t.Fatalf("stored = %s %v", got, ok)
	}
	h.m.SetNative("ws", "machine", labels, HealthDegraded, []string{"cpu", "memory"})
	h.flush()
	if sigs := h.drain(); len(sigs) != 0 {
		t.Fatalf("reason change alone published %+v", sigs)
	}
	if s, _ := h.m.Resource("ws"); !slices.Equal(s.Native.Reasons, []string{"cpu", "memory"}) {
		t.Fatalf("reasons = %v", s.Native.Reasons)
	}
	if got := h.m.Healths([]string{"ws", "ghost"}); len(got) != 1 || got["ws"] != HealthDegraded {
		t.Fatalf("healths = %v", got)
	}

	// Sync with no definitions keeps a native-only resource.
	h.sync()
	if _, ok := h.m.Resource("ws"); !ok {
		t.Fatal("Sync dropped a native-only resource")
	}

	h.m.Forget("ws")
	h.flush()
	if _, ok := h.m.Resource("ws"); ok || len(h.m.Resources()) != 0 {
		t.Fatal("forgotten resource still listed")
	}
	if _, ok := storedHealth(h, "ws"); ok {
		t.Fatal("forgotten resource still stored")
	}
	if sigs := h.drain(); len(sigs) != 0 {
		t.Fatalf("forgetting published %+v", sigs)
	}
}

func TestCombinedHealthIsWorst(t *testing.T) {
	h := newHarness(t)
	h.sync(fakeDef("api", "web"))
	h.step(0)
	if h.health("api") != HealthHealthy {
		t.Fatalf("checks only = %s", h.health("api"))
	}
	h.drain()

	h.m.SetNative("api", "service", nil, HealthDegraded, []string{"slow"})
	h.flush()
	if h.health("api") != HealthDegraded {
		t.Fatalf("healthy checks + degraded native = %s", h.health("api"))
	}
	hs := kinds(h.drain(), signal.KindHealth)
	if len(hs) != 1 || hs[0].ResourceType != "service" || hs[0].Data["native"] != "degraded" {
		t.Fatalf("signals = %+v", hs)
	}

	// A down check is worse than degraded native health.
	h.fail("api/web")
	h.step(testInterval)
	if h.health("api") != HealthDown {
		t.Fatalf("down check + degraded native = %s", h.health("api"))
	}
	h.drain()

	// Native unknown is better than a down check: still down, no signal.
	h.m.SetNative("api", "service", nil, HealthUnknown, nil)
	h.flush()
	if h.health("api") != HealthDown {
		t.Fatalf("down check + unknown native = %s", h.health("api"))
	}
	if sigs := kinds(h.drain(), signal.KindHealth); len(sigs) != 0 {
		t.Fatalf("unchanged combined health published %+v", sigs)
	}

	// Recovering checks leave unknown native health: unknown beats healthy.
	h.pass("api/web")
	h.step(testInterval)
	if h.health("api") != HealthUnknown {
		t.Fatalf("healthy checks + unknown native = %s", h.health("api"))
	}

	// Removing the checks while native health is reported keeps the
	// resource with its native health.
	h.m.SetNative("api", "service", nil, HealthHealthy, nil)
	h.sync()
	s, ok := h.m.Resource("api")
	if !ok || len(s.Checks) != 0 || s.Health != HealthHealthy {
		t.Fatalf("native after checks removed = %+v %v", s, ok)
	}
}

func TestNativeSurvivesRestartWithoutSignal(t *testing.T) {
	h := newHarness(t)
	h.m.SetNative("ws", "machine", nil, HealthDegraded, []string{"disk"})
	h.flush()
	h.restart()

	// The first Sync after a restart runs before the machine reports.
	h.sync(fakeDef("api", "web"))
	h.flush()
	if _, ok := storedHealth(h, "ws"); !ok {
		t.Fatal("Sync deleted the stored health of a native-only resource")
	}
	h.drain()
	h.m.SetNative("ws", "machine", nil, HealthDegraded, []string{"disk"})
	h.flush()
	if hs := kinds(h.drain(), signal.KindHealth); len(hs) != 0 {
		t.Fatalf("restart looked like a health change: %+v", hs)
	}

	// A native-only resource that is not reported again is forgotten.
	h.m.SetNative("old", "machine", nil, HealthHealthy, nil)
	h.flush()
	h.restart()
	h.m.Forget("old")
	h.flush()
	if _, ok := storedHealth(h, "old"); ok {
		t.Fatal("Forget kept a stored native-only resource")
	}
}

// TestForgetIsSilent covers a resource that leaves observation while its
// checks are still registered: recomputing its health from the checks
// would announce a change that never happened.
func TestForgetIsSilent(t *testing.T) {
	h := newHarness(t)
	h.sync(fakeDef("ws", "web"))
	h.step(0)
	h.m.SetNative("ws", "machine", nil, HealthDegraded, []string{"cpu"})
	h.flush()
	if h.health("ws") != HealthDegraded {
		t.Fatalf("health = %s", h.health("ws"))
	}
	h.drain()

	// A sync that read the resources before the Forget.
	stale := h.m.Generation()
	h.m.Forget("ws")
	h.flush()
	if sigs := h.drain(); len(sigs) != 0 {
		t.Fatalf("Forget published %+v", sigs)
	}
	if _, ok := h.m.Resource("ws"); ok {
		t.Fatal("forgotten resource still listed")
	}
	if _, ok := storedHealth(h, "ws"); ok {
		t.Fatal("forgotten resource still stored")
	}
	var n int
	if err := h.db.Reader().QueryRow(`SELECT COUNT(*) FROM monitor_checks WHERE resource = 'ws'`).Scan(&n); err != nil || n != 0 {
		t.Fatalf("stored checks = %d %v", n, err)
	}
	calls := h.callCount("ws/web")
	h.step(testInterval)
	if h.callCount("ws/web") != calls {
		t.Fatal("a forgotten check still runs")
	}

	// The stale snapshot does not bring it back; a later one does.
	if err := h.m.SyncAt(h.ctx, stale, []Def{fakeDef("ws", "web")}); err != nil {
		t.Fatal(err)
	}
	if _, ok := h.m.Resource("ws"); ok {
		t.Fatal("a sync older than Forget re-added the resource")
	}
	h.sync(fakeDef("ws", "web"))
	if _, ok := h.m.Resource("ws"); !ok {
		t.Fatal("a sync after Forget did not add the resource")
	}
}

func TestSetNativeRejectsInvalidInput(t *testing.T) {
	h := newHarness(t)
	h.m.SetNative("Bad Name", "machine", nil, HealthHealthy, nil)
	h.m.SetNative("ws", "machine", nil, Health("bogus"), nil)
	h.flush()
	if len(h.m.Resources()) != 1 || h.health("ws") != HealthUnknown {
		t.Fatalf("resources = %+v", h.m.Resources())
	}
}

func TestAlertSignalCarriesKnownResourceType(t *testing.T) {
	h := newHarness(t)
	h.m.SetNative("ws", "machine", nil, HealthHealthy, nil)
	sigs, err := h.m.IngestAlert(h.ctx, "generic", []byte(`[{"resource":"ws","alert":"a","status":"firing"},{"resource":"other","alert":"b","status":"firing"}]`))
	if err != nil || len(sigs) != 2 {
		t.Fatalf("ingest = %+v %v", sigs, err)
	}
	if sigs[0].ResourceType != "machine" || sigs[1].ResourceType != "" {
		t.Fatalf("types = %q %q", sigs[0].ResourceType, sigs[1].ResourceType)
	}
}
