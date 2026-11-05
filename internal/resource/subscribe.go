package resource

import (
	"context"
	"sync"
)

// changeQueue is the number of changes buffered per subscriber. A writer
// whose change finds a full queue waits for that subscriber.
const changeQueue = 256

// subscriber delivers changes to one function on its own goroutine.
type subscriber struct {
	fn   func(Change)
	ch   chan delivery
	done chan struct{}
}

// delivery is a change, or a flush marker when ack is set.
type delivery struct {
	change Change
	ack    chan struct{}
}

// subscribers fans committed changes out to the functions passed to
// Subscribe.
type subscribers struct {
	// mu is held for reading while changes are queued and for writing to
	// add a subscriber or close, so that no queue is closed under a sender.
	mu     sync.RWMutex
	subs   []*subscriber
	closed bool
}

// Subscribe calls fn with every change committed after it returns, on a
// goroutine owned by the store, in commit order. Delivery is asynchronous:
// the writer does not wait for fn, and a reader that needs the effect of a
// change it made calls Flush. Changes are never coalesced or dropped while
// the store is open; when fn falls behind by more than a bounded queue, the
// writers wait for it. fn may read from the store but must not write to
// it. A panic in fn is logged and the next change is still delivered.
// Subscribe after Close does nothing.
func (s *Store) Subscribe(fn func(Change)) {
	s.subs.mu.Lock()
	defer s.subs.mu.Unlock()
	if s.subs.closed {
		return
	}
	sub := &subscriber{fn: fn, ch: make(chan delivery, changeQueue), done: make(chan struct{})}
	s.subs.subs = append(s.subs.subs, sub)
	go s.deliver(sub)
}

func (s *Store) deliver(sub *subscriber) {
	defer close(sub.done)
	for d := range sub.ch {
		if d.ack != nil {
			close(d.ack)
			continue
		}
		s.call(sub, d.change)
	}
}

func (s *Store) call(sub *subscriber, c Change) {
	defer func() {
		if r := recover(); r != nil {
			s.log.Error("resource change subscriber panicked", "resource", c.Name, "change", c.Change, "panic", r)
		}
	}()
	sub.fn(c)
}

// changed queues a committed change for every subscriber.
func (s *Store) changed(c Change) {
	s.subs.mu.RLock()
	defer s.subs.mu.RUnlock()
	if s.subs.closed {
		return
	}
	for _, sub := range s.subs.subs {
		sub.ch <- delivery{change: c}
	}
}

// Flush waits until every subscriber has handled the changes committed
// before the call, or ctx ends.
func (s *Store) Flush(ctx context.Context) error {
	var acks []chan struct{}
	s.subs.mu.RLock()
	if !s.subs.closed {
		for _, sub := range s.subs.subs {
			ack := make(chan struct{})
			select {
			case sub.ch <- delivery{ack: ack}:
			case <-ctx.Done():
				s.subs.mu.RUnlock()
				return ctx.Err()
			}
			acks = append(acks, ack)
		}
	}
	s.subs.mu.RUnlock()
	for _, ack := range acks {
		select {
		case <-ack:
		case <-ctx.Done():
			return ctx.Err()
		}
	}
	return nil
}

// Close delivers the queued changes and stops the subscriber goroutines.
// Changes committed afterwards are not delivered. It is safe to call more
// than once.
func (s *Store) Close() {
	s.subs.mu.Lock()
	if s.subs.closed {
		s.subs.mu.Unlock()
		return
	}
	s.subs.closed = true
	subs := s.subs.subs
	s.subs.subs = nil
	for _, sub := range subs {
		close(sub.ch)
	}
	s.subs.mu.Unlock()
	for _, sub := range subs {
		<-sub.done
	}
}
