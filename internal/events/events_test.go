package events_test

import (
	"io"
	"log/slog"
	"testing"
	"time"

	"go.uber.org/goleak"

	"github.com/ACaccel/lcm/internal/catalog"
	"github.com/ACaccel/lcm/internal/engine"
	"github.com/ACaccel/lcm/internal/events"
	"github.com/ACaccel/lcm/internal/signal"
)

func TestMain(m *testing.M) { goleak.VerifyTestMain(m) }

var epoch = time.Date(2026, 3, 1, 12, 0, 0, 0, time.UTC)

func newHub() *events.Hub {
	return events.NewHub(slog.New(slog.NewTextHandler(io.Discard, nil)), func() time.Time { return epoch })
}

func recv(t *testing.T, s *events.Subscription) events.Event {
	t.Helper()
	select {
	case e, ok := <-s.C:
		if !ok {
			t.Fatal("subscription closed")
		}
		return e
	case <-time.After(5 * time.Second):
		t.Fatal("no event")
	}
	return events.Event{}
}

func TestFilterMatch(t *testing.T) {
	e := events.Event{Type: events.TypeRun, Service: "web", Policy: "heal", Run: "r1"}
	for name, tc := range map[string]struct {
		f    events.Filter
		want bool
	}{
		"empty":         {events.Filter{}, true},
		"service":       {events.Filter{Service: "web"}, true},
		"other service": {events.Filter{Service: "db"}, false},
		"policy":        {events.Filter{Policy: "heal"}, true},
		"other run":     {events.Filter{Run: "r2"}, false},
		"type":          {events.Filter{Types: []events.Type{events.TypeSignal, events.TypeRun}}, true},
		"other type":    {events.Filter{Types: []events.Type{events.TypeNode}}, false},
		"all must hold": {events.Filter{Service: "web", Run: "r2"}, false},
	} {
		if got := tc.f.Match(e); got != tc.want {
			t.Errorf("%s: Match = %v, want %v", name, got, tc.want)
		}
	}
}

func TestPublishStampsAndFilters(t *testing.T) {
	h := newHub()
	defer h.Close()
	all := h.Subscribe(events.Filter{}, 8)
	web := h.Subscribe(events.Filter{Service: "web"}, 8)
	defer all.Close()
	defer web.Close()

	h.Publish(events.Event{Type: events.TypeAction, Service: "db", Data: 1})
	got := h.Publish(events.Event{Type: events.TypeAction, Service: "web", Data: 2})
	if got.ID != 2 || !got.Time.Equal(epoch) {
		t.Fatalf("stamped event = %+v", got)
	}
	if e := recv(t, all); e.Service != "db" {
		t.Fatalf("all[0] = %+v", e)
	}
	if e := recv(t, all); e.ID != 2 {
		t.Fatalf("all[1] = %+v", e)
	}
	if e := recv(t, web); e.ID != 2 {
		t.Fatalf("filtered = %+v", e)
	}
	select {
	case e := <-web.C:
		t.Fatalf("filtered subscriber got %+v", e)
	default:
	}
}

func TestSlowSubscriberIsCutOff(t *testing.T) {
	h := newHub()
	defer h.Close()
	slow := h.Subscribe(events.Filter{}, 2)
	ok := h.Subscribe(events.Filter{}, 16)
	defer ok.Close()

	for range 5 {
		h.Publish(events.Event{Type: events.TypeSignal})
	}
	n := 0
	for range slow.C {
		n++
	}
	if n != 2 || !slow.Overflowed() {
		t.Fatalf("slow subscriber got %d events, overflowed=%v", n, slow.Overflowed())
	}
	if h.Subscribers() != 1 {
		t.Fatalf("subscribers = %d, want 1", h.Subscribers())
	}
	for range 5 {
		recv(t, ok)
	}
	slow.Close() // already cut off; must not panic
}

func TestCloseDisconnectsEveryone(t *testing.T) {
	h := newHub()
	s := h.Subscribe(events.Filter{}, 1)
	h.Close()
	h.Close()
	if _, open := <-s.C; open || s.Overflowed() {
		t.Fatal("subscription not closed cleanly")
	}
	late := h.Subscribe(events.Filter{}, 1)
	if _, open := <-late.C; open {
		t.Fatal("subscription after Close is open")
	}
	h.Publish(events.Event{}) // no subscribers; must not panic
}

func TestFollowBus(t *testing.T) {
	h := newHub()
	bus := signal.NewBus(slog.New(slog.NewTextHandler(io.Discard, nil)), nil)
	h.FollowBus(bus)
	s := h.Subscribe(events.Filter{Types: []events.Type{events.TypeSignal}}, 8)
	defer s.Close()

	local := time.Date(2026, 3, 1, 20, 0, 0, 0, time.FixedZone("UTC+8", 8*3600))
	bus.Publish(signal.Signal{Kind: signal.KindHealth, Service: "web", Source: "monitor", Time: local})
	e := recv(t, s)
	sig, ok := e.Data.(signal.Signal)
	if e.Type != events.TypeSignal || e.Service != "web" || !ok || sig.Kind != signal.KindHealth {
		t.Fatalf("event = %+v", e)
	}
	if sig.Time.Location() != time.UTC || !sig.Time.Equal(local) || e.Time.Location() != time.UTC {
		t.Fatalf("times must be UTC: signal %v, event %v", sig.Time, e.Time)
	}
	h.Close()
}

func TestObserverAndCatalogSources(t *testing.T) {
	h := newHub()
	defer h.Close()
	s := h.Subscribe(events.Filter{}, 16)
	defer s.Close()

	obs := h.Observer()
	obs.Observe(engine.Event{Kind: engine.EventRunStarted, Policy: "heal", RunID: "r1", Time: time.Now()})
	obs.Observe(engine.Event{Kind: engine.EventNodeFinished, Policy: "heal", RunID: "r1", NodeID: "n"})
	obs.Observe(engine.Event{Kind: engine.EventPolicyDeployed, Policy: "heal", Version: 2})
	h.Document("web", catalog.ChangeUpdated)
	h.Timeline(catalog.Entry{Service: "web", Kind: catalog.KindAction, Summary: "restart succeeded"})

	want := []struct {
		typ     events.Type
		service string
		policy  string
		run     string
	}{
		{events.TypeRun, "", "heal", "r1"},
		{events.TypeNode, "", "heal", "r1"},
		{events.TypeCatalog, "", "heal", ""},
		{events.TypeCatalog, "web", "", ""},
		{events.TypeTimeline, "web", "", ""},
	}
	for i, w := range want {
		e := recv(t, s)
		if e.Type != w.typ || e.Service != w.service || e.Policy != w.policy || e.Run != w.run {
			t.Errorf("event %d = %+v, want %+v", i, e, w)
		}
	}
}
