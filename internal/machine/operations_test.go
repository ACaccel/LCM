package machine

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/ACaccel/lcm/internal/maintenance"
)

type fakePower struct {
	submits     int
	receipt     Receipt
	submitErr   error
	prepareErr  error
	prepareHook func()
}

func (f *fakePower) Preflight(context.Context) error { return nil }
func (f *fakePower) Prepare(_ context.Context, r PowerRequest) (Receipt, error) {
	if f.prepareHook != nil {
		f.prepareHook()
	}
	return Receipt{State: "prepared", BootID: r.BootID}, f.prepareErr
}
func (f *fakePower) Submit(_ context.Context, r PowerRequest) (Receipt, error) {
	f.submits++
	f.receipt = Receipt{State: "accepted", BootID: r.BootID, AcceptedAt: time.Date(2026, 11, 1, 0, 1, 0, 0, time.UTC)}
	return f.receipt, f.submitErr
}
func (f *fakePower) Query(context.Context, PowerRequest) (Receipt, error) { return f.receipt, nil }

func readyPower(t *testing.T) (*testEnv, *fakePower) {
	t.Helper()
	e := newEnv(t)
	p := &fakePower{}
	e.m.power = p
	e.cfg.Power = p
	if _, err := e.m.Put(e.ctx, []byte(testDoc), 0); err != nil {
		t.Fatal(err)
	}
	e.poll(t)
	e.poll(t)
	return e, p
}
func enter(t *testing.T, e *testEnv) {
	t.Helper()
	if _, err := e.m.EnterMaintenance(DirectRequest(e.ctx), "workstation", "workloads prepared; manual recovery available", true, 1); err != nil {
		t.Fatal(err)
	}
}

func TestOperationIntentIdempotencyAndRebootEvidence(t *testing.T) {
	e, p := readyPower(t)
	enter(t, e)
	if _, err := e.m.Submit(e.ctx, "workstation", "reboot", "policy-key", 1); !errors.Is(err, ErrForbidden) {
		t.Fatalf("policy allowed: %v", err)
	}
	ctx := DirectRequest(e.ctx)
	op, err := e.m.Submit(ctx, "workstation", "reboot", "request-a", 1)
	if err != nil || op.State != "observing" {
		t.Fatalf("submit %+v %v", op, err)
	}
	prior, err := e.m.Submit(ctx, "workstation", "reboot", "request-a", 1)
	if err != nil || prior.ID != op.ID || p.submits != 1 {
		t.Fatalf("retry %+v %v count=%d", prior, err, p.submits)
	}
	if _, err = e.m.Submit(ctx, "workstation", "graceful-shutdown", "request-a", 1); !errors.Is(err, ErrConflict) {
		t.Fatalf("collision: %v", err)
	}
	if err = e.m.ExitMaintenance(ctx, "workstation", "ready", 1); !errors.Is(err, ErrPrecondition) {
		t.Fatalf("unresolved exit: %v", err)
	}
	e.m, _ = New(e.cfg)
	if err = e.m.Reconcile(ctx); err != nil {
		t.Fatal(err)
	}
	op, _ = e.m.Operation(ctx, "workstation", op.ID)
	if op.State == "succeeded" || p.submits != 1 {
		t.Fatal("process restart treated as reboot")
	}
	e.probe.snapshot.BootID = "boot-b"
	e.poll(t)
	e.poll(t)
	if err = e.m.Reconcile(ctx); err != nil {
		t.Fatal(err)
	}
	op, _ = e.m.Operation(ctx, "workstation", op.ID)
	if op.State != "succeeded" || op.Unresolved {
		t.Fatalf("new boot %+v", op)
	}
	if _, _, err = e.cfg.Gate.Acquire(e.ctx); !errors.Is(err, maintenance.ErrActive) {
		t.Fatalf("maintenance auto-released: %v", err)
	}
	if err = e.m.ExitMaintenance(ctx, "workstation", "verified recovery", 1); err != nil {
		t.Fatal(err)
	}
}

func TestShutdownRemainsUnverifiedAfterReturn(t *testing.T) {
	e, p := readyPower(t)
	enter(t, e)
	ctx := DirectRequest(e.ctx)
	op, err := e.m.Submit(ctx, "workstation", "graceful-shutdown", "shutdown-a", 1)
	if err != nil || op.State != "indeterminate" {
		t.Fatalf("shutdown %+v %v", op, err)
	}
	e.probe.snapshot.BootID = "boot-b"
	e.poll(t)
	e.poll(t)
	if err = e.m.Reconcile(ctx); err != nil {
		t.Fatal(err)
	}
	op, _ = e.m.Operation(ctx, "workstation", op.ID)
	if op.State != "indeterminate" {
		t.Fatalf("off invented %+v", op)
	}
	op, err = e.m.Resolve(ctx, "workstation", op.ID, "manual return checked; off not verified", 1)
	if err != nil || op.State != "indeterminate" || op.Unresolved || op.ResolvedBy == nil {
		t.Fatalf("resolve %+v %v", op, err)
	}
	if err = e.m.ExitMaintenance(ctx, "workstation", "required checks verified", 1); !errors.Is(err, ErrPrecondition) {
		t.Fatalf("exit accepted old sample: %v", err)
	}
	e.clk.Advance(time.Nanosecond)
	p.prepareErr = errors.New("prepare refused")
	failed, err := e.m.Submit(ctx, "workstation", "reboot", "prepare-after-resolution", 1)
	if err != nil || failed.State != "failed" {
		t.Fatalf("prepare failure %+v %v", failed, err)
	}
	if err = e.m.ExitMaintenance(ctx, "workstation", "ready", 1); !errors.Is(err, ErrPrecondition) {
		t.Fatalf("new failed operation hid resolution anchor: %v", err)
	}
	e.poll(t)
	if err = e.m.ExitMaintenance(ctx, "workstation", "required checks verified", 1); err != nil {
		t.Fatal(err)
	}
	if p.submits != 1 {
		t.Fatalf("duplicate shutdown %d", p.submits)
	}
}

func TestAmbiguousHandoffDoesNotResubmit(t *testing.T) {
	e, p := readyPower(t)
	enter(t, e)
	ctx := DirectRequest(e.ctx)
	p.submitErr = errors.New("transport failed")
	op, err := e.m.Submit(ctx, "workstation", "reboot", "unknown-a", 1)
	if err != nil || op.State != "indeterminate" {
		t.Fatalf("submit %+v %v", op, err)
	}
	e.m, _ = New(e.cfg)
	if err = e.m.Reconcile(ctx); err != nil {
		t.Fatal(err)
	}
	if _, err = e.m.Submit(ctx, "workstation", "reboot", "unknown-a", 1); err != nil {
		t.Fatal(err)
	}
	if p.submits != 1 {
		t.Fatalf("repeated side effect %d", p.submits)
	}
	if _, err = e.m.Submit(ctx, "workstation", "power-on", "unsupported", 1); !errors.Is(err, ErrUnsupported) {
		t.Fatalf("power-on: %v", err)
	}
}

func TestNewConfigurationAndBrokenCPUBaseline(t *testing.T) {
	e, _ := readyPower(t)
	changed := strings.Replace(testDoc, "consecutive: 2", "consecutive: 3", 1)
	if _, err := e.m.Put(e.ctx, []byte(changed), 1); err != nil {
		t.Fatal(err)
	}
	r, _ := e.m.Get(e.ctx, "workstation")
	if r.Status.Fresh || r.Status.Health != "unknown" {
		t.Fatalf("stale configuration health: %+v", r.Status)
	}
	e.probe.snapshot.CPUTotal = 0
	e.probe.snapshot.CPUIdle = 0
	e.probe.snapshot.CPUCount = 0
	e.poll(t)
	e.probe.snapshot.CPUTotal = 1000
	e.probe.snapshot.CPUIdle = 100
	e.probe.snapshot.CPUCount = 4
	r = e.poll(t)
	if r.Status.Snapshot.CPUPercent != nil {
		t.Fatal("CPU averaged across missing sample")
	}
}

func TestHandoffRechecksHealthAfterSlowPreparation(t *testing.T) {
	e, p := readyPower(t)
	enter(t, e)
	p.prepareHook = func() { e.clk.Advance(2 * time.Minute) }
	op, err := e.m.Submit(DirectRequest(e.ctx), "workstation", "reboot", "slow-prepare", 1)
	if err != nil || op.State != "failed" || op.Unresolved || p.submits != 0 {
		t.Fatalf("unsafe handoff %+v %v submits=%d", op, err, p.submits)
	}
}

func TestHandoffRechecksOSCoverageAfterPreparation(t *testing.T) {
	e, p := readyPower(t)
	enter(t, e)
	p.prepareHook = func() { e.probe.snapshot.Coverage["uptime"] = Coverage{State: "unavailable"}; e.poll(t) }
	op, err := e.m.Submit(DirectRequest(e.ctx), "workstation", "reboot", "changed-evidence", 1)
	if err != nil || op.State != "failed" || p.submits != 0 {
		t.Fatalf("unsafe handoff %+v %v submits=%d", op, err, p.submits)
	}
}

func TestExpiryDoesNotRevokeAcceptedShutdown(t *testing.T) {
	e, _ := readyPower(t)
	enter(t, e)
	ctx := DirectRequest(e.ctx)
	op, err := e.m.Submit(ctx, "workstation", "graceful-shutdown", "same-boot", 1)
	if err != nil {
		t.Fatal(err)
	}
	e.clk.Advance(10 * time.Minute)
	e.poll(t)
	e.poll(t)
	if _, err = e.m.Resolve(ctx, "workstation", op.ID, "request expired", 1); !errors.Is(err, ErrPrecondition) {
		t.Fatalf("accepted action resolved on same boot: %v", err)
	}
}

func TestRebootRequiresUptimeAndBoundedBootEvidence(t *testing.T) {
	e, _ := readyPower(t)
	enter(t, e)
	ctx := DirectRequest(e.ctx)
	op, err := e.m.Submit(ctx, "workstation", "reboot", "proof", 1)
	if err != nil {
		t.Fatal(err)
	}
	e.probe.snapshot.BootID = "boot-b"
	e.probe.snapshot.Coverage["uptime"] = Coverage{State: "unavailable"}
	e.poll(t)
	e.poll(t)
	if err = e.m.Reconcile(ctx); err != nil {
		t.Fatal(err)
	}
	got, _ := e.m.Operation(ctx, "workstation", op.ID)
	if got.State == "succeeded" {
		t.Fatal("missing uptime accepted")
	}
	e.probe.snapshot.Coverage["uptime"] = Coverage{State: "available"}
	e.clk.Advance(time.Hour)
	e.poll(t)
	e.poll(t)
	if err = e.m.Reconcile(ctx); err != nil {
		t.Fatal(err)
	}
	got, _ = e.m.Operation(ctx, "workstation", op.ID)
	if got.State == "succeeded" {
		t.Fatal("unrelated later boot accepted")
	}
}

func TestMaintenanceRequiresNewObservationAndStablePreview(t *testing.T) {
	e, _ := readyPower(t)
	p, err := e.m.Preview(e.ctx, "workstation")
	if err != nil {
		t.Fatal(err)
	}
	if _, err = e.m.EnterMaintenance(DirectRequest(e.ctx), "workstation", "prepared", true, 1, "wrong"); !errors.Is(err, ErrPrecondition) {
		t.Fatalf("bad preview: %v", err)
	}
	if _, err = e.m.EnterMaintenance(DirectRequest(e.ctx), "workstation", "prepared", true, 1, p.Hash); err != nil {
		t.Fatal(err)
	}
	if err = e.m.ExitMaintenance(DirectRequest(e.ctx), "workstation", "ready", 1); !errors.Is(err, ErrPrecondition) {
		t.Fatalf("pre-entry sample accepted: %v", err)
	}
	e.poll(t)
	if err = e.m.ExitMaintenance(DirectRequest(e.ctx), "workstation", "ready", 1); err != nil {
		t.Fatal(err)
	}
}
