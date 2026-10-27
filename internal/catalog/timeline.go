package catalog

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"strings"
	"sync"
	"time"

	"github.com/ACaccel/lcm/internal/signal"
)

// Timeline entry kinds written by the catalog. Entries that come from
// signals use the signal kind (such as check.changed).
const (
	KindConfig     = "config.changed"
	KindLifecycle  = string(signal.KindLifecycle)
	KindDeployment = "deployment"
	KindAction     = "action"
	KindBackup     = "backup"
	KindJob        = "job"
)

// Page sizes of timeline queries.
const (
	DefaultTimelineLimit = 100
	MaxTimelineLimit     = 1000
)

// Entry is one line of a service's timeline.
type Entry struct {
	ID      int64
	Service string
	Time    time.Time
	Kind    string
	Summary string
	Data    map[string]any
	// Ref links the entry to its cause: a signal id, run id or deployment id.
	Ref string
}

func insertTimeline(ctx context.Context, tx *sql.Tx, e Entry) error {
	data := e.Data
	if data == nil {
		data = map[string]any{}
	}
	blob, err := json.Marshal(data)
	if err != nil {
		return fmt.Errorf("catalog: encode timeline data: %w", err)
	}
	_, err = tx.ExecContext(ctx,
		`INSERT INTO timeline (service, time, kind, summary, data, ref) VALUES (?, ?, ?, ?, ?, ?)`,
		e.Service, e.Time.UnixMilli(), e.Kind, e.Summary, string(blob), e.Ref)
	if err != nil {
		return fmt.Errorf("catalog: insert timeline: %w", err)
	}
	return nil
}

// AppendTimeline adds an entry; a zero Time means now.
func (c *Catalog) AppendTimeline(ctx context.Context, e Entry) error {
	if e.Service == "" || e.Kind == "" {
		return fmt.Errorf("catalog: timeline entry needs a service and a kind")
	}
	if e.Time.IsZero() {
		e.Time = c.now()
	}
	err := c.db.Write(ctx, func(tx *sql.Tx) error { return insertTimeline(ctx, tx, e) })
	if err != nil {
		return err
	}
	c.timelineAdded(e)
	return nil
}

// TimelineQuery selects timeline entries. All filters are optional.
type TimelineQuery struct {
	Service string
	// From is inclusive and To exclusive.
	From, To time.Time
	Kinds    []string
	Limit    int
	// Before, when positive, returns only entries with a smaller id: pass
	// the previous page's Next to continue.
	Before int64
}

// TimelinePage is one page of entries, newest first.
type TimelinePage struct {
	Entries []Entry
	// Next is the cursor for the following page; zero when there is none.
	Next int64
}

// Timeline returns entries in reverse insertion order, which is the order
// events reached the catalog.
func (c *Catalog) Timeline(ctx context.Context, q TimelineQuery) (TimelinePage, error) {
	limit := q.Limit
	switch {
	case limit <= 0:
		limit = DefaultTimelineLimit
	case limit > MaxTimelineLimit:
		limit = MaxTimelineLimit
	}
	var where []string
	var args []any
	if q.Service != "" {
		where = append(where, "service = ?")
		args = append(args, q.Service)
	}
	if !q.From.IsZero() {
		where = append(where, "time >= ?")
		args = append(args, q.From.UnixMilli())
	}
	if !q.To.IsZero() {
		where = append(where, "time < ?")
		args = append(args, q.To.UnixMilli())
	}
	if len(q.Kinds) > 0 {
		where = append(where, "kind IN ("+strings.TrimSuffix(strings.Repeat("?,", len(q.Kinds)), ",")+")")
		for _, k := range q.Kinds {
			args = append(args, k)
		}
	}
	if q.Before > 0 {
		where = append(where, "id < ?")
		args = append(args, q.Before)
	}
	query := `SELECT id, service, time, kind, summary, data, ref FROM timeline`
	if len(where) > 0 {
		query += ` WHERE ` + strings.Join(where, " AND ")
	}
	// One extra row tells whether another page exists.
	query += ` ORDER BY id DESC LIMIT ?`
	args = append(args, limit+1)

	rows, err := c.db.Reader().QueryContext(ctx, query, args...)
	if err != nil {
		return TimelinePage{}, fmt.Errorf("catalog: query timeline: %w", err)
	}
	defer func() { _ = rows.Close() }()
	page := TimelinePage{Entries: []Entry{}}
	for rows.Next() {
		var (
			e    Entry
			ms   int64
			data string
		)
		if err := rows.Scan(&e.ID, &e.Service, &ms, &e.Kind, &e.Summary, &data, &e.Ref); err != nil {
			return TimelinePage{}, fmt.Errorf("catalog: scan timeline: %w", err)
		}
		e.Time = time.UnixMilli(ms).UTC()
		if err := json.Unmarshal([]byte(data), &e.Data); err != nil {
			return TimelinePage{}, fmt.Errorf("catalog: decode timeline entry %d: %w", e.ID, err)
		}
		page.Entries = append(page.Entries, e)
	}
	if err := rows.Err(); err != nil {
		return TimelinePage{}, fmt.Errorf("catalog: query timeline: %w", err)
	}
	if len(page.Entries) > limit {
		page.Entries = page.Entries[:limit]
		page.Next = page.Entries[limit-1].ID
	}
	return page, nil
}

// PruneTimeline deletes entries older than retention and returns how many
// were removed. A non-positive retention keeps everything.
func (c *Catalog) PruneTimeline(ctx context.Context, retention time.Duration) (int64, error) {
	if retention <= 0 {
		return 0, nil
	}
	cutoff := c.now().Add(-retention).UnixMilli()
	var n int64
	err := c.db.Write(ctx, func(tx *sql.Tx) error {
		res, err := tx.ExecContext(ctx, `DELETE FROM timeline WHERE time < ?`, cutoff)
		if err != nil {
			return err
		}
		n, err = res.RowsAffected()
		return err
	})
	if err != nil {
		return 0, fmt.Errorf("catalog: prune timeline: %w", err)
	}
	return n, nil
}

// Follower copies signals that carry a service name onto the timeline.
type Follower struct {
	sub  *signal.Subscription
	done chan struct{}
	once sync.Once
}

// FollowSignals subscribes to bus and appends a timeline entry for every
// signal with a service. Lifecycle signals are skipped because state
// changes write their own entry in the transaction that makes them.
// Delivery is lossy by design (see package signal); the timeline is a log
// for people, not a source of truth.
func (c *Catalog) FollowSignals(ctx context.Context, bus *signal.Bus) *Follower {
	f := &Follower{sub: bus.Subscribe("catalog-timeline", 256), done: make(chan struct{})}
	go func() {
		defer close(f.done)
		for sig := range f.sub.C {
			if sig.Service == "" || sig.Kind == signal.KindLifecycle {
				continue
			}
			e := entryFromSignal(sig)
			// Keep writing after ctx ends so that Close drains the buffer.
			if err := c.AppendTimeline(context.WithoutCancel(ctx), e); err != nil {
				c.log.Warn("append timeline entry from signal failed", "service", sig.Service, "kind", sig.Kind, "error", err)
			}
		}
	}()
	return f
}

// Close unsubscribes and waits until every signal received so far has been
// written. It is safe to call more than once.
func (f *Follower) Close() {
	f.once.Do(f.sub.Close)
	<-f.done
}

func entryFromSignal(sig signal.Signal) Entry {
	data := map[string]any{"source": sig.Source}
	if sig.Severity != "" {
		data["severity"] = sig.Severity
	}
	if len(sig.Labels) > 0 {
		data["labels"] = sig.Labels
	}
	if len(sig.Data) > 0 {
		data["data"] = sig.Data
	}
	return Entry{
		Service: sig.Service, Time: sig.Time, Kind: string(sig.Kind),
		Summary: summarize(sig), Data: data, Ref: sig.ID,
	}
}

func summarize(sig signal.Signal) string {
	str := func(k string) string {
		v, _ := sig.Data[k].(string)
		return v
	}
	arrow := func(prev, cur string) string {
		if prev == "" {
			return cur
		}
		return prev + " -> " + cur
	}
	switch sig.Kind {
	case signal.KindCheck:
		s := fmt.Sprintf("check %s: %s", str("check"), arrow(str("previous"), str("status")))
		if m := str("message"); m != "" {
			s += " (" + m + ")"
		}
		return s
	case signal.KindHealth:
		return "health: " + arrow(str("previous"), str("health"))
	case signal.KindAlert:
		return fmt.Sprintf("alert %s: %s", str("alert"), str("status"))
	}
	return string(sig.Kind)
}
