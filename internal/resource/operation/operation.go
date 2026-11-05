// Package operation owns the durable mechanics shared by resource
// operations (machine power requests, router reboots), stored in the
// resource_operations table: the stored form, creation under an
// idempotency key, compare-and-swap saves that record a change event (and
// an audit entry, when the change has one) in the same transaction,
// closing abandoned operations and the checks before a manual resolve.
//
// Each resource type keeps what is specific to it: its request and
// receipt, how a request is submitted and what evidence decides the
// outcome.
//
// # States
//
// An operation is stored pending before anything is sent, marked
// submitting before the request may leave LCM, and then observing,
// succeeded, failed or indeterminate. Only one operation per resource is
// unresolved at a time; an indeterminate one stays unresolved until an
// administrator resolves it, which never turns it into a success.
//
// # Abandoned operations
//
// An operation still pending or submitting that no submission is working
// on any more was abandoned (the controller stopped, or recording the
// outcome failed). A pending one was never submitted, so it failed; a
// submitting one may have reached the device or helper, so its outcome is
// indeterminate. Neither is ever sent again. A type that cannot prove
// that a pending request was never submitted closes it as indeterminate.
package operation

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"regexp"
	"strconv"
	"strings"
	"time"

	"github.com/ACaccel/lcm/internal/audit"
	"github.com/ACaccel/lcm/internal/auth"
	"github.com/ACaccel/lcm/internal/clock"
	"github.com/ACaccel/lcm/internal/resource"
	"github.com/ACaccel/lcm/internal/store"
)

// Operation states.
const (
	Pending       = "pending"
	Submitting    = "submitting"
	Observing     = "observing"
	Succeeded     = "succeeded"
	Failed        = "failed"
	Indeterminate = "indeterminate"
)

// ErrStale means the stored operation changed since it was read; the save
// was not applied.
var ErrStale = errors.New("operation: changed concurrently")

// Operation is a durable request against a resource. E is the type's
// status kept as evidence, Q its request and R its receipt. Its JSON form
// is the API's ResourceOperation.
type Operation[E, Q, R any] struct {
	Evidence   *E             `json:"evidence,omitempty"`
	ID         string         `json:"id"`
	ResourceID string         `json:"resourceId"`
	Action     string         `json:"action"`
	State      string         `json:"state"`
	Unresolved bool           `json:"unresolved"`
	Version    int            `json:"version"`
	CreatedAt  time.Time      `json:"createdAt"`
	UpdatedAt  time.Time      `json:"updatedAt"`
	Actor      resource.Actor `json:"actor"`
	// Request is fixed when the operation is stored, before anything is
	// sent.
	Request Q  `json:"request"`
	Receipt *R `json:"receipt"`
	// Reason explains the current state.
	Reason string `json:"reason"`
	// RequestReason is the operator's reason for the request.
	RequestReason string          `json:"requestReason,omitempty"`
	Resolution    string          `json:"resolution,omitempty"`
	ResolvedBy    *resource.Actor `json:"resolvedBy,omitempty"`
	ResolvedAt    *time.Time      `json:"resolvedAt,omitempty"`

	// stored is the row body this value was read as or last saved as; a
	// save applies only while the row still holds it.
	stored string
}

// CloseAbandoned sets the outcome of an operation left pending or
// submitting although no submission is working on it any more; the caller
// makes sure of that. neverSent tells that a pending request certainly
// never left LCM: it failed. Otherwise the request may have reached the
// device or helper and its outcome is indeterminate. It reports whether op
// was abandoned; other states are left alone.
func (op *Operation[E, Q, R]) CloseAbandoned(neverSent bool) bool {
	switch {
	case op.State == Pending && neverSent:
		op.State, op.Unresolved, op.Reason = Failed, false, resource.NeverSent
	case op.State == Pending || op.State == Submitting:
		op.State, op.Reason = Indeterminate, resource.InterruptedBeforeOutcome
	default:
		return false
	}
	return true
}

// MarkResolved records a manual resolution; the state stays as it was.
func (op *Operation[E, Q, R]) MarkResolved(by resource.Actor, reason string, at time.Time) {
	op.Unresolved, op.Resolution, op.ResolvedBy, op.ResolvedAt = false, reason, &by, &at
}

var keyPattern = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9_.:-]{0,127}$`)

// ValidKey reports whether key is a bounded idempotency key.
func ValidKey(key string) bool { return keyPattern.MatchString(key) }

// ValidReason reports whether v is a usable operator reason.
func ValidReason(v string) bool {
	return strings.TrimSpace(v) != "" && len(v) <= 1000 && !strings.ContainsAny(v, "\x00\r")
}

// RequestHash binds an idempotency key to the resource, action and
// document version it was first used with.
func RequestHash(resourceID, action string, version int) string {
	sum := sha256.Sum256([]byte(resourceID + "\x00" + action + "\x00" + strconv.Itoa(version)))
	return hex.EncodeToString(sum[:])
}

// RequireDirect refuses everything but a direct administrator request
// (auth.WithDirect) by a user, a token or the local CLI, returning
// forbidden; policy runs never carry the mark.
func RequireDirect(ctx context.Context, forbidden error) (auth.Actor, error) {
	a, ok := auth.ActorFrom(ctx)
	if !ok || !a.Kind.Valid() || a.ID == "" {
		return auth.Actor{}, audit.ErrNoActor
	}
	if !auth.IsDirect(ctx) || (a.Kind != auth.KindUser && a.Kind != auth.KindToken && a != auth.CLI) {
		return auth.Actor{}, forbidden
	}
	return a, nil
}

// Errors are the domain errors of a resource type, so that messages name
// the type. They wrap the shared resource errors (resource.TypeError),
// which the API maps for every type.
type Errors struct {
	NotFound     error
	Conflict     error
	Precondition func(reason string) error
}

// Config supplies the dependencies of a Store.
type Config struct {
	DB    *store.DB
	Audit *audit.Log
	Clock clock.Clock
	// Type is the resource type; it prefixes event kinds and audit
	// actions, and limits queries over all resources to that type.
	Type   string
	Errors Errors
	// Severity maps an operation state to the severity of its change
	// event.
	Severity func(state string) string
	// Publish, when set, receives committed events.
	Publish func([]resource.Event)
}

// Store reads and writes the operations of one resource type.
type Store[E, Q, R any] struct{ c Config }

// New returns a Store.
func New[E, Q, R any](c Config) (*Store[E, Q, R], error) {
	if c.DB == nil || c.Audit == nil || c.Type == "" || c.Errors.NotFound == nil || c.Errors.Conflict == nil || c.Errors.Precondition == nil {
		return nil, errors.New("operation: DB, Audit, Type and Errors are required")
	}
	if c.Clock == nil {
		c.Clock = clock.Real()
	}
	if c.Severity == nil {
		c.Severity = func(string) string { return "warning" }
	}
	return &Store[E, Q, R]{c: c}, nil
}

// Start returns a pending operation with a new ID; the caller fills in the
// request and stores it with InsertTx.
func (s *Store[E, Q, R]) Start(resourceID, action string, version int, by resource.Actor, reason string) Operation[E, Q, R] {
	now := s.c.Clock.Now().UTC()
	return Operation[E, Q, R]{
		ID: store.NewID(), ResourceID: resourceID, Action: action, State: Pending, Unresolved: true, Version: version,
		CreatedAt: now, UpdatedAt: now, Actor: by, RequestReason: reason,
	}
}

// Replay returns the operation stored for resourceID under key, if any.
// A key first used with another request hash is a conflict.
func (s *Store[E, Q, R]) Replay(ctx context.Context, resourceID, key, hash string) (Operation[E, Q, R], bool, error) {
	var body, stored string
	err := s.c.DB.Reader().QueryRowContext(ctx, `SELECT body, request_hash FROM resource_operations WHERE resource_id=? AND idempotency_key=?`, resourceID, key).Scan(&body, &stored)
	if errors.Is(err, sql.ErrNoRows) {
		return Operation[E, Q, R]{}, false, nil
	}
	if err != nil {
		return Operation[E, Q, R]{}, false, err
	}
	if stored != hash {
		return Operation[E, Q, R]{}, false, s.c.Errors.Conflict
	}
	op, err := s.Decode(body)
	return op, err == nil, err
}

// InsertTx stores op under key within tx, refusing while another
// operation of the resource is unresolved.
func (s *Store[E, Q, R]) InsertTx(ctx context.Context, tx *sql.Tx, op *Operation[E, Q, R], key, hash string) error {
	n, err := s.UnresolvedCount(ctx, tx, op.ResourceID)
	if err != nil {
		return err
	}
	if n > 0 {
		return s.c.Errors.Precondition(resource.OperationUnresolved)
	}
	body, err := json.Marshal(op)
	if err != nil {
		return err
	}
	if _, err := tx.ExecContext(ctx, `INSERT INTO resource_operations(id,resource_id,idempotency_key,request_hash,action,state,unresolved,created_at,updated_at,body) VALUES(?,?,?,?,?,?,?,?,?,?)`,
		op.ID, op.ResourceID, key, hash, op.Action, op.State, op.Unresolved, op.CreatedAt.UnixMilli(), op.UpdatedAt.UnixMilli(), string(body)); err != nil {
		return err
	}
	op.stored = string(body)
	return nil
}

// Audit is an audit entry, with its control event, written in the
// transaction of the change it describes.
type Audit struct {
	// Action is appended to the type, as in "<type>.<action>".
	Action  string
	Target  string
	Details map[string]any
}

// Change adds to a save.
type Change struct {
	// Check runs first within the transaction; an error aborts the save.
	Check func(*sql.Tx) error
	// Audit, when set, is recorded within the transaction.
	Audit *Audit
}

// Save stores op if its row still holds what op was read or last saved
// as (compare-and-swap) and otherwise returns ErrStale, wrapped in the
// type's conflict error. Nothing is written when op did not change. A
// change of state, resolution or reason records a "<type>.operation.changed"
// event in the same transaction; the events are published after the
// commit.
func (s *Store[E, Q, R]) Save(ctx context.Context, op *Operation[E, Q, R], c Change) error {
	if op.stored == "" {
		return errors.New("operation: save of an operation that was never stored")
	}
	same, err := json.Marshal(op)
	if err != nil {
		return err
	}
	if string(same) == op.stored && c.Audit == nil {
		return nil
	}
	var prior struct {
		State      string `json:"state"`
		Unresolved bool   `json:"unresolved"`
		Reason     string `json:"reason"`
	}
	if err := json.Unmarshal([]byte(op.stored), &prior); err != nil {
		return err
	}
	updated := op.UpdatedAt
	op.UpdatedAt = s.c.Clock.Now().UTC()
	raw, err := json.Marshal(op)
	if err != nil {
		op.UpdatedAt = updated
		return err
	}
	body := string(raw)
	var events []resource.Event
	err = s.c.DB.Write(ctx, func(tx *sql.Tx) error {
		events = events[:0]
		if c.Check != nil {
			if err := c.Check(tx); err != nil {
				return err
			}
		}
		res, err := tx.ExecContext(ctx, `UPDATE resource_operations SET state=?,unresolved=?,updated_at=?,body=? WHERE id=? AND body=?`,
			op.State, op.Unresolved, op.UpdatedAt.UnixMilli(), body, op.ID, op.stored)
		if err != nil {
			return err
		}
		if n, _ := res.RowsAffected(); n == 0 {
			// Wrapped in the type's conflict, so that an API caller gets a
			// version conflict to reload after.
			return fmt.Errorf("%w: %w", s.c.Errors.Conflict, ErrStale)
		}
		if prior.State != op.State || prior.Unresolved != op.Unresolved || prior.Reason != op.Reason {
			e := resource.Event{
				ResourceID: op.ResourceID, OperationID: op.ID, Source: "control", Key: store.NewID(), Kind: s.c.Type + ".operation.changed",
				Severity: s.c.Severity(op.State), OccurredAt: op.UpdatedAt, IngestedAt: op.UpdatedAt, Reason: op.State,
			}
			if !op.Unresolved && op.State == Indeterminate {
				e.Reason = resource.ReasonResolved
			}
			if err := resource.InsertEvent(ctx, tx, &e); err != nil {
				return err
			}
			events = append(events, e)
		}
		if c.Audit != nil {
			e, err := s.RecordTx(ctx, tx, c.Audit.Action, c.Audit.Target, op.ResourceID, op.ID, c.Audit.Details)
			if err != nil {
				return err
			}
			events = append(events, e)
		}
		return nil
	})
	if err != nil {
		op.UpdatedAt = updated
		return err
	}
	op.stored = body
	s.publish(events)
	return nil
}

// RecordTx audits a control action on an operation within tx and stores
// its "<type>.<action>" event; the caller publishes the event after the
// commit.
func (s *Store[E, Q, R]) RecordTx(ctx context.Context, tx *sql.Tx, action, target, resourceID, operationID string, details map[string]any) (resource.Event, error) {
	if err := s.c.Audit.RecordTx(ctx, tx, s.c.Type+"."+action, target, audit.OutcomeOK, details); err != nil {
		return resource.Event{}, err
	}
	now := s.c.Clock.Now().UTC()
	e := resource.Event{ResourceID: resourceID, OperationID: operationID, Source: "control", Key: store.NewID(), Kind: s.c.Type + "." + action, Severity: "info", OccurredAt: now, IngestedAt: now}
	return e, resource.InsertEvent(ctx, tx, &e)
}

// publish hands committed events to the configured subscriber.
func (s *Store[E, Q, R]) publish(events []resource.Event) {
	if s.c.Publish != nil && len(events) > 0 {
		s.c.Publish(events)
	}
}

// Querier is a transaction or a reader pool.
type Querier interface {
	QueryRowContext(ctx context.Context, query string, args ...any) *sql.Row
}

// UnresolvedCount counts the unresolved operations of a resource.
func (s *Store[E, Q, R]) UnresolvedCount(ctx context.Context, q Querier, resourceID string) (int, error) {
	var n int
	err := q.QueryRowContext(ctx, `SELECT COUNT(*) FROM resource_operations WHERE resource_id=? AND unresolved=1`, resourceID).Scan(&n)
	return n, err
}

// Decode reads a stored body.
func (s *Store[E, Q, R]) Decode(body string) (Operation[E, Q, R], error) {
	var op Operation[E, Q, R]
	if err := json.Unmarshal([]byte(body), &op); err != nil {
		return Operation[E, Q, R]{}, err
	}
	op.stored = body
	return op, nil
}

// Get returns one operation of a resource.
func (s *Store[E, Q, R]) Get(ctx context.Context, resourceID, id string) (Operation[E, Q, R], error) {
	var body string
	err := s.c.DB.Reader().QueryRowContext(ctx, `SELECT body FROM resource_operations WHERE resource_id=? AND id=?`, resourceID, id).Scan(&body)
	if errors.Is(err, sql.ErrNoRows) {
		return Operation[E, Q, R]{}, s.c.Errors.NotFound
	}
	if err != nil {
		return Operation[E, Q, R]{}, err
	}
	return s.Decode(body)
}

// List returns the newest 200 operations of a resource first.
func (s *Store[E, Q, R]) List(ctx context.Context, resourceID string) ([]Operation[E, Q, R], error) {
	return s.query(ctx, `SELECT body FROM resource_operations WHERE resource_id=? ORDER BY created_at DESC LIMIT 200`, resourceID)
}

// Unresolved returns the unresolved operations of the type, oldest first:
// of one resource unless resourceID is empty, and in one of states unless
// none is given.
func (s *Store[E, Q, R]) Unresolved(ctx context.Context, resourceID string, states ...string) ([]Operation[E, Q, R], error) {
	q := `SELECT o.body FROM resource_operations o JOIN resources r ON r.id = o.resource_id WHERE o.unresolved = 1 AND r.type = ?`
	args := []any{s.c.Type}
	if resourceID != "" {
		q += ` AND o.resource_id = ?`
		args = append(args, resourceID)
	}
	if len(states) > 0 {
		q += ` AND o.state IN (?` + strings.Repeat(`,?`, len(states)-1) + `)`
		for _, st := range states {
			args = append(args, st)
		}
	}
	return s.query(ctx, q+` ORDER BY o.created_at`, args...)
}

func (s *Store[E, Q, R]) query(ctx context.Context, q string, args ...any) ([]Operation[E, Q, R], error) {
	rows, err := s.c.DB.Reader().QueryContext(ctx, q, args...)
	if err != nil {
		return nil, err
	}
	defer func() { _ = rows.Close() }()
	out := []Operation[E, Q, R]{}
	for rows.Next() {
		var body string
		if err := rows.Scan(&body); err != nil {
			return nil, err
		}
		op, err := s.Decode(body)
		if err != nil {
			return nil, err
		}
		out = append(out, op)
	}
	return out, rows.Err()
}

// CheckResolvable returns why op cannot be resolved manually, or nil: only
// an unresolved indeterminate operation can.
func (s *Store[E, Q, R]) CheckResolvable(op Operation[E, Q, R]) error {
	switch {
	case !op.Unresolved:
		return s.c.Errors.Precondition("operation is already resolved")
	case op.State != Indeterminate:
		return s.c.Errors.Precondition("only indeterminate operations can be manually resolved")
	}
	return nil
}
