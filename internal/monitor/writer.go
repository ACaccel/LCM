package monitor

import (
	"context"
	"database/sql"
	"errors"
	"log/slog"
	"sync"

	"github.com/ACaccel/lcm/internal/store"
)

// maxBatch bounds the entries committed in one transaction.
const maxBatch = 256

var errWriterClosed = errors.New("monitor: writer closed")

// record is one storage write.
type record interface {
	exec(ctx context.Context, st *stmts) error
}

// stmts prepares each statement once per batch transaction.
type stmts struct {
	tx *sql.Tx
	m  map[string]*sql.Stmt
}

func (s *stmts) exec(ctx context.Context, query string, args ...any) error {
	st, ok := s.m[query]
	if !ok {
		var err error
		if st, err = s.tx.PrepareContext(ctx, query); err != nil {
			return err
		}
		s.m[query] = st
	}
	_, err := st.ExecContext(ctx, args...)
	return err
}

func (s *stmts) close() {
	for _, st := range s.m {
		_ = st.Close()
	}
}

type writeEntry struct {
	recs []record
	// done runs after the commit, in submission order, on the writer
	// goroutine. It must not block.
	done func(error)
}

// writer commits check results and state on one goroutine, grouping
// whatever has accumulated into a single transaction. Many checks finishing
// within the same moment therefore cost one commit, not one each.
type writer struct {
	db  *store.DB
	log *slog.Logger

	mu      sync.Mutex
	queue   []writeEntry
	wake    chan struct{}
	closing bool
	stopped bool
	done    chan struct{}
}

func newWriter(db *store.DB, log *slog.Logger) *writer {
	w := &writer{db: db, log: log, wake: make(chan struct{}, 1), done: make(chan struct{})}
	go w.loop()
	return w
}

// submit queues records without blocking. After close has drained the
// queue, done is called with errWriterClosed instead.
func (w *writer) submit(done func(error), recs ...record) {
	w.mu.Lock()
	if w.stopped {
		w.mu.Unlock()
		if done != nil {
			done(errWriterClosed)
		}
		return
	}
	w.queue = append(w.queue, writeEntry{recs: recs, done: done})
	w.mu.Unlock()
	select {
	case w.wake <- struct{}{}:
	default:
	}
}

// flush blocks until everything submitted before the call is committed.
func (w *writer) flush(ctx context.Context) error {
	ch := make(chan error, 1)
	w.submit(func(err error) { ch <- err })
	select {
	case err := <-ch:
		return err
	case <-ctx.Done():
		return ctx.Err()
	}
}

func (w *writer) loop() {
	defer close(w.done)
	for {
		w.mu.Lock()
		n := min(len(w.queue), maxBatch)
		batch := w.queue[:n:n]
		w.queue = w.queue[n:]
		if n == 0 && w.closing {
			w.stopped = true
			w.mu.Unlock()
			return
		}
		w.mu.Unlock()
		if n == 0 {
			<-w.wake
			continue
		}
		w.commit(batch)
	}
}

func (w *writer) commit(batch []writeEntry) {
	// Results must not be lost because a caller's context ended; the
	// store's busy timeout bounds the wait instead.
	ctx := context.Background()
	err := w.db.Write(ctx, func(tx *sql.Tx) error {
		st := &stmts{tx: tx, m: map[string]*sql.Stmt{}}
		defer st.close()
		for _, e := range batch {
			for _, r := range e.recs {
				if err := r.exec(ctx, st); err != nil {
					return err
				}
			}
		}
		return nil
	})
	if err == nil || len(batch) == 1 {
		if err != nil {
			w.log.Error("monitor write failed", "error", err)
		}
		for _, e := range batch {
			if e.done != nil {
				e.done(err)
			}
		}
		return
	}
	// Retry one by one so a single bad record fails only its own entry.
	for _, e := range batch {
		w.commit([]writeEntry{e})
	}
}

// close drains the queue, including entries queued by completion callbacks,
// then stops the goroutine.
func (w *writer) close() {
	w.mu.Lock()
	w.closing = true
	w.mu.Unlock()
	select {
	case w.wake <- struct{}{}:
	default:
	}
	<-w.done
}
