package resource_test

import (
	"context"
	"slices"
	"testing"

	"github.com/ACaccel/lcm/internal/resource"
)

func TestSubscribeDeliversInOrderAsynchronously(t *testing.T) {
	h := newHarness(t)
	// A subscriber that blocks does not hold up the writer; its changes
	// stay queued in commit order.
	release := make(chan struct{})
	var got []string
	h.store.Subscribe(func(c resource.Change) {
		<-release
		got = append(got, c.Name+":"+c.Change)
	})
	// A panicking subscriber neither stops its own later deliveries nor
	// affects other subscribers.
	var survived []string
	h.store.Subscribe(func(c resource.Change) {
		if c.Name == "a" {
			panic("subscriber failure")
		}
		survived = append(survived, c.Name)
	})
	h.put(t, service("a", "command", "  config: { start: run }\n"), 0)
	h.put(t, service("b", "command", "  config: { start: run }\n"), 0)
	if err := h.store.SetPhase(h.ctx, "a", resource.PhaseRetired, 1, ""); err != nil {
		t.Fatal(err)
	}

	// Flush waits for the blocked subscriber and respects its context.
	ctx, cancel := context.WithCancel(h.ctx)
	cancel()
	if err := h.store.Flush(ctx); err == nil {
		t.Fatal("flush returned before the blocked subscriber ran")
	}
	close(release)
	if err := h.store.Flush(h.ctx); err != nil {
		t.Fatal(err)
	}
	if want := []string{"a:created", "b:created", "a:phase"}; !slices.Equal(got, want) {
		t.Fatalf("delivered = %v, want %v", got, want)
	}
	if want := []string{"b"}; !slices.Equal(survived, want) {
		t.Fatalf("after panic = %v, want %v", survived, want)
	}
}

func TestCloseDrainsQueuedChanges(t *testing.T) {
	h := newHarness(t)
	release := make(chan struct{})
	var got []string
	h.store.Subscribe(func(c resource.Change) {
		<-release
		got = append(got, c.Name)
	})
	h.put(t, service("a", "command", "  config: { start: run }\n"), 0)
	h.put(t, service("b", "command", "  config: { start: run }\n"), 0)
	closed := make(chan struct{})
	go func() {
		h.store.Close()
		close(closed)
	}()
	close(release)
	<-closed
	if want := []string{"a", "b"}; !slices.Equal(got, want) {
		t.Fatalf("drained = %v, want %v", got, want)
	}
	// After Close nothing is delivered and Subscribe and Flush do nothing.
	h.store.Subscribe(func(resource.Change) { t.Error("subscribed after close") })
	h.put(t, service("c", "command", "  config: { start: run }\n"), 0)
	if err := h.store.Flush(h.ctx); err != nil {
		t.Fatal(err)
	}
	if len(got) != 2 {
		t.Fatalf("delivered after close: %v", got)
	}
	h.store.Close()
}
