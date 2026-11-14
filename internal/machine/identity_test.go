package machine

import (
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/ACaccel/lcm/internal/resource"
)

// movedHost makes the next samples read as a restored database on other
// hardware: another device and another boot.
func movedHost(t *testing.T, e *testEnv) {
	t.Helper()
	e.probe.snapshot.DeviceHash = "device-b"
	e.probe.snapshot.BootID = "boot-b"
	if r := e.poll(t); r.Status.Identity != "changed" {
		t.Fatalf("identity = %s", r.Status.Identity)
	}
}

func rebind(e *testEnv, version int) (Record, error) {
	e.clk.Advance(30 * time.Second)
	return e.m.RebindIdentity(DirectRequest(e.ctx), "workstation", version, "host moved", "workstation")
}

func TestRebindIdentityAcceptsTheObservedDevice(t *testing.T) {
	e, p := readyPower(t)
	before, err := e.m.Get(e.ctx, "workstation")
	if err != nil {
		t.Fatal(err)
	}
	movedHost(t, e)
	if caps, _ := e.m.Capabilities(e.ctx, "workstation"); caps[0].Available {
		t.Fatal("control available with a changed identity")
	}
	r, err := rebind(e, 1)
	if err != nil {
		t.Fatal(err)
	}
	if r.ID != before.ID || r.Status.Identity != "verified" {
		t.Fatalf("rebound record %+v", r)
	}
	var stored string
	if err := e.db.Reader().QueryRow(`SELECT identity_hash FROM resource_status WHERE resource_id=?`, r.ID).Scan(&stored); err != nil || stored != "device-b" {
		t.Fatalf("stored identity %q, %v", stored, err)
	}
	var details string
	if err := e.db.Reader().QueryRow(`SELECT details FROM audit_log WHERE action='machine.identity.rebind'`).Scan(&details); err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{"device-a", "device-b", "host moved"} {
		if !strings.Contains(details, want) {
			t.Fatalf("audit details %s lack %q", details, want)
		}
	}
	found := false
	for _, ev := range e.published(t) {
		found = found || ev.Kind == "machine.identity.rebind" && ev.ResourceID == r.ID
	}
	if !found {
		t.Fatal("no control event published")
	}
	// History is kept, the new identity stays verified and control returns.
	if snaps, err := e.m.History(e.ctx, "workstation", 0, 200); err != nil || len(snaps) < 4 {
		t.Fatalf("history %d, %v", len(snaps), err)
	}
	e.poll(t)
	if got := e.poll(t); got.Status.Identity != "verified" || got.Status.Health != "healthy" {
		t.Fatalf("after rebind %+v", got.Status)
	}
	if caps, _ := e.m.Capabilities(e.ctx, "workstation"); !caps[0].Available {
		t.Fatalf("control stays blocked: %+v", caps[0])
	}
	if p.submits != 0 {
		t.Fatal("rebind submitted a power request")
	}
	// A second device change is again refused until accepted.
	e.probe.snapshot.DeviceHash = "device-c"
	if got := e.poll(t); got.Status.Identity != "changed" {
		t.Fatalf("identity %s", got.Status.Identity)
	}
}

func TestRebindIdentityRefusals(t *testing.T) {
	e, _ := readyPower(t)
	direct := DirectRequest(e.ctx)
	if _, err := e.m.RebindIdentity(direct, "workstation", 1, "no change", "workstation"); !errors.Is(err, ErrPrecondition) {
		t.Fatalf("verified identity: %v", err)
	}
	movedHost(t, e)
	cases := []struct {
		name string
		call func() error
		want error
	}{
		{"policy", func() error { _, err := e.m.RebindIdentity(e.ctx, "workstation", 1, "r", "workstation"); return err }, ErrForbidden},
		{"no reason", func() error { _, err := e.m.RebindIdentity(direct, "workstation", 1, " ", "workstation"); return err }, ErrPrecondition},
		{"wrong name", func() error { _, err := e.m.RebindIdentity(direct, "workstation", 1, "r", "other"); return err }, ErrPrecondition},
		{"stale version", func() error { _, err := e.m.RebindIdentity(direct, "workstation", 7, "r", "workstation"); return err }, ErrConflict},
		{"unknown", func() error { _, err := e.m.RebindIdentity(direct, "missing", 1, "r", "missing"); return err }, ErrNotFound},
	}
	for _, c := range cases {
		if err := c.call(); !errors.Is(err, c.want) {
			t.Errorf("%s: %v, want %v", c.name, err, c.want)
		}
	}
	// A stale observation is no evidence.
	e.clk.Advance(time.Hour)
	if _, err := e.m.RebindIdentity(direct, "workstation", 1, "r", "workstation"); !errors.Is(err, ErrPrecondition) {
		t.Fatalf("stale: %v", err)
	}
	var entries int
	if err := e.db.Reader().QueryRow(`SELECT COUNT(*) FROM audit_log WHERE action='machine.identity.rebind'`).Scan(&entries); err != nil || entries != 0 {
		t.Fatalf("audit entries %d, %v", entries, err)
	}
	// A retired machine is not observed.
	e.setPhase(t, "workstation", resource.PhaseRetired, 1)
	if _, err := e.m.RebindIdentity(direct, "workstation", 1, "r", "workstation"); !errors.Is(err, ErrPrecondition) {
		t.Fatalf("retired: %v", err)
	}
}

// An incomplete sample must not become the expected identity.
func TestRebindIdentityRequiresACompleteSample(t *testing.T) {
	e, _ := readyPower(t)
	movedHost(t, e)
	e.probe.snapshot.Coverage["identity"] = Coverage{State: "unavailable"}
	if _, err := rebind(e, 1); !errors.Is(err, ErrPrecondition) {
		t.Fatalf("incomplete identity coverage: %v", err)
	}
	e.probe.snapshot.Coverage["identity"] = Coverage{State: "available"}
	e.probe.snapshot.DeviceHash = "device-a"
	if _, err := rebind(e, 1); !errors.Is(err, ErrPrecondition) {
		t.Fatalf("original device read again: %v", err)
	}
	// The refused sample was not committed: the stored status still reads
	// the last committed one.
	if r, _ := e.m.Get(e.ctx, "workstation"); r.Status.Identity != "changed" {
		t.Fatalf("identity %s", r.Status.Identity)
	}
}

// A request submitted on the old host is bound to its boot: after the
// database moved, no helper accepts it, and the rebind closes it without
// verification instead of leaving it to block power control.
func TestRebindIdentityClosesOperationsOfAnotherBoot(t *testing.T) {
	e, p := readyPower(t)
	enter(t, e)
	op, err := e.m.Submit(DirectRequest(e.ctx), "workstation", powerReq("reboot", "old-host", 1))
	if err != nil || op.State != "observing" {
		t.Fatalf("submit %+v %v", op, err)
	}
	movedHost(t, e)
	if _, err := rebind(e, 1); err != nil {
		t.Fatal(err)
	}
	got, err := e.m.Operation(e.ctx, "workstation", op.ID)
	if err != nil {
		t.Fatal(err)
	}
	if got.Unresolved || got.State == "succeeded" || got.Resolution == "" {
		t.Fatalf("operation %+v", got)
	}
	if p.submits != 1 {
		t.Fatalf("submits = %d", p.submits)
	}
	var entries int
	if err := e.db.Reader().QueryRow(`SELECT COUNT(*) FROM audit_log WHERE action='machine.operation.resolve' AND json_extract(details,'$.identityRebind') = 1`).Scan(&entries); err != nil || entries != 1 {
		t.Fatalf("resolve audit entries %d, %v", entries, err)
	}
	// Maintenance is kept and can be exited after a fresh observation.
	if v, err := e.m.Maintenance(e.ctx, "workstation"); err != nil || v == nil {
		t.Fatalf("maintenance %+v, %v", v, err)
	}
	e.poll(t)
	if err := e.m.ExitMaintenance(DirectRequest(e.ctx), "workstation", "host moved", 1); err != nil {
		t.Fatal(err)
	}
}

// A request that may still act on the current boot blocks the rebind.
func TestRebindIdentityRefusesALiveRequest(t *testing.T) {
	e, _ := readyPower(t)
	enter(t, e)
	op, err := e.m.Submit(DirectRequest(e.ctx), "workstation", powerReq("graceful-shutdown", "live", 1))
	if err != nil {
		t.Fatal(err)
	}
	e.probe.snapshot.DeviceHash = "device-b"
	if r := e.poll(t); r.Status.Identity != "changed" {
		t.Fatalf("identity %s", r.Status.Identity)
	}
	if _, err := e.m.RebindIdentity(DirectRequest(e.ctx), "workstation", 1, "reinstalled", "workstation"); !errors.Is(err, ErrPrecondition) {
		t.Fatalf("live request: %v", err)
	}
	got, _ := e.m.Operation(e.ctx, "workstation", op.ID)
	if !got.Unresolved {
		t.Fatalf("operation resolved: %+v", got)
	}
	if r, _ := e.m.Get(e.ctx, "workstation"); r.Status.Identity != "changed" {
		t.Fatalf("identity %s", r.Status.Identity)
	}
}

// Rebind is offered only while the identity reads changed.
func TestRebindIdentityAction(t *testing.T) {
	e, _ := readyPower(t)
	available := func() (bool, string) {
		rec, err := e.store.Get(e.ctx, "workstation")
		if err != nil {
			t.Fatal(err)
		}
		for _, a := range e.m.Type().Actions(e.ctx, rec) {
			if a.Name == "rebind-identity" {
				return a.Available, a.Reason
			}
		}
		t.Fatal("action not listed")
		return false, ""
	}
	if ok, reason := available(); ok || reason != resource.IdentityNotChanged {
		t.Fatalf("verified: %v %q", ok, reason)
	}
	movedHost(t, e)
	if ok, reason := available(); !ok {
		t.Fatalf("changed: %v %q", ok, reason)
	}
}
