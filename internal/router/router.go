// Package router implements the router resource type: network routers
// reached through a host-configured connection (internal/connection),
// observed periodically and rebooted only by a direct administrator
// request. Identity, documents and phase live in the resource store; this
// package owns observations, native health and reboot operations, stored
// in the shared resource_status, resource_snapshots, resource_points,
// resource_events and resource_operations tables.
//
// # Observation
//
// Every managed router has its own observation loop (Run starts and stops
// them as resources change), bounded by a shared concurrency limit, so a
// router that is down never delays the others. Each attempt is stored as a
// snapshot whose outcome says whether the router answered: ok, unreachable
// (no HTTP answer), auth-failed, unavailable (an answer without usable
// status) or not-configured (no usable connection or secret).
//
// Native health: fresh unreachable evidence is down; CPU or memory above a
// threshold for the configured number of consecutive samples is degraded
// until the value falls below the threshold minus the hysteresis; anything
// that prevents an evaluation and stale data are unknown. State is online
// while the latest attempt is fresh and ok, offline while it is fresh and
// unreachable, and unknown otherwise.
//
// # Reboot
//
// A reboot is a durable operation: the intent is stored (pending), marked
// submitting, sent once, and then observed. It succeeds only when a fresh
// observation after the submission shows that the router booted after it
// (uptime shorter than the time since submission, within bootTolerance)
// before a ten-minute deadline; a request the router refused or that never
// left LCM fails; everything else becomes indeterminate and needs a manual
// resolve. An interrupted submission is never sent again.
package router

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"sync"
	"time"

	"github.com/ACaccel/lcm/internal/audit"
	"github.com/ACaccel/lcm/internal/clock"
	"github.com/ACaccel/lcm/internal/connection"
	"github.com/ACaccel/lcm/internal/maintenance"
	"github.com/ACaccel/lcm/internal/message"
	"github.com/ACaccel/lcm/internal/model"
	"github.com/ACaccel/lcm/internal/monitor"
	"github.com/ACaccel/lcm/internal/resource"
	"github.com/ACaccel/lcm/internal/resource/operation"
	"github.com/ACaccel/lcm/internal/router/xiaomi"
	"github.com/ACaccel/lcm/internal/secret"
	"github.com/ACaccel/lcm/internal/store"
)

// Domain errors wrap the shared resource errors, which the API maps; an
// unsupported action is a failed precondition.
var (
	ErrNotFound     = resource.TypeError("router: not found", resource.ErrNotFound)
	ErrConflict     = resource.TypeError("router: version conflict", resource.ErrConflict)
	ErrPrecondition = resource.TypeError("router: precondition failed", resource.ErrPrecondition)
	ErrUnsupported  = resource.TypeError("router: action unsupported", resource.ErrPrecondition)
	ErrForbidden    = resource.TypeError("router: only a direct administrator request is allowed", resource.ErrForbidden)
)

const (
	// rebootDeadline bounds the wait for reboot evidence.
	rebootDeadline = 10 * time.Minute
	// observingWait caps the observation interval while a reboot waits for
	// evidence, so that a router observed hourly is still verified before
	// rebootDeadline.
	observingWait = 30 * time.Second
	// bootTolerance absorbs request latency and uptime rounding when a
	// boot time is derived from observation time minus uptime.
	bootTolerance = time.Minute
	// defaultConcurrency bounds simultaneous router requests.
	defaultConcurrency = 4
)

// Snapshot outcomes.
const (
	OutcomeOK            = "ok"
	OutcomeUnreachable   = "unreachable"
	OutcomeAuthFailed    = "auth-failed"
	OutcomeUnavailable   = "unavailable"
	OutcomeNotConfigured = "not-configured"
)

// Reachability of the latest attempt.
const (
	reachable   = "reachable"
	unreachable = "unreachable"
	unknown     = "unknown"
)

// Health values, as the machine type reports them.
const (
	healthHealthy  = "healthy"
	healthDegraded = "degraded"
	healthDown     = "down"
	healthUnknown  = "unknown"
)

// Coverage reports whether one status source of the router contributed.
type Coverage struct {
	State  string `json:"state"`
	Reason string `json:"reason,omitempty"`
}

// WAN is the router's WAN link and traffic. Addresses, DNS servers and MAC
// addresses are never read.
type WAN struct {
	Link            *bool    `json:"link"`
	Status          *int     `json:"status"`
	UptimeSeconds   *float64 `json:"uptimeSeconds"`
	Type            string   `json:"type,omitempty"`
	UpBytesPerSec   *float64 `json:"upBytesPerSec"`
	DownBytesPerSec *float64 `json:"downBytesPerSec"`
	UploadedBytes   *int64   `json:"uploadedBytes"`
	DownloadedBytes *int64   `json:"downloadedBytes"`
}

// Snapshot is one observation attempt. Measurements are nil when the router
// did not report them.
type Snapshot struct {
	ID         int64     `json:"id"`
	ObservedAt time.Time `json:"observedAt"`
	Outcome    string    `json:"outcome"`
	// Reason is a fixed description of a failed attempt; it never contains
	// router answers or credentials.
	Reason        string              `json:"reason,omitempty"`
	CPUPercent    *float64            `json:"cpuPercent"`
	CPUCores      *int                `json:"cpuCores"`
	MemoryPercent *float64            `json:"memoryPercent"`
	UptimeSeconds *float64            `json:"uptimeSeconds"`
	TemperatureC  *float64            `json:"temperatureC"`
	Clients       *int                `json:"clients"`
	WAN           WAN                 `json:"wan"`
	Sources       map[string]Coverage `json:"sources"`
	Model         string              `json:"model"`
	Firmware      string              `json:"firmware"`
}

// StatusView is the status as clients read it: the stored status with a
// translatable message for each health reason.
type StatusView struct {
	Status
	// Snapshot replaces the stored snapshot with its translatable form.
	Snapshot       *SnapshotView     `json:"snapshot"`
	ReasonMessages []message.Message `json:"reasonMessages"`
}

func newStatusView(s Status) StatusView {
	v := StatusView{Status: s, ReasonMessages: resource.ReasonMessages(s.Reasons)}
	if s.Snapshot != nil {
		sv := NewSnapshotView(*s.Snapshot)
		v.Snapshot = &sv
	}
	return v
}

// Status is the current observed state; Fresh, and with it Health, is
// computed at read time.
type Status struct {
	// Snapshot is the latest observation attempt.
	Snapshot *Snapshot `json:"snapshot"`
	Health   string    `json:"health"`
	Reasons  []string  `json:"reasons"`
	Fresh    bool      `json:"fresh"`
	// Reachability of the latest attempt: reachable, unreachable or
	// unknown (no attempt or no usable connection).
	Reachability string `json:"reachability"`
	// BootAt estimates the router's last boot from the latest reported
	// uptime.
	BootAt *time.Time `json:"bootAt"`
}

type transition struct {
	Count  int  `json:"count"`
	Active bool `json:"active"`
}

type storedStatus struct {
	Status
	Baseline        map[string]transition `json:"baseline"`
	DocumentVersion int                   `json:"documentVersion"`
	// State is the state the latest attempt showed and Since when that
	// state started, across consecutive fresh attempts.
	State string    `json:"state"`
	Since time.Time `json:"since,omitzero"`
	// LastKnown is the latest known reachability and Sources the source
	// coverage of the latest successful attempt, for change events.
	LastKnown string              `json:"lastKnown,omitempty"`
	Sources   map[string]Coverage `json:"sources,omitempty"`
}

// Record is a router resource with its current status.
type Record struct {
	ID         string
	Name       string
	Version    int
	Phase      string
	UpdatedAt  time.Time
	Document   *model.Resource
	Config     *model.RouterConfig
	Connection string
	Status     Status
	state      string
	since      time.Time
}

// NativeHealth receives the router's native health for the monitor, which
// combines it with the resource's checks; *monitor.Monitor implements it.
type NativeHealth interface {
	SetNative(resource, typ string, labels map[string]string, h monitor.Health, reasons []string)
	// Forget drops the resource's native input, checks and health without
	// a signal, when the resource leaves observation.
	Forget(resource string)
}

// Client is what the manager needs from a router client; *xiaomi.Client
// implements it.
type Client interface {
	Status(ctx context.Context) (xiaomi.Status, error)
	Reboot(ctx context.Context) (xiaomi.RebootOutcome, error)
}

// Config supplies the dependencies of a Manager.
type Config struct {
	DB        *store.DB
	Resources *resource.Store
	Audit     *audit.Log
	// Gate is the local write gate: reboots are refused while machine
	// maintenance is active. Observation does not pass through it.
	Gate *maintenance.Gate
	// Connections are the host-configured connections; nil is none.
	Connections *connection.Set
	Secrets     secret.Source
	// Native, when set, receives every native health evaluation.
	Native NativeHealth
	Clock  clock.Clock
	Log    *slog.Logger
	// OnEvent, when set, receives every committed event.
	OnEvent func(resource.Event)
	// Concurrency bounds simultaneous router requests; 0 selects 4.
	Concurrency int
	// HTTP replaces the HTTP client of router clients, for tests.
	HTTP *http.Client
}

// Manager owns router observation and operations. It starts no goroutines
// until Run is called.
type Manager struct {
	db        *store.DB
	resources *resource.Store
	audit     *audit.Log
	gate      *maintenance.Gate
	conns     *connection.Set
	secrets   secret.Source
	native    NativeHealth
	clk       clock.Clock
	log       *slog.Logger
	onEvent   func(resource.Event)
	httpc     *http.Client
	sem       chan struct{}
	ops       *operation.Store[Status, Request, Receipt]

	// locks serializes observations and operations per router.
	locksMu sync.Mutex
	locks   map[string]*routerLocks

	// clients are cached per connection: routers sharing a connection
	// share a session and the login nonce sequence the router enforces.
	clientsMu sync.Mutex
	clients   map[string]Client

	loopsMu sync.Mutex
	loops   map[string]*loop
	changed map[string]bool
	kick    chan struct{}
}

type routerLocks struct {
	observe sync.Mutex
	control sync.Mutex
}

// New returns a Manager.
func New(c Config) (*Manager, error) {
	if c.DB == nil || c.Resources == nil || c.Audit == nil || c.Secrets == nil {
		return nil, errors.New("router: DB, Resources, Audit and Secrets are required")
	}
	if c.Clock == nil {
		c.Clock = clock.Real()
	}
	if c.Log == nil {
		c.Log = slog.New(slog.DiscardHandler)
	}
	if c.Concurrency <= 0 {
		c.Concurrency = defaultConcurrency
	}
	if c.Connections == nil {
		c.Connections = &connection.Set{}
	}
	m := &Manager{
		db: c.DB, resources: c.Resources, audit: c.Audit, gate: c.Gate, conns: c.Connections, secrets: c.Secrets,
		native: c.Native, clk: c.Clock, log: c.Log, onEvent: c.OnEvent, httpc: c.HTTP,
		sem: make(chan struct{}, c.Concurrency), locks: map[string]*routerLocks{}, clients: map[string]Client{},
		loops: map[string]*loop{}, changed: map[string]bool{}, kick: make(chan struct{}, 1),
	}
	var err error
	m.ops, err = operation.New[Status, Request, Receipt](operation.Config{
		DB: c.DB, Audit: c.Audit, Clock: c.Clock, Type: model.ResourceRouter, Severity: severity, Publish: m.publish,
		Errors: operation.Errors{NotFound: ErrNotFound, Conflict: ErrConflict, Precondition: precondition},
	})
	if err != nil {
		return nil, err
	}
	return m, nil
}

func (m *Manager) locksFor(id string) *routerLocks {
	m.locksMu.Lock()
	defer m.locksMu.Unlock()
	l := m.locks[id]
	if l == nil {
		l = &routerLocks{}
		m.locks[id] = l
	}
	return l
}

// errNotConfigured explains why no client can be built; its message is a
// fixed text safe for snapshots and the API.
type errNotConfigured string

func (e errNotConfigured) Error() string { return string(e) }

// client returns the cached client of the router's connection, building it
// on first use. The password is read from the secret source only then, so
// a rotated secret takes effect after an authentication failure dropped
// the client.
func (m *Manager) client(r Record) (Client, error) {
	conn, ok := m.conns.Get(r.Connection)
	switch {
	case !ok:
		return nil, errNotConfigured(resource.ReasonNoConnection)
	case conn.Type != r.Document.Spec.Adapter:
		return nil, errNotConfigured(resource.ReasonConnectionType)
	case !conn.AllowsResource(r.Name):
		return nil, errNotConfigured(resource.ReasonConnectionDenied)
	}
	m.clientsMu.Lock()
	defer m.clientsMu.Unlock()
	if c := m.clients[conn.Name]; c != nil {
		return c, nil
	}
	pw, err := m.secrets.Get(conn.PasswordSecret)
	if err != nil || pw.Reveal() == "" {
		return nil, errNotConfigured(resource.PasswordSecretUnavailable)
	}
	c, err := xiaomi.New(xiaomi.Config{
		BaseURL: conn.URL, Username: conn.Username, Password: pw.Reveal(), DeviceID: conn.DeviceID,
		InsecureSkipVerify: conn.InsecureSkipVerify, Clock: m.clk, HTTP: m.httpc,
	})
	if err != nil {
		return nil, errNotConfigured(resource.ConnectionSettingsInvalid)
	}
	m.clients[conn.Name] = c
	return c, nil
}

// dropClient forgets the client of a connection after an authentication
// failure, so that the next attempt reads the secret again.
func (m *Manager) dropClient(name string) {
	m.clientsMu.Lock()
	delete(m.clients, name)
	m.clientsMu.Unlock()
}

// acquire takes a slot of the shared concurrency limit.
func (m *Manager) acquire(ctx context.Context) error {
	select {
	case m.sem <- struct{}{}:
		return nil
	case <-ctx.Done():
		return context.Cause(ctx)
	}
}

func (m *Manager) release() { <-m.sem }

func (m *Manager) publish(events []resource.Event) {
	if m.onEvent == nil {
		return
	}
	for _, e := range events {
		m.onEvent(e)
	}
}

func precondition(reason string) error { return fmt.Errorf("%w: %s", ErrPrecondition, reason) }

func encode(v any) (string, error) { b, err := json.Marshal(v); return string(b), err }

// List returns the managed and retired routers ordered by name.
func (m *Manager) List(ctx context.Context) ([]Record, error) {
	recs, err := m.resources.List(ctx, resource.Filter{Type: model.ResourceRouter, IncludeRetired: true})
	if err != nil {
		return nil, err
	}
	out := make([]Record, 0, len(recs))
	for _, rec := range recs {
		r, err := m.view(ctx, rec)
		if err != nil {
			return nil, err
		}
		out = append(out, r)
	}
	return out, nil
}

// Get returns a router by the name of an active resource or by "@<id>".
func (m *Manager) Get(ctx context.Context, ref string) (Record, error) {
	rec, err := m.resources.Get(ctx, ref)
	if errors.Is(err, resource.ErrNotFound) {
		return Record{}, fmt.Errorf("%w: %w", ErrNotFound, err)
	}
	if err != nil {
		return Record{}, err
	}
	if rec.Type != model.ResourceRouter {
		return Record{}, fmt.Errorf("%w: %s is a %s resource", ErrNotFound, rec.Name, rec.Type)
	}
	return m.view(ctx, rec)
}

// view adds the stored status to a resource record.
func (m *Manager) view(ctx context.Context, rec resource.Record) (Record, error) {
	cfg, ok := rec.Document.Router()
	if !ok {
		return Record{}, fmt.Errorf("router: %s has no router config", rec.Name)
	}
	r := Record{
		ID: rec.ID, Name: rec.Name, Version: rec.Version, Phase: rec.Phase, UpdatedAt: rec.UpdatedAt,
		Document: rec.Document, Config: cfg, Connection: rec.Document.Spec.Connection,
	}
	var raw string
	err := m.db.Reader().QueryRowContext(ctx, `SELECT status FROM resource_status WHERE resource_id = ?`, rec.ID).Scan(&raw)
	switch {
	case errors.Is(err, sql.ErrNoRows):
		raw = "{}"
	case err != nil:
		return Record{}, err
	}
	var st storedStatus
	if err := json.Unmarshal([]byte(raw), &st); err != nil {
		return Record{}, err
	}
	r.Status, r.state, r.since = st.Status, st.State, st.Since
	m.freshen(&r, st.DocumentVersion)
	return r, nil
}

// freshen derives freshness and the read-time health: data older than
// staleAfter, of another document version or of a resource that is not
// managed says nothing about the router now.
func (m *Manager) freshen(r *Record, documentVersion int) {
	now := m.clk.Now()
	s := r.Status.Snapshot
	r.Status.Fresh = s != nil && r.Phase == resource.PhaseManaged && documentVersion == r.Version &&
		!s.ObservedAt.After(now) && now.Sub(s.ObservedAt) <= r.Config.StaleAfter.D()
	if !r.Status.Fresh {
		r.Status.Health = healthUnknown
		r.Status.Reasons = []string{resource.ReasonStale}
		if s != nil && documentVersion != r.Version && r.Phase == resource.PhaseManaged {
			r.Status.Reasons = []string{resource.ReasonNotObserved}
		}
	}
	if r.Status.Health == "" {
		r.Status.Health = healthUnknown
	}
	if r.Status.Reachability == "" {
		r.Status.Reachability = unknown
	}
	if r.Status.Reasons == nil {
		r.Status.Reasons = []string{}
	}
}

// online reports whether the latest attempt is fresh and succeeded.
func online(r Record) bool {
	return r.Status.Fresh && r.Status.Snapshot != nil && r.Status.Snapshot.Outcome == OutcomeOK
}

// current is the part of a router's state that writes recheck inside their
// transaction.
type current struct {
	version int
	phase   string
	status  storedStatus
}

func readCurrent(ctx context.Context, tx *sql.Tx, id string) (current, error) {
	var c current
	var raw string
	err := tx.QueryRowContext(ctx, `SELECT r.version, r.phase, COALESCE(s.status, '{}')
		FROM resources r LEFT JOIN resource_status s ON s.resource_id = r.id WHERE r.id = ? AND r.type = ?`,
		id, model.ResourceRouter).Scan(&c.version, &c.phase, &raw)
	if errors.Is(err, sql.ErrNoRows) {
		return c, ErrNotFound
	}
	if err != nil {
		return c, err
	}
	return c, json.Unmarshal([]byte(raw), &c.status)
}

func saveStatus(ctx context.Context, tx *sql.Tx, id string, st storedStatus, at time.Time) error {
	body, err := encode(st)
	if err != nil {
		return err
	}
	_, err = tx.ExecContext(ctx, `INSERT INTO resource_status (resource_id, identity_hash, status, updated_at) VALUES (?, '', ?, ?)
		ON CONFLICT (resource_id) DO UPDATE SET status = excluded.status, updated_at = excluded.updated_at`,
		id, body, at.UnixMilli())
	return err
}
