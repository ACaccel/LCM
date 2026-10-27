// Package app is the composition root: it builds every component in
// dependency order, wires the adapters between them, runs the background
// loops and shuts everything down in reverse. Components never construct
// each other; this package is the only place that knows the whole graph.
package app

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"net"
	"net/http"
	"os"
	"sync"
	"sync/atomic"
	"time"

	"github.com/ACaccel/lcm/internal/api"
	"github.com/ACaccel/lcm/internal/audit"
	"github.com/ACaccel/lcm/internal/auth"
	"github.com/ACaccel/lcm/internal/buildinfo"
	"github.com/ACaccel/lcm/internal/catalog"
	"github.com/ACaccel/lcm/internal/clock"
	"github.com/ACaccel/lcm/internal/config"
	"github.com/ACaccel/lcm/internal/engine"
	"github.com/ACaccel/lcm/internal/execx"
	"github.com/ACaccel/lcm/internal/lock"
	"github.com/ACaccel/lcm/internal/monitor"
	"github.com/ACaccel/lcm/internal/node"
	"github.com/ACaccel/lcm/internal/nodes/core"
	"github.com/ACaccel/lcm/internal/nodes/flow"
	"github.com/ACaccel/lcm/internal/nodes/guard"
	"github.com/ACaccel/lcm/internal/nodes/lifecycle"
	"github.com/ACaccel/lcm/internal/nodes/network"
	"github.com/ACaccel/lcm/internal/nodes/notify"
	"github.com/ACaccel/lcm/internal/nodes/stateful"
	rt "github.com/ACaccel/lcm/internal/runtime"
	"github.com/ACaccel/lcm/internal/secret"
	"github.com/ACaccel/lcm/internal/signal"
	"github.com/ACaccel/lcm/internal/state"
	"github.com/ACaccel/lcm/internal/store"
	"github.com/ACaccel/lcm/internal/trigger"
)

// shutdownTimeout bounds the graceful stop of the HTTP server.
const shutdownTimeout = 10 * time.Second

// Option customizes New, mainly to substitute fakes in tests.
type Option func(*options)

type options struct {
	clock  clock.Clock
	log    *slog.Logger
	runner execx.Runner
	client *http.Client
	git    trigger.GitRunner

	observers []engine.Observer
}

// WithClock replaces the real clock in every component.
func WithClock(c clock.Clock) Option { return func(o *options) { o.clock = c } }

// WithLogger replaces the logger, which otherwise follows the
// configuration and writes to stderr.
func WithLogger(l *slog.Logger) Option { return func(o *options) { o.log = l } }

// WithRunner replaces the command runner behind runtime adapters, lifecycle
// commands and command-backed health checks.
func WithRunner(r execx.Runner) Option { return func(o *options) { o.runner = r } }

// WithHTTPClient replaces the HTTP client of the network, notify and
// flow nodes.
func WithHTTPClient(c *http.Client) Option { return func(o *options) { o.client = c } }

// WithGitRunner replaces the git remote reader of trigger.git.
func WithGitRunner(g trigger.GitRunner) Option { return func(o *options) { o.git = g } }

// WithObservers adds engine observers next to the ones the app installs.
func WithObservers(obs ...engine.Observer) Option {
	return func(o *options) { o.observers = append(o.observers, obs...) }
}

// App is a fully wired LCM process. The exported components are for the
// HTTP layer and tests; callers must not Close them individually.
type App struct {
	Config   config.Config
	DB       *store.DB
	Audit    *audit.Log
	Auth     *auth.Service
	Bus      *signal.Bus
	Secrets  *secret.Resolver
	State    *state.Store
	Locks    *lock.Manager
	Runtimes *rt.Registry
	Catalog  *catalog.Catalog
	Manager  *catalog.Manager
	Monitor  *monitor.Monitor
	Registry *node.Registry
	Engine   *engine.Engine
	Triggers *trigger.Manager
	Clock    clock.Clock

	log      *slog.Logger
	listener net.Listener
	server   *http.Server
	syncer   *monitorSyncer
	follower *catalog.Follower
	releaser *stateful.Releaser

	// ctx ends when Close starts, which stops loops that Run started.
	ctx    context.Context
	cancel context.CancelFunc
	// loops tracks the goroutines Run starts; Close waits for them.
	loops     sync.WaitGroup
	started   atomic.Bool
	closeOnce sync.Once
	closeErr  error
}

// New validates cfg, builds and recovers all components and binds the
// listen address. It starts no background loops other than the ones the
// components own (engine workers, timeline follower); Run starts the rest.
// On error everything built so far is released.
func New(ctx context.Context, cfg config.Config, opts ...Option) (_ *App, err error) {
	var o options
	for _, opt := range opts {
		opt(&o)
	}
	if err := cfg.Validate(); err != nil {
		return nil, fmt.Errorf("invalid configuration: %w", err)
	}
	if err := cfg.EnsureDataDir(); err != nil {
		return nil, fmt.Errorf("invalid configuration: %w", err)
	}
	if err := cfg.EnsureServeDirs(); err != nil {
		return nil, fmt.Errorf("invalid configuration: %w", err)
	}
	if o.clock == nil {
		o.clock = clock.Real()
	}
	if o.log == nil {
		o.log = cfg.NewLogger(os.Stderr)
	}
	if o.runner == nil {
		o.runner = execx.New()
	}
	log, clk := o.log, o.clock

	if !cfg.IsLoopback() {
		log.Warn("listening on a non-loopback address; the API is served over plain HTTP, put a TLS proxy in front",
			"listen", cfg.Listen, "cookie_secure", cfg.CookieSecure)
	}
	if missing := cfg.MissingBinaries(); len(missing) > 0 {
		log.Warn("configured programs not found; the runtimes that need them will fail", "missing", missing)
	}

	a := &App{Config: cfg, Clock: clk, log: log}
	a.ctx, a.cancel = context.WithCancel(context.Background())
	// Unwinding on a failed New uses the same path as Close, so a half-built
	// App can never leak a database handle or goroutine.
	defer func() {
		if err != nil {
			closeCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), shutdownTimeout)
			defer cancel()
			_ = a.Close(closeCtx)
		}
	}()

	// Binding first makes a port conflict fail before recovery restarts
	// runs or touches services.
	if a.listener, err = net.Listen("tcp", cfg.Listen); err != nil {
		return nil, fmt.Errorf("listen: %w", err)
	}

	if a.DB, err = store.Open(ctx, cfg.DBPath()); err != nil {
		return nil, err
	}
	ready := func(component string, attrs ...any) {
		log.Info("component ready", append([]any{"component", component}, attrs...)...)
	}
	ready("store", "path", cfg.DBPath())
	a.Audit = audit.New(a.DB, audit.WithClock(clk.Now))
	if a.Auth, err = auth.NewService(a.DB, cfg.SessionTTL, auth.WithClock(clk.Now)); err != nil {
		return nil, err
	}
	a.Bus = signal.NewBus(log.With("component", "signal"), clk.Now)
	a.Secrets = secret.New(cfg.SecretsPath(), secret.WithLogger(log.With("component", "secret")))
	a.State = state.New(a.DB, state.WithClock(clk.Now))
	a.Locks = lock.New(a.DB, lock.WithClock(clk), lock.WithAudit(a.Audit))
	ready("secrets", "dir", cfg.SecretsPath())

	a.Runtimes = rt.NewRegistry(o.runner, rt.Options{
		Systemctl: cfg.Systemctl, Privilege: cfg.Privilege(), Docker: cfg.Docker,
		StatusTimeout: cfg.RuntimeStatusTimeout, ActionTimeout: cfg.RuntimeActionTimeout,
	})

	// The catalog's change hook needs the syncer and the syncer needs the
	// catalog and monitor, so the hook indirects through a variable that is
	// set once the syncer exists. A change before that point is covered by
	// the initial sync in Run.
	var kick atomic.Pointer[monitorSyncer]
	a.Catalog, err = catalog.New(catalog.Config{
		DB: a.DB, Audit: a.Audit, Bus: a.Bus, Clock: clk, Log: log.With("component", "catalog"),
		OnChange: func() {
			if s := kick.Load(); s != nil {
				s.Kick()
			}
		},
	})
	if err != nil {
		return nil, err
	}
	ready("runtimes", "systemctl", cfg.Systemctl, "docker", cfg.Docker, "privilege", cfg.Privilege())
	a.Manager, err = catalog.NewManager(catalog.ManagerConfig{
		Catalog: a.Catalog, Runtimes: a.Runtimes, Runner: o.runner, Locker: lockAdapter{m: a.Locks},
		Log: log.With("component", "lifecycle"), LockTTL: cfg.LockTTL, Git: cfg.Git,
	})
	if err != nil {
		return nil, err
	}
	a.follower = a.Catalog.FollowSignals(a.ctx, a.Bus)
	ready("catalog")

	a.Monitor, err = monitor.New(ctx, monitor.Config{
		DB: a.DB, Bus: a.Bus, Logger: log.With("component", "monitor"), Clock: clk,
		Runner:      monitorRunner{run: o.runner, binaries: map[string]string{"systemctl": cfg.Systemctl, "docker": cfg.Docker}},
		Concurrency: cfg.MonitorConcurrency, RawRetention: cfg.MonitorRawRetention,
		RollupRetention: cfg.MonitorRollupRetention, PruneInterval: cfg.PruneInterval,
		AlertServiceLabels: cfg.AlertServiceLabels,
	})
	if err != nil {
		return nil, err
	}
	ready("monitor", "concurrency", cfg.MonitorConcurrency)
	a.syncer = newMonitorSyncer(a.Catalog, a.Monitor, log.With("component", "monitor"))
	kick.Store(a.syncer)

	a.Registry = node.NewRegistry()
	core.Register(a.Registry)
	lifecycle.Register(a.Registry, a.Manager)
	trigger.Register(a.Registry)
	flow.Register(a.Registry, flow.Deps{Client: o.client, Secrets: a.Secrets})
	guard.Register(a.Registry)
	network.Register(a.Registry, network.Deps{Client: o.client, Secrets: a.Secrets})
	notify.Register(a.Registry, notify.Deps{Client: o.client, Secrets: a.Secrets, State: a.State})
	stateful.Register(a.Registry, stateful.Deps{State: a.State, Locks: a.Locks})

	ready("nodes", "types", len(a.Registry.Schemas()))
	a.releaser = stateful.NewReleaser(a.Locks, clk, log.With("component", "locks"))
	// The trigger manager needs the engine, which needs the manager as an
	// observer: events that arrive before it exists are covered by its
	// first sync.
	var triggers lateObserver
	a.Engine, err = engine.New(engine.Config{
		DB: a.DB, Registry: a.Registry, Workers: cfg.EngineWorkers, Clock: clk, Audit: a.Audit,
		Logger:    log.With("component", "engine"),
		Observers: append([]engine.Observer{a.releaser, &triggers}, o.observers...),
		Vars:      []engine.Var{a.State.Var()},
	})
	if err != nil {
		return nil, err
	}
	git := o.git
	if git == nil {
		git = trigger.ExecGit{Binary: cfg.Git}
	}
	a.Triggers, err = trigger.NewManager(trigger.Config{
		Engine: a.Engine, DB: a.DB, Bus: a.Bus, Clock: clk, Git: git, Secrets: a.Secrets,
		CalendarDir: cfg.CalendarPath(), Logger: log.With("component", "trigger"),
	})
	if err != nil {
		return nil, err
	}
	triggers.set(a.Triggers)
	ready("engine", "policies", len(a.Engine.Plans()))

	// Reconcile first: it corrects lifecycle states that a crash left
	// untrue, and runs recovered by the engine act on those states at once.
	// Neither failure keeps the server down; both are logged and the rest
	// of the process stays usable.
	if changes, err := a.Manager.Reconcile(ctx); err != nil {
		log.Error("service reconciliation incomplete", "error", err)
	} else if len(changes) > 0 {
		log.Info("reconciled service states", "changes", len(changes))
	}
	if err := a.Engine.Recover(ctx); err != nil {
		log.Error("engine recovery incomplete", "error", err)
	}

	info := buildinfo.Get()
	a.server = &http.Server{
		Handler: api.New(api.Deps{
			DB: a.DB, Auth: a.Auth, Audit: a.Audit, Logger: log,
			Version: info.Version, CookieSecure: cfg.CookieSecure,
		}).Handler(),
		ReadHeaderTimeout: 10 * time.Second,
		ReadTimeout:       30 * time.Second,
		WriteTimeout:      60 * time.Second,
		IdleTimeout:       120 * time.Second,
		ErrorLog:          slog.NewLogLogger(log.Handler(), slog.LevelError),
	}
	return a, nil
}

// Addr returns the bound listen address.
func (a *App) Addr() net.Addr { return a.listener.Addr() }

// SyncMonitor applies the catalog's checks to the monitor now. The catalog
// change hook does this in the background; callers that need the checks
// scheduled before they continue use this.
func (a *App) SyncMonitor(ctx context.Context) error { return a.syncer.Sync(ctx) }

// Run starts the background loops and serves HTTP until ctx ends or the
// server fails, then stops them. It may be called once. Stopping the
// components themselves is Close's job.
func (a *App) Run(ctx context.Context) error {
	if !a.started.CompareAndSwap(false, true) {
		return errors.New("app: Run called twice")
	}
	if a.ctx.Err() != nil {
		return errors.New("app: closed")
	}
	runCtx, cancel := context.WithCancel(ctx)
	defer cancel()
	go func() {
		// Close during Run ends the loops as well.
		select {
		case <-a.ctx.Done():
			cancel()
		case <-runCtx.Done():
		}
	}()

	spawn := func(name string, fn func()) {
		a.loops.Add(1)
		go func() {
			defer a.loops.Done()
			defer func() {
				if r := recover(); r != nil {
					a.log.Error("background loop panicked", "loop", name, "panic", r)
				}
			}()
			fn()
		}()
	}
	a.log.Info("starting background loops", "loops", []string{"triggers", "lock releaser", "monitor sync", "retention"})
	spawn("triggers", func() { a.Triggers.Run(runCtx) })
	spawn("lock releaser", func() { a.releaser.Run(runCtx, a.Engine) })
	spawn("monitor sync", func() {
		every(runCtx, a.Clock, monitorSyncInterval, a.syncer.kick, a.syncer.syncQuietly)
	})
	spawn("retention", func() {
		every(runCtx, a.Clock, a.Config.PruneInterval, nil, a.retentionJobs)
	})

	serveErr := make(chan error, 1)
	go func() { serveErr <- a.server.Serve(a.listener) }()
	info := buildinfo.Get()
	a.log.Info("server started", "addr", a.listener.Addr().String(), "version", info.String(), "data_dir", a.Config.DataDir)

	var result error
	select {
	case err := <-serveErr:
		result = fmt.Errorf("serve: %w", err)
	case <-runCtx.Done():
		a.log.Info("shutting down", "timeout", shutdownTimeout)
		shutdownCtx, stop := context.WithTimeout(context.WithoutCancel(ctx), shutdownTimeout)
		defer stop()
		if err := a.server.Shutdown(shutdownCtx); err != nil {
			_ = a.server.Close()
			result = fmt.Errorf("shutdown: %w", err)
		} else if err := <-serveErr; err != nil && !errors.Is(err, http.ErrServerClosed) {
			result = fmt.Errorf("serve: %w", err)
		}
	}

	// HTTP is down; stop the loops (triggers stop starting runs) before the
	// caller closes the engine.
	cancel()
	a.loops.Wait()
	if result == nil {
		a.log.Info("server stopped")
	}
	return result
}

// Close releases everything in reverse build order: loops, HTTP listener,
// engine (in-flight nodes are cancelled when ctx ends first; their runs
// resume after the next start), monitor, timeline follower and finally the
// database. It is safe to call more than once and after a failed Run.
func (a *App) Close(ctx context.Context) error {
	a.closeOnce.Do(func() {
		a.cancel()
		a.loops.Wait()

		var errs []error
		if a.server != nil {
			if err := a.server.Close(); err != nil {
				errs = append(errs, fmt.Errorf("close http server: %w", err))
			}
		}
		if a.listener != nil {
			// Serve closes the listener; closing it again is only needed when
			// Run never started, and its error then is harmless.
			_ = a.listener.Close()
		}
		if a.Triggers != nil {
			a.Triggers.Close()
		}
		if a.Engine != nil {
			if err := a.Engine.Close(ctx); err != nil {
				errs = append(errs, fmt.Errorf("close engine: %w", err))
			}
		}
		if a.Monitor != nil {
			if err := a.Monitor.Close(); err != nil {
				errs = append(errs, fmt.Errorf("close monitor: %w", err))
			}
		}
		if a.follower != nil {
			a.follower.Close()
		}
		if a.DB != nil {
			if err := a.DB.Close(); err != nil {
				errs = append(errs, fmt.Errorf("close database: %w", err))
			}
		}
		a.closeErr = errors.Join(errs...)
	})
	return a.closeErr
}

// lateObserver forwards engine events to an observer that can only be
// created after the engine.
type lateObserver struct {
	target atomic.Pointer[trigger.Manager]
}

func (l *lateObserver) set(m *trigger.Manager) { l.target.Store(m) }

// Observe implements engine.Observer.
func (l *lateObserver) Observe(e engine.Event) {
	if m := l.target.Load(); m != nil {
		m.Observe(e)
	}
}
