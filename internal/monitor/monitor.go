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

var serviceNameRe = regexp.MustCompile(`^[a-z0-9][a-z0-9-]{0,62}$`)

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
	// AlertServiceLabels name the labels that map an external alert to a
	// service; nil selects "lcm_service" and "service".
	AlertServiceLabels []string
}

// entry is one active check.
type entry struct {
	id      string
	service string
	check   model.Check // defaults applied
	fp      string
	built   Built
	timer   clock.Timer
	removed bool
	running bool

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
		id: e.id, service: e.service, name: e.check.Name, typ: e.check.Type, critical: e.check.IsCritical(),
		interval: e.check.Interval.D(), status: e.status, since: e.since, okRun: e.okRun, failRun: e.failRun,
		lastAt: e.lastAt, lastOK: e.lastOK, lastLatency: e.lastLatency, lastMessage: e.lastMessage, lastBeat: e.lastBeat,
	}
}

// service groups the checks of one service.
type service struct {
	name   string
	labels map[string]string
	// health is the stored view (stale statuses count as they were), the
	// one changes are detected and published on.
	health Health
	since  time.Time
	checks map[string]*entry // by check name
}

// Monitor runs health checks, derives service health and publishes
// changes. Create it with New and release it with Close.
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
	services     map[string]*service
	persisted    map[string]checkRow
	persistedSvc map[string]serviceRow
	pruneTimer   clock.Timer
	inflight     int
	idle         chan struct{}
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

	checks, services, err := loadState(ctx, cfg.DB.Reader())
	if err != nil {
		return nil, err
	}
	runCtx, cancel := context.WithCancel(context.Background())
	m := &Monitor{
		db: cfg.DB, bus: cfg.Bus, registry: cfg.Registry, env: Env{Runner: cfg.Runner}, clock: cfg.Clock,
		log: cfg.Logger, w: newWriter(cfg.DB, cfg.Logger),
		rawRetention: cfg.RawRetention, rollupRetention: cfg.RollupRetention, pruneInterval: cfg.PruneInterval, pruneChunk: defaultPruneChunk,
		startSpread: cfg.StartSpread, alertLabels: cfg.AlertServiceLabels,
		ctx: runCtx, cancel: cancel, sem: make(chan struct{}, cfg.Concurrency),
		checks: map[string]*entry{}, services: map[string]*service{},
		persisted: checks, persistedSvc: services,
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
// reported in the returned model.Issues (paths are "service/check..."), and
// a check that was running keeps running with its previous definition.
func (m *Monitor) Sync(ctx context.Context, defs []Def) error {
	m.mu.Lock()
	if m.closed {
		m.mu.Unlock()
		return ErrClosed
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
	seenSvc := map[string]bool{}
	var order []string
	for _, d := range defs {
		id := d.ID()
		seen[id] = true
		seenSvc[d.Service] = true
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
		delete(m.services[e.service].checks, e.check.Name)
		recs = append(recs, deleteCheckRec{id: id})
		touched[e.service] = true
	}

	for _, id := range order {
		w := desired[id]
		old := m.checks[id]
		if old != nil && old.fp == w.fp {
			continue
		}
		e := &entry{id: id, service: w.def.Service, check: w.check, fp: w.fp, built: w.built, status: StatusUnknown, since: now, anchor: now}
		switch {
		case old != nil:
			old.removed = true
			if old.timer != nil {
				old.timer.Stop()
			}
			e.restore(old)
		case m.persisted[id].id != "":
			e.restorePersisted(m.persisted[id], now)
		}
		delete(m.persisted, id)
		m.checks[id] = e
		svc := m.serviceLocked(w.def.Service, now)
		svc.checks[w.check.Name] = e
		touched[w.def.Service] = true
		e.timer = m.clock.AfterFunc(m.firstDelay(id, w.check.Interval.D()), func() { m.fire(e) })
		recs = append(recs, e.row())
	}

	// Labels follow the definitions even when no check changed.
	labels := map[string]map[string]string{}
	for _, id := range order {
		w := desired[id]
		if _, ok := labels[w.def.Service]; !ok {
			labels[w.def.Service] = w.def.Labels
		}
	}
	for name, l := range labels {
		svc := m.serviceLocked(name, now)
		if !equalLabels(svc.labels, l) {
			svc.labels = cloneLabels(l)
			touched[name] = true
		}
	}

	// Stored state of checks that vanished while the monitor was down.
	for id, row := range m.persisted {
		if !seen[id] {
			recs = append(recs, deleteCheckRec{id: id})
			touched[row.service] = true
		}
		delete(m.persisted, id)
	}

	var sigs []signal.Signal
	for name := range touched {
		svc := m.services[name]
		if svc == nil {
			continue
		}
		if len(svc.checks) == 0 {
			delete(m.services, name)
			recs = append(recs, deleteServiceRec{service: name})
			continue
		}
		sigs, recs = m.healthChangeLocked(svc, now, sigs, recs)
		recs = append(recs, svc.row(now))
	}
	// Stored services whose checks all disappeared while the monitor was
	// down. A service with only invalid definitions keeps its row.
	for name := range m.persistedSvc {
		if m.services[name] == nil && !seenSvc[name] {
			recs = append(recs, deleteServiceRec{service: name})
		}
		delete(m.persistedSvc, name)
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
