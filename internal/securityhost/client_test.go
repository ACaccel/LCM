package securityhost

import (
	"context"
	"encoding/json"
	"errors"
	"slices"
	"sync"
	"testing"
	"time"

	"github.com/ACaccel/lcm/internal/audit"
	"github.com/ACaccel/lcm/internal/auth"
	"github.com/ACaccel/lcm/internal/clock"
	"github.com/ACaccel/lcm/internal/store/storetest"
)

type fakeRunner struct {
	mu            sync.Mutex
	calls         []string
	fail          bool
	responseState string
	digest        string
	operations    map[string]Operation
	now           time.Time
	// onApply runs while the helper holds an apply request.
	onApply func()
}

func (f *fakeRunner) Run(_ context.Context, r Request) (Response, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.calls = append(f.calls, r.Command)
	if r.Command == "apply" && f.onApply != nil {
		f.onApply()
	}
	if f.fail {
		return Response{}, errors.New("local secret must never escape")
	}
	if f.digest == "" {
		f.digest = "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"
	}
	switch r.Command {
	case "status":
		return Response{Version: 1, Status: &HostStatus{Available: true, MachineID: r.MachineID, EffectiveDigest: f.digest}}, nil
	case "preview":
		return Response{Version: 1, Preview: &Preview{RequestID: r.RequestID, Action: r.Action, PreviewHash: f.digest, ExpiresAt: f.now.Add(10 * time.Minute), Changes: []string{"Validated fixed change"}}}, nil
	case "apply":
		op := Operation{ID: r.RequestID, Action: "aide.check", State: "running", PreviewHash: r.PreviewHash, EffectiveDigest: f.digest}
		f.operations[r.RequestID] = op
		return Response{Version: 1, Operation: &op}, nil
	case "operation":
		op, ok := f.operations[r.RequestID]
		if !ok {
			return Response{}, ErrNotFound
		}
		if f.responseState != "" {
			op.State = f.responseState
		}
		return Response{Version: 1, Operation: &op}, nil
	}
	return Response{}, ErrUnavailable
}
func fixture(t *testing.T) (*Client, *fakeRunner, *clock.Fake, context.Context) {
	t.Helper()
	db := storetest.Open(t)
	clk := clock.NewFake(time.Date(2026, 11, 2, 1, 0, 0, 0, time.UTC))
	runner := &fakeRunner{now: clk.Now(), operations: map[string]Operation{}}
	c, err := New(Config{DB: db, Audit: audit.New(db, audit.WithClock(clk.Now)), Clock: clk, MachineID: "machine-a", Runner: runner})
	if err != nil {
		t.Fatal(err)
	}
	return c, runner, clk, auth.WithActor(context.Background(), auth.CLI)
}
func intent(id string) PreviewRequest {
	return PreviewRequest{RequestID: id, Action: "aide.check", Reason: "Routine integrity check", ExpectedDigest: "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa", Settings: json.RawMessage(`{}`)}
}
func prepare(t *testing.T, c *Client, ctx context.Context, id string) (PreviewRequest, Preview) {
	t.Helper()
	r := intent(id)
	p, err := c.Preview(ctx, r)
	if err != nil {
		t.Fatal(err)
	}
	return r, p
}

func TestStatusReportsConfiguration(t *testing.T) {
	c, _, _, ctx := fixture(t)
	if s, err := c.Status(ctx); err != nil || !s.Configured || !s.Available {
		t.Fatalf("status=%+v err=%v", s, err)
	}
	u := Unconfigured()
	if u.Configured || u.Available || len(u.Capabilities) != len(actions) || u.Reason == "" {
		t.Fatalf("unconfigured=%+v", u)
	}
}

func TestUnavailableIsReadOnlyWithoutHelperErrors(t *testing.T) {
	c, r, _, ctx := fixture(t)
	r.fail = true
	s, err := c.Status(ctx)
	if err != nil || s.Available || !s.Configured || len(s.Capabilities) != 6 || s.Reason == "local secret must never escape" {
		t.Fatalf("status=%+v err=%v", s, err)
	}
	for _, capability := range s.Capabilities {
		if capability.Enabled {
			t.Fatal("unavailable helper enabled a mutation")
		}
	}
}
func TestPreviewReplayAndConflictPersistAcrossRestart(t *testing.T) {
	c, r, _, ctx := fixture(t)
	in, p := prepare(t, c, ctx, "one")
	again, err := c.Preview(ctx, in)
	if err != nil || again.PreviewHash != p.PreviewHash {
		t.Fatalf("replay=%+v err=%v", again, err)
	}
	if len(r.calls) != 1 {
		t.Fatal("preview was repeated at privileged boundary")
	}
	next, err := New(c.cfg)
	if err != nil {
		t.Fatal(err)
	}
	in.Reason = "Changed intent"
	if _, err = next.Preview(ctx, in); !errors.Is(err, ErrConflict) {
		t.Fatalf("want conflict, got %v", err)
	}
	op, err := next.Operation(ctx, "one")
	if err != nil || op.State != "prepared" {
		t.Fatalf("restart op=%+v err=%v", op, err)
	}
}
func TestApplyReplayQueriesInsteadOfStartingTwice(t *testing.T) {
	c, r, _, ctx := fixture(t)
	in, p := prepare(t, c, ctx, "one")
	request := ApplyRequest{RequestID: in.RequestID, PreviewHash: p.PreviewHash, Reason: in.Reason}
	op, err := c.Apply(ctx, request)
	if err != nil || op.State != "running" {
		t.Fatalf("op=%+v err=%v", op, err)
	}
	r.responseState = "succeeded"
	op, err = c.Apply(ctx, request)
	if err != nil || op.State != "succeeded" {
		t.Fatalf("replay=%+v err=%v", op, err)
	}
	applies := 0
	for _, call := range r.calls {
		if call == "apply" {
			applies++
		}
	}
	if applies != 1 {
		t.Fatalf("privileged apply calls=%d", applies)
	}
}
func TestApplyOutcomeSurvivesCallerCancellation(t *testing.T) {
	c, r, _, base := fixture(t)
	in, p := prepare(t, c, base, "one")
	ctx, cancel := context.WithCancel(base)
	defer cancel()
	// The client disconnects while the root helper is already changing the host.
	r.onApply = cancel
	op, err := c.Apply(ctx, ApplyRequest{RequestID: in.RequestID, PreviewHash: p.PreviewHash, Reason: in.Reason})
	if err != nil || op.State != "running" {
		t.Fatalf("op=%+v err=%v", op, err)
	}
	stored, _, _, err := c.lookup(base, in.RequestID)
	if err != nil || stored.State != "running" || stored.EffectiveDigest != r.digest {
		t.Fatalf("stored=%+v err=%v", stored, err)
	}
	entries, err := c.cfg.Audit.List(base, 50, 0)
	if err != nil {
		t.Fatal(err)
	}
	var actions []string
	for _, e := range entries {
		if e.Target == in.RequestID {
			actions = append(actions, e.Action)
		}
	}
	slices.Sort(actions)
	if want := []string{"security.host.apply", "security.host.apply.start", "security.host.preview"}; !slices.Equal(actions, want) {
		t.Fatalf("audit actions=%v, want %v", actions, want)
	}
}

func TestExpiredPreviewAndAnotherActorCannotApply(t *testing.T) {
	c, _, clk, ctx := fixture(t)
	in, p := prepare(t, c, ctx, "one")
	request := ApplyRequest{RequestID: in.RequestID, PreviewHash: p.PreviewHash, Reason: in.Reason}
	other := auth.WithActor(context.Background(), auth.Actor{Kind: auth.KindUser, ID: "other"})
	if _, err := c.Apply(other, request); !errors.Is(err, ErrConflict) {
		t.Fatalf("actor err=%v", err)
	}
	clk.Advance(11 * time.Minute)
	if _, err := c.Apply(ctx, request); !errors.Is(err, ErrConflict) {
		t.Fatalf("expiry err=%v", err)
	}
}
func TestUnknownResultDoesNotReexecuteOnRetry(t *testing.T) {
	c, r, _, ctx := fixture(t)
	in, p := prepare(t, c, ctx, "one")
	r.fail = true
	request := ApplyRequest{RequestID: in.RequestID, PreviewHash: p.PreviewHash, Reason: in.Reason}
	op, err := c.Apply(ctx, request)
	if err != nil || op.State != "indeterminate" {
		t.Fatalf("op=%+v err=%v", op, err)
	}
	_, err = c.Apply(ctx, request)
	if err != nil {
		t.Fatal(err)
	}
	applies := 0
	for _, call := range r.calls {
		if call == "apply" {
			applies++
		}
	}
	if applies != 1 {
		t.Fatalf("unsafe replay count=%d", applies)
	}
}
func TestOperationPagingAndValidation(t *testing.T) {
	c, _, _, ctx := fixture(t)
	prepare(t, c, ctx, "one")
	prepare(t, c, ctx, "two")
	rows, err := c.Operations(ctx, 0, 1)
	if err != nil || len(rows) != 1 || rows[0].ID != "two" {
		t.Fatalf("page=%+v err=%v", rows, err)
	}
	next, err := c.Operations(ctx, rows[0].Seq, 1)
	if err != nil || len(next) != 1 || next[0].ID != "one" {
		t.Fatalf("page=%+v err=%v", next, err)
	}
	bad := intent("../../etc/shadow")
	if _, err = c.Preview(ctx, bad); err == nil {
		t.Fatal("unsafe identifier accepted")
	}
	bad = intent("three")
	bad.Action = "baseline.accept"
	if _, err = c.Preview(ctx, bad); err == nil {
		t.Fatal("baseline acceptance accepted")
	}
	if _, err = c.Preview(context.Background(), intent("four")); !errors.Is(err, audit.ErrNoActor) {
		t.Fatalf("actor err=%v", err)
	}
}
