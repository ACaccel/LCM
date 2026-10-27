package signal

import (
	"io"
	"log/slog"
	"sync"
	"testing"
	"time"
)

func newTestBus() *Bus {
	fixed := time.Date(2026, 1, 2, 3, 4, 5, 0, time.UTC)
	return NewBus(slog.New(slog.NewTextHandler(io.Discard, nil)), func() time.Time { return fixed })
}

func TestPublishFanOutAndDefaults(t *testing.T) {
	b := newTestBus()
	a, c := b.Subscribe("a", 4), b.Subscribe("c", 4)
	defer a.Close()
	defer c.Close()

	sent := b.Publish(Signal{Kind: KindHealth, Service: "web", Source: "monitor"})
	if sent.ID == "" || sent.Time.IsZero() {
		t.Fatalf("ID and Time must be filled: %+v", sent)
	}
	for _, s := range []*Subscription{a, c} {
		got := <-s.C
		if got.ID != sent.ID || got.Service != "web" {
			t.Fatalf("subscriber %s got %+v", s.name, got)
		}
	}
}

func TestPublishDoesNotBlockOnFullSubscriber(t *testing.T) {
	b := newTestBus()
	slow := b.Subscribe("slow", 1)
	defer slow.Close()
	for range 5 {
		b.Publish(Signal{Kind: KindAlert})
	}
	if got := slow.Dropped(); got != 4 {
		t.Fatalf("dropped = %d, want 4", got)
	}
}

func TestCloseIsIdempotentAndStopsDelivery(t *testing.T) {
	b := newTestBus()
	s := b.Subscribe("s", 1)
	s.Close()
	s.Close()
	if _, ok := <-s.C; ok {
		t.Fatal("C must be closed")
	}
	b.Publish(Signal{Kind: KindAlert}) // must not panic on a closed channel
}

func TestConcurrentPublishAndClose(t *testing.T) {
	b := newTestBus()
	var wg sync.WaitGroup
	for range 4 {
		wg.Go(func() {
			for range 100 {
				s := b.Subscribe("x", 1)
				b.Publish(Signal{Kind: KindCheck})
				s.Close()
			}
		})
	}
	wg.Wait()
}
