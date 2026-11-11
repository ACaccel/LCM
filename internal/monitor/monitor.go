package monitor

import (
	"context"
	"errors"
	"log/slog"
	"regexp"
	"sync"
	"time"

	"github.com/ACaccel/lcm/internal/clock"
	"github.com/ACaccel/lcm/internal/model"
	"github.com/ACaccel/lcm/internal/signal"
	"github.com/ACaccel/lcm/internal/store"
)

// Defaults for Config.
const (
	DefaultConcurrency     = 8
	DefaultRawRetention    = 7 * 24 * time.Hour
	DefaultRollupRetention = 90 * 24 * time.Hour
	DefaultPruneInterval   = time.Hour
	DefaultStartSpread     = 10 * time.Second
)

// Errors returned by Heartbeat.
var (
	ErrUnknownCheck = errors.New("monitor: unknown check")
	ErrNotHeartbeat = errors.New("monitor: check is not a heartbeat check")
	ErrClosed       = errors.New("monitor: closed")
)

// signalSource is the Source of signals published by the monitor.
const signalSource = "monitor"

// maxRun caps the consecutive-result counters.
const maxRun = 1 << 20

var resourceNameRe = regexp.MustCompile(`^[a-z0-9][a-z0-9-]{0,62}$`)

// Config configures a Monitor.
type Config struct {
	DB  *store.DB
	Bus *signal.Bus
	// Registry defaults to DefaultRegistry().
	Registry *Registry
	// Runner executes external commands; defaults to os/exec.
	Runner Runner
	// Clock defaults to the real clock.
	Clock  clock.Clock
	Logger *slog.Logger
	// Concurrency limits simultaneously running probes; 0 selects
	// DefaultConcurrency.
	Concurrency int
	// RawRetention and RollupRetention bound how long raw results and
	// one-minute rollups are kept; 0 selects the defaults.
	RawRetention    time.Duration
	RollupRetention time.Duration
	// PruneInterval is how often old data is deleted; 0 selects
	// DefaultPruneInterval.
	PruneInterval time.Duration
	// StartSpread bounds the deterministic delay before a check's first
	// run, which keeps a restart from starting every probe at once; 0
	// selects DefaultStartSpread and a negative value disables it.
	StartSpread time.Duration
	// AlertResourceLabels name the labels that map an external alert to a
	// resource; nil selects alert.DefaultResourceLabels.
	AlertResourceLabels []string
}

// entry is one active check.
type entry struct {
	id       string
	resource string
	check    model.Check // defaults applied
	fp       string
	built    Built
	timer    clock.Timer
	removed  bool
	running  bool
	// paused entries keep their place but are not scheduled and do not
	// count towards health.
	paused      bool
	pauseReason string

	status  Status
	since   time.Time
	okRun   int
	failRun int
	// stale marks a status restored from storage that is older than twice
	// the interval; it is cleared by the first fresh result.
	stale bool

	lastAt      time.Time
	lastOK      bool
	lastLatency time.Duration
	lastMessage string

	// lastBeat and anchor drive heartbeat checks: a beat is missing when
	// neither has been seen within interval plus grace.
	lastBeat time.Time
	anchor   time.Time
}

func (e *entry) row() checkRow {
	return checkRow{
		id: e.id, resource: e.resource, name: e.check.Name, typ: e.check.Type, critical: e.check.IsCritical(),
		interval: e.check.Interval.D(), status: e.status, since: e.since, okRun: e.okRun, failRun: e.failRun,
		lastAt: e.lastAt, lastOK: e.lastOK, lastLatency: e.lastLatency, lastMessage: e.lastMessage, lastBeat: e.lastBeat,
	}
}

// resource groups the checks and the native health of one resource. It
// exists while it has at least one check or native input.
type resource struct {
	name   string
	typ    string
	labels map[string]string
	// health is the stored view (stale statuses count as they were), the
	// one changes are detected and published on.
	health Health
	since  time.Time
	checks map[string]*entry // by check name
	// native is the health the resource type reported through SetNative;
	// nil when it reports none.
	native *NativeStatus
}

// Monitor runs health checks, combines them with native health into
// resource health and publishes changes. Create it with New and release it with Close.
type Monitor struct {
	db       *store.DB
	bus      *signal.Bus
	registry *Registry
	env      Env
	clock    clock.Clock
	log      *slog.Logger
	w        *writer

	rawRetention    time.Duration
	rollupRetention time.Duration
	pruneInterval   time.Duration
	pruneChunk      int
	startSpread     time.Duration
	alertLabels     []string

	ctx    context.Context
	cancel context.CancelFunc
	sem    chan struct{}

	mu           sync.Mutex
	closed       bool
	checks       map[string]*entry
	resources    map[string]*resource
	persisted    map[string]checkRow
	persistedRes map[string]resourceRow
	// gen counts Forget calls; forgotten maps a forgotten resource to the
	// generation of its Forget, so that a Sync built from an older snapshot
	// does not bring it back (see SyncAt).
	gen        uint64
	forgotten  map[string]uint64
	pruneTimer clock.Timer
	inflight   int
	idle       chan struct{}
}

// New creates a Monitor and loads the stored state. No check runs until
// Sync supplies definitions.
func New(ctx context.Context, cfg Config) (*Monitor, error) {
	if cfg.DB == nil || cfg.Bus == nil {
		return nil, errors.New("monitor: DB and Bus are required")
	}
	if cfg.Registry == nil {
		cfg.Registry = DefaultRegistry()
	}
	if cfg.Runner == nil {
		cfg.Runner = execRunner{}
	}
	if cfg.Clock == nil {
		cfg.Clock = clock.Real()
	}
	if cfg.Logger == nil {
		cfg.Logger = slog.Default()
	}
	if cfg.Concurrency <= 0 {
		cfg.Concurrency = DefaultConcurrency
	}
	cfg.RawRetention = orDefault(cfg.RawRetention, DefaultRawRetention)
	cfg.RollupRetention = orDefault(cfg.RollupRetention, DefaultRollupRetention)
	cfg.PruneInterval = orDefault(cfg.PruneInterval, DefaultPruneInterval)
	if cfg.StartSpread == 0 {
		cfg.StartSpread = DefaultStartSpread
	}
	if cfg.RollupRetention < cfg.RawRetention {
		return nil, errors.New("monitor: rollup retention must not be shorter than raw retention")
	}

	checks, resources, err := loadState(ctx, cfg.DB.Reader())
	if err != nil {
		return nil, err
	}
	runCtx, cancel := context.WithCancel(context.Background())
	m := &Monitor{
		db: cfg.DB, bus: cfg.Bus, registry: cfg.Registry, env: Env{Runner: cfg.Runner, Clock: cfg.Clock}, clock: cfg.Clock,
		log: cfg.Logger, w: newWriter(cfg.DB, cfg.Logger),
		rawRetention: cfg.RawRetention, rollupRetention: cfg.RollupRetention, pruneInterval: cfg.PruneInterval, pruneChunk: defaultPruneChunk,
		startSpread: cfg.StartSpread, alertLabels: cfg.AlertResourceLabels,
		ctx: runCtx, cancel: cancel, sem: make(chan struct{}, cfg.Concurrency),
		checks: map[string]*entry{}, resources: map[string]*resource{},
		persisted: checks, persistedRes: resources, forgotten: map[string]uint64{},
	}
	m.mu.Lock()
	m.pruneTimer = m.clock.AfterFunc(m.pruneInterval, m.pruneTick)
	m.mu.Unlock()
	return m, nil
}

func orDefault(v, def time.Duration) time.Duration {
	if v <= 0 {
		return def
	}
	return v
}

func (m *Monitor) beginLocked() {
	if m.inflight == 0 {
		m.idle = make(chan struct{})
	}
	m.inflight++
}

func (m *Monitor) end() {
	m.mu.Lock()
	m.inflight--
	if m.inflight == 0 {
		close(m.idle)
	}
	m.mu.Unlock()
}

// waitIdle blocks until no probe or prune is running.
func (m *Monitor) waitIdle(ctx context.Context) error {
	for {
		m.mu.Lock()
		if m.inflight == 0 {
			m.mu.Unlock()
			return nil
		}
		ch := m.idle
		m.mu.Unlock()
		select {
		case <-ch:
		case <-ctx.Done():
			return ctx.Err()
		}
	}
}

// Flush blocks until every running probe has finished and all results so
// far are stored and published. Tests use it as a barrier; Close uses it to
// drain.
func (m *Monitor) Flush(ctx context.Context) error {
	if err := m.waitIdle(ctx); err != nil {
		return err
	}
	if err := m.w.flush(ctx); err != nil && !errors.Is(err, errWriterClosed) {
		return err
	}
	return nil
}

// Close stops scheduling, waits for running probes and stores their
// results.
func (m *Monitor) Close() error {
	m.mu.Lock()
	if m.closed {
		m.mu.Unlock()
		return nil
	}
	m.closed = true
	for _, e := range m.checks {
		if e.timer != nil {
			e.timer.Stop()
		}
	}
	if m.pruneTimer != nil {
		m.pruneTimer.Stop()
	}
	m.mu.Unlock()
	m.cancel()
	_ = m.waitIdle(context.Background())
	m.w.close()
	return nil
}

// Sync replaces the active check set with defs. Checks whose definition is
// unchanged keep their state and schedule; changed checks keep their state
// but are rescheduled; checks no longer listed stop and are forgotten.
//
// Invalid definitions do not stop the rest from being applied: they are
// reported in the returned model.Issues (paths are "resource/check..."),
// and a check that was running keeps running with its previous definition.
//
// Native health (SetNative) is independent of the definitions: a resource
// without checks stays as long as its type reports native health.
func (m *Monitor) Sync(ctx context.Context, defs []Def) error {
	return m.SyncAt(ctx, m.Generation(), defs)
}

// Generation returns the current Forget generation. A caller that builds
// definitions from a snapshot reads it before taking the snapshot and
// passes it to SyncAt.
func (m *Monitor) Generation() uint64 {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.gen
}

// SyncAt is Sync for definitions taken from a snapshot made at generation
// gen. Definitions of a resource forgotten after gen are ignored, because
// the snapshot predates the change that made the resource leave
// observation; a snapshot made after the Forget applies normally.
func (m *Monitor) SyncAt(ctx context.Context, gen uint64, defs []Def) error {
	m.mu.Lock()
	if m.closed {
		m.mu.Unlock()
		return ErrClosed
	}
	for name, at := range m.forgotten {
		if at <= gen {
			delete(m.forgotten, name)
		}
	}
	if len(m.forgotten) > 0 {
		kept := make([]Def, 0, len(defs))
		for _, d := range defs {
			if _, stale := m.forgotten[d.Resource]; !stale {
				kept = append(kept, d)
			}
		}
		defs = kept
	}
	now := m.clock.Now()
	var issues model.Issues
	type wanted struct {
		def   Def
		check model.Check
		built Built
		fp    string
	}
	desired := map[string]wanted{}
	seen := map[string]bool{}
	seenRes := map[string]bool{}
	// Stored resources that had checks before the restart; the others were
	// native-only and wait for their type's first SetNative.
	hadChecks := map[string]bool{}
	for _, row := range m.persisted {
		hadChecks[row.resource] = true
	}
	var order []string
	for _, d := range defs {
		id := d.ID()
		seen[id] = true
		seenRes[d.Resource] = true
		if is := validateDef(d); len(is) > 0 {
			issues = append(issues, is...)
			continue
		}
		if _, dup := desired[id]; dup {
			issues = append(issues, model.Issue{Path: id, Message: "duplicate check"})
			continue
		}
		c := d.Check.WithDefaults()
		fp, err := fingerprint(c)
		if err != nil {
			issues = append(issues, model.Issue{Path: id, Message: err.Error()})
			continue
		}
		if d.Paused {
			// Pausing and resuming replace the entry like a changed
			// definition; the check itself is built from c alone.
			fp += " paused " + d.PauseReason
		}
		w := wanted{def: d, check: c, fp: fp}
		if old := m.checks[id]; old != nil && old.fp == fp {
			w.built = old.built
		} else {
			b, err := m.registry.Build(c, m.env)
			if err != nil {
				var is model.Issues
				if errors.As(err, &is) {
					for _, i := range is {
						issues = append(issues, model.Issue{Path: id + "." + i.Path, Message: i.Message})
					}
				} else {
					issues = append(issues, model.Issue{Path: id, Message: err.Error()})
				}
				continue
			}
			w.built = b
		}
		desired[id] = w
		order = append(order, id)
	}

	var recs []record
	touched := map[string]bool{}

	// Remove checks that are no longer listed. An invalid definition of a
	// running check retains it.
	for id, e := range m.checks {
		if _, keep := desired[id]; keep || seen[id] {
			continue
		}
		e.removed = true
		if e.timer != nil {
			e.timer.Stop()
		}
		delete(m.checks, id)
		delete(m.resources[e.resource].checks, e.check.Name)
		recs = append(recs, deleteCheckRec{id: id})
		touched[e.resource] = true
	}

	for _, id := range order {
		w := desired[id]
		old := m.checks[id]
		if old != nil && old.fp == w.fp {
			continue
		}
		e := &entry{id: id, resource: w.def.Resource, check: w.check, fp: w.fp, built: w.built, status: StatusUnknown, since: now, anchor: now,
			paused: w.def.Paused, pauseReason: w.def.PauseReason}
		switch {
		case old != nil:
			old.removed = true
			if old.timer != nil {
				old.timer.Stop()
			}
			e.restore(old)
			if old.paused && !e.paused {
				// What it reported before the pause says nothing now.
				e.status, e.since, e.okRun, e.failRun, e.stale = StatusUnknown, now, 0, 0, false
			}
		case m.persisted[id].id != "":
			e.restorePersisted(m.persisted[id], now)
		}
		delete(m.persisted, id)
		m.checks[id] = e
		r := m.resourceLocked(w.def.Resource, now)
		r.checks[w.check.Name] = e
		touched[w.def.Resource] = true
		if !e.paused {
			e.timer = m.clock.AfterFunc(m.firstDelay(id, w.check.Interval.D()), func() { m.fire(e) })
		}
		recs = append(recs, e.row())
	}

	// Labels and types follow the definitions even when no check changed.
	described := map[string]Def{}
	for _, id := range order {
		w := desired[id]
		if _, ok := described[w.def.Resource]; !ok {
			described[w.def.Resource] = w.def
		}
	}
	for name, d := range described {
		r := m.resourceLocked(name, now)
		if !equalLabels(r.labels, d.Labels) {
			r.labels = cloneLabels(d.Labels)
			touched[name] = true
		}
		if d.ResourceType != "" {
			r.typ = d.ResourceType
		}
	}

	// Stored state of checks that vanished while the monitor was down.
	for id, row := range m.persisted {
		if !seen[id] {
			recs = append(recs, deleteCheckRec{id: id})
			touched[row.resource] = true
		}
		delete(m.persisted, id)
	}

	var sigs []signal.Signal
	for name := range touched {
		r := m.resources[name]
		if r == nil {
			continue
		}
		if len(r.checks) == 0 && r.native == nil {
			delete(m.resources, name)
			recs = append(recs, deleteResourceRec{resource: name})
			continue
		}
		sigs, recs = m.healthChangeLocked(r, now, sigs, recs)
		recs = append(recs, r.row(now))
	}
	// Stored resources whose checks all disappeared while the monitor was
	// down. A resource with only invalid definitions keeps its row, and so
	// does a native-only one until its type reports again or clears it.
	for name := range m.persistedRes {
		if !hadChecks[name] {
			continue
		}
		if m.resources[name] == nil && !seenRes[name] {
			recs = append(recs, deleteResourceRec{resource: name})
		}
		delete(m.persistedRes, name)
	}

	var committed <-chan struct{}
	if len(recs) > 0 || len(sigs) > 0 {
		committed = m.commitLocked(sigs, recs)
	}
	m.mu.Unlock()

	if committed != nil {
		select {
		case <-committed:
		case <-ctx.Done():
			return ctx.Err()
		}
	}
	return issues.Err()
}
