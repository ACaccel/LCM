// Package signal carries events that may trigger policies or appear on a
// service timeline: check and health changes, lifecycle state changes and
// external alerts. Producers publish to a Bus; consumers such as the
// trigger manager and the timeline subscribe.
//
// Delivery is in-process and lossy by design: Publish never blocks, and a
// subscriber whose buffer is full loses the signal (counted in Dropped).
// Consumers that must not miss state should re-read the authoritative store
// (catalog, monitor) instead of relying on signals alone.
package signal

import (
	"crypto/rand"
	"encoding/hex"
	"log/slog"
	"sync"
	"sync/atomic"
	"time"
)

// Kind identifies the type of a signal.
type Kind string

// Signal kinds.
const (
	// KindCheck: one check changed status. Data: check, status, previous, message.
	KindCheck Kind = "check.changed"
	// KindHealth: a service's aggregated health changed. Data: health, previous.
	KindHealth Kind = "health.changed"
	// KindLifecycle: a service's lifecycle state changed. Data: state, previous, action.
	KindLifecycle Kind = "lifecycle.changed"
	// KindAlert: an external alert fired or resolved. Data: alert, status, source-specific fields.
	KindAlert Kind = "alert"
)

// Severity levels used by alerts and health signals.
const (
	SeverityInfo     = "info"
	SeverityWarning  = "warning"
	SeverityCritical = "critical"
)

// Signal is one event. Data must be JSON-serializable; it becomes the
// trigger payload of runs started by the signal.
type Signal struct {
	ID       string            `json:"id"`
	Kind     Kind              `json:"kind"`
	Time     time.Time         `json:"time"`
	Service  string            `json:"service,omitempty"`
	Source   string            `json:"source"`
	Severity string            `json:"severity,omitempty"`
	Labels   map[string]string `json:"labels,omitempty"`
	Data     map[string]any    `json:"data,omitempty"`
}

// Bus fans signals out to subscribers.
type Bus struct {
	log  *slog.Logger
	now  func() time.Time
	mu   sync.RWMutex
	subs map[*Subscription]struct{}
}

// NewBus returns a Bus. now may be nil to use time.Now.
func NewBus(log *slog.Logger, now func() time.Time) *Bus {
	if now == nil {
		now = time.Now
	}
	return &Bus{log: log, now: now, subs: map[*Subscription]struct{}{}}
}

// Subscription receives signals on C until Close is called.
type Subscription struct {
	C       <-chan Signal
	ch      chan Signal
	name    string
	bus     *Bus
	dropped atomic.Uint64
	once    sync.Once
}

// Subscribe registers a subscriber with the given buffer size (minimum 1).
// name identifies the subscriber in drop warnings.
func (b *Bus) Subscribe(name string, buffer int) *Subscription {
	ch := make(chan Signal, max(buffer, 1))
	s := &Subscription{C: ch, ch: ch, name: name, bus: b}
	b.mu.Lock()
	b.subs[s] = struct{}{}
	b.mu.Unlock()
	return s
}

// Dropped returns how many signals this subscriber lost to a full buffer.
func (s *Subscription) Dropped() uint64 { return s.dropped.Load() }

// Close unsubscribes and closes C. It is safe to call more than once.
func (s *Subscription) Close() {
	s.once.Do(func() {
		s.bus.mu.Lock()
		delete(s.bus.subs, s)
		close(s.ch)
		s.bus.mu.Unlock()
	})
}

// Publish delivers sig to every subscriber without blocking. Missing ID and
// Time are filled in. The returned signal is what subscribers receive.
func (b *Bus) Publish(sig Signal) Signal {
	if sig.ID == "" {
		sig.ID = newID()
	}
	if sig.Time.IsZero() {
		sig.Time = b.now()
	}
	b.mu.RLock()
	defer b.mu.RUnlock()
	for s := range b.subs {
		select {
		case s.ch <- sig:
		default:
			if n := s.dropped.Add(1); n == 1 || n%100 == 0 {
				b.log.Warn("signal subscriber is falling behind; signal dropped",
					"subscriber", s.name, "kind", sig.Kind, "service", sig.Service, "dropped", n)
			}
		}
	}
	return sig
}

func newID() string {
	var b [12]byte
	// crypto/rand.Read does not fail on supported platforms.
	_, _ = rand.Read(b[:])
	return hex.EncodeToString(b[:])
}
