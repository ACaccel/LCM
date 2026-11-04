package app

import (
	"context"
	"log/slog"

	"github.com/ACaccel/lcm/internal/api"
	"github.com/ACaccel/lcm/internal/events"
	"github.com/ACaccel/lcm/internal/machine"
	"github.com/ACaccel/lcm/internal/model"
	"github.com/ACaccel/lcm/internal/resource"
	"github.com/ACaccel/lcm/internal/router"
	"github.com/ACaccel/lcm/internal/store"
)

// stateTypes lists the resource.Type of every API registration, for
// components that only need state (the resource expression variable).
func stateTypes(regs []api.ResourceType) []resource.Type {
	out := make([]resource.Type, 0, len(regs))
	for _, r := range regs {
		out = append(out, r.Type)
	}
	return out
}

// publishObservation announces a committed machine or router event on the event
// stream under its resource's name. The name is read per event because the
// event only carries the immutable ID; an event of a resource that cannot
// be read any more is still announced by ID.
func publishObservation(hub *events.Hub, res *resource.Store, e resource.Event) {
	name := ""
	if rec, err := res.Get(context.Background(), "@"+e.ResourceID); err == nil {
		name = rec.Name
	}
	hub.Observation(name, e.ResourceID, e)
}

// machineObservations serves the API's observation routes from the machine
// manager.
type machineObservations struct{ m *machine.Manager }

var _ api.Observations = machineObservations{}

func (o machineObservations) Observe(ctx context.Context, ref string) error {
	_, err := o.m.Observe(ctx, ref)
	return err
}

func (o machineObservations) History(ctx context.Context, ref string, after int64, limit int, minute bool) ([]api.ObservedItem, error) {
	rec, err := o.m.Get(ctx, ref)
	if err != nil {
		return nil, err
	}
	snaps, err := o.m.HistoryResolution(ctx, ref, after, limit, minute)
	if err != nil {
		return nil, err
	}
	out := make([]api.ObservedItem, 0, len(snaps))
	for _, s := range snaps {
		out = append(out, api.ObservedItem{ID: s.ID, Body: api.Snapshot{
			ID: s.ID, ResourceID: rec.ID, Type: model.ResourceMachine, ObservedAt: s.ObservedAt, Machine: s,
		}})
	}
	return out, nil
}

func (o machineObservations) Events(ctx context.Context, ref string, after int64, limit int) ([]api.ObservedItem, error) {
	evs, err := o.m.Events(ctx, ref, after, limit)
	if err != nil {
		return nil, err
	}
	out := make([]api.ObservedItem, 0, len(evs))
	for _, e := range evs {
		out = append(out, api.ObservedItem{ID: e.ID, Body: e})
	}
	return out, nil
}

// machineOperations serves the API's durable operation routes from the
// machine manager, which refuses writes that are not direct administrator
// requests.
type machineOperations struct{ m *machine.Manager }

var _ api.Operations = machineOperations{}

func toAPIOperation(op machine.Operation) api.Operation {
	return api.Operation{ID: op.ID, Unresolved: op.Unresolved, Body: op}
}

func (o machineOperations) Capabilities(ctx context.Context, ref string) ([]api.Capability, error) {
	caps, err := o.m.Capabilities(ctx, ref)
	if err != nil {
		return nil, err
	}
	out := make([]api.Capability, 0, len(caps))
	for _, c := range caps {
		out = append(out, api.Capability{Action: c.Action, Available: c.Available, Reason: c.Reason})
	}
	return out, nil
}

func (o machineOperations) List(ctx context.Context, ref string) ([]api.Operation, error) {
	ops, err := o.m.Operations(ctx, ref)
	if err != nil {
		return nil, err
	}
	out := make([]api.Operation, 0, len(ops))
	for _, op := range ops {
		out = append(out, toAPIOperation(op))
	}
	return out, nil
}

func (o machineOperations) Get(ctx context.Context, ref, id string) (api.Operation, error) {
	op, err := o.m.Operation(ctx, ref, id)
	return toAPIOperation(op), err
}

func (o machineOperations) Submit(ctx context.Context, ref, action, key string, expected int) (api.Operation, error) {
	op, err := o.m.Submit(ctx, ref, action, key, expected)
	return toAPIOperation(op), err
}

func (o machineOperations) Resolve(ctx context.Context, ref, id, reason string, expected int) (api.Operation, error) {
	op, err := o.m.Resolve(ctx, ref, id, reason, expected)
	return toAPIOperation(op), err
}

// routerObservations serves the API's observation routes from the router
// manager.
type routerObservations struct{ m *router.Manager }

var _ api.Observations = routerObservations{}

func (o routerObservations) Observe(ctx context.Context, ref string) error {
	_, err := o.m.Observe(ctx, ref)
	return err
}

func (o routerObservations) History(ctx context.Context, ref string, after int64, limit int, minute bool) ([]api.ObservedItem, error) {
	rec, err := o.m.Get(ctx, ref)
	if err != nil {
		return nil, err
	}
	snaps, err := o.m.History(ctx, ref, after, limit, minute)
	if err != nil {
		return nil, err
	}
	out := make([]api.ObservedItem, 0, len(snaps))
	for _, s := range snaps {
		out = append(out, api.ObservedItem{ID: s.ID, Body: api.Snapshot{
			ID: s.ID, ResourceID: rec.ID, Type: model.ResourceRouter, ObservedAt: s.ObservedAt, Router: s,
		}})
	}
	return out, nil
}

func (o routerObservations) Events(ctx context.Context, ref string, after int64, limit int) ([]api.ObservedItem, error) {
	evs, err := o.m.Events(ctx, ref, after, limit)
	if err != nil {
		return nil, err
	}
	out := make([]api.ObservedItem, 0, len(evs))
	for _, e := range evs {
		out = append(out, api.ObservedItem{ID: e.ID, Body: e})
	}
	return out, nil
}

// routerOperations serves the API's durable operation routes from the
// router manager, which refuses writes that are not direct administrator
// requests.
type routerOperations struct{ m *router.Manager }

var _ api.Operations = routerOperations{}

func toRouterOperation(op router.Operation) api.Operation {
	return api.Operation{ID: op.ID, Unresolved: op.Unresolved, Body: op}
}

func (o routerOperations) Capabilities(ctx context.Context, ref string) ([]api.Capability, error) {
	caps, err := o.m.Capabilities(ctx, ref)
	if err != nil {
		return nil, err
	}
	out := make([]api.Capability, 0, len(caps))
	for _, c := range caps {
		out = append(out, api.Capability{Action: c.Action, Available: c.Available, Reason: c.Reason})
	}
	return out, nil
}

func (o routerOperations) List(ctx context.Context, ref string) ([]api.Operation, error) {
	ops, err := o.m.Operations(ctx, ref)
	if err != nil {
		return nil, err
	}
	out := make([]api.Operation, 0, len(ops))
	for _, op := range ops {
		out = append(out, toRouterOperation(op))
	}
	return out, nil
}

func (o routerOperations) Get(ctx context.Context, ref, id string) (api.Operation, error) {
	op, err := o.m.Operation(ctx, ref, id)
	return toRouterOperation(op), err
}

func (o routerOperations) Submit(ctx context.Context, ref, action, key string, expected int) (api.Operation, error) {
	op, err := o.m.Submit(ctx, ref, action, key, expected)
	return toRouterOperation(op), err
}

func (o routerOperations) Resolve(ctx context.Context, ref, id, reason string, expected int) (api.Operation, error) {
	op, err := o.m.Resolve(ctx, ref, id, reason, expected)
	return toRouterOperation(op), err
}

// logMigrationWarnings repeats, at every start, what the resource migration
// could not convert by itself (such as a policy expression built from
// strings), so that an operator sees what needs fixing until it is fixed.
// A warning is repeated only while its policy or module is still at the
// version it was recorded for: a newer version is the operator's fix (or
// decision), and a document that no longer exists needs nothing. The
// stored messages name documents and expressions, never secret values.
func logMigrationWarnings(ctx context.Context, db *store.DB, log *slog.Logger) {
	rows, err := db.Reader().QueryContext(ctx, `SELECT w.migration, w.kind, w.name, w.version, w.message FROM migration_warnings w
		WHERE (w.kind = 'Policy' AND w.version = (SELECT MAX(p.version) FROM policy_versions p WHERE p.name = w.name))
		   OR (w.kind = 'Module' AND w.version = (SELECT MAX(m.version) FROM module_versions m WHERE m.name = w.name))
		ORDER BY w.id`)
	if err != nil {
		log.Warn("cannot read migration warnings", "error", err)
		return
	}
	defer func() { _ = rows.Close() }()
	for rows.Next() {
		var (
			migration, version int
			kind, name, msg    string
		)
		if err := rows.Scan(&migration, &kind, &name, &version, &msg); err != nil {
			log.Warn("cannot read migration warnings", "error", err)
			return
		}
		log.Warn("converted document needs review", "migration", migration, "kind", kind, "name", name, "version", version, "problem", msg)
	}
	if err := rows.Err(); err != nil {
		log.Warn("cannot read migration warnings", "error", err)
	}
}
