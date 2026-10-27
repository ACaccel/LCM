package catalog_test

import (
	"context"
	"errors"
	"testing"

	"github.com/ACaccel/lcm/internal/catalog"
	"github.com/ACaccel/lcm/internal/signal"
)

// allowed is the specification of the state machine, written out
// independently of the table in the implementation.
var allowed = map[[2]catalog.State]bool{
	{catalog.StateRegistered, catalog.StateDeploying}: true,
	{catalog.StateRegistered, catalog.StateRunning}:   true,
	{catalog.StateRegistered, catalog.StateStopped}:   true,
	{catalog.StateRegistered, catalog.StateFailed}:    true,
	{catalog.StateRegistered, catalog.StateRetired}:   true,
	{catalog.StateDeploying, catalog.StateRunning}:    true,
	{catalog.StateDeploying, catalog.StateFailed}:     true,
	{catalog.StateUpdating, catalog.StateRunning}:     true,
	{catalog.StateUpdating, catalog.StateFailed}:      true,
	{catalog.StateRollingBack, catalog.StateRunning}:  true,
	{catalog.StateRollingBack, catalog.StateFailed}:   true,
	{catalog.StateRunning, catalog.StateUpdating}:     true,
	{catalog.StateRunning, catalog.StateRollingBack}:  true,
	{catalog.StateRunning, catalog.StateStopped}:      true,
	{catalog.StateRunning, catalog.StateFailed}:       true,
	{catalog.StateRunning, catalog.StateRetired}:      true,
	{catalog.StateStopped, catalog.StateDeploying}:    true,
	{catalog.StateStopped, catalog.StateRunning}:      true,
	{catalog.StateStopped, catalog.StateRollingBack}:  true,
	{catalog.StateStopped, catalog.StateFailed}:       true,
	{catalog.StateStopped, catalog.StateRetired}:      true,
	{catalog.StateFailed, catalog.StateDeploying}:     true,
	{catalog.StateFailed, catalog.StateRollingBack}:   true,
	{catalog.StateFailed, catalog.StateRunning}:       true,
	{catalog.StateFailed, catalog.StateStopped}:       true,
	{catalog.StateFailed, catalog.StateRetired}:       true,
}

func TestStateMachineEveryPair(t *testing.T) {
	if len(catalog.States) != 8 {
		t.Fatalf("expected 8 states, have %d", len(catalog.States))
	}
	for _, from := range catalog.States {
		for _, to := range catalog.States {
			want := allowed[[2]catalog.State{from, to}]
			if got := catalog.CanTransition(from, to); got != want {
				t.Errorf("CanTransition(%s, %s) = %v, want %v", from, to, got, want)
			}
		}
	}
	if catalog.CanTransition("bogus", catalog.StateRunning) || catalog.CanTransition(catalog.StateRunning, "bogus") {
		t.Error("unknown states must never transition")
	}
}

func TestRetiredIsTerminal(t *testing.T) {
	for _, to := range catalog.States {
		if catalog.CanTransition(catalog.StateRetired, to) {
			t.Errorf("retired -> %s allowed", to)
		}
	}
}

func TestTransientStates(t *testing.T) {
	want := map[catalog.State]bool{catalog.StateDeploying: true, catalog.StateUpdating: true, catalog.StateRollingBack: true}
	for _, s := range catalog.States {
		if s.Transient() != want[s] {
			t.Errorf("%s.Transient() = %v", s, s.Transient())
		}
	}
}

func TestCanAct(t *testing.T) {
	type c = catalog.State
	tests := []struct {
		action string
		ok     []c
	}{
		{catalog.ActionStart, []c{catalog.StateRegistered, catalog.StateStopped, catalog.StateFailed}},
		{catalog.ActionStop, []c{catalog.StateRegistered, catalog.StateRunning, catalog.StateFailed}},
		{catalog.ActionRestart, []c{catalog.StateRunning, catalog.StateStopped, catalog.StateFailed}},
		{catalog.ActionDeploy, []c{catalog.StateRegistered, catalog.StateRunning, catalog.StateStopped, catalog.StateFailed}},
		{catalog.ActionRollback, []c{catalog.StateRunning, catalog.StateStopped, catalog.StateFailed}},
		{catalog.ActionBackup, []c{catalog.StateRegistered, catalog.StateRunning, catalog.StateStopped, catalog.StateFailed}},
		{catalog.ActionRunJob, []c{catalog.StateRegistered, catalog.StateRunning, catalog.StateStopped, catalog.StateFailed}},
		{catalog.ActionRetire, []c{catalog.StateRegistered, catalog.StateRunning, catalog.StateStopped, catalog.StateFailed}},
	}
	for _, tt := range tests {
		okSet := map[c]bool{}
		for _, s := range tt.ok {
			okSet[s] = true
		}
		for _, s := range catalog.States {
			if got := catalog.CanAct(tt.action, s); got != okSet[s] {
				t.Errorf("CanAct(%s, %s) = %v, want %v", tt.action, s, got, okSet[s])
			}
		}
	}
	if catalog.CanAct("explode", catalog.StateRunning) {
		t.Error("unknown action allowed")
	}
	// Every action that moves the service must have a legal first step.
	for _, a := range []string{catalog.ActionStart, catalog.ActionStop, catalog.ActionRestart, catalog.ActionRetire} {
		for _, s := range catalog.States {
			if !catalog.CanAct(a, s) {
				continue
			}
			target := map[string]catalog.State{
				catalog.ActionStart: catalog.StateRunning, catalog.ActionStop: catalog.StateStopped,
				catalog.ActionRestart: catalog.StateRunning, catalog.ActionRetire: catalog.StateRetired,
			}[a]
			if s != target && !catalog.CanTransition(s, target) {
				t.Errorf("action %s allowed in %s but %s -> %s is not a transition", a, s, s, target)
			}
		}
	}
	for _, a := range []string{catalog.ActionDeploy, catalog.ActionRollback} {
		for _, s := range catalog.States {
			if !catalog.CanAct(a, s) {
				continue
			}
			transient := catalog.StateDeploying
			switch {
			case a == catalog.ActionRollback:
				transient = catalog.StateRollingBack
			case s == catalog.StateRunning:
				transient = catalog.StateUpdating
			}
			if !catalog.CanTransition(s, transient) || !catalog.CanTransition(transient, catalog.StateRunning) || !catalog.CanTransition(transient, catalog.StateFailed) {
				t.Errorf("%s from %s lacks a legal path through %s", a, s, transient)
			}
		}
	}
}

func TestTransitionStoresAndPublishes(t *testing.T) {
	e := newEnv(t)
	e.put(svcDoc("app", ""), 0)
	sub := e.bus.Subscribe("test", 8)
	defer sub.Close()
	ctx := context.Background()

	e.clk.Advance(5_000_000_000)
	if err := e.cat.Transition(ctx, "app", catalog.StateRegistered, catalog.StateRunning, catalog.TransitionInfo{Action: "start", Ref: "run1"}); err != nil {
		t.Fatal(err)
	}
	rec, _ := e.cat.Get(ctx, "app")
	if rec.State != catalog.StateRunning || !rec.StateSince.Equal(e.clk.Now()) {
		t.Fatalf("record = %+v", rec)
	}

	select {
	case sig := <-sub.C:
		if sig.Kind != signal.KindLifecycle || sig.Service != "app" || sig.Data["state"] != "running" ||
			sig.Data["previous"] != "registered" || sig.Data["action"] != "start" || sig.Labels["tier"] != "web" {
			t.Fatalf("signal = %+v", sig)
		}
	default:
		t.Fatal("no lifecycle signal published")
	}

	page, err := e.cat.Timeline(ctx, catalog.TimelineQuery{Service: "app", Kinds: []string{catalog.KindLifecycle}})
	if err != nil || len(page.Entries) != 1 || page.Entries[0].Ref != "run1" || page.Entries[0].Summary != "registered -> running (start)" {
		t.Fatalf("timeline = %+v %v", page, err)
	}
}

func TestTransitionErrors(t *testing.T) {
	e := newEnv(t)
	e.put(svcDoc("app", ""), 0)
	ctx := context.Background()
	sub := e.bus.Subscribe("test", 8)
	defer sub.Close()

	if err := e.cat.Transition(ctx, "app", catalog.StateRegistered, catalog.StateUpdating, catalog.TransitionInfo{}); !errors.Is(err, catalog.ErrInvalidTransition) {
		t.Fatalf("forbidden transition: %v", err)
	}
	if err := e.cat.Transition(ctx, "app", catalog.StateRunning, catalog.StateStopped, catalog.TransitionInfo{}); !errors.Is(err, catalog.ErrStateChanged) {
		t.Fatalf("stale expected state: %v", err)
	}
	if err := e.cat.Transition(ctx, "ghost", catalog.StateRegistered, catalog.StateRunning, catalog.TransitionInfo{}); !errors.Is(err, catalog.ErrNotFound) {
		t.Fatalf("missing service: %v", err)
	}
	if err := e.cat.Transition(ctx, "app", "bogus", catalog.StateRunning, catalog.TransitionInfo{}); err == nil {
		t.Fatal("unknown state accepted")
	}
	if err := e.cat.Transition(ctx, "app", catalog.StateRegistered, catalog.StateRegistered, catalog.TransitionInfo{}); err != nil {
		t.Fatalf("same-state transition must be a no-op: %v", err)
	}
	if e.state("app") != catalog.StateRegistered {
		t.Fatal("state changed by failed transitions")
	}
	select {
	case sig := <-sub.C:
		t.Fatalf("unexpected signal %+v", sig)
	default:
	}
	if err := (&catalog.StateError{Service: "a", Action: "x", State: catalog.StateRunning}); !errors.Is(err, catalog.ErrInvalidTransition) {
		t.Fatal("StateError must match ErrInvalidTransition")
	}
}
