package router

import (
	"context"
	"errors"
	"time"

	"github.com/ACaccel/lcm/internal/model"
	"github.com/ACaccel/lcm/internal/resource"
)

// retryWait is the pause of a loop that could not read its router.
const retryWait = time.Minute

// loop is the observation goroutine of one managed router.
type loop struct {
	id     string
	name   string
	cancel context.CancelFunc
	wake   chan struct{}
	done   chan struct{}
}

// exited reports whether the loop ended by itself, which it does when it
// finds its router no longer managed.
func (l *loop) exited() bool {
	select {
	case <-l.done:
		return true
	default:
		return false
	}
}

// Changed notes a committed resource change; connect it to the resource
// store's OnChange. It never blocks and never calls back into the store:
// Run starts, wakes or stops the affected loop.
func (m *Manager) Changed(c resource.Change) {
	if c.Type != model.ResourceRouter {
		return
	}
	m.loopsMu.Lock()
	m.changed[c.ID] = true
	m.loopsMu.Unlock()
	select {
	case m.kick <- struct{}{}:
	default:
	}
}

// Run tells the monitor the stored health of every router, keeps one
// observation loop per managed router until ctx ends and then stops them.
// A changed router is observed again at once.
func (m *Manager) Run(ctx context.Context) {
	m.sync(ctx, true)
	for {
		select {
		case <-ctx.Done():
			m.loopsMu.Lock()
			loops := m.loops
			m.loops = map[string]*loop{}
			m.loopsMu.Unlock()
			for _, l := range loops {
				l.cancel()
			}
			for _, l := range loops {
				<-l.done
			}
			return
		case <-m.kick:
			m.sync(ctx, false)
		}
	}
}

// sync makes the running loops match the managed routers.
func (m *Manager) sync(ctx context.Context, initial bool) {
	m.loopsMu.Lock()
	changed := m.changed
	m.changed = map[string]bool{}
	m.loopsMu.Unlock()
	routers, err := m.List(ctx)
	if err != nil {
		if ctx.Err() == nil {
			m.log.Warn("list routers failed", "error", err)
			// Keep the changes for the next attempt.
			m.loopsMu.Lock()
			for id := range changed {
				m.changed[id] = true
			}
			m.loopsMu.Unlock()
		}
		return
	}
	var stop []*loop
	present, names := map[string]bool{}, map[string]bool{}
	m.loopsMu.Lock()
	for _, r := range routers {
		present[r.ID] = true
		if r.Phase == resource.PhaseManaged {
			names[r.Name] = true
		}
		if initial {
			m.feedNative(r)
		}
		l := m.loops[r.ID]
		switch {
		case r.Phase == resource.PhaseManaged && (l == nil || l.exited()):
			if l != nil {
				l.cancel()
			}
			lctx, cancel := context.WithCancel(ctx)
			l = &loop{id: r.ID, name: r.Name, cancel: cancel, wake: make(chan struct{}, 1), done: make(chan struct{})}
			m.loops[r.ID] = l
			go m.loop(lctx, l)
		case r.Phase == resource.PhaseManaged && changed[r.ID]:
			select {
			case l.wake <- struct{}{}:
			default:
			}
		case r.Phase != resource.PhaseManaged && l != nil:
			delete(m.loops, r.ID)
			stop = append(stop, l)
		}
	}
	for id, l := range m.loops {
		if !present[id] {
			delete(m.loops, id)
			stop = append(stop, l)
		}
	}
	m.loopsMu.Unlock()
	for _, l := range stop {
		l.cancel()
		<-l.done
		// A new incarnation may already use the name.
		if m.native != nil && !names[l.name] {
			m.native.Forget(l.name)
		}
	}
}

// loop observes one router every interval of its current document until
// ctx ends or the router is no longer managed. A failed attempt is stored
// like any other; the loop itself only logs what it could not store.
func (m *Manager) loop(ctx context.Context, l *loop) {
	defer close(l.done)
	for ctx.Err() == nil {
		wait := retryWait
		r, err := m.Get(ctx, "@"+l.id)
		switch {
		case err == nil && r.Phase != resource.PhaseManaged:
			return
		case err == nil:
			wait = r.Config.Interval.D()
			if _, err := m.Observe(ctx, "@"+l.id); err != nil && ctx.Err() == nil && !errors.Is(err, ErrConflict) {
				m.log.Warn("router observation not stored", "router", l.name, "error", err)
			}
			// Deadlines pass even when nothing could be observed.
			m.settle(ctx, l.id)
			m.refreshNative(ctx, l.id)
			if m.observing(ctx, l.id) {
				wait = min(wait, observingWait)
			}
		case errors.Is(err, ErrNotFound):
			return
		case ctx.Err() == nil:
			m.log.Warn("read router failed", "router", l.name, "error", err)
		}
		ready := make(chan struct{}, 1)
		timer := m.clk.AfterFunc(wait, func() { ready <- struct{}{} })
		select {
		case <-ctx.Done():
			timer.Stop()
			return
		case <-ready:
		case <-l.wake:
			timer.Stop()
		}
	}
}

// refreshNative reports the stored health of a router to the monitor, under
// the observe lock so that it cannot interleave with a phase change: a
// router read as managed must not report health after it was retired and
// forgotten.
func (m *Manager) refreshNative(ctx context.Context, id string) {
	locks := m.locksFor(id)
	locks.observe.Lock()
	defer locks.observe.Unlock()
	if latest, err := m.Get(ctx, "@"+id); err == nil {
		m.feedNative(latest)
	}
}

// wake makes the loop of a router observe at once, for example after a
// reboot was submitted.
func (m *Manager) wake(id string) {
	m.loopsMu.Lock()
	defer m.loopsMu.Unlock()
	if l := m.loops[id]; l != nil {
		select {
		case l.wake <- struct{}{}:
		default:
		}
	}
}
