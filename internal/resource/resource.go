// Package resource is the registry of managed resources of every type:
// identity, versioned lcm/v2 documents and the management phase.
//
// # Identity
//
// A resource has an immutable ID and a name. Names are unique among
// resources that are not unmanaged; unmanaging archives a resource and
// releases its name. Creating the name again starts a new incarnation with
// a new ID and version 1. "@<id>" refers to any incarnation, including
// archives.
//
// # Documents
//
// Every save of a changed document adds a version. Saves carry the version
// the caller based the change on; a mismatch is ErrConflict. Restoring an
// old version saves it as a new one. Type and adapter never change: a
// different kind of object is a different resource.
//
// # Phase
//
// managed -> retired (observation and control stop, records stay) ->
// unmanaged (archived). A retired resource can be resumed. Type-specific
// work (stopping a service, refusing while an operation is open) runs in
// TypeHooks inside the write transaction or in the type's ChangePhase.
package resource

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"sync"
	"time"

	"github.com/ACaccel/lcm/internal/audit"
	"github.com/ACaccel/lcm/internal/auth"
	"github.com/ACaccel/lcm/internal/clock"
	"github.com/ACaccel/lcm/internal/maintenance"
	"github.com/ACaccel/lcm/internal/model"
	"github.com/ACaccel/lcm/internal/store"
)

// Errors returned by the store.
var (
	ErrNotFound = errors.New("resource: not found")
	ErrExists   = errors.New("resource: already exists")
	// ErrConflict means the stored version differs from the expected one.
	ErrConflict = errors.New("resource: version conflict")
	// ErrPrecondition means the request is well formed but not allowed in
	// the current state.
	ErrPrecondition = errors.New("resource: precondition failed")
	// ErrInvalid wraps the model.Issues of a rejected document.
	ErrInvalid = errors.New("resource: invalid document")
)

// Management phases.
const (
	PhaseManaged   = "managed"
	PhaseRetired   = "retired"
	PhaseUnmanaged = "unmanaged"
)

// Changes reported to Config.OnChange.
const (
	ChangeCreated   = "created"
	ChangeUpdated   = "updated"
	ChangeRestored  = "restored"
	ChangePhase     = "phase"
	ChangeUnmanaged = "unmanaged"
)

// Record is a resource as stored, with its current document.
type Record struct {
	ID        string
	Name      string
	Type      string
	Adapter   string
	Phase     string
	Version   int
	Document  *model.Resource
	CreatedAt time.Time
	UpdatedAt time.Time
}

// Version describes one stored version of a resource document.
type Version struct {
	Version      int
	RestoredFrom int
	CreatedAt    time.Time
	Actor        auth.Actor
}

// PutResult is the outcome of saving a document.
type PutResult struct {
	Version int
	// Created is true for the first version of a resource.
	Created bool
	// Changed is false when the document equals the current version and
	// nothing was written.
	Changed bool
	// Unresolved lists relation targets that do not exist (yet).
	Unresolved []string
}

// Change describes a committed change for Config.OnChange.
type Change struct {
	ID     string `json:"id"`
	Name   string `json:"name"`
	Type   string `json:"type"`
	Change string `json:"change"`
}

// Filter selects resources for List.
type Filter struct {
	// Type limits the result to one resource type when set.
	Type string
	// Labels must all match the document's labels.
	Labels map[string]string
	// IncludeRetired adds retired resources to the managed ones.
	IncludeRetired bool
	// Phase, when set, limits the result to managed or retired resources
	// and overrides IncludeRetired.
	Phase string
	// After continues a listing: only names after it are returned.
	After string
	// Limit bounds the number of records; 0 returns all.
	Limit int
}

// TypeHooks lets a resource type take part in saves and phase changes.
// Both run inside the write transaction; an error aborts it.
type TypeHooks interface {
	// BeforeSave runs before a new version is written. On create the
	// resources row already exists, so type tables can reference it.
	BeforeSave(ctx context.Context, tx *sql.Tx, s Save) error
	// BeforePhase runs before a resource moves to another phase.
	BeforePhase(ctx context.Context, tx *sql.Tx, p PhaseChange) error
}

// Save describes a version about to be written, for TypeHooks.
type Save struct {
	// Old is the current record; nil on create.
	Old *Record
	// Next carries the ID, new version number and document.
	Next *Record
	// RestoredFrom is the restored version, 0 for a normal save.
	RestoredFrom int
	Actor        auth.Actor
}

// PhaseChange describes a phase change about to be written, for TypeHooks.
type PhaseChange struct {
	// Record is the resource in its current phase.
	Record *Record
	To     string
	Reason string
	Actor  auth.Actor
}

// HostValidator is implemented by the TypeHooks of a type whose documents
// have rules that depend on this host, such as a service source that must
// not point into LCM's build directory. The store refuses a save that it
// reports issues for.
type HostValidator interface {
	ValidateHost(doc *model.Resource) model.Issues
}

// CheckValidator validates check definitions against the registered check
// types; *monitor.Monitor implements it. path is the location of the list,
// such as "spec.checks".
type CheckValidator interface {
	ValidateChecks(checks []model.Check, path string) model.Issues
}

// Config configures a Store.
type Config struct {
	DB *store.DB
	// Gate is the shared local write gate; nil allows every write.
	Gate  *maintenance.Gate
	Audit *audit.Log
	// Clock defaults to the real clock.
	Clock clock.Clock
	Log   *slog.Logger
	// Checks, when set, validates the checks of every saved document, so
	// that a check type or props error is refused for all resource types
	// in one place.
	Checks CheckValidator
	// OnChange, when set, is called after a committed change. It runs on
	// the caller's goroutine and must not block or call back into the
	// Store.
	OnChange func(Change)
}

// Store keeps resource identities, documents and phases.
type Store struct {
	db       *store.DB
	gate     *maintenance.Gate
	audit    *audit.Log
	clk      clock.Clock
	log      *slog.Logger
	checks   CheckValidator
	onChange func(Change)

	mu    sync.RWMutex
	hooks map[string]TypeHooks
}

// New returns a Store.
func New(cfg Config) (*Store, error) {
	if cfg.DB == nil || cfg.Audit == nil {
		return nil, errors.New("resource: DB and Audit are required")
	}
	s := &Store{db: cfg.DB, gate: cfg.Gate, audit: cfg.Audit, clk: cfg.Clock, log: cfg.Log, checks: cfg.Checks, onChange: cfg.OnChange, hooks: map[string]TypeHooks{}}
	if s.clk == nil {
		s.clk = clock.Real()
	}
	if s.log == nil {
		s.log = slog.New(slog.NewTextHandler(io.Discard, nil))
	}
	return s, nil
}

// RegisterHooks installs the hooks of a resource type, replacing earlier
// ones. Register before the store is used.
func (s *Store) RegisterHooks(typ string, h TypeHooks) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.hooks[typ] = h
}

func (s *Store) hooksFor(typ string) TypeHooks {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return s.hooks[typ]
}

func (s *Store) changed(c Change) {
	if s.onChange != nil {
		s.onChange(c)
	}
}

func requireActor(ctx context.Context) (auth.Actor, error) {
	a, ok := auth.ActorFrom(ctx)
	if !ok || !a.Kind.Valid() || a.ID == "" {
		return auth.Actor{}, audit.ErrNoActor
	}
	return a, nil
}

func invalid(err error) error {
	var is model.Issues
	if errors.As(err, &is) {
		return fmt.Errorf("%w: %w", ErrInvalid, is)
	}
	return err
}

func precondition(format string, args ...any) error {
	return fmt.Errorf("%w: %s", ErrPrecondition, fmt.Sprintf(format, args...))
}
