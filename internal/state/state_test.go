package state_test

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/ACaccel/lcm/internal/state"
	"github.com/ACaccel/lcm/internal/store/storetest"
)

func newStore(t *testing.T) *state.Store {
	t.Helper()
	return state.New(storetest.Open(t), state.WithClock(func() time.Time { return time.UnixMilli(1_700_000_000_000) }))
}

func TestSetGetDelete(t *testing.T) {
	ctx := context.Background()
	s := newStore(t)

	if e, err := s.Get(ctx, "p", "k"); err != nil || e.Found() {
		t.Fatalf("absent key: %+v, %v", e, err)
	}
	e1, err := s.Set(ctx, "p", "k", map[string]any{"commit": "abc", "n": 3})
	if err != nil || !e1.Found() {
		t.Fatalf("Set: %+v, %v", e1, err)
	}
	got, _ := s.Get(ctx, "p", "k")
	want := map[string]any{"commit": "abc", "n": int64(3)}
	if got.Version != e1.Version || !equalMaps(got.Value, want) {
		t.Fatalf("Get = %+v", got)
	}
	e2, _ := s.Set(ctx, "p", "k", "v2")
	if e2.Version <= e1.Version {
		t.Fatalf("version did not increase: %d then %d", e1.Version, e2.Version)
	}
	// Other policies have their own namespace.
	if e, _ := s.Get(ctx, "other", "k"); e.Found() {
		t.Fatal("state leaked across policies")
	}
	if ok, err := s.Delete(ctx, "p", "k"); err != nil || !ok {
		t.Fatalf("Delete = %v, %v", ok, err)
	}
	if ok, _ := s.Delete(ctx, "p", "k"); ok {
		t.Fatal("second delete reported a removal")
	}
}

func equalMaps(a, b any) bool {
	am, ok1 := a.(map[string]any)
	bm, ok2 := b.(map[string]any)
	if !ok1 || !ok2 || len(am) != len(bm) {
		return false
	}
	for k, v := range am {
		if bm[k] != v {
			return false
		}
	}
	return true
}

func TestCompareAndSetByVersion(t *testing.T) {
	ctx := context.Background()
	s := newStore(t)
	zero := int64(0)

	e1, ok, err := s.CompareAndSet(ctx, "p", "k", "a", state.Condition{Version: &zero})
	if err != nil || !ok {
		t.Fatalf("create-if-absent: %v %v", ok, err)
	}
	if _, ok, _ := s.CompareAndSet(ctx, "p", "k", "b", state.Condition{Version: &zero}); ok {
		t.Fatal("create-if-absent succeeded on an existing key")
	}
	cur, ok, _ := s.CompareAndSet(ctx, "p", "k", "b", state.Condition{Version: new(e1.Version + 1)})
	if ok || cur.Value != "a" || cur.Version != e1.Version {
		t.Fatalf("stale version: ok=%v cur=%+v", ok, cur)
	}
	e2, ok, _ := s.CompareAndSet(ctx, "p", "k", "b", state.Condition{Version: &e1.Version})
	if !ok || e2.Value != "b" {
		t.Fatalf("matching version: ok=%v %+v", ok, e2)
	}

	// A deleted and re-created key never reuses a version.
	_, _ = s.Delete(ctx, "p", "k")
	e3, _ := s.Set(ctx, "p", "k", "a")
	if e3.Version == e1.Version || e3.Version == e2.Version {
		t.Fatalf("version %d reused", e3.Version)
	}
}

func TestCompareAndSetByValue(t *testing.T) {
	ctx := context.Background()
	s := newStore(t)
	_, _ = s.Set(ctx, "p", "k", map[string]any{"a": []any{1, 2}})

	_, ok, _ := s.CompareAndSet(ctx, "p", "k", "x", state.Condition{HasValue: true, Value: map[string]any{"a": []any{1, 3}}})
	if ok {
		t.Fatal("different value matched")
	}
	_, ok, _ = s.CompareAndSet(ctx, "p", "k", "x", state.Condition{HasValue: true, Value: map[string]any{"a": []any{int64(1), float64(2)}}})
	if !ok {
		t.Fatal("equal value (after JSON normalization) did not match")
	}
	if _, ok, _ = s.CompareAndSet(ctx, "p", "absent", "x", state.Condition{HasValue: true, Value: nil}); ok {
		t.Fatal("value condition on an absent key matched")
	}
}

func TestLimitsAndKeys(t *testing.T) {
	ctx := context.Background()
	db := storetest.Open(t)
	s := state.New(db)
	for _, k := range []string{"", "a b", "../x", strings.Repeat("k", 129), ".x"} {
		if _, err := s.Set(ctx, "p", k, 1); !errors.Is(err, state.ErrInvalidKey) {
			t.Fatalf("key %q: %v", k, err)
		}
	}
	if _, err := s.Set(ctx, "p", "big", strings.Repeat("x", state.MaxValueBytes)); !errors.Is(err, state.ErrTooLarge) {
		t.Fatalf("big value: %v", err)
	}
	if _, err := s.Set(ctx, "p", "bad", make(chan int)); err == nil {
		t.Fatal("non-JSON value accepted")
	}
	err := db.Write(ctx, func(tx *sql.Tx) error {
		for i := range state.MaxKeys {
			if _, err := tx.ExecContext(ctx, `INSERT INTO policy_state (policy, key, value, version, updated_at) VALUES ('full', ?, '1', 1, 0)`,
				fmt.Sprintf("k%d", i)); err != nil {
				return err
			}
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := s.Set(ctx, "full", "one-more", 1); !errors.Is(err, state.ErrTooMany) {
		t.Fatalf("over key cap: %v", err)
	}
	// Overwriting an existing key stays possible at the cap.
	if _, err := s.Set(ctx, "full", "k0", "again"); err != nil {
		t.Fatalf("overwrite at cap: %v", err)
	}
}

func TestListAllAndDeletePolicy(t *testing.T) {
	ctx := context.Background()
	s := newStore(t)
	_, _ = s.Set(ctx, "p", "b", 2)
	_, _ = s.Set(ctx, "p", "a", 1)
	_, _ = s.Set(ctx, "q", "z", 9)
	entries, err := s.List(ctx, "p")
	if err != nil || len(entries) != 2 || entries[0].Key != "a" {
		t.Fatalf("List = %+v, %v", entries, err)
	}
	all, _ := s.All(ctx, "p")
	if all["a"] != int64(1) || all["b"] != int64(2) || len(all) != 2 {
		t.Fatalf("All = %v", all)
	}
	if n, _ := s.DeletePolicy(ctx, "p"); n != 2 {
		t.Fatalf("DeletePolicy = %d", n)
	}
	if e, _ := s.Get(ctx, "q", "z"); !e.Found() {
		t.Fatal("DeletePolicy removed another policy's state")
	}
}

func TestConcurrentCompareAndSet(t *testing.T) {
	ctx := context.Background()
	s := newStore(t)
	zero := int64(0)
	const n = 16
	wins := make(chan bool, n)
	for range n {
		go func() {
			_, ok, err := s.CompareAndSet(ctx, "p", "leader", "me", state.Condition{Version: &zero})
			wins <- ok && err == nil
		}()
	}
	count := 0
	for range n {
		if <-wins {
			count++
		}
	}
	if count != 1 {
		t.Fatalf("%d writers won create-if-absent, want 1", count)
	}
}
