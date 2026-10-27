package policy

import "sync"

/*
Quiescence for a module's inner walk.

A module has to answer a question the top-level policy never asks: is the walk
finished? The enclosing walker is blocked inside Module.Execute waiting for a
result, so the module cannot simply spawn walkers and forget them.

"All walkers have exited" is the wrong answer. A node that joins two inputs is
reached by one walker per input, exactly one of them consumes the readiness
tokens and runs, and the others wait forever -- that is the engine's join model,
not a fault. Waiting for those to exit would hang every module whose branches
rejoin, which is the ordinary shape.

The right answer is "nothing is runnable any more": every walker has either
finished or is parked waiting for an input that no remaining walker will
produce. That is what this counts. Once it holds, the module cancels the parked
walkers and waits for them to actually leave, so the next run finds the inner
packets untouched.

A nil *quiesce is a top-level node, where none of this applies: the policy loop
retires a run rather than waiting for one, so every method is a no-op.
*/
type quiesce struct {
	mu sync.Mutex
	// runnable counts walkers that are neither finished nor parked.
	runnable int
	idle     chan struct{}
	closed   bool
	// walkers counts every live walker, parked included, so the module can wait
	// for them to leave once it has cancelled them.
	walkers sync.WaitGroup
	// done is cancelled when the walk is over; inner walkers watch it the way
	// top-level ones watch the run's own cancellation.
	done chan struct{}
}

func newQuiesce() *quiesce {
	q := &quiesce{}
	q.reset()
	return q
}

// reset arms the counter for another walk.
//
// Safe to call between walks only, which Module.Execute guarantees by draining
// the previous one before it returns.
func (q *quiesce) reset() {
	q.mu.Lock()
	defer q.mu.Unlock()
	q.runnable = 0
	q.closed = false
	q.idle = make(chan struct{})
	q.done = make(chan struct{})
}

func (q *quiesce) idleCh() <-chan struct{} {
	q.mu.Lock()
	defer q.mu.Unlock()
	return q.idle
}

func (q *quiesce) doneCh() <-chan struct{} {
	q.mu.Lock()
	defer q.mu.Unlock()
	return q.done
}

// cancel releases every parked walker. Idempotent: the walk ends once, but both
// quiescence and the enclosing run's cancellation can be what ends it.
func (q *quiesce) cancel() {
	q.mu.Lock()
	defer q.mu.Unlock()
	select {
	case <-q.done:
	default:
		close(q.done)
	}
}

// spawn starts a walker and counts it as runnable.
//
// Add happens here rather than inside the goroutine so a walker that has been
// scheduled but not started cannot be mistaken for quiescence.
func (q *quiesce) spawn(walk func()) {
	q.mu.Lock()
	q.runnable++
	q.mu.Unlock()
	q.walkers.Add(1)
	go func() {
		defer q.walkers.Done()
		defer q.finished()
		walk()
	}()
}

func (q *quiesce) finished() {
	q.mu.Lock()
	defer q.mu.Unlock()
	q.runnable--
	q.maybeIdle()
}

// enterWait and leaveWait bracket the only place a walker blocks on another
// walker. Counting at exactly that point is what makes quiescence precise:
// bracketing the whole of inputsSatisfied instead would report a walker as
// parked while it was in fact about to run, and the module would conclude the
// walk was over while work remained.
func (q *quiesce) enterWait() {
	if q == nil {
		return
	}
	q.mu.Lock()
	defer q.mu.Unlock()
	q.runnable--
	q.maybeIdle()
}

func (q *quiesce) leaveWait() {
	if q == nil {
		return
	}
	q.mu.Lock()
	defer q.mu.Unlock()
	q.runnable++
}

// maybeIdle must be called with the lock held.
func (q *quiesce) maybeIdle() {
	if q.runnable == 0 && !q.closed {
		q.closed = true
		close(q.idle)
	}
}

// drain waits for every walker, parked ones included. Only meaningful after
// cancel, which is what releases them.
func (q *quiesce) drain() {
	q.walkers.Wait()
}
