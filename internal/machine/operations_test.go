package machine

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/ACaccel/lcm/internal/maintenance"
	"github.com/ACaccel/lcm/internal/resource"
)

type fakePower struct {
	submits      int
	queries      int
	receipt      Receipt
	preflightErr error
	submitErr    error
	prepareErr   error
	queryErr     error
	prepareHook  func()
	queryHook    func()
}

func (f *fakePower) Preflight(context.Context) error { return f.preflightErr }
func (f *fakePower) Prepare(ctx context.Context, r PowerRequest) (Receipt, error) {
	if f.prepareHook != nil {
		f.prepareHook()
	}
	// Like the helper client, a cancelled request fails.
	if err := ctx.Err(); err != nil {
		return Receipt{}, err
	}
	return Receipt{State: "prepared", BootID: r.BootID}, f.prepareErr
}
func (f *fakePower) Submit(_ context.Context, r PowerRequest) (Receipt, error) {
	f.submits++
	f.receipt = Receipt{State: "accepted", BootID: r.BootID, AcceptedAt: time.Date(2026, 11, 1, 0, 1, 0, 0, time.UTC)}
	return f.receipt, f.submitErr
}
func (f *fakePower) Query(context.Context, PowerRequest) (Receipt, error) {
	f.queries++
	if f.queryHook != nil {
		f.queryHook()
	}
	return f.receipt, f.queryErr
}

func readyPower(t *testing.T) (*testEnv, *fakePower) {
	t.Helper()
	e := newEnv(t)
	p := &fakePower{}
	e.m.power = p
	e.cfg.Power = p
	e.put(t, testDoc, 0)
	e.poll(t)
	e.poll(t)
	return e, p
}
func enter(t *testing.T, e *testEnv) {
	t.Helper()
	if _, err := e.m.EnterMaintenance(DirectRequest(e.ctx), "workstation", "workloads prepared; manual recovery available", 1, ""); err != nil {
		t.Fatal(err)
	}
}

// powerReq is a confirmed request for the test machine.
func powerReq(action, key string, version int) resource.OperationRequest {
	return resource.OperationRequest{Action: action, Key: key, Reason: "scheduled maintenance", Confirm: "workstation", Version: version}
}

func TestOperationIntentIdempotencyAndRebootEvidence(t *testing.T) {
	e, p := readyPower(t)
	enter(t, e)
	if _, err := e.m.Submit(e.ctx, "workstation", powerReq("reboot", "policy-key", 1)); !errors.Is(err, ErrForbidden) {
		t.Fatalf("policy allowed: %v", err)
	}
	ctx := DirectRequest(e.ctx)
	op, err := e.m.Submit(ctx, "workstation", powerReq("reboot", "request-a", 1))
	if err != nil || op.State != "observing" {
		t.Fatalf("submit %+v %v", op, err)
	}
	prior, err := e.m.Submit(ctx, "workstation", powerReq("reboot", "request-a", 1))
	if err != nil || prior.ID != op.ID || p.submits != 1 {
		t.Fatalf("retry %+v %v count=%d", prior, err, p.submits)
	}
	if _, err = e.m.Submit(ctx, "workstation", powerReq("graceful-shutdown", "request-a", 1)); !errors.Is(err, ErrConflict) {
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

// A caller that goes away after the intent is stored must not leave a
// pending operation behind, which would block every later power action.
func TestCancelledSubmitDoesNotStayPending(t *testing.T) {
	e, p := readyPower(t)
	enter(t, e)
	ctx, cancel := context.WithCancel(DirectRequest(e.ctx))
	defer cancel()
	p.prepareHook = cancel
	op, err := e.m.Submit(ctx, "workstation", powerReq("reboot", "cancelled", 1))
	if err != nil {
		t.Fatalf("submit: %v", err)
	}
	stored, err := e.m.Operation(DirectRequest(e.ctx), "workstation", op.ID)
	if err != nil || stored.State == "pending" || p.submits != 1 {
		t.Fatalf("stored = %+v, %v, submits = %d", stored, err, p.submits)
	}
}

func TestShutdownRemainsUnverifiedAfterReturn(t *testing.T) {
	e, p := readyPower(t)
	enter(t, e)
	ctx := DirectRequest(e.ctx)
	op, err := e.m.Submit(ctx, "workstation", powerReq("graceful-shutdown", "shutdown-a", 1))
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
	failed, err := e.m.Submit(ctx, "workstation", powerReq("reboot", "prepare-after-resolution", 1))
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
	op, err := e.m.Submit(ctx, "workstation", powerReq("reboot", "unknown-a", 1))
	if err != nil || op.State != "indeterminate" {
		t.Fatalf("submit %+v %v", op, err)
	}
	e.m, _ = New(e.cfg)
	if err = e.m.Reconcile(ctx); err != nil {
		t.Fatal(err)
	}
	if _, err = e.m.Submit(ctx, "workstation", powerReq("reboot", "unknown-a", 1)); err != nil {
		t.Fatal(err)
	}
	if p.submits != 1 {
		t.Fatalf("repeated side effect %d", p.submits)
	}
	if _, err = e.m.Submit(ctx, "workstation", powerReq("power-on", "unsupported", 1)); !errors.Is(err, ErrUnsupported) {
		t.Fatalf("power-on: %v", err)
	}
}

func TestNewConfigurationAndBrokenCPUBaseline(t *testing.T) {
	e, _ := readyPower(t)
	changed := strings.Replace(testDoc, "consecutive: 2", "consecutive: 3", 1)
	e.put(t, changed, 1)
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
	op, err := e.m.Submit(DirectRequest(e.ctx), "workstation", powerReq("reboot", "slow-prepare", 1))
	if err != nil || op.State != "failed" || op.Unresolved || p.submits != 0 {
		t.Fatalf("unsafe handoff %+v %v submits=%d", op, err, p.submits)
	}
}

func TestHandoffRechecksOSCoverageAfterPreparation(t *testing.T) {
	e, p := readyPower(t)
	enter(t, e)
	p.prepareHook = func() { e.probe.snapshot.Coverage["uptime"] = Coverage{State: "unavailable"}; e.poll(t) }
	op, err := e.m.Submit(DirectRequest(e.ctx), "workstation", powerReq("reboot", "changed-evidence", 1))
	if err != nil || op.State != "failed" || p.submits != 0 {
		t.Fatalf("unsafe handoff %+v %v submits=%d", op, err, p.submits)
	}
}

func TestExpiryDoesNotRevokeAcceptedShutdown(t *testing.T) {
	e, _ := readyPower(t)
	enter(t, e)
	ctx := DirectRequest(e.ctx)
	op, err := e.m.Submit(ctx, "workstation", powerReq("graceful-shutdown", "same-boot", 1))
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
	op, err := e.m.Submit(ctx, "workstation", powerReq("reboot", "proof", 1))
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
	if _, err = e.m.EnterMaintenance(DirectRequest(e.ctx), "workstation", "prepared", 1, "wrong"); !errors.Is(err, ErrPrecondition) {
		t.Fatalf("bad preview: %v", err)
	}
	if _, err = e.m.EnterMaintenance(DirectRequest(e.ctx), "workstation", "prepared", 1, p.Hash); err != nil {
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

func TestSubmitEntersMaintenanceAndRecordsInventory(t *testing.T) {
	e, p := readyPower(t)
	ctx := DirectRequest(e.ctx)
	op, err := e.m.Submit(ctx, "workstation", powerReq("reboot", "single-request", 1))
	if err != nil || op.State != "observing" || op.RequestReason != "scheduled maintenance" || p.submits != 1 {
		t.Fatalf("submit %+v %v submits=%d", op, err, p.submits)
	}
	v, err := e.m.Maintenance(ctx, "workstation")
	if err != nil || v == nil || v.State != "active" || !v.Acknowledged || v.Action != "reboot" || v.Reason != "scheduled maintenance" || v.Preview.Hash == "" || !v.Preview.PartialCoverage {
		t.Fatalf("maintenance %+v %v", v, err)
	}
	if !v.EnteredAt.Equal(op.CreatedAt) {
		t.Fatalf("maintenance entered at %s, operation created at %s", v.EnteredAt, op.CreatedAt)
	}
	if _, _, err = e.cfg.Gate.Acquire(e.ctx); !errors.Is(err, maintenance.ErrActive) {
		t.Fatalf("write gate open: %v", err)
	}
	var entries int
	if err = e.db.Reader().QueryRow(`SELECT COUNT(*) FROM audit_log WHERE action IN ('machine.maintenance.enter','machine.operation.submit') AND json_extract(details,'$.reason') = 'scheduled maintenance'`).Scan(&entries); err != nil || entries != 2 {
		t.Fatalf("audit entries = %d, %v", entries, err)
	}
	// The replay returns the stored operation and enters nothing again.
	again, err := e.m.Submit(ctx, "workstation", powerReq("reboot", "single-request", 1))
	if err != nil || again.ID != op.ID || p.submits != 1 {
		t.Fatalf("replay %+v %v submits=%d", again, err, p.submits)
	}
	if after, _ := e.m.Maintenance(ctx, "workstation"); after == nil || !after.EnteredAt.Equal(v.EnteredAt) {
		t.Fatalf("replay changed maintenance: %+v", after)
	}
}

func TestSubmitReusesActiveMaintenance(t *testing.T) {
	e, p := readyPower(t)
	enter(t, e)
	ctx := DirectRequest(e.ctx)
	before, _ := e.m.Maintenance(ctx, "workstation")
	e.clk.Advance(time.Second)
	if _, err := e.m.Submit(ctx, "workstation", powerReq("graceful-shutdown", "reuse", 1)); err != nil || p.submits != 1 {
		t.Fatalf("submit %v submits=%d", err, p.submits)
	}
	after, _ := e.m.Maintenance(ctx, "workstation")
	if after == nil || after.Action != "" || !after.EnteredAt.Equal(before.EnteredAt) || after.Reason != before.Reason {
		t.Fatalf("maintenance replaced: %+v, was %+v", after, before)
	}
	var entries int
	if err := e.db.Reader().QueryRow(`SELECT COUNT(*) FROM audit_log WHERE action = 'machine.maintenance.enter'`).Scan(&entries); err != nil || entries != 1 {
		t.Fatalf("maintenance entries = %d, %v", entries, err)
	}
}

func TestRefusedSubmitLeavesNoMaintenance(t *testing.T) {
	cases := []struct {
		name   string
		mutate func(*testing.T, *testEnv, *fakePower, *resource.OperationRequest) func()
		want   error
	}{
		{"wrong name", func(_ *testing.T, _ *testEnv, _ *fakePower, r *resource.OperationRequest) func() {
			r.Confirm = "workstation-2"
			return nil
		}, ErrPrecondition},
		{"missing reason", func(_ *testing.T, _ *testEnv, _ *fakePower, r *resource.OperationRequest) func() {
			r.Reason = ""
			return nil
		}, ErrPrecondition},
		{"stale observations", func(_ *testing.T, e *testEnv, _ *fakePower, _ *resource.OperationRequest) func() {
			e.clk.Advance(5 * time.Minute)
			return nil
		}, ErrPrecondition},
		{"helper not ready", func(_ *testing.T, _ *testEnv, p *fakePower, _ *resource.OperationRequest) func() {
			p.preflightErr = errors.New("helper unavailable")
			return nil
		}, ErrPrecondition},
		{"stale version", func(_ *testing.T, _ *testEnv, _ *fakePower, r *resource.OperationRequest) func() {
			r.Version = 2
			return nil
		}, ErrConflict},
		{"write in flight", func(t *testing.T, e *testEnv, _ *fakePower, _ *resource.OperationRequest) func() {
			_, release, err := e.cfg.Gate.Acquire(e.ctx)
			if err != nil {
				t.Fatal(err)
			}
			return release
		}, maintenance.ErrActive},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			e, p := readyPower(t)
			req := powerReq("reboot", "refused", 1)
			if release := tc.mutate(t, e, p, &req); release != nil {
				defer release()
			}
			if _, err := e.m.Submit(DirectRequest(e.ctx), "workstation", req); !errors.Is(err, tc.want) {
				t.Fatalf("submit: %v, want %v", err, tc.want)
			}
			if v, err := e.m.Maintenance(e.ctx, "workstation"); err != nil || v != nil {
				t.Fatalf("maintenance left behind: %+v %v", v, err)
			}
			if ops, err := e.m.Operations(e.ctx, "workstation"); err != nil || len(ops) != 0 || p.submits != 0 {
				t.Fatalf("operations %+v %v submits=%d", ops, err, p.submits)
			}
		})
	}
}
