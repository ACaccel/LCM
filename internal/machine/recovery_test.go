package machine

import (
	"database/sql"
	"errors"
	"testing"
	"time"

	"github.com/ACaccel/lcm/internal/auth"
	"github.com/ACaccel/lcm/internal/resource"
	"github.com/ACaccel/lcm/internal/resource/operation"
	"github.com/ACaccel/lcm/internal/store/storetest"
)

// storedOp stores an operation of the test machine in state, as an
// interrupted Submit leaves it.
func storedOp(t *testing.T, e *testEnv, state string) Operation {
	t.Helper()
	r, err := e.m.Get(e.ctx, "workstation")
	if err != nil {
		t.Fatal(err)
	}
	op := e.m.ops.Start(r.ID, "reboot", r.Version, actorInfo(auth.CLI), "planned")
	now := op.CreatedAt
	op.Request = PowerRequest{ControllerID: r.ID, OperationID: op.ID, Action: "reboot", BootID: r.Status.Snapshot.BootID, IssuedAt: now, ValidUntil: now.Add(2 * time.Minute), Deadline: now.Add(10 * time.Minute)}
	if err := e.db.Write(e.ctx, func(tx *sql.Tx) error { return e.m.ops.InsertTx(e.ctx, tx, &op, op.ID, "h") }); err != nil {
		t.Fatal(err)
	}
	if state != operation.Pending {
		op.State = state
		if err := e.m.ops.Save(e.ctx, &op, operation.Change{}); err != nil {
			t.Fatal(err)
		}
	}
	return op
}

// An abandoned request failed only when it certainly never reached the
// OS: still pending, with a helper receipt (or none) proving that nothing
// was requested. Anything less certain stays indeterminate.
func TestAbandonedOperationOutcomes(t *testing.T) {
	cases := []struct {
		name       string
		state      string
		receipt    string
		queryErr   error
		noHelper   bool
		want       string
		unresolved bool
		reason     string
	}{
		{"pending, prepared receipt", operation.Pending, "prepared", nil, false, operation.Failed, false, resource.NeverSent},
		{"pending, failed receipt", operation.Pending, "failed", nil, false, operation.Failed, false, resource.NeverSent},
		{"pending, no receipt", operation.Pending, "", ErrReceiptMissing, false, operation.Failed, false, resource.NeverSent},
		{"pending, helper unreachable", operation.Pending, "", errors.New("timeout"), false, operation.Indeterminate, true, resource.HelperNoReceipt},
		{"pending, no helper", operation.Pending, "", nil, true, operation.Indeterminate, true, resource.HelperDownOnRecovery},
		// A database restored from before the handoff: the helper knows better.
		{"pending, accepted receipt", operation.Pending, "accepted", nil, false, operation.Indeterminate, true, resource.InterruptedBeforeOutcome},
		{"submitting, prepared receipt", operation.Submitting, "prepared", nil, false, operation.Indeterminate, true, resource.InterruptedBeforeOutcome},
		{"submitting, no receipt", operation.Submitting, "", ErrReceiptMissing, false, operation.Indeterminate, true, resource.InterruptedBeforeOutcome},
		{"submitting, accepted receipt", operation.Submitting, "accepted", nil, false, operation.Indeterminate, true, resource.InterruptedBeforeOutcome},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			e, p := readyPower(t)
			op := storedOp(t, e, tc.state)
			p.receipt = Receipt{State: tc.receipt, BootID: op.Request.BootID, AcceptedAt: op.CreatedAt}
			p.queryErr = tc.queryErr
			if tc.noHelper {
				e.m.power = nil
			}
			if err := e.m.Reconcile(e.ctx); err != nil {
				t.Fatal(err)
			}
			got, err := e.m.Operation(e.ctx, "workstation", op.ID)
			if err != nil || got.State != tc.want || got.Unresolved != tc.unresolved || got.Reason != tc.reason {
				t.Fatalf("closed = %+v, %v", got, err)
			}
			if p.submits != 0 {
				t.Fatal("an abandoned request was submitted")
			}
		})
	}
}

// A retry with the same key and a resolve close an abandoned request
// before reporting or judging it.
func TestAbandonedOperationClosedOnRetryAndResolve(t *testing.T) {
	e, p := readyPower(t)
	ctx := DirectRequest(e.ctx)
	op := storedOp(t, e, operation.Pending)
	p.receipt = Receipt{State: "prepared", BootID: op.Request.BootID}
	if err := e.db.Write(e.ctx, func(tx *sql.Tx) error {
		_, err := tx.Exec(`UPDATE resource_operations SET idempotency_key='retry', request_hash=? WHERE id=?`, operation.RequestHash(op.ResourceID, "reboot", 1), op.ID)
		return err
	}); err != nil {
		t.Fatal(err)
	}
	got, err := e.m.Submit(ctx, "workstation", powerReq("reboot", "retry", 1))
	if err != nil || got.ID != op.ID || got.State != operation.Failed || got.Unresolved || p.submits != 0 {
		t.Fatalf("retry = %+v, %v, submits %d", got, err, p.submits)
	}

	late := storedOp(t, e, operation.Submitting)
	if _, err := e.m.Resolve(ctx, "workstation", late.ID, "checked", 1); !errors.Is(err, ErrPrecondition) {
		t.Fatalf("resolve of an interrupted handoff on the same boot: %v", err)
	}
	if got, _ := e.m.Operation(ctx, "workstation", late.ID); got.State != operation.Indeterminate || !got.Unresolved {
		t.Fatalf("submitting after resolve = %+v", got)
	}
}

// Machine saves are compare-and-swap: a change made while an operation
// was being decided is detected and kept, never overwritten.
func TestOperationSaveDetectsConcurrentChange(t *testing.T) {
	t.Run("reconcile", func(t *testing.T) {
		e, p := readyPower(t)
		op := storedOp(t, e, operation.Pending)
		p.receipt = Receipt{State: "prepared", BootID: op.Request.BootID}
		p.queryHook = func() {
			cur, err := e.m.ops.Get(e.ctx, op.ResourceID, op.ID)
			if err != nil {
				t.Fatal(err)
			}
			cur.State, cur.Reason = operation.Indeterminate, "decided elsewhere"
			if err := e.m.ops.Save(e.ctx, &cur, operation.Change{}); err != nil {
				t.Fatal(err)
			}
		}
		if err := e.m.Reconcile(e.ctx); err != nil {
			t.Fatal(err)
		}
		got, _ := e.m.Operation(e.ctx, "workstation", op.ID)
		if got.State != operation.Indeterminate || got.Reason != "decided elsewhere" || !got.Unresolved {
			t.Fatalf("concurrent change overwritten: %+v", got)
		}
	})
	t.Run("submission fence", func(t *testing.T) {
		e, p := readyPower(t)
		enter(t, e)
		p.prepareHook = func() {
			ops, err := e.m.ops.Unresolved(e.ctx, "")
			if err != nil || len(ops) != 1 {
				t.Fatalf("unresolved = %+v, %v", ops, err)
			}
			ops[0].State, ops[0].Reason = operation.Indeterminate, "decided elsewhere"
			if err := e.m.ops.Save(e.ctx, &ops[0], operation.Change{}); err != nil {
				t.Fatal(err)
			}
		}
		op, err := e.m.Submit(DirectRequest(e.ctx), "workstation", powerReq("reboot", "raced", 1))
		if !errors.Is(err, operation.ErrStale) || p.submits != 0 {
			t.Fatalf("submit = %+v, %v, submits %d", op, err, p.submits)
		}
		if got, _ := e.m.Operation(e.ctx, "workstation", op.ID); got.Reason != "decided elsewhere" {
			t.Fatalf("concurrent change overwritten: %+v", got)
		}
	})
}

// operationRow returns the stored updated_at of an operation and the number
// of its change events.
func operationRow(t *testing.T, e *testEnv, id string) (int64, int) {
	t.Helper()
	var updated int64
	var events int
	if err := e.db.Reader().QueryRow(`SELECT updated_at, (SELECT COUNT(*) FROM resource_events WHERE kind='machine.operation.changed' AND json_extract(body,'$.operationId')=?) FROM resource_operations WHERE id=?`, id, id).Scan(&updated, &events); err != nil {
		t.Fatal(err)
	}
	return updated, events
}

// The minute Reconcile neither rewrites an operation whose outcome did not
// change nor asks the helper for a receipt that cannot change it.
func TestReconcileQueriesOnlyWhenAReceiptCanDecide(t *testing.T) {
	t.Run("accepted reboot", func(t *testing.T) {
		e, p := readyPower(t)
		enter(t, e)
		op, err := e.m.Submit(DirectRequest(e.ctx), "workstation", powerReq("reboot", "accepted", 1))
		if err != nil || op.State != operation.Observing {
			t.Fatalf("submit = %+v, %v", op, err)
		}
		updated, events := operationRow(t, e, op.ID)
		for range 3 {
			e.poll(t)
			if err := e.m.Reconcile(e.ctx); err != nil {
				t.Fatal(err)
			}
		}
		if u, n := operationRow(t, e, op.ID); u != updated || n != events || p.queries != 0 {
			t.Fatalf("updated %d -> %d, events %d -> %d, queries %d", updated, u, events, n, p.queries)
		}
		e.probe.snapshot.BootID = "boot-b"
		e.poll(t)
		e.poll(t)
		if err := e.m.Reconcile(e.ctx); err != nil {
			t.Fatal(err)
		}
		if got, _ := e.m.Operation(e.ctx, "workstation", op.ID); got.State != operation.Succeeded || p.queries != 0 {
			t.Fatalf("verified = %+v, queries %d", got, p.queries)
		}
	})
	t.Run("uncertain handoff", func(t *testing.T) {
		e, p := readyPower(t)
		enter(t, e)
		p.submitErr = errors.New("transport failed")
		op, err := e.m.Submit(DirectRequest(e.ctx), "workstation", powerReq("reboot", "uncertain", 1))
		if err != nil || op.State != operation.Indeterminate {
			t.Fatalf("submit = %+v, %v", op, err)
		}
		e.poll(t)
		if err := e.m.Reconcile(e.ctx); err != nil {
			t.Fatal(err)
		}
		if p.queries != 0 {
			t.Fatalf("queried without a new boot: %d", p.queries)
		}
		// A new boot within the deadline: the receipt now decides.
		e.probe.snapshot.BootID = "boot-b"
		e.poll(t)
		e.poll(t)
		if err := e.m.Reconcile(e.ctx); err != nil {
			t.Fatal(err)
		}
		got, _ := e.m.Operation(e.ctx, "workstation", op.ID)
		if got.State != operation.Succeeded || got.Unresolved || p.queries != 1 || p.submits != 1 {
			t.Fatalf("verified = %+v, queries %d, submits %d", got, p.queries, p.submits)
		}
	})
	t.Run("past the deadline", func(t *testing.T) {
		e, p := readyPower(t)
		enter(t, e)
		p.submitErr = errors.New("transport failed")
		op, err := e.m.Submit(DirectRequest(e.ctx), "workstation", powerReq("reboot", "late", 1))
		if err != nil {
			t.Fatal(err)
		}
		e.clk.Advance(11 * time.Minute)
		e.probe.snapshot.BootID = "boot-b"
		e.poll(t)
		e.poll(t)
		updated, _ := operationRow(t, e, op.ID)
		if err := e.m.Reconcile(e.ctx); err != nil {
			t.Fatal(err)
		}
		got, _ := e.m.Operation(e.ctx, "workstation", op.ID)
		if u, _ := operationRow(t, e, op.ID); got.State != operation.Indeterminate || u != updated || p.queries != 0 {
			t.Fatalf("late = %+v, rewritten %v, queries %d", got, u != updated, p.queries)
		}
	})
}

// The audit entry of a resolution commits with it.
func TestResolveAuditCommitsWithTheResolution(t *testing.T) {
	e, _ := readyPower(t)
	enter(t, e)
	ctx := DirectRequest(e.ctx)
	op, err := e.m.Submit(ctx, "workstation", powerReq("graceful-shutdown", "audited", 1))
	if err != nil || op.State != operation.Indeterminate {
		t.Fatalf("submit = %+v, %v", op, err)
	}
	e.probe.snapshot.BootID = "boot-b"
	e.poll(t)
	e.poll(t)
	restore := storetest.FailInserts(t, e.db, "audit_log")
	if _, err := e.m.Resolve(ctx, "workstation", op.ID, "returned by hand", 1); err == nil {
		t.Fatal("resolved without an audit entry")
	}
	if got, _ := e.m.Operation(ctx, "workstation", op.ID); !got.Unresolved {
		t.Fatal("resolution committed without its audit entry")
	}
	restore()
	if got, err := e.m.Resolve(ctx, "workstation", op.ID, "returned by hand", 1); err != nil || got.Unresolved {
		t.Fatalf("resolve = %+v, %v", got, err)
	}
	var entries int
	if err := e.db.Reader().QueryRow(`SELECT COUNT(*) FROM audit_log WHERE action='machine.operation.resolve'`).Scan(&entries); err != nil || entries != 1 {
		t.Fatalf("audit entries = %d, %v", entries, err)
	}
}
