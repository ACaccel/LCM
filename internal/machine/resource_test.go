package machine

import (
	"database/sql"
	"errors"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/ACaccel/lcm/internal/model"
	"github.com/ACaccel/lcm/internal/monitor"
	"github.com/ACaccel/lcm/internal/resource"
)

func TestHooksCreateStatusAndResetCursors(t *testing.T) {
	e := newEnv(t)
	e.put(t, testDoc, 0)
	r, err := e.m.Get(e.ctx, "workstation")
	if err != nil {
		t.Fatal(err)
	}
	var status string
	if err := e.db.Reader().QueryRow(`SELECT status FROM resource_status WHERE resource_id = ?`, r.ID).Scan(&status); err != nil || status != "{}" {
		t.Fatalf("status row = %q %v", status, err)
	}
	if len(e.events) != 1 || e.events[0].Kind != "machine.save" || e.events[0].ID == 0 || e.events[0].ResourceID != r.ID {
		t.Fatalf("save event = %+v", e.events)
	}
	e.poll(t)
	cursors := func() int {
		var n int
		if err := e.db.Reader().QueryRow(`SELECT COUNT(*) FROM machine_cursors WHERE resource_id = ?`, r.ID).Scan(&n); err != nil {
			t.Fatal(err)
		}
		return n
	}
	if cursors() != 1 {
		t.Fatal("journal cursor not stored")
	}
	e.put(t, strings.Replace(testDoc, "consecutive: 2", "consecutive: 3", 1), 1)
	if cursors() != 1 {
		t.Fatal("cursor reset without a selection change")
	}
	e.put(t, strings.Replace(testDoc, "units: [app.service]", "units: [app.service, db.service]", 1), 2)
	if cursors() != 0 {
		t.Fatal("cursor kept after the unit selection changed")
	}
}

func TestPhaseRefusedWithUnresolvedOperation(t *testing.T) {
	e, _ := readyPower(t)
	enter(t, e)
	p := &fakePower{submitErr: errors.New("transport failed")}
	e.m.power = p
	if _, err := e.m.Submit(DirectRequest(e.ctx), "workstation", "reboot", "unknown", 1); err != nil {
		t.Fatal(err)
	}
	rec, _ := e.store.Get(e.ctx, "workstation")
	err := e.m.Type().ChangePhase(e.ctx, rec, resource.PhaseRetired, 1, "")
	// Maintenance holds the write gate, so the store refuses before the hook.
	if err == nil {
		t.Fatal("retired during maintenance")
	}
	// Without maintenance the hook itself refuses the unresolved operation.
	if err := e.db.Write(e.ctx, func(tx *sql.Tx) error {
		_, err := tx.Exec(`DELETE FROM machine_maintenance`)
		return err
	}); err != nil {
		t.Fatal(err)
	}
	err = e.m.Type().ChangePhase(e.ctx, rec, resource.PhaseRetired, 1, "")
	if !errors.Is(err, resource.ErrPrecondition) || !errors.Is(err, ErrPrecondition) {
		t.Fatalf("retired with unresolved operation: %v", err)
	}
	if got, _ := e.store.Get(e.ctx, "workstation"); got.Phase != resource.PhaseManaged {
		t.Fatalf("phase = %s", got.Phase)
	}
}

func TestNativeHealthFeed(t *testing.T) {
	e := newEnv(t)
	e.put(t, testDoc, 0)
	e.poll(t)
	if c := e.native.last(); c.resource != "workstation" || c.typ != model.ResourceMachine || c.health != monitor.HealthUnknown || c.labels["site"] != "home" {
		t.Fatalf("first sample feed = %+v", c)
	}
	e.poll(t)
	if c := e.native.last(); c.health != monitor.HealthHealthy || len(c.reasons) != 0 {
		t.Fatalf("healthy feed = %+v", c)
	}
	e.probe.snapshot.MemoryAvailable = 1
	e.poll(t)
	e.poll(t)
	if c := e.native.last(); c.health != monitor.HealthDegraded || !slices.Contains(c.reasons, "memory") {
		t.Fatalf("degraded feed = %+v", c)
	}

	// A failed probe is stored as a sample with unknown coverage and
	// reports unknown.
	e.clk.Advance(5 * time.Minute)
	e.probe.err = errors.New("probe failed")
	r, err := e.m.Observe(e.ctx, "workstation")
	if err != nil {
		t.Fatal(err)
	}
	if c := e.native.last(); c.health != monitor.HealthUnknown || r.Status.Health != "unknown" {
		t.Fatalf("failed probe feed = %+v", c)
	}

	e.setPhase(t, "workstation", resource.PhaseRetired, 1)
	if !slices.Equal(e.native.cleared, []string{"workstation"}) {
		t.Fatalf("cleared = %v", e.native.cleared)
	}
	calls := len(e.native.calls)
	e.setPhase(t, "workstation", resource.PhaseManaged, 1)
	if len(e.native.calls) != calls+1 || e.native.last().health != monitor.HealthUnknown {
		t.Fatalf("resume feed = %+v", e.native.calls[calls:])
	}
}

func TestTypeStatusAndActions(t *testing.T) {
	e, _ := readyPower(t)
	typ := e.m.Type()
	if typ.Name() != model.ResourceMachine {
		t.Fatal(typ.Name())
	}
	rec, _ := e.store.Get(e.ctx, "workstation")
	st, err := typ.Status(e.ctx, rec)
	if err != nil {
		t.Fatal(err)
	}
	firstSample := e.clk.Now().Add(-30 * time.Second)
	if st.State != resource.StateOnline || !st.StateSince.Equal(firstSample) {
		t.Fatalf("status = %+v, want online since %s", st, firstSample)
	}
	if _, ok := st.Detail.(Status); !ok {
		t.Fatalf("detail = %T", st.Detail)
	}
	actions := map[string]resource.Action{}
	for _, a := range typ.Actions(e.ctx, rec) {
		if a.Policy {
			t.Fatalf("policy may run %s", a.Name)
		}
		actions[a.Name] = a
	}
	if a := actions["reboot"]; a.Available || a.Kind != resource.KindOperation || a.Risk != resource.RiskDisruptive || a.Reason != "enter acknowledged maintenance first" {
		t.Fatalf("reboot before maintenance = %+v", a)
	}
	if !actions["retire"].Available || actions["resume"].Available || actions["unmanage"].Available || !actions["observe"].Available {
		t.Fatalf("actions = %+v", actions)
	}
	enter(t, e)
	for _, a := range typ.Actions(e.ctx, rec) {
		actions[a.Name] = a
	}
	if !actions["reboot"].Available || actions["graceful-shutdown"].Risk != resource.RiskDestructive || actions["retire"].Available || !actions["maintenance-exit"].Available {
		t.Fatalf("actions in maintenance = %+v", actions)
	}

	e.clk.Advance(5 * time.Minute)
	st, _ = typ.Status(e.ctx, rec)
	if st.State != resource.StateUnknown || !st.StateSince.Equal(e.clk.Now().Add(-5*time.Minute).Add(90*time.Second)) {
		t.Fatalf("stale status = %+v", st)
	}
}
