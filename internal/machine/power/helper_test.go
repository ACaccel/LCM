package power

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"sync"
	"testing"
	"time"

	"github.com/ACaccel/lcm/internal/clock"
)

const controller = "11111111-1111-4111-8111-111111111111"
const operation = "22222222-2222-4222-8222-222222222222"
const boot = "33333333-3333-4333-8333-333333333333"

func binding() Binding {
	b := Binding{ControllerID: controller, OperationID: operation, Action: Reboot, BootID: boot, IssuedAt: 1000000, ExpiresAt: 1060000}
	b.RequestHash = Hash(b)
	return b
}
func request(method Method) Request {
	return Request{Version: Version, Method: method, Binding: binding()}
}

type fakeSystem struct {
	boot                 string
	checkErr, requestErr error
	calls                int
	crash                bool
	checkHook            func()
}

func (s *fakeSystem) BootID() (string, error) { return s.boot, nil }
func (s *fakeSystem) Check(context.Context, Action) error {
	if s.checkHook != nil {
		s.checkHook()
	}
	return s.checkErr
}
func (s *fakeSystem) Request(context.Context, Action) error {
	s.calls++
	if s.crash {
		panic("fixture: process lost")
	}
	return s.requestErr
}

type memoryStore struct {
	mu               sync.Mutex
	receipts         map[string]Receipt
	writes, failAt   int
	persistOnFailure bool
	putHook          func(Receipt)
}
type memoryTx struct{ store *memoryStore }

func (s *memoryStore) Lock(context.Context) (ReceiptTx, error) { s.mu.Lock(); return &memoryTx{s}, nil }
func (tx *memoryTx) Get(c, o string) (Receipt, bool, error) {
	r, ok := tx.store.receipts[receiptKey(c, o)]
	return r, ok, nil
}
func (tx *memoryTx) Put(r Receipt) error {
	tx.store.writes++
	if tx.store.writes == tx.store.failAt {
		if tx.store.persistOnFailure {
			tx.store.receipts[receiptKey(r.Binding.ControllerID, r.Binding.OperationID)] = r
		}
		return errors.New("fixture: disk failure")
	}
	tx.store.receipts[receiptKey(r.Binding.ControllerID, r.Binding.OperationID)] = r
	if tx.store.putHook != nil {
		tx.store.putHook(r)
	}
	return nil
}

func TestExpiryOrCancellationDuringFenceDoesNotRequestPower(t *testing.T) {
	for _, expire := range []bool{true, false} {
		t.Run(fmt.Sprintf("expiry-%t", expire), func(t *testing.T) {
			h, store, system, clk := fixture()
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			mustReceipt(t, h.Handle(ctx, request(Prepare)), Accepted, false)
			store.putHook = func(r Receipt) {
				if r.State == Submitting {
					if expire {
						clk.Advance(time.Minute)
					} else {
						cancel()
					}
				}
			}
			mustReceipt(t, h.Handle(ctx, request(Submit)), Failed, false)
			mustReceipt(t, h.Handle(context.Background(), request(Query)), Failed, false)
			if system.calls != 0 {
				t.Fatal("durability extended power authorization")
			}
		})
	}
}
func (tx *memoryTx) Close() error { tx.store.mu.Unlock(); return nil }
func fixture() (*Helper, *memoryStore, *fakeSystem, *clock.Fake) {
	s := &memoryStore{receipts: map[string]Receipt{}}
	system := &fakeSystem{boot: boot}
	c := clock.NewFake(time.UnixMilli(1000000))
	return &Helper{ControllerID: controller, Store: s, System: system, Clock: c}, s, system, c
}
func mustReceipt(t *testing.T, result Response, state State, osRequested bool) {
	t.Helper()
	if result.Code != "" || result.Receipt == nil || result.Receipt.State != state || result.Receipt.OSRequested != osRequested {
		t.Fatalf("unexpected response: %+v", result)
	}
}
func TestProtocolLifecycleAndDedupe(t *testing.T) {
	for _, action := range []Action{Reboot, GracefulShutdown} {
		t.Run(string(action), func(t *testing.T) {
			h, _, sys, c := fixture()
			req := request(Prepare)
			req.Binding.Action = action
			req.Binding.RequestHash = Hash(req.Binding)
			mustReceipt(t, h.Handle(context.Background(), req), Accepted, false)
			req.Method = Submit
			mustReceipt(t, h.Handle(context.Background(), req), Accepted, true)
			c.Advance(24 * time.Hour)
			sys.boot = controller
			for _, method := range []Method{Prepare, Submit, Query} {
				req.Method = method
				mustReceipt(t, h.Handle(context.Background(), req), Accepted, true)
			}
			if sys.calls != 1 {
				t.Fatalf("OS requests = %d", sys.calls)
			}
		})
	}
}

func TestDisabledControlPreservesReceiptQueries(t *testing.T) {
	h, _, sys, _ := fixture()
	mustReceipt(t, h.Handle(context.Background(), request(Prepare)), Accepted, false)
	h.ControlDisabled = true
	sys.checkHook = func() { t.Fatal("disabled control called system checks") }
	mustReceipt(t, h.Handle(context.Background(), request(Query)), Accepted, false)
	for _, method := range []Method{Prepare, Submit} {
		if r := h.Handle(context.Background(), request(method)); r.Code != "control-disabled" {
			t.Fatalf("disabled %s: %+v", method, r)
		}
	}
	if sys.calls != 0 {
		t.Fatal("disabled control issued an OS request")
	}
}
func TestCollisionsExpiryAndMissingReceipts(t *testing.T) {
	h, _, sys, c := fixture()
	if got := h.Handle(context.Background(), request(Submit)); got.Code != "receipt-missing" {
		t.Fatal(got)
	}
	mustReceipt(t, h.Handle(context.Background(), request(Prepare)), Accepted, false)
	req := request(Submit)
	req.Binding.Action = GracefulShutdown
	req.Binding.RequestHash = Hash(req.Binding)
	if got := h.Handle(context.Background(), req); got.Code != "id-collision" {
		t.Fatal(got)
	}
	c.Advance(time.Minute)
	mustReceipt(t, h.Handle(context.Background(), request(Submit)), Failed, false)
	mustReceipt(t, h.Handle(context.Background(), request(Prepare)), Failed, false)
	if sys.calls != 0 {
		t.Fatal("expired operation executed")
	}
	h, _, sys, c = fixture()
	c.Advance(time.Hour)
	for _, method := range []Method{Prepare, Submit} {
		if got := h.Handle(context.Background(), request(method)); got.Code != "expired-or-future" {
			t.Fatal(got)
		}
	}
	if sys.calls != 0 {
		t.Fatal("expired unknown ID executed")
	}
}
func TestCrashWindows(t *testing.T) {
	for _, failAt := range []int{1, 2, 3} {
		t.Run(fmt.Sprint(failAt), func(t *testing.T) {
			h, store, sys, _ := fixture()
			store.failAt = failAt
			prep := h.Handle(context.Background(), request(Prepare))
			if failAt == 1 {
				if prep.Code != "storage-unavailable" {
					t.Fatal(prep)
				}
				if got := h.Handle(context.Background(), request(Submit)); got.Code != "receipt-missing" {
					t.Fatal(got)
				}
				return
			}
			mustReceipt(t, prep, Accepted, false)
			got := h.Handle(context.Background(), request(Submit))
			if got.Code != "storage-unavailable" {
				t.Fatal(got)
			}
			if failAt == 2 {
				if sys.calls != 0 {
					t.Fatal("OS request before durable fence")
				}
				mustReceipt(t, h.Handle(context.Background(), request(Query)), Accepted, false)
			} else {
				mustReceipt(t, h.Handle(context.Background(), request(Query)), Indeterminate, false)
				mustReceipt(t, h.Handle(context.Background(), request(Submit)), Indeterminate, false)
				if sys.calls != 1 {
					t.Fatal("ambiguous request repeated")
				}
			}
		})
	}
	h, _, sys, _ := fixture()
	mustReceipt(t, h.Handle(context.Background(), request(Prepare)), Accepted, false)
	sys.crash = true
	func() {
		defer func() {
			if recover() == nil {
				t.Error("expected crash")
			}
		}()
		h.Handle(context.Background(), request(Submit))
	}()
	mustReceipt(t, h.Handle(context.Background(), request(Query)), Indeterminate, false)
	mustReceipt(t, h.Handle(context.Background(), request(Submit)), Indeterminate, false)
	if sys.calls != 1 {
		t.Fatal("crash repeated OS request")
	}
}
func TestHandoffErrorsAndPreconditions(t *testing.T) {
	for _, kind := range []string{"inhibitor", "boot", "timeout", "expiry-during-check", "cancelled"} {
		t.Run(kind, func(t *testing.T) {
			h, _, sys, c := fixture()
			mustReceipt(t, h.Handle(context.Background(), request(Prepare)), Accepted, false)
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			switch kind {
			case "inhibitor":
				sys.checkErr = errors.New("blocked")
			case "boot":
				sys.boot = controller
			case "timeout":
				sys.requestErr = context.DeadlineExceeded
			case "expiry-during-check":
				sys.checkHook = func() { c.Advance(time.Minute) }
			case "cancelled":
				cancel()
			}
			got := h.Handle(ctx, request(Submit))
			if kind == "timeout" {
				mustReceipt(t, got, Indeterminate, false)
				mustReceipt(t, h.Handle(context.Background(), request(Submit)), Indeterminate, false)
			} else if kind == "expiry-during-check" {
				mustReceipt(t, got, Failed, false)
			} else if got.Code == "" {
				t.Fatal(got)
			}
			if kind != "timeout" && sys.calls != 0 {
				t.Fatal("unsafe OS request")
			}
		})
	}
}
func TestConcurrentSubmit(t *testing.T) {
	h, _, sys, _ := fixture()
	mustReceipt(t, h.Handle(context.Background(), request(Prepare)), Accepted, false)
	var wg sync.WaitGroup
	for range 20 {
		wg.Go(func() { h.Handle(context.Background(), request(Submit)) })
	}
	wg.Wait()
	if sys.calls != 1 {
		t.Fatalf("OS requests = %d", sys.calls)
	}
}
func TestStrictProtocol(t *testing.T) {
	raw, _ := json.Marshal(request(Prepare))
	cases := []string{string([]byte{0xff}), "", "null", string(raw) + "{}", string(raw[:len(raw)-1]) + `,"extra":true}`, string(raw[:len(raw)-1]) + `,"version":1}`, string(bytes.Repeat([]byte(" "), MaxMessage+1))}
	for _, mutate := range []func(*Request){func(r *Request) { r.Version = 2 }, func(r *Request) { r.Method = "exec" }, func(r *Request) { r.Binding.Action = "power-on"; r.Binding.RequestHash = Hash(r.Binding) }, func(r *Request) { r.Binding.OperationID = "../../bad" }, func(r *Request) { r.Binding.RequestHash = "bad" }, func(r *Request) {
		r.Binding.ExpiresAt = r.Binding.IssuedAt + MaxLifetimeMillis + 1
		r.Binding.RequestHash = Hash(r.Binding)
	}} {
		req := request(Prepare)
		mutate(&req)
		b, _ := json.Marshal(req)
		cases = append(cases, string(b))
	}
	for i, raw := range cases {
		if _, err := DecodeRequest(bytes.NewBufferString(raw)); err == nil {
			t.Errorf("case %d accepted", i)
		}
	}
	if _, err := DecodeRequest(bytes.NewReader(raw)); err != nil {
		t.Fatal(err)
	}
	h, _, sys, _ := fixture()
	var out bytes.Buffer
	if err := h.Serve(context.Background(), bytes.NewBufferString(`{"version":2}`), &out); err == nil {
		t.Fatal("malformed request accepted")
	}
	if sys.calls != 0 || !bytes.Contains(out.Bytes(), []byte("invalid-request")) {
		t.Fatal(out.String())
	}
}

func TestUncertainPersistenceOfFence(t *testing.T) {
	h, store, sys, _ := fixture()
	mustReceipt(t, h.Handle(context.Background(), request(Prepare)), Accepted, false)
	store.failAt, store.persistOnFailure = 2, true
	if got := h.Handle(context.Background(), request(Submit)); got.Code != "storage-unavailable" {
		t.Fatal(got)
	}
	if sys.calls != 0 {
		t.Fatal("OS request after uncertain persistence")
	}
	mustReceipt(t, h.Handle(context.Background(), request(Query)), Indeterminate, false)
	mustReceipt(t, h.Handle(context.Background(), request(Submit)), Indeterminate, false)
	if sys.calls != 0 {
		t.Fatal("uncertain fence replayed")
	}
}

func TestPrepareCannotOutliveWindow(t *testing.T) {
	h, _, sys, c := fixture()
	sys.checkHook = func() { c.Advance(time.Minute) }
	if got := h.Handle(context.Background(), request(Prepare)); got.Code != "expired-or-future" {
		t.Fatal(got)
	}
	if sys.calls != 0 {
		t.Fatal("expired prepare requested power")
	}
	if got := h.Handle(context.Background(), request(Query)); got.Code != "receipt-missing" {
		t.Fatal(got)
	}
}
