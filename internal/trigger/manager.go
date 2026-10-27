package trigger

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"sync"
	"time"

	"github.com/ACaccel/lcm/internal/clock"
	"github.com/ACaccel/lcm/internal/engine"
	"github.com/ACaccel/lcm/internal/secret"
	"github.com/ACaccel/lcm/internal/signal"
	"github.com/ACaccel/lcm/internal/store"
)

// DefaultSyncInterval is how often the manager reconciles its watchers with
// the engine's deployed policies. Deploy events trigger an immediate sync,
// but observer delivery is lossy, so the periodic pass is the safety net.
const DefaultSyncInterval = 30 * time.Second

// Starter is the part of the engine the manager needs.
type Starter interface {
	Plans() []*engine.Plan
	Start(ctx context.Context, policy, triggerNode string, event any, mode engine.Mode) (string, error)
}

// Config configures a Manager.
type Config struct {
	Engine Starter
	DB     *store.DB
	// Bus feeds trigger.signal; without it signal triggers do not start.
	Bus *signal.Bus
	// Clock defaults to the real clock.
	Clock clock.Clock
	// Git defaults to ExecGit.
	Git GitRunner
	// Secrets resolves webhook HMAC secrets; without it webhooks that
	// require a signature reject every delivery.
	Secrets secret.Source
	// CalendarDir, when set, confines trigger.cron calendars to relative
	// paths inside it.
	CalendarDir  string
	SyncInterval time.Duration
	Logger       *slog.Logger
}

// Manager keeps one watcher per trigger node of every deployed policy.
type Manager struct {
	cfg    Config
	clock  clock.Clock
	log    *slog.Logger
	git    GitRunner
	ctx    context.Context
	cancel context.CancelFunc
	wg     sync.WaitGroup
	kick   chan struct{}

	syncMu sync.Mutex // serializes Sync
	mu     sync.Mutex // guards watchers and failed
	// watchers are keyed by policy, trigger node and plan version, so a new
	// version replaces its watchers and an unchanged one keeps running.
	watchers map[watchKey]*running
	failed   map[watchKey]string

	gitMu       sync.Mutex
	gitWatchers map[gitKey]*gitWatcher
}

type watchKey struct {
	policy  string
	node    string
	version int
}

type running struct {
	w      watcher
	cancel context.CancelFunc
	done   chan struct{}
}

// watcher runs until its context ends.
type watcher interface {
	run(ctx context.Context)
}

// NewManager validates cfg and returns a Manager. Nothing runs until Run or
// Sync is called.
func NewManager(cfg Config) (*Manager, error) {
	if cfg.Engine == nil || cfg.DB == nil {
		return nil, errors.New("trigger: Engine and DB are required")
	}
	if cfg.Clock == nil {
		cfg.Clock = clock.Real()
	}
	if cfg.Logger == nil {
		cfg.Logger = slog.New(slog.DiscardHandler)
	}
	if cfg.SyncInterval <= 0 {
		cfg.SyncInterval = DefaultSyncInterval
	}
	m := &Manager{
		cfg: cfg, clock: cfg.Clock, log: cfg.Logger, git: cfg.Git,
		kick: make(chan struct{}, 1), watchers: map[watchKey]*running{}, failed: map[watchKey]string{},
	}
	if m.git == nil {
		m.git = ExecGit{}
	}
	m.ctx, m.cancel = context.WithCancel(context.Background())
	return m, nil
}

// Observe implements engine.Observer: deployments and removals trigger a
// sync. It never blocks.
func (m *Manager) Observe(e engine.Event) {
	if e.Kind == engine.EventPolicyDeployed || e.Kind == engine.EventPolicyUndeployed {
		select {
		case m.kick <- struct{}{}:
		default:
		}
	}
}

// Run syncs, then re-syncs on deploy events and every SyncInterval, until
// ctx ends; it then stops all watchers. Call it after Engine.Recover so
// that restored policies are visible.
func (m *Manager) Run(ctx context.Context) {
	defer m.Close()
	for {
		if err := m.Sync(ctx); err != nil && ctx.Err() == nil {
			m.log.Warn("trigger sync failed", "error", err)
		}
		timer := make(chan struct{})
		t := m.clock.AfterFunc(m.cfg.SyncInterval, func() { close(timer) })
		select {
		case <-ctx.Done():
			t.Stop()
			return
		case <-m.kick:
			t.Stop()
		case <-timer:
		}
	}
}

// Close stops every watcher and waits for them. It is safe to call twice.
func (m *Manager) Close() {
	m.cancel()
	m.wg.Wait()
}

// Sync starts watchers for new trigger nodes and stops those whose policy
// version changed or was undeployed.
func (m *Manager) Sync(ctx context.Context) error {
	m.syncMu.Lock()
	defer m.syncMu.Unlock()
	if m.ctx.Err() != nil {
		return errors.New("trigger: manager closed")
	}

	type want struct {
		info engine.TriggerInfo
		plan *engine.Plan
	}
	desired := map[watchKey]want{}
	deployed := map[string]bool{}
	for _, plan := range m.cfg.Engine.Plans() {
		deployed[plan.Name()] = true
		for _, ti := range plan.Triggers() {
			desired[watchKey{plan.Name(), ti.ID, plan.Version()}] = want{ti, plan}
		}
	}

	m.mu.Lock()
	var stale []*running
	var gone []watchKey
	for k, r := range m.watchers {
		if _, ok := desired[k]; !ok {
			stale = append(stale, r)
			delete(m.watchers, k)
			gone = append(gone, k)
		}
	}
	for k := range m.failed {
		if _, ok := desired[k]; !ok {
			delete(m.failed, k)
		}
	}
	m.mu.Unlock()
	for _, r := range stale {
		r.cancel()
		<-r.done
	}
	// Progress of a policy that is no longer deployed must not turn into a
	// catch-up burst if it is deployed again much later.
	forgotten := map[string]bool{}
	for _, k := range gone {
		if !deployed[k.policy] && !forgotten[k.policy] {
			forgotten[k.policy] = true
			if err := m.forgetState(ctx, k.policy); err != nil {
				m.log.Warn("forget trigger state failed", "policy", k.policy, "error", err)
			}
		}
	}

	for k, w := range desired {
		m.mu.Lock()
		_, exists := m.watchers[k]
		m.mu.Unlock()
		if exists {
			continue
		}
		wt, err := m.newWatcher(k, w.info)
		if err != nil {
			m.noteFailure(k, err)
			continue
		}
		if wt == nil {
			continue
		}
		wctx, cancel := context.WithCancel(m.ctx)
		r := &running{w: wt, cancel: cancel, done: make(chan struct{})}
		m.mu.Lock()
		m.watchers[k] = r
		delete(m.failed, k)
		m.mu.Unlock()
		m.wg.Go(func() {
			defer close(r.done)
			wt.run(wctx)
		})
	}
	return nil
}

func (m *Manager) noteFailure(k watchKey, err error) {
	m.mu.Lock()
	prev := m.failed[k]
	m.failed[k] = err.Error()
	m.mu.Unlock()
	if prev != err.Error() {
		m.log.Error("trigger cannot start; will retry", "policy", k.policy, "node", k.node, "error", err)
	}
}

// Watching returns the number of running watchers.
func (m *Manager) Watching() int {
	m.mu.Lock()
	defer m.mu.Unlock()
	return len(m.watchers)
}

func (m *Manager) newWatcher(k watchKey, ti engine.TriggerInfo) (watcher, error) {
	base := baseWatcher{m: m, policy: k.policy, node: k.node, log: m.log.With("policy", k.policy, "trigger", k.node, "type", ti.Type)}
	switch p := ti.Props.(type) {
	case SignalProps:
		if m.cfg.Bus == nil {
			return nil, errors.New("no signal bus configured")
		}
		return newSignalWatcher(base, p), nil
	case GitProps:
		return newGitWatcher(base, p), nil
	case CronProps:
		return newCronWatcher(base, p)
	case WebhookProps:
		return nil, nil // passive: Deliver finds the node in the current plans
	}
	return nil, nil
}

// baseWatcher carries what every watcher needs.
type baseWatcher struct {
	m      *Manager
	policy string
	node   string
	log    *slog.Logger
}

// fire starts a run and reports whether the event was consumed. Dropped
// events count as consumed (the engine stored them as dropped runs); a
// paused or undeployed policy does not, so watchers that track progress can
// deliver the event once the policy runs again.
func (b baseWatcher) fire(ctx context.Context, event any) (runID string, consumed bool) {
	id, err := b.m.cfg.Engine.Start(ctx, b.policy, b.node, event, engine.ModeLive)
	switch {
	case err == nil:
		b.log.Info("run started", "run", id)
		return id, true
	case errors.Is(err, engine.ErrDropped):
		b.log.Info("event dropped: a run is active", "run", id)
		return id, true
	case errors.Is(err, engine.ErrPolicyPaused), errors.Is(err, engine.ErrPolicyNotDeployed):
		b.log.Debug("event not started", "reason", err)
		return "", false
	case ctx.Err() != nil:
		return "", false
	default:
		b.log.Warn("start failed", "error", err)
		return "", false
	}
}

func (b baseWatcher) loadState(ctx context.Context) (map[string]any, error) {
	return b.m.loadState(ctx, b.policy, b.node)
}

func (b baseWatcher) saveState(ctx context.Context, data map[string]any) error {
	return b.m.saveState(ctx, b.policy, b.node, data)
}

// sleep waits for d on the manager clock or until ctx ends.
func (m *Manager) sleep(ctx context.Context, d time.Duration) bool {
	done := make(chan struct{})
	t := m.clock.AfterFunc(d, func() { close(done) })
	select {
	case <-done:
		return true
	case <-ctx.Done():
		t.Stop()
		return false
	}
}

func (m *Manager) loadState(ctx context.Context, policy, nodeID string) (map[string]any, error) {
	var raw string
	err := m.cfg.DB.Reader().QueryRowContext(ctx,
		`SELECT data FROM trigger_state WHERE policy = ? AND node_id = ?`, policy, nodeID).Scan(&raw)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("trigger: load state: %w", err)
	}
	var data map[string]any
	if err := json.Unmarshal([]byte(raw), &data); err != nil {
		// A corrupt row is treated as absent; the watcher rewrites it.
		m.log.Warn("trigger state is unreadable; starting fresh", "policy", policy, "node", nodeID)
		return nil, nil
	}
	return data, nil
}

func (m *Manager) saveState(ctx context.Context, policy, nodeID string, data map[string]any) error {
	raw, err := json.Marshal(data)
	if err != nil {
		return fmt.Errorf("trigger: encode state: %w", err)
	}
	err = m.cfg.DB.Write(ctx, func(tx *sql.Tx) error {
		_, err := tx.ExecContext(ctx,
			`INSERT INTO trigger_state (policy, node_id, data, updated_at) VALUES (?, ?, ?, ?)
			 ON CONFLICT (policy, node_id) DO UPDATE SET data = excluded.data, updated_at = excluded.updated_at`,
			policy, nodeID, string(raw), m.clock.Now().UnixMilli())
		return err
	})
	if err != nil {
		return fmt.Errorf("trigger: save state: %w", err)
	}
	return nil
}

func (m *Manager) forgetState(ctx context.Context, policy string) error {
	return m.cfg.DB.Write(ctx, func(tx *sql.Tx) error {
		_, err := tx.ExecContext(ctx, `DELETE FROM trigger_state WHERE policy = ?`, policy)
		return err
	})
}

func numberAt(data map[string]any, key string) (int64, bool) {
	switch v := data[key].(type) {
	case float64:
		return int64(v), true
	case int64:
		return v, true
	}
	return 0, false
}
