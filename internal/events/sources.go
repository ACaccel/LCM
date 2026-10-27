package events

import (
	"strings"
	"time"

	"github.com/ACaccel/lcm/internal/catalog"
	"github.com/ACaccel/lcm/internal/engine"
	"github.com/ACaccel/lcm/internal/signal"
)

// busBuffer absorbs bursts of signals while the hub fans them out.
const busBuffer = 256

// FollowBus republishes every signal on the bus as a TypeSignal event until
// the hub closes.
func (h *Hub) FollowBus(bus *signal.Bus) {
	sub := bus.Subscribe("events", busBuffer)
	h.bg.Add(1)
	go func() {
		defer h.bg.Done()
		defer sub.Close()
		for {
			select {
			case <-h.stop:
				return
			case sig, ok := <-sub.C:
				if !ok {
					return
				}
				sig.Time = sig.Time.UTC()
				h.Publish(Event{Type: TypeSignal, Time: sig.Time, Service: sig.Service, Data: sig})
			}
		}
	}()
}

// Observer returns an engine observer that republishes engine events: run
// and node events keep their payload, policy deployments become catalog
// events.
func (h *Hub) Observer() engine.Observer {
	return engine.ObserverFunc(func(e engine.Event) {
		t := TypeCatalog
		switch {
		case strings.HasPrefix(string(e.Kind), "run."):
			t = TypeRun
		case strings.HasPrefix(string(e.Kind), "node."):
			t = TypeNode
		}
		e.Time = e.Time.UTC()
		h.Publish(Event{Type: t, Time: e.Time, Policy: e.Policy, Run: e.RunID, Data: e})
	})
}

// TimelineEntry is the wire form of a timeline entry. It is the payload of
// TypeTimeline events and the item of the timeline endpoints.
type TimelineEntry struct {
	// ID is 0 for entries announced by an event whose row id is not known;
	// clients refetch the timeline instead of relying on it.
	ID      int64          `json:"id"`
	Service string         `json:"service"`
	Time    time.Time      `json:"time"`
	Kind    string         `json:"kind"`
	Summary string         `json:"summary"`
	Data    map[string]any `json:"data"`
	Ref     string         `json:"ref"`
}

// NewTimelineEntry converts a catalog entry.
func NewTimelineEntry(e catalog.Entry) TimelineEntry {
	data := e.Data
	if data == nil {
		data = map[string]any{}
	}
	return TimelineEntry{ID: e.ID, Service: e.Service, Time: e.Time.UTC(), Kind: e.Kind, Summary: e.Summary, Data: data, Ref: e.Ref}
}

// Timeline announces a stored timeline entry.
func (h *Hub) Timeline(e catalog.Entry) {
	h.Publish(Event{Type: TypeTimeline, Time: e.Time, Service: e.Service, Data: NewTimelineEntry(e)})
}

// CatalogChange is the payload of TypeCatalog events about services.
type CatalogChange struct {
	Service string `json:"service"`
	// Change is created, updated, restored or deleted.
	Change string `json:"change"`
}

// Document announces a changed service document.
func (h *Hub) Document(service, change string) {
	h.Publish(Event{Type: TypeCatalog, Service: service, Data: CatalogChange{Service: service, Change: change}})
}
