package resource

import (
	"context"
	"time"
)

// Type is the runtime side of one resource type: its status and actions.
// The service, machine and router implementations live with the components
// that control them and are connected in internal/app.
type Type interface {
	// Name is the resource type, such as model.ResourceService.
	Name() string
	// Status reports the type-specific state of a resource.
	Status(ctx context.Context, rec Record) (Status, error)
	// Actions reports what can be done with a resource now.
	Actions(ctx context.Context, rec Record) []Action
	// ActionCatalog lists every action and phase change of the type in the
	// order Actions reports them, without availability, for clients that
	// describe the type rather than one resource.
	ActionCatalog() []Action
	// ChangePhase moves a resource to another phase. It may act first
	// (retiring a running service stops it) and then calls Store.SetPhase.
	ChangePhase(ctx context.Context, rec Record, to string, expected int, reason string) error
}

// StatusBatch is implemented by types that read the status of many
// resources in one query, for list pages.
type StatusBatch interface {
	// Statuses returns the status of each record, in the order given.
	Statuses(ctx context.Context, recs []Record) ([]Status, error)
}

// Statuses returns the status of each record of type t, in order: in one
// call when t implements StatusBatch, otherwise one Status call per record.
func Statuses(ctx context.Context, t Type, recs []Record) ([]Status, error) {
	if b, ok := t.(StatusBatch); ok {
		return b.Statuses(ctx, recs)
	}
	out := make([]Status, len(recs))
	for i, rec := range recs {
		st, err := t.Status(ctx, rec)
		if err != nil {
			return nil, err
		}
		out[i] = st
	}
	return out, nil
}

// Status is the type-specific state of a resource. Machines and routers
// report StateOnline, StateOffline or StateUnknown; services report their
// lifecycle state.
type Status struct {
	State      string
	StateSince time.Time
	// Detail is type-specific data encoded as JSON for clients.
	Detail any
}

// Observed states of machines and routers. Offline needs fresh evidence of
// unreachability: a missing observation is unknown, not offline.
const (
	StateOnline  = "online"
	StateOffline = "offline"
	StateUnknown = "unknown"
)

// Action describes one thing a client can do with a resource.
type Action struct {
	Name      string
	Available bool
	// Reason explains why the action is not available.
	Reason string
	Risk   string
	// Policy reports whether policies may run the action.
	Policy bool
	Kind   string
}

// Action risks, from harmless to irreversible.
const (
	RiskRead        = "read"
	RiskReversible  = "reversible"
	RiskDisruptive  = "disruptive"
	RiskDestructive = "destructive"
)

// Action kinds: a synchronous lifecycle action, a durable operation with an
// observed outcome, or a phase change.
const (
	KindAction    = "action"
	KindOperation = "operation"
	KindPhase     = "phase"
)

// OperationRequest is a direct administrator request for a durable
// operation. Confirm must repeat the resource's name, so that a request
// typed for one resource cannot act on another; Version is the document
// version the request was reviewed against (If-Match). The same Key with
// the same Action and Version returns the stored operation instead of
// sending a second request.
type OperationRequest struct {
	Action  string
	Key     string
	Reason  string
	Confirm string
	Version int
}
