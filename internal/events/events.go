// Package events is the in-process hub behind the server-sent event stream.
// It gathers what happens in LCM (signals, engine runs and nodes, timeline
// entries, actions, catalog changes) into one ordered feed that HTTP clients
// subscribe to with optional filters.
//
// Delivery is best effort and never blocks a producer: every subscriber has
// a bounded buffer, and a subscriber that falls behind is cut off with its
// Overflowed flag set so the client can resynchronize from the REST API.
package events

import (
	"log/slog"
	"sync"
	"sync/atomic"
	"time"
)

// Type names an event stream category.
type Type string

// Event types.
const (
	// TypeSignal carries a signal.Signal published on the bus.
	TypeSignal Type = "signal"
	// TypeRun carries an engine run event (queued, started, finished, ...).
	TypeRun Type = "run"
	// TypeNode carries an engine node event.
	TypeNode Type = "node"
	// TypeTimeline announces a stored timeline entry.
	TypeTimeline Type = "timeline"
	// TypeAction carries the progress of an API-started service action.
	TypeAction Type = "action"
	// TypeCatalog announces a changed service document, or a deployed or
	// removed policy.
	TypeCatalog Type = "catalog"
)

// Types lists every event type.
var Types = []Type{TypeSignal, TypeRun, TypeNode, TypeTimeline, TypeAction, TypeCatalog}

// Valid reports whether t is a known event type.
func (t Type) Valid() bool {
	for _, k := range Types {
		if k == t {
			return true
		}
	}
	return false
}

// Event is one entry of the feed. Service, Policy and Run route the event
// to filtered subscribers; Data is the JSON-serializable payload.
type Event struct {
	// ID increases with every published event.
	ID      uint64
	Type    Type
	Time    time.Time
	Service string
	Policy  string
	Run     string
	Data    any
}

// Filter restricts a subscription. Empty fields match everything; the
// non-empty ones must all match.
type Filter struct {
	Service string
	Policy  string
	Run     string
	Types   []Type
}

// Match reports whether e passes the filter.
func (f Filter) Match(e Event) bool {
	if f.Service != "" && e.Service != f.Service {
		return false
	}
	if f.Policy != "" && e.Policy != f.Policy {
		return false
	}
	if f.Run != "" && e.Run != f.Run {
		return false
	}
	if len(f.Types) > 0 {
		for _, t := range f.Types {
			if t == e.Type {
				return true
			}
		}
		return false
	}
	return true
}

// Hub fans events out to subscribers.
type Hub struct {
	log *slog.Logger
	now func() time.Time
	seq atomic.Uint64

	mu     sync.RWMutex
	subs   map[*Subscription]struct{}
	closed bool

	bg   sync.WaitGroup
	stop chan struct{}
}

// NewHub returns a Hub. now may be nil to use time.Now.
func NewHub(log *slog.Logger, now func() time.Time) *Hub {
	if log == nil {
		log = slog.New(slog.DiscardHandler)
	}
	if now == nil {
		now = time.Now
	}
	return &Hub{log: log, now: now, subs: map[*Subscription]struct{}{}, stop: make(chan struct{})}
}

// Subscription receives matching events on C. C is closed when the
// subscription is closed, the hub closes, or the subscriber overflows.
type Subscription struct {
	C <-chan Event

	hub    *Hub
	filter Filter
	ch     chan Event

	mu         sync.Mutex
	closed     bool
	overflowed bool
}

// Subscribe registers a subscriber with a buffer of the given size
// (minimum 1). After the hub closed it returns a subscription that is
// already closed.
func (h *Hub) Subscribe(f Filter, buffer int) *Subscription {
	ch := make(chan Event, max(buffer, 1))
	s := &Subscription{C: ch, ch: ch, hub: h, filter: f}
	h.mu.Lock()
	defer h.mu.Unlock()
	if h.closed {
		s.closed = true
		close(ch)
		return s
	}
	h.subs[s] = struct{}{}
	return s
}

// Overflowed reports whether the subscription was cut off because its
// buffer was full.
func (s *Subscription) Overflowed() bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.overflowed
}

// Close unsubscribes. It is safe to call more than once.
func (s *Subscription) Close() {
	s.hub.mu.Lock()
	delete(s.hub.subs, s)
	s.hub.mu.Unlock()
	s.shut(false)
}

func (s *Subscription) shut(overflow bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.closed {
		return
	}
	s.closed = true
	s.overflowed = overflow
	close(s.ch)
}

func (s *Subscription) offer(e Event) (ok bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.closed {
		return true
	}
	select {
	case s.ch <- e:
		return true
	default:
		s.closed, s.overflowed = true, true
		close(s.ch)
		return false
	}
}

// Publish stamps e with an ID and, when unset, a time, and delivers it to
// every matching subscriber without blocking. Subscribers whose buffer is
// full are dropped. It returns the stamped event.
func (h *Hub) Publish(e Event) Event {
	e.ID = h.seq.Add(1)
	if e.Time.IsZero() {
		e.Time = h.now()
	}
	var slow []*Subscription
	h.mu.RLock()
	for s := range h.subs {
		if s.filter.Match(e) && !s.offer(e) {
			slow = append(slow, s)
		}
	}
	h.mu.RUnlock()
	if len(slow) > 0 {
		h.mu.Lock()
		for _, s := range slow {
			delete(h.subs, s)
		}
		h.mu.Unlock()
		h.log.Warn("event subscriber is too slow; disconnected", "subscribers", len(slow), "type", e.Type)
	}
	return e
}

// Subscribers returns the number of open subscriptions.
func (h *Hub) Subscribers() int {
	h.mu.RLock()
	defer h.mu.RUnlock()
	return len(h.subs)
}

// Close disconnects every subscriber and stops the background followers.
// It is safe to call more than once.
func (h *Hub) Close() {
	h.mu.Lock()
	if h.closed {
		h.mu.Unlock()
		return
	}
	h.closed = true
	subs := make([]*Subscription, 0, len(h.subs))
	for s := range h.subs {
		subs = append(subs, s)
	}
	h.subs = map[*Subscription]struct{}{}
	close(h.stop)
	h.mu.Unlock()
	for _, s := range subs {
		s.shut(false)
	}
	h.bg.Wait()
}
