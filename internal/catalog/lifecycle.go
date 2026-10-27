package catalog

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"

	"github.com/ACaccel/lcm/internal/signal"
)

// TransitionInfo explains a state change for the timeline and the signal.
type TransitionInfo struct {
	// Action is the lifecycle action behind the change, such as deploy.
	Action string
	// Reason is free text, for example the runtime status found by Reconcile.
	Reason string
	// Ref links the entry to a run or deployment.
	Ref string
}

// Transition moves a service from one state to another. from must equal the
// current state, which makes the change a compare-and-set: callers that
// hold the service lock cannot overwrite a state they did not see. A
// transition to the same state does nothing. The change, its timeline entry
// and (after commit) its KindLifecycle signal are produced together.
func (c *Catalog) Transition(ctx context.Context, name string, from, to State, info TransitionInfo) error {
	if !from.Valid() || !to.Valid() {
		return fmt.Errorf("catalog: unknown state in transition %q -> %q", from, to)
	}
	if from == to {
		return nil
	}
	if !CanTransition(from, to) {
		return fmt.Errorf("%w: %s -> %s", ErrInvalidTransition, from, to)
	}
	now := c.now()
	var labels map[string]string
	err := c.db.Write(ctx, func(tx *sql.Tx) error {
		var cur, rawLabels string
		err := tx.QueryRowContext(ctx,
			`SELECT s.state, COALESCE(json_extract(v.document, '$.metadata.labels'), '{}')
			 FROM services s JOIN service_versions v ON v.name = s.name AND v.version = s.version
			 WHERE s.name = ?`, name).Scan(&cur, &rawLabels)
		switch {
		case errors.Is(err, sql.ErrNoRows):
			return fmt.Errorf("%w: service %s", ErrNotFound, name)
		case err != nil:
			return err
		case State(cur) != from:
			return fmt.Errorf("%w: %s is %s, expected %s", ErrStateChanged, name, cur, from)
		}
		if err := json.Unmarshal([]byte(rawLabels), &labels); err != nil {
			return fmt.Errorf("decode labels: %w", err)
		}
		if _, err := tx.ExecContext(ctx,
			`UPDATE services SET state = ?, state_since = ? WHERE name = ?`, string(to), now.UnixMilli(), name); err != nil {
			return err
		}
		summary := fmt.Sprintf("%s -> %s", from, to)
		if info.Action != "" {
			summary += " (" + info.Action + ")"
		}
		return insertTimeline(ctx, tx, Entry{
			Service: name, Time: now, Kind: KindLifecycle, Summary: summary, Ref: info.Ref,
			Data: map[string]any{"state": string(to), "previous": string(from), "action": info.Action, "reason": info.Reason},
		})
	})
	if err != nil {
		return err
	}
	if c.bus != nil {
		sev := signal.SeverityInfo
		if to == StateFailed {
			sev = signal.SeverityWarning
		}
		c.bus.Publish(signal.Signal{
			Kind: signal.KindLifecycle, Time: now, Service: name, Source: "catalog", Severity: sev, Labels: labels,
			Data: map[string]any{"state": string(to), "previous": string(from), "action": info.Action},
		})
	}
	return nil
}
