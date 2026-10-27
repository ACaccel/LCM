package engine

import "sync"

// scheduler is the shared ready queue. Runs with ready nodes take turns
// (round-robin), so a run that fans out to many nodes cannot starve other
// runs; within a run, nodes are taken in the order they became ready.
type scheduler struct {
	mu      sync.Mutex
	cond    *sync.Cond
	runs    []*run
	stopped bool
}

func newScheduler() *scheduler {
	s := &scheduler{}
	s.cond = sync.NewCond(&s.mu)
	return s
}

// push queues node i of r. It never blocks and is a no-op once stopped.
func (s *scheduler) push(r *run, i int) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.stopped {
		return
	}
	r.sq = append(r.sq, i)
	if !r.inSched {
		r.inSched = true
		s.runs = append(s.runs, r)
	}
	s.cond.Signal()
}

// pop blocks until a node is ready or the scheduler stops.
func (s *scheduler) pop() (*run, int, bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	for len(s.runs) == 0 && !s.stopped {
		s.cond.Wait()
	}
	if s.stopped {
		return nil, 0, false
	}
	r := s.runs[0]
	s.runs[0] = nil
	s.runs = s.runs[1:]
	i := r.sq[0]
	r.sq = r.sq[1:]
	if len(r.sq) > 0 {
		s.runs = append(s.runs, r)
	} else {
		r.inSched = false
		r.sq = nil
	}
	return r, i, true
}

// stop wakes every worker; queued work stays undispatched.
func (s *scheduler) stop() {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.stopped = true
	s.cond.Broadcast()
}
