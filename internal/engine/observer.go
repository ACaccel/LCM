package engine

import (
	"log/slog"
	"sync"
	"sync/atomic"
	"time"
)

// EventKind identifies an engine lifecycle event.
type EventKind string

// Event kinds.
const (
	EventPolicyDeployed   EventKind = "policy.deployed"
	EventPolicyUndeployed EventKind = "policy.undeployed"
	EventRunQueued        EventKind = "run.queued"
	EventRunStarted       EventKind = "run.started"
	EventRunSuspended     EventKind = "run.suspended"
	EventRunResumed       EventKind = "run.resumed"
	EventRunFinished      EventKind = "run.finished"
	EventNodeStarted      EventKind = "node.started"
	EventNodeFinished     EventKind = "node.finished"
	EventNodeRetrying     EventKind = "node.retrying"
	EventNodeSuspended    EventKind = "node.suspended"
	EventNodeDead         EventKind = "node.dead"
)

// Event is delivered to observers. Fields that do not apply are zero.
type Event struct {
	Kind    EventKind `json:"kind"`
	Time    time.Time `json:"time"`
	Policy  string    `json:"policy"`
	Version int       `json:"version,omitempty"`
	RunID   string    `json:"runId,omitempty"`
	Mode    Mode      `json:"mode,omitempty"`
	// Status is the run status for run events and the node status for node
	// events.
	Status   string `json:"status,omitempty"`
	NodeID   string `json:"nodeId,omitempty"`
	NodeType string `json:"nodeType,omitempty"`
	Port     string `json:"port,omitempty"`
	Attempt  int    `json:"attempt,omitempty"`
	Error    string `json:"error,omitempty"`
	// RetryAt is set for node.retrying and, when timed, node.suspended.
	RetryAt *time.Time `json:"retryAt,omitempty"`
}

// Observer receives engine events. Delivery is asynchronous and lossy: each
// observer has its own buffer and goroutine, and events are dropped (and
// counted) when the buffer is full, so a slow observer never stalls
// scheduling. Events of one run are delivered in order.
type Observer interface {
	Observe(Event)
}

// ObserverFunc adapts a function to Observer.
type ObserverFunc func(Event)

// Observe implements Observer.
func (f ObserverFunc) Observe(e Event) { f(e) }

const observerBuffer = 4096

type dispatcher struct {
	obs     Observer
	ch      chan Event
	dropped atomic.Uint64
	log     *slog.Logger
	done    chan struct{}
}

type observers struct {
	ds     []*dispatcher
	mu     sync.RWMutex
	closed bool
}

func newObservers(obs []Observer, log *slog.Logger) *observers {
	o := &observers{}
	for _, ob := range obs {
		d := &dispatcher{obs: ob, ch: make(chan Event, observerBuffer), log: log, done: make(chan struct{})}
		o.ds = append(o.ds, d)
		go d.loop()
	}
	return o
}

func (d *dispatcher) loop() {
	defer close(d.done)
	for e := range d.ch {
		d.deliver(e)
	}
}

func (d *dispatcher) deliver(e Event) {
	defer func() {
		if r := recover(); r != nil {
			d.log.Error("observer panicked", "event", e.Kind, "panic", r)
		}
	}()
	d.obs.Observe(e)
}

// emit never blocks.
func (o *observers) emit(e Event) {
	o.mu.RLock()
	defer o.mu.RUnlock()
	if o.closed {
		return
	}
	for _, d := range o.ds {
		select {
		case d.ch <- e:
		default:
			d.dropped.Add(1)
		}
	}
}

func (o *observers) dropped() uint64 {
	var n uint64
	for _, d := range o.ds {
		n += d.dropped.Load()
	}
	return n
}

// close stops accepting events and waits until every buffered event has
// been delivered or done is closed.
func (o *observers) close(done <-chan struct{}) {
	o.mu.Lock()
	if o.closed {
		o.mu.Unlock()
		return
	}
	o.closed = true
	for _, d := range o.ds {
		close(d.ch)
	}
	o.mu.Unlock()
	for _, d := range o.ds {
		select {
		case <-d.done:
		case <-done:
			return
		}
	}
}
