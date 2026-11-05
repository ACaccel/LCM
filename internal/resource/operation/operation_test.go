package operation_test

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"slices"
	"testing"
	"time"

	"github.com/ACaccel/lcm/internal/audit"
	"github.com/ACaccel/lcm/internal/auth"
	"github.com/ACaccel/lcm/internal/clock"
	"github.com/ACaccel/lcm/internal/resource"
	"github.com/ACaccel/lcm/internal/resource/operation"
	"github.com/ACaccel/lcm/internal/store"
	"github.com/ACaccel/lcm/internal/store/storetest"
)

type (
	evidence struct{ Seen string }
	request  struct{ Target string }
	receipt  struct{ Outcome string }
	op       = operation.Operation[evidence, request, receipt]
	opStore  = operation.Store[evidence, request, receipt]
)

var (
	errNotFound     = errors.New("test: not found")
	errConflict     = errors.New("test: conflict")
	errPrecondition = errors.New("test: precondition")
)

type env struct {
	db     *store.DB
	clk    *clock.Fake
	ctx    context.Context
	ops    *opStore
	events []resource.Event
}

func newEnv(t *testing.T) *env {
	t.Helper()
	e := &env{db: storetest.Open(t), clk: clock.NewFake(time.Date(2026, 11, 1, 0, 0, 0, 0, time.UTC)), ctx: auth.WithActor(context.Background(), auth.CLI)}
	var err error
	e.ops, err = operation.New[evidence, request, receipt](operation.Config{
		DB: e.db, Audit: audit.New(e.db, audit.WithClock(e.clk.Now)), Clock: e.clk, Type: "machine",
		Errors:  operation.Errors{NotFound: errNotFound, Conflict: errConflict, Precondition: func(r string) error { return fmt.Errorf("%w: %s", errPrecondition, r) }},
		Publish: func(evs []resource.Event) { e.events = append(e.events, evs...) },
	})
	if err != nil {
		t.Fatal(err)
	}
	for _, r := range []struct{ id, typ string }{{"m1", "machine"}, {"m2", "machine"}, {"r1", "router"}} {
		e.exec(t, `INSERT INTO resources(id,name,type,adapter,phase,version,created_at,updated_at) VALUES(?,?,?,'x','managed',1,0,0)`, r.id, r.id, r.typ)
	}
	return e
}

func (e *env) exec(t *testing.T, q string, args ...any) {
	t.Helper()
	if err := e.db.Write(e.ctx, func(tx *sql.Tx) error { _, err := tx.ExecContext(e.ctx, q, args...); return err }); err != nil {
		t.Fatal(err)
	}
}

// create stores a pending operation of resource id under key.
func (e *env) create(t *testing.T, id, key string) op {
	t.Helper()
	o := e.ops.Start(id, "reboot", 1, resource.ActorOf(auth.CLI), "planned")
	o.Request = request{Target: id}
	if err := e.db.Write(e.ctx, func(tx *sql.Tx) error {
		return e.ops.InsertTx(e.ctx, tx, &o, key, operation.RequestHash(id, "reboot", 1))
	}); err != nil {
		t.Fatal(err)
	}
	return o
}

// row returns the stored state, updated_at and body of an operation.
func (e *env) row(t *testing.T, id string) (string, int64, string) {
	t.Helper()
	var state, body string
	var updated int64
	if err := e.db.Reader().QueryRow(`SELECT state, updated_at, body FROM resource_operations WHERE id=?`, id).Scan(&state, &updated, &body); err != nil {
		t.Fatal(err)
	}
	return state, updated, body
}

func (e *env) kinds() []string {
	var out []string
	for _, ev := range e.events {
		out = append(out, ev.Kind+":"+ev.Reason)
	}
	return out
}

func (e *env) count(t *testing.T, q string, args ...any) int {
	t.Helper()
	var n int
	if err := e.db.Reader().QueryRow(q, args...).Scan(&n); err != nil {
		t.Fatal(err)
	}
	return n
}

func TestCreateAndReplay(t *testing.T) {
	e := newEnv(t)
	first := e.create(t, "m1", "key-1")
	if first.State != operation.Pending || !first.Unresolved || first.ID == "" || first.Actor.ID != "cli" || first.RequestReason != "planned" {
		t.Fatalf("created = %+v", first)
	}
	got, ok, err := e.ops.Replay(e.ctx, "m1", "key-1", operation.RequestHash("m1", "reboot", 1))
	if err != nil || !ok || got.ID != first.ID || got.Request.Target != "m1" {
		t.Fatalf("replay = %+v, %v, %v", got, ok, err)
	}
	// The same key with another action or version is a different request.
	if _, _, err := e.ops.Replay(e.ctx, "m1", "key-1", operation.RequestHash("m1", "reboot", 2)); !errors.Is(err, errConflict) {
		t.Fatalf("replay of another request: %v", err)
	}
	if _, ok, err := e.ops.Replay(e.ctx, "m1", "key-2", "h"); ok || err != nil {
		t.Fatalf("unknown key: %v, %v", ok, err)
	}
	// One unresolved operation per resource.
	second := e.ops.Start("m1", "reboot", 1, resource.ActorOf(auth.CLI), "again")
	err = e.db.Write(e.ctx, func(tx *sql.Tx) error { return e.ops.InsertTx(e.ctx, tx, &second, "key-2", "h") })
	if !errors.Is(err, errPrecondition) {
		t.Fatalf("second unresolved: %v", err)
	}
	if _, err := e.ops.Get(e.ctx, "m2", first.ID); !errors.Is(err, errNotFound) {
		t.Fatalf("operation of another resource: %v", err)
	}
	if !operation.ValidKey("cli-1:a.b_c") || operation.ValidKey("-x") || operation.ValidKey("bad key") {
		t.Fatal("key validation")
	}
	if !operation.ValidReason("why") || operation.ValidReason(" ") || operation.ValidReason("a\rb") {
		t.Fatal("reason validation")
	}
}

// A save applies only while the row still holds what the operation was
// read as; a concurrent writer is detected instead of overwritten.
func TestSaveIsCompareAndSwap(t *testing.T) {
	e := newEnv(t)
	created := e.create(t, "m1", "key-1")
	a, err := e.ops.Get(e.ctx, "m1", created.ID)
	if err != nil {
		t.Fatal(err)
	}
	b := a
	a.State = operation.Submitting
	if err := e.ops.Save(e.ctx, &a, operation.Change{}); err != nil {
		t.Fatal(err)
	}
	b.State, b.Unresolved, b.Reason = operation.Failed, false, resource.NeverSent
	if err := e.ops.Save(e.ctx, &b, operation.Change{}); !errors.Is(err, operation.ErrStale) || !errors.Is(err, errConflict) {
		t.Fatalf("stale save: %v", err)
	}
	if state, _, _ := e.row(t, a.ID); state != operation.Submitting {
		t.Fatalf("stale save overwrote the row: %s", state)
	}
	// The winner keeps saving from what it stored.
	a.State = operation.Observing
	if err := e.ops.Save(e.ctx, &a, operation.Change{}); err != nil {
		t.Fatal(err)
	}
	if !slices.Equal(e.kinds(), []string{"machine.operation.changed:submitting", "machine.operation.changed:observing"}) {
		t.Fatalf("events %v", e.kinds())
	}
}

func TestSaveSkipsUnchangedAndEventsOnlyOnOutcome(t *testing.T) {
	e := newEnv(t)
	o := e.create(t, "m1", "key-1")
	_, before, body := e.row(t, o.ID)
	e.clk.Advance(time.Minute)
	if err := e.ops.Save(e.ctx, &o, operation.Change{}); err != nil {
		t.Fatal(err)
	}
	if _, after, again := e.row(t, o.ID); after != before || again != body || len(e.events) != 0 {
		t.Fatalf("unchanged operation rewritten: %d -> %d, events %v", before, after, e.kinds())
	}
	// New evidence is stored, but only a change of state, resolution or
	// reason is an event.
	o.Receipt = &receipt{Outcome: "queried"}
	if err := e.ops.Save(e.ctx, &o, operation.Change{}); err != nil {
		t.Fatal(err)
	}
	if _, after, _ := e.row(t, o.ID); after != e.clk.Now().UnixMilli() || len(e.events) != 0 || !o.UpdatedAt.Equal(e.clk.Now()) {
		t.Fatalf("receipt change: updated %d, events %v", after, e.kinds())
	}
}

func TestSaveCheckAndAuditCommitWithTheChange(t *testing.T) {
	e := newEnv(t)
	o := e.create(t, "m1", "key-1")
	o.State = operation.Indeterminate
	refused := errors.New("authorization expired")
	if err := e.ops.Save(e.ctx, &o, operation.Change{Check: func(*sql.Tx) error { return refused }}); !errors.Is(err, refused) {
		t.Fatalf("check: %v", err)
	}
	if state, _, _ := e.row(t, o.ID); state != operation.Pending {
		t.Fatalf("saved despite the check: %s", state)
	}
	if err := e.ops.Save(e.ctx, &o, operation.Change{}); err != nil {
		t.Fatal(err)
	}

	restore := storetest.FailInserts(t, e.db, "audit_log")
	o.MarkResolved(resource.ActorOf(auth.CLI), "checked by hand", e.clk.Now())
	resolve := operation.Change{Audit: &operation.Audit{Action: "operation.resolve", Target: "m1", Details: map[string]any{"operationId": o.ID, "reason": "checked by hand"}}}
	if err := e.ops.Save(e.ctx, &o, resolve); err == nil {
		t.Fatal("saved without its audit entry")
	}
	if n := e.count(t, `SELECT COUNT(*) FROM resource_operations WHERE id=? AND unresolved=1`, o.ID); n != 1 {
		t.Fatal("resolution committed without its audit entry")
	}
	restore()
	if err := e.ops.Save(e.ctx, &o, resolve); err != nil {
		t.Fatal(err)
	}
	if n := e.count(t, `SELECT COUNT(*) FROM audit_log WHERE action='machine.operation.resolve' AND json_extract(details,'$.operationId')=?`, o.ID); n != 1 {
		t.Fatalf("audit entries = %d", n)
	}
	want := []string{"machine.operation.changed:indeterminate", "machine.operation.changed:resolved", "machine.operation.resolve:"}
	if !slices.Equal(e.kinds(), want) {
		t.Fatalf("events %v, want %v", e.kinds(), want)
	}
	if n := e.count(t, `SELECT COUNT(*) FROM resource_events WHERE resource_id='m1'`); n != len(want) {
		t.Fatalf("stored events = %d", n)
	}
}

func TestCloseAbandoned(t *testing.T) {
	cases := []struct {
		state      string
		neverSent  bool
		closed     bool
		want       string
		unresolved bool
		reason     string
	}{
		// Never submitted: certainly did nothing.
		{operation.Pending, true, true, operation.Failed, false, resource.NeverSent},
		// The type cannot prove that a pending request was never submitted.
		{operation.Pending, false, true, operation.Indeterminate, true, resource.InterruptedBeforeOutcome},
		// Submission may have started: never claimed as failed.
		{operation.Submitting, true, true, operation.Indeterminate, true, resource.InterruptedBeforeOutcome},
		{operation.Submitting, false, true, operation.Indeterminate, true, resource.InterruptedBeforeOutcome},
		{operation.Observing, true, false, operation.Observing, true, ""},
		{operation.Indeterminate, true, false, operation.Indeterminate, true, ""},
	}
	for _, tc := range cases {
		t.Run(fmt.Sprintf("%s/%v", tc.state, tc.neverSent), func(t *testing.T) {
			o := op{State: tc.state, Unresolved: true}
			if closed := o.CloseAbandoned(tc.neverSent); closed != tc.closed || o.State != tc.want || o.Unresolved != tc.unresolved || o.Reason != tc.reason {
				t.Fatalf("closed=%v %+v", closed, o)
			}
		})
	}
}

func TestResolvable(t *testing.T) {
	e := newEnv(t)
	for _, tc := range []struct {
		state      string
		unresolved bool
		ok         bool
	}{
		{operation.Indeterminate, true, true},
		{operation.Indeterminate, false, false},
		{operation.Observing, true, false},
		{operation.Pending, true, false},
		{operation.Failed, false, false},
	} {
		err := e.ops.CheckResolvable(op{State: tc.state, Unresolved: tc.unresolved})
		if (err == nil) != tc.ok || (err != nil && !errors.Is(err, errPrecondition)) {
			t.Fatalf("%s unresolved=%v: %v", tc.state, tc.unresolved, err)
		}
	}
}

func TestUnresolvedByTypeAndState(t *testing.T) {
	e := newEnv(t)
	a := e.create(t, "m1", "a")
	e.clk.Advance(time.Second)
	b := e.create(t, "m2", "b")
	b.State = operation.Observing
	if err := e.ops.Save(e.ctx, &b, operation.Change{}); err != nil {
		t.Fatal(err)
	}
	// An operation of another type is never read.
	e.exec(t, `INSERT INTO resource_operations(id,resource_id,idempotency_key,request_hash,action,state,created_at,updated_at,body) VALUES('r','r1','r','h','reboot','pending',0,0,'{}')`)
	ids := func(ops []op, err error) []string {
		t.Helper()
		if err != nil {
			t.Fatal(err)
		}
		var out []string
		for _, o := range ops {
			out = append(out, o.ID)
		}
		return out
	}
	if got := ids(e.ops.Unresolved(e.ctx, "")); !slices.Equal(got, []string{a.ID, b.ID}) {
		t.Fatalf("all = %v", got)
	}
	if got := ids(e.ops.Unresolved(e.ctx, "", operation.Pending, operation.Submitting)); !slices.Equal(got, []string{a.ID}) {
		t.Fatalf("abandoned candidates = %v", got)
	}
	if got := ids(e.ops.Unresolved(e.ctx, "m2", operation.Observing)); !slices.Equal(got, []string{b.ID}) {
		t.Fatalf("observing of m2 = %v", got)
	}
	if n, err := e.ops.UnresolvedCount(e.ctx, e.db.Reader(), "m1"); err != nil || n != 1 {
		t.Fatalf("count = %d, %v", n, err)
	}
	if got := ids(e.ops.List(e.ctx, "m1")); !slices.Equal(got, []string{a.ID}) {
		t.Fatalf("list = %v", got)
	}
}

func TestRequireDirect(t *testing.T) {
	forbidden := errors.New("forbidden")
	policy := auth.WithActor(context.Background(), auth.Actor{Kind: auth.KindSystem, ID: "run:1", Name: "policy"})
	for _, tc := range []struct {
		ctx  context.Context
		want error
	}{
		{auth.WithDirect(auth.WithActor(context.Background(), auth.CLI)), nil},
		{auth.WithDirect(auth.WithActor(context.Background(), auth.Actor{Kind: auth.KindUser, ID: "u", Name: "u"})), nil},
		{auth.WithActor(context.Background(), auth.CLI), forbidden},
		{auth.WithDirect(policy), forbidden},
		{auth.WithDirect(context.Background()), audit.ErrNoActor},
	} {
		if _, err := operation.RequireDirect(tc.ctx, forbidden); !errors.Is(err, tc.want) && (err != nil || tc.want != nil) {
			t.Fatalf("got %v, want %v", err, tc.want)
		}
	}
}
