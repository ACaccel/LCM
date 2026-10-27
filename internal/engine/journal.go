package engine

import (
	"context"
	"database/sql"
	"errors"
	"log/slog"
	"sync"

	"github.com/ACaccel/lcm/internal/store"
)

// maxBatch bounds the entries committed in one transaction.
const maxBatch = 512

var errJournalClosed = errors.New("engine: checkpoint journal closed")

// record is one checkpoint write.
type record interface {
	exec(ctx context.Context, st *stmts) error
}

// stmts prepares each statement once per batch transaction; parsing SQL
// dominates the cost of small writes otherwise.
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

type journalEntry struct {
	recs []record
	// done runs after the commit with its outcome, in submission order,
	// on the journal goroutine. It must not block.
	done func(error)
}

// journal serializes checkpoint writes on one goroutine and commits
// whatever has accumulated in a single transaction (group commit), so the
// write rate stays bounded under load while each submitter still learns
// when its records are durable. Callers schedule follow-up work (such as
// starting downstream nodes) in done, which guarantees that a node's
// completion is stored before anything depending on it runs.
type journal struct {
	db  *store.DB
	log *slog.Logger

	mu      sync.Mutex
	queue   []journalEntry
	wake    chan struct{}
	closing bool
	stopped bool
	done    chan struct{}
}

func newJournal(db *store.DB, log *slog.Logger) *journal {
	j := &journal{db: db, log: log, wake: make(chan struct{}, 1), done: make(chan struct{})}
	go j.loop()
	return j
}

// submit queues records without blocking. After close has drained the
// queue, submissions are dropped and logged.
func (j *journal) submit(done func(error), recs ...record) {
	j.mu.Lock()
	if j.stopped {
		j.mu.Unlock()
		j.log.Error("checkpoint dropped after shutdown", "records", len(recs))
		return
	}
	j.queue = append(j.queue, journalEntry{recs: recs, done: done})
	j.mu.Unlock()
	select {
	case j.wake <- struct{}{}:
	default:
	}
}

// write blocks until recs are committed or ctx ends. The records may still
// be committed after ctx ends.
func (j *journal) write(ctx context.Context, recs ...record) error {
	ch := make(chan error, 1)
	j.submit(func(err error) { ch <- err }, recs...)
	select {
	case err := <-ch:
		return err
	case <-ctx.Done():
		return ctx.Err()
	case <-j.done:
		select {
		case err := <-ch:
			return err
		default:
			return errJournalClosed
		}
	}
}

func (j *journal) loop() {
	defer close(j.done)
	for {
		j.mu.Lock()
		n := min(len(j.queue), maxBatch)
		batch := j.queue[:n:n]
		j.queue = j.queue[n:]
		if n == 0 && j.closing {
			j.stopped = true
			j.mu.Unlock()
			return
		}
		j.mu.Unlock()
		if n == 0 {
			<-j.wake
			continue
		}
		j.commit(batch)
	}
}

func (j *journal) commit(batch []journalEntry) {
	// Checkpoints must not be lost because a caller's context ended, so
	// they use a background context; the store's busy timeout bounds them.
	ctx := context.Background()
	err := j.db.Write(ctx, func(tx *sql.Tx) error {
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
			j.log.Error("checkpoint failed", "error", err)
		}
		for _, e := range batch {
			if e.done != nil {
				e.done(err)
			}
		}
		return
	}
	// Retry entries one by one so that a single bad record fails only its
	// own entry.
	for _, e := range batch {
		j.commit([]journalEntry{e})
	}
}

// close drains every queued entry, including entries queued by completion
// callbacks during the drain, then stops the goroutine.
func (j *journal) close() {
	j.mu.Lock()
	j.closing = true
	j.mu.Unlock()
	select {
	case j.wake <- struct{}{}:
	default:
	}
	<-j.done
}
