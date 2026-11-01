// Package machine manages the local physical controller host independently of services.
package machine

import (
	"context"
	"crypto/rand"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
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

// Domain errors have stable API mappings.
var (
	ErrNotFound     = errors.New("machine: not found")
	ErrExists       = errors.New("machine: an active local machine already exists")
	ErrConflict     = errors.New("machine: version conflict")
	ErrPrecondition = errors.New("machine: precondition failed")
	ErrUnsupported  = errors.New("machine: capability unsupported or not configured")
	ErrForbidden    = errors.New("machine: only a direct administrator request is allowed")
)

// Coverage reports source availability separately from machine health.
type Coverage struct {
	State  string `json:"state"`
	Reason string `json:"reason,omitempty"`
}

// Actor is the public representation of an authenticated principal.
type Actor struct {
	Kind auth.Kind `json:"kind"`
	ID   string    `json:"id"`
	Name string    `json:"name"`
}

func actorInfo(a auth.Actor) Actor { return Actor{Kind: a.Kind, ID: a.ID, Name: a.Name} }

// Filesystem is one explicitly selected filesystem, with available capacity.
type Filesystem struct {
	Path       string `json:"path"`
	Total      uint64 `json:"total"`
	Available  uint64 `json:"available"`
	Inodes     uint64 `json:"inodes"`
	FreeInodes uint64 `json:"freeInodes"`
}

// Snapshot contains bounded normalized measurements. DeviceHash is never an API field.
type Snapshot struct {
	ID              int64               `json:"id"`
	ObservedAt      time.Time           `json:"observedAt"`
	DeviceHash      string              `json:"-"`
	BootID          string              `json:"bootId"`
	OS              string              `json:"os"`
	Init            string              `json:"init"`
	UptimeSeconds   float64             `json:"uptimeSeconds"`
	CPUTotal        uint64              `json:"cpuTotal"`
	CPUIdle         uint64              `json:"cpuIdle"`
	CPUCount        int                 `json:"cpuCount"`
	CPUPercent      *float64            `json:"cpuPercent"`
	Load            []float64           `json:"load,omitempty"`
	MemoryTotal     uint64              `json:"memoryTotal"`
	MemoryAvailable uint64              `json:"memoryAvailable"`
	SwapTotal       uint64              `json:"swapTotal"`
	SwapFree        uint64              `json:"swapFree"`
	Filesystems     []Filesystem        `json:"filesystems"`
	Interfaces      map[string]string   `json:"interfaces,omitempty"`
	Units           map[string]string   `json:"units,omitempty"`
	Pressure        map[string]float64  `json:"pressure,omitempty"`
	Coverage        map[string]Coverage `json:"coverage"`
}

// Event is persisted before being announced to best-effort subscribers.
type Event struct {
	ID          int64     `json:"id"`
	MachineID   string    `json:"machineId"`
	Source      string    `json:"source"`
	Key         string    `json:"-"`
	Kind        string    `json:"kind"`
	Severity    string    `json:"severity"`
	OccurredAt  time.Time `json:"occurredAt"`
	IngestedAt  time.Time `json:"ingestedAt"`
	BootID      string    `json:"bootId,omitempty"`
	Unit        string    `json:"unit,omitempty"`
	Historical  bool      `json:"historical"`
	Reason      string    `json:"reason,omitempty"`
	OperationID string    `json:"operationId,omitempty"`
}

// JournalBatch leaves checkpoint ownership to the transactional machine store.
type JournalBatch struct {
	Events      []Event
	Cursor      string
	Coverage    Coverage
	ResetCursor bool
}

// Observer provides read-only snapshots and bounded journal batches.
type Observer interface {
	Observe(context.Context, model.MachineMonitoring) (Snapshot, error)
	ReadJournal(context.Context, model.MachineMonitoring, string, time.Time) (JournalBatch, error)
}

// Status is the current observed state; Fresh is computed at read time.
type Status struct {
	Snapshot *Snapshot `json:"snapshot"`
	Health   string    `json:"health"`
	Reasons  []string  `json:"reasons"`
	Identity string    `json:"identity"`
	Journal  Coverage  `json:"journal"`
	Fresh    bool      `json:"fresh"`
}

type transition struct {
	Count  int  `json:"count"`
	Active bool `json:"active"`
}
type storedStatus struct {
	Status
	Baseline        map[string]transition `json:"baseline"`
	DocumentVersion int                   `json:"documentVersion"`
}

// Record is an active machine document and its current status.
type Record struct {
	ID        string         `json:"id"`
	Name      string         `json:"name"`
	Host      string         `json:"host"`
	Version   int            `json:"version"`
	Phase     string         `json:"phase"`
	CreatedAt time.Time      `json:"createdAt"`
	UpdatedAt time.Time      `json:"updatedAt"`
	Document  *model.Machine `json:"document"`
	Status    Status         `json:"status"`
}

// Version records the actor and origin of a document revision.
type Version struct {
	Version      int            `json:"version"`
	CreatedAt    time.Time      `json:"createdAt"`
	CreatedBy    Actor          `json:"createdBy"`
	RestoredFrom int            `json:"restoredFrom"`
	Document     *model.Machine `json:"document"`
}

// PutResult identifies a committed document revision.
type PutResult struct {
	Version int  `json:"version"`
	Created bool `json:"created"`
	Changed bool `json:"changed"`
}

// Config supplies dependencies; platform permissions belong to deployment config.
type Config struct {
	MaintenancePreflight func(context.Context) error
	DB                   *store.DB
	Audit                *audit.Log
	Clock                clock.Clock
	Gate                 *maintenance.Gate
	Observer             Observer
	Power                PowerBackend
	Log                  *slog.Logger
	OnEvent              func(Event)
	RawRetention         time.Duration
	RollupRetention      time.Duration
	EventRetention       time.Duration
}

// Manager owns machine state, but starts no goroutines until Run is called.
type Manager struct {
	maintenancePreflight func(context.Context) error
	db                   *store.DB
	audit                *audit.Log
	clk                  clock.Clock
	gate                 *maintenance.Gate
	observer             Observer
	power                PowerBackend
	log                  *slog.Logger
	onEvent              func(Event)
	rawRetention         time.Duration
	rollupRetention      time.Duration
	eventRetention       time.Duration
	pollMu               sync.Mutex
	controlMu            sync.Mutex
}

func New(c Config) (*Manager, error) {
	if c.DB == nil || c.Audit == nil || c.Gate == nil {
		return nil, errors.New("machine: DB, Audit and Gate are required")
	}
	if c.Clock == nil {
		c.Clock = clock.Real()
	}
	if c.Log == nil {
		c.Log = slog.New(slog.DiscardHandler)
	}
	if c.RawRetention == 0 {
		c.RawRetention = 7 * 24 * time.Hour
	}
	if c.EventRetention == 0 {
		c.EventRetention = 90 * 24 * time.Hour
	}
	if c.RollupRetention == 0 {
		c.RollupRetention = 90 * 24 * time.Hour
	}
	if c.RawRetention <= 0 || c.EventRetention <= 0 || c.RollupRetention < c.RawRetention {
		return nil, errors.New("machine: invalid retention")
	}
	return &Manager{db: c.DB, audit: c.Audit, clk: c.Clock, gate: c.Gate, observer: c.Observer, power: c.Power, log: c.Log, onEvent: c.OnEvent, rawRetention: c.RawRetention, eventRetention: c.EventRetention, rollupRetention: c.RollupRetention, maintenancePreflight: c.MaintenancePreflight}, nil
}

func newID() string {
	var b [16]byte
	if _, err := rand.Read(b[:]); err != nil {
		panic(err)
	}
	b[6] = (b[6] & 0x0f) | 0x40
	b[8] = (b[8] & 0x3f) | 0x80
	h := hex.EncodeToString(b[:])
	return h[:8] + "-" + h[8:12] + "-" + h[12:16] + "-" + h[16:20] + "-" + h[20:]
}

func actor(ctx context.Context) (auth.Actor, error) {
	a, ok := auth.ActorFrom(ctx)
	if !ok || !a.Kind.Valid() || a.ID == "" {
		return auth.Actor{}, audit.ErrNoActor
	}
	return a, nil
}

func (m *Manager) publish(events []Event) {
	if m.onEvent != nil {
		for _, e := range events {
			m.onEvent(e)
		}
	}
}

func (m *Manager) record(ctx context.Context, action, name string, details map[string]any) error {
	if err := m.audit.Record(ctx, "machine."+action, name, audit.OutcomeOK, details); err != nil {
		return err
	}
	id, _ := details["machineId"].(string)
	if id == "" {
		r, err := m.Get(ctx, name)
		if err != nil {
			return err
		}
		id = r.ID
	}
	e := Event{MachineID: id, Source: "control", Key: newID(), Kind: "machine." + action, Severity: "info", OccurredAt: m.clk.Now().UTC(), IngestedAt: m.clk.Now().UTC()}
	if id, ok := details["operationId"].(string); ok {
		e.OperationID = id
	}
	err := m.db.Write(ctx, func(tx *sql.Tx) error { return insertEvent(ctx, tx, &e) })
	if err == nil {
		m.publish([]Event{e})
	}
	return err
}

func precondition(reason string) error { return fmt.Errorf("%w: %s", ErrPrecondition, reason) }

func encode(v any) (string, error) { b, err := json.Marshal(v); return string(b), err }

func insertEvent(ctx context.Context, tx *sql.Tx, e *Event) error {
	body, err := encode(e)
	if err != nil {
		return err
	}
	res, err := tx.ExecContext(ctx, `INSERT OR IGNORE INTO machine_events(machine_id,source,source_key,occurred_at,ingested_at,kind,body) VALUES(?,?,?,?,?,?,?)`, e.MachineID, e.Source, e.Key, e.OccurredAt.UnixMilli(), e.IngestedAt.UnixMilli(), e.Kind, body)
	if err != nil {
		return err
	}
	if n, _ := res.RowsAffected(); n == 0 {
		e.ID = 0
		return nil
	}
	e.ID, err = res.LastInsertId()
	if err == nil && e.Source == "control" {
		err = trimEvents(ctx, tx)
	}
	return err
}

func trimEvents(ctx context.Context, tx *sql.Tx) error {
	_, err := tx.ExecContext(ctx, `DELETE FROM machine_events WHERE id <= (SELECT MAX(id)-200000 FROM machine_events)`)
	return err
}
