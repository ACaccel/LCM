package lock_test

import (
	"context"
	"errors"
	"fmt"
	"sync"
	"testing"
	"time"

	"github.com/ACaccel/lcm/internal/audit"
	"github.com/ACaccel/lcm/internal/auth"
	"github.com/ACaccel/lcm/internal/clock"
	"github.com/ACaccel/lcm/internal/lock"
	"github.com/ACaccel/lcm/internal/store/storetest"
)

var epoch = time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)

type fixture struct {
	m     *lock.Manager
	clk   *clock.Fake
	audit *audit.Log
}

func newFixture(t *testing.T) fixture {
	t.Helper()
	db := storetest.Open(t)
	clk := clock.NewFake(epoch)
	al := audit.New(db, audit.WithClock(clk.Now))
	return fixture{m: lock.New(db, lock.WithClock(clk), lock.WithAudit(al), lock.WithPollInterval(time.Second)), clk: clk, audit: al}
}

func TestContentionAndRelease(t *testing.T) {
	ctx := context.Background()
	f := newFixture(t)

	info, err := f.m.TryAcquire(ctx, "deploy", "run-1", time.Minute)
	if err != nil || info.Owner != "run-1" || !info.ExpiresAt.Equal(epoch.Add(time.Minute)) {
		t.Fatalf("first acquire: %+v, %v", info, err)
	}
	_, err = f.m.TryAcquire(ctx, "deploy", "run-2", time.Minute)
	var busy *lock.BusyError
	if !errors.As(err, &busy) || busy.Holder.Owner != "run-1" {
		t.Fatalf("contended acquire: %v", err)
	}
	if ok, _ := f.m.Release(ctx, "deploy", "run-2"); ok {
		t.Fatal("non-owner released the lock")
	}
	if ok, err := f.m.Release(ctx, "deploy", "run-1"); err != nil || !ok {
		t.Fatalf("release: %v %v", ok, err)
	}
	if _, err := f.m.TryAcquire(ctx, "deploy", "run-2", time.Minute); err != nil {
		t.Fatalf("acquire after release: %v", err)
	}
}

func TestOwnerReacquireExtends(t *testing.T) {
	ctx := context.Background()
	f := newFixture(t)
	first, _ := f.m.TryAcquire(ctx, "l", "o", time.Minute)
	f.clk.Advance(30 * time.Second)
	again, err := f.m.TryAcquire(ctx, "l", "o", time.Minute)
	if err != nil {
		t.Fatal(err)
	}
	if !again.AcquiredAt.Equal(first.AcquiredAt) || !again.ExpiresAt.Equal(epoch.Add(90*time.Second)) {
		t.Fatalf("extend: first=%+v again=%+v", first, again)
	}
}

func TestTTLExpiryFreesLock(t *testing.T) {
	ctx := context.Background()
	f := newFixture(t)
	_, _ = f.m.TryAcquire(ctx, "l", "dead-run", time.Minute)
	f.clk.Advance(59 * time.Second)
	if _, err := f.m.TryAcquire(ctx, "l", "other", time.Minute); err == nil {
		t.Fatal("acquired before expiry")
	}
	f.clk.Advance(time.Second)
	if _, found, _ := f.m.Holder(ctx, "l"); found {
		t.Fatal("expired lock still reported as held")
	}
	info, err := f.m.TryAcquire(ctx, "l", "other", time.Minute)
	if err != nil || info.Owner != "other" || !info.AcquiredAt.Equal(f.clk.Now().UTC()) {
		t.Fatalf("takeover: %+v, %v", info, err)
	}
	// The dead owner's late release must not free the new owner's lock.
	if ok, _ := f.m.Release(ctx, "l", "dead-run"); ok {
		t.Fatal("stale owner released a lock it lost")
	}
}

func TestAcquireDoesNotWait(t *testing.T) {
	ctx := context.Background()
	f := newFixture(t)
	release, err := f.m.Acquire(ctx, "svc", "a", time.Minute)
	if err != nil {
		t.Fatal(err)
	}
	_, err = f.m.Acquire(ctx, "svc", "b", time.Minute)
	var busy *lock.BusyError
	if !errors.Is(err, lock.ErrHeld) || !errors.As(err, &busy) || busy.Holder.Owner != "a" {
		t.Fatalf("held lock: %v", err)
	}
	if err := release(ctx); err != nil {
		t.Fatal(err)
	}
	if _, err := f.m.Acquire(ctx, "svc", "b", time.Minute); err != nil {
		t.Fatalf("after release: %v", err)
	}
	if _, err := f.m.Acquire(ctx, "bad name", "b", time.Minute); errors.Is(err, lock.ErrHeld) {
		t.Fatal("invalid argument reported as held")
	}
}

func TestAcquireWaitsForRelease(t *testing.T) {
	ctx := context.Background()
	f := newFixture(t)
	_, _ = f.m.TryAcquire(ctx, "l", "a", time.Hour)

	type result struct {
		release func(context.Context) error
		err     error
	}
	done := make(chan result, 1)
	go func() {
		r, err := f.m.AcquireWait(ctx, "l", "b", time.Hour)
		done <- result{r, err}
	}()
	wait(t, f.clk, 1)
	f.clk.Advance(time.Second) // still held: one more round
	wait(t, f.clk, 1)
	if _, err := f.m.Release(ctx, "l", "a"); err != nil {
		t.Fatal(err)
	}
	f.clk.Advance(time.Second)
	r := <-done
	if r.err != nil {
		t.Fatal(r.err)
	}
	if h, _, _ := f.m.Holder(ctx, "l"); h.Owner != "b" {
		t.Fatalf("holder = %q", h.Owner)
	}
	if err := r.release(ctx); err != nil {
		t.Fatal(err)
	}
	if err := r.release(ctx); err != nil {
		t.Fatalf("second release must be a no-op: %v", err)
	}
	if _, found, _ := f.m.Holder(ctx, "l"); found {
		t.Fatal("lock still held after release")
	}
}

func wait(t *testing.T, clk *clock.Fake, n int) {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	if err := clk.WaitForTimers(ctx, n); err != nil {
		t.Fatalf("waiting for timers: %v", err)
	}
}

func TestAcquireHonoursContext(t *testing.T) {
	f := newFixture(t)
	_, _ = f.m.TryAcquire(context.Background(), "l", "a", time.Hour)
	ctx, cancel := context.WithCancel(context.Background())
	errc := make(chan error, 1)
	go func() {
		_, err := f.m.AcquireWait(ctx, "l", "b", time.Hour)
		errc <- err
	}()
	wait(t, f.clk, 1)
	cancel()
	if err := <-errc; !errors.Is(err, context.Canceled) {
		t.Fatalf("err = %v", err)
	}
}

func TestMutualExclusionUnderRace(t *testing.T) {
	ctx := context.Background()
	f := newFixture(t)
	const n = 16
	var wg sync.WaitGroup
	wins := make(chan string, n)
	for i := range n {
		wg.Go(func() {
			owner := fmt.Sprintf("owner-%d", i)
			if _, err := f.m.TryAcquire(ctx, "l", owner, time.Minute); err == nil {
				wins <- owner
			}
		})
	}
	wg.Wait()
	close(wins)
	if len(wins) != 1 {
		t.Fatalf("%d owners acquired the same lock", len(wins))
	}
}

func TestReleaseOwnerAndListAndHeld(t *testing.T) {
	ctx := context.Background()
	f := newFixture(t)
	_, _ = f.m.TryAcquire(ctx, "a", "run:1", time.Minute)
	_, _ = f.m.TryAcquire(ctx, "b", "run:1", time.Minute)
	_, _ = f.m.TryAcquire(ctx, "c", "svc:x", time.Minute)
	_, _ = f.m.TryAcquire(ctx, "stale", "run:2", time.Second)
	f.clk.Advance(2 * time.Second)

	all, _ := f.m.List(ctx)
	if len(all) != 3 || all[0].Name != "a" {
		t.Fatalf("List = %+v", all)
	}
	held, _ := f.m.Held(ctx, "run:")
	if len(held) != 2 {
		t.Fatalf("Held = %+v", held)
	}
	names, err := f.m.ReleaseOwner(ctx, "run:1")
	if err != nil || len(names) != 2 {
		t.Fatalf("ReleaseOwner = %v, %v", names, err)
	}
	if n, _ := f.m.PurgeExpired(ctx); n != 1 {
		t.Fatalf("purged %d", n)
	}
}

func TestForceReleaseIsAudited(t *testing.T) {
	ctx := auth.WithActor(context.Background(), auth.Actor{Kind: auth.KindUser, ID: "u1", Name: "admin"})
	f := newFixture(t)
	_, _ = f.m.TryAcquire(ctx, "stuck", "run-9", time.Hour)

	if err := f.m.ForceRelease(context.Background(), "stuck"); err == nil {
		t.Fatal("force release without an actor was accepted")
	}
	if _, found, _ := f.m.Holder(ctx, "stuck"); !found {
		t.Fatal("a refused force release must leave the lock in place")
	}
	if err := f.m.ForceRelease(ctx, "stuck"); err != nil {
		t.Fatal(err)
	}
	entries, _ := f.audit.List(ctx, 10, 0)
	if len(entries) == 0 || entries[0].Action != "lock.force_release" || entries[0].Details["owner"] != "run-9" {
		t.Fatalf("audit = %+v", entries)
	}
	if err := f.m.ForceRelease(ctx, "stuck"); !errors.Is(err, lock.ErrNotHeld) {
		t.Fatalf("force release of a free lock: %v", err)
	}
}

func TestValidation(t *testing.T) {
	ctx := context.Background()
	f := newFixture(t)
	for _, c := range []struct {
		name, owner string
		ttl         time.Duration
	}{
		{"", "o", time.Minute}, {"bad name", "o", time.Minute}, {"ok", "", time.Minute},
		{"ok", "o", 0}, {"ok", "o", lock.MaxTTL + 1},
	} {
		if _, err := f.m.TryAcquire(ctx, c.name, c.owner, c.ttl); !errors.Is(err, lock.ErrInvalid) {
			t.Fatalf("%+v: %v", c, err)
		}
	}
}
