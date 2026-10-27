package clock_test

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/ACaccel/lcm/internal/clock"
)

func TestFakeFiresInOrder(t *testing.T) {
	start := time.Unix(1000, 0)
	f := clock.NewFake(start)
	var got []int
	f.AfterFunc(2*time.Second, func() { got = append(got, 2) })
	f.AfterFunc(time.Second, func() {
		got = append(got, 1)
		// A timer armed by a callback fires within the same Advance.
		f.AfterFunc(500*time.Millisecond, func() { got = append(got, 15) })
	})
	stopped := f.AfterFunc(time.Second, func() { got = append(got, -1) })
	if !stopped.Stop() || stopped.Stop() {
		t.Fatal("Stop should succeed once")
	}
	f.Advance(1500 * time.Millisecond)
	if len(got) != 2 || got[0] != 1 || got[1] != 15 {
		t.Fatalf("got %v", got)
	}
	if f.Pending() != 1 {
		t.Fatalf("pending = %d", f.Pending())
	}
	f.Advance(time.Second)
	if len(got) != 3 || got[2] != 2 {
		t.Fatalf("got %v", got)
	}
	if !f.Now().Equal(start.Add(2500 * time.Millisecond)) {
		t.Fatalf("now = %v", f.Now())
	}
}

func TestFakeWaitForTimers(t *testing.T) {
	f := clock.NewFake(time.Unix(0, 0))
	done := make(chan error, 1)
	go func() { done <- f.WaitForTimers(context.Background(), 1) }()
	f.AfterFunc(time.Hour, func() {})
	if err := <-done; err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if err := f.WaitForTimers(ctx, 5); !errors.Is(err, context.Canceled) {
		t.Fatalf("err = %v", err)
	}
}

func TestWithTimeoutCause(t *testing.T) {
	cause := errors.New("too slow")
	f := clock.NewFake(time.Unix(0, 0))
	ctx, cancel := clock.WithTimeoutCause(context.Background(), f, time.Minute, cause)
	defer cancel()
	f.Advance(59 * time.Second)
	if ctx.Err() != nil {
		t.Fatal("cancelled early")
	}
	f.Advance(time.Second)
	if !errors.Is(context.Cause(ctx), cause) {
		t.Fatalf("cause = %v", context.Cause(ctx))
	}

	ctx2, cancel2 := clock.WithTimeoutCause(context.Background(), f, time.Minute, cause)
	cancel2()
	if !errors.Is(context.Cause(ctx2), context.Canceled) || f.Pending() != 0 {
		t.Fatalf("cancel: cause = %v pending = %d", context.Cause(ctx2), f.Pending())
	}

	ctx3, cancel3 := clock.WithTimeoutCause(context.Background(), clock.Real(), time.Hour, cause)
	defer cancel3()
	if _, ok := ctx3.Deadline(); !ok {
		t.Fatal("real clock context has no deadline")
	}
	if clock.Real().Now().IsZero() {
		t.Fatal("real now is zero")
	}
	tm := clock.Real().AfterFunc(time.Hour, func() {})
	if !tm.Stop() {
		t.Fatal("real timer stop")
	}
}
