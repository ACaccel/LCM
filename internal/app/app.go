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
	"path/filepath"
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
	"github.com/ACaccel/lcm/internal/configsync"
	"github.com/ACaccel/lcm/internal/connection"
	"github.com/ACaccel/lcm/internal/deployhelper"
	"github.com/ACaccel/lcm/internal/engine"
	"github.com/ACaccel/lcm/internal/events"
	"github.com/ACaccel/lcm/internal/execx"
	"github.com/ACaccel/lcm/internal/hostsetup"
	"github.com/ACaccel/lcm/internal/lock"
	"github.com/ACaccel/lcm/internal/machine"
	"github.com/ACaccel/lcm/internal/machine/power"
	"github.com/ACaccel/lcm/internal/maintenance"
	"github.com/ACaccel/lcm/internal/model"
	"github.com/ACaccel/lcm/internal/modules"
	"github.com/ACaccel/lcm/internal/monitor"
	"github.com/ACaccel/lcm/internal/node"
	"github.com/ACaccel/lcm/internal/nodes/core"
	"github.com/ACaccel/lcm/internal/nodes/flow"
	"github.com/ACaccel/lcm/internal/nodes/guard"
	"github.com/ACaccel/lcm/internal/nodes/lifecycle"
	"github.com/ACaccel/lcm/internal/nodes/network"
	"github.com/ACaccel/lcm/internal/nodes/notify"
	"github.com/ACaccel/lcm/internal/nodes/stateful"
	"github.com/ACaccel/lcm/internal/resource"
	"github.com/ACaccel/lcm/internal/retention"
	"github.com/ACaccel/lcm/internal/router"
	rt "github.com/ACaccel/lcm/internal/runtime"
	"github.com/ACaccel/lcm/internal/secret"
	"github.com/ACaccel/lcm/internal/security"
	"github.com/ACaccel/lcm/internal/securityhost"
	"github.com/ACaccel/lcm/internal/selfbackup"
	"github.com/ACaccel/lcm/internal/signal"
	"github.com/ACaccel/lcm/internal/state"
	"github.com/ACaccel/lcm/internal/store"
	"github.com/ACaccel/lcm/internal/trigger"
	"github.com/ACaccel/lcm/internal/webui"
)

// shutdownTimeout bounds the graceful stop of the HTTP server.
const shutdownTimeout = 10 * time.Second

// Option customizes New, mainly to substitute fakes in tests.
type Option func(*options)

type options struct {
	machineObserver machine.Observer
	machinePower    machine.PowerBackend
	deployHelper    rt.DeployHelper
	restoreSettle   time.Duration
	routerHTTP      *http.Client
	clock           clock.Clock
	log             *slog.Logger
	runner          execx.Runner
	client          *http.Client
	git             trigger.GitRunner
	connectionTrust connection.TrustCheck
	hostStatus      api.HostStatusFunc
	commit          string

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

// WithMachineObserver substitutes telemetry without reading the test host.
func WithMachineObserver(observer machine.Observer) Option {
	return func(o *options) { o.machineObserver = observer }
}

// WithMachinePower substitutes the privileged boundary for deterministic tests.
func WithMachinePower(backend machine.PowerBackend) Option {
	return func(o *options) { o.machinePower = backend }
}

// WithDeployHelper substitutes the deploy helper of managed services, which
// otherwise runs through its sudo wrapper when that is installed.
func WithDeployHelper(h rt.DeployHelper) Option {
	return func(o *options) { o.deployHelper = h }
}

// WithRestoreSettle sets how long a restored service gets to become active
// before the restore is undone; negative checks at once. Tests use it.
func WithRestoreSettle(d time.Duration) Option { return func(o *options) { o.restoreSettle = d } }

// WithRouterHTTPClient replaces the HTTP client that router clients use to
// reach router web interfaces.
func WithRouterHTTPClient(c *http.Client) Option { return func(o *options) { o.routerHTTP = c } }

// WithHTTPClient replaces the HTTP client of the network, notify and
// flow nodes.
func WithHTTPClient(c *http.Client) Option { return func(o *options) { o.client = c } }

// WithGitRunner replaces the git remote reader of trigger.git.
func WithGitRunner(g trigger.GitRunner) Option { return func(o *options) { o.git = g } }

// WithConnectionsTrust replaces the root ownership check of the
// connections file, for tests that cannot create root-owned files.
func WithConnectionsTrust(c connection.TrustCheck) Option {
	return func(o *options) { o.connectionTrust = c }
}

// WithCommit replaces the commit that the process reports as its own build,
// which the marker of "lcm host upgrade" is compared with.
func WithCommit(c string) Option { return func(o *options) { o.commit = c } }

// WithHostStatus replaces the read of the root-owned host files behind the
// host components endpoint, for tests that must not depend on the machine
// they run on.
func WithHostStatus(f api.HostStatusFunc) Option { return func(o *options) { o.hostStatus = f } }

// WithObservers adds engine observers next to the ones the app installs.
func WithObservers(obs ...engine.Observer) Option {
	return func(o *options) { o.observers = append(o.observers, obs...) }
}

// App is a fully wired LCM process. The exported components are for the
// HTTP layer and tests; callers must not Close them individually.
type App struct {
	Security     *security.Manager
	SecurityHost *securityhost.Client
	Machines     *machine.Manager
	// Routers observes and reboots router resources through Connections,
	// the host-configured connections.
	Routers     *router.Manager
	Connections *connection.Set
	Maintenance *maintenance.Gate
	// Resources holds the identity, documents and phase of every resource.
	Resources      *resource.Store
	controllerLock *os.File
	Config         config.Config
	DB             *store.DB
	Audit          *audit.Log
	Auth           *auth.Service
	Bus            *signal.Bus
	Events         *events.Hub
	Secrets        *secret.Resolver
	State          *state.Store
	Locks          *lock.Manager
	Runtimes       *rt.Registry
	Catalog        *catalog.Catalog
	Manager        *catalog.Manager
	Routes         *catalog.RouteSync
	Monitor        *monitor.Monitor
	Modules        *modules.Store
	Registry       *node.Registry
	Engine         *engine.Engine
	Triggers       *trigger.Manager
	// ConfigSync applies documents and syncs config sources (policy-as-code).
	ConfigSync *configsync.Syncer
	// Retention bounds document versions, finished operations and
	// failed-login audit entries across components.
	Retention *retention.Pruner
	// SelfBackup backs up LCM's own database and secrets; nil when
	// self-backup-keep is 0.
	SelfBackup *selfbackup.Backuper
	Clock      clock.Clock

	log      *slog.Logger
	listener net.Listener
	server   *http.Server
	// pprofListener and pprofServer exist only when profiling is enabled.
	pprofListener net.Listener
	pprofServer   *http.Server
	api           *api.Server
	syncer        *monitorSyncer
	follower      *catalog.Follower
	releaser      *stateful.Releaser

	// ctx ends when Close starts, which stops loops that Run started.
	ctx    context.Context
	cancel context.CancelFunc
	// loops tracks the goroutines Run starts; Close waits for them.
	loops            sync.WaitGroup
	started          atomic.Bool
	closeOnce        sync.Once
	closeErr         error
	recoveryDeferred atomic.Bool
	automationReady  chan struct{}
	automationOnce   sync.Once
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
	if o.hostStatus == nil {
		// Only the filesystem is set: status reads files and runs nothing.
		o.hostStatus = func() ([]hostsetup.ComponentStatus, error) {
			return hostsetup.Status(hostsetup.Host{FS: hostsetup.OS{}})
		}
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

	if a.controllerLock, err = lockController(cfg.DataDir); err != nil {
		return nil, err
	}

	// Binding first makes a port conflict fail before recovery restarts
	// runs or touches services.
	if a.listener, err = net.Listen("tcp", cfg.Listen); err != nil {
		return nil, fmt.Errorf("listen: %w", err)
	}

	if cfg.PprofListen != "" {
		if a.pprofListener, err = net.Listen("tcp", cfg.PprofListen); err != nil {
			return nil, fmt.Errorf("pprof listen: %w", err)
		}
		a.pprofServer = newPprofServer()
		log.Warn("pprof is enabled; it exposes runtime internals", "listen", a.pprofListener.Addr().String())
	}

	workDir, err := cfg.WorkPath()
	if err != nil {
		return nil, err
	}
	if a.DB, err = store.Open(ctx, cfg.DBPath(), store.WithGit(cfg.Git), store.WithWorkDir(workDir)); err != nil {
		return nil, err
	}
	ready := func(component string, attrs ...any) {
		log.Info("component ready", append([]any{"component", component}, attrs...)...)
	}
	ready("store", "path", cfg.DBPath())
	a.Audit = audit.New(a.DB, audit.WithClock(clk.Now))
	if a.Auth, err = auth.NewService(a.DB, a.Audit, cfg.SessionTTL, auth.WithClock(clk.Now)); err != nil {
		return nil, err
	}
	a.Bus = signal.NewBus(log.With("component", "signal"), clk.Now)
	a.Events = events.NewHub(log.With("component", "events"), clk.Now)
	a.Events.FollowBus(a.Bus)
	// Strict: a secret file other users can read is refused, not used.
	a.Secrets = secret.New(cfg.SecretsPath(), secret.WithStrict(), secret.WithLogger(log.With("component", "secret")))
	// Connections are host configuration: a broken file or an unresolvable
	// password secret stops the server instead of failing later.
	var connOpts []connection.LoadOption
	if o.connectionTrust != nil {
		connOpts = append(connOpts, connection.WithTrustCheck(o.connectionTrust))
	}
	if a.Connections, err = connection.Load(cfg.ConnectionsFile, a.Secrets, connOpts...); err != nil {
		return nil, fmt.Errorf("invalid configuration: %w", err)
	}
	a.State = state.New(a.DB, state.WithClock(clk.Now))
	if a.Retention, err = retention.New(retention.Config{
		DB: a.DB, Clock: clk, VersionKeep: cfg.VersionKeep, OperationAge: cfg.OperationRetention, FailedLoginKeep: cfg.FailedLoginKeep,
	}); err != nil {
		return nil, err
	}
	a.Locks = lock.New(a.DB, lock.WithClock(clk), lock.WithAudit(a.Audit))
	a.Maintenance = maintenance.New(a.DB)
	// The monitor comes first: the resource store validates every saved
	// document's checks against its check types, and the machine type feeds
	// it native health.
	a.Monitor, err = monitor.New(ctx, monitor.Config{
		DB: a.DB, Bus: a.Bus, Logger: log.With("component", "monitor"), Clock: clk,
		Runner:      monitorRunner{run: o.runner, binaries: map[string]string{"systemctl": cfg.Systemctl, "docker": cfg.Docker}},
		Concurrency: cfg.MonitorConcurrency, RawRetention: cfg.MonitorRawRetention,
		RollupRetention: cfg.MonitorRollupRetention, PruneInterval: cfg.PruneInterval,
		AlertResourceLabels: cfg.AlertResourceLabels,
	})
	if err != nil {
		return nil, err
	}
	ready("monitor", "concurrency", cfg.MonitorConcurrency)

	// The phase guard refuses to unmanage a resource a config source owns;
	// it reads ownership inside the phase change and needs no Syncer.
	a.Resources, err = resource.New(resource.Config{
		DB: a.DB, Gate: a.Maintenance, Audit: a.Audit, Clock: clk, Log: log.With("component", "resource"),
		Checks: a.Monitor, Phases: configsync.PhaseGuard{},
	})
	if err != nil {
		return nil, err
	}
	logMigrationWarnings(ctx, a.DB, log)

	observer := o.machineObserver
	if observer == nil {
		observer = machineObserver{runner: machineRunner{runner: o.runner, systemctl: cfg.Systemctl, journalctl: cfg.Journalctl}, clock: clk}
	}
	backend := o.machinePower
	if backend == nil {
		backend = machinePower{enabled: cfg.MachinePower, client: power.Client{Runner: o.runner}, runner: o.runner, pending: "/run/lcm-self-install.pending"}
	}
	a.Machines, err = machine.New(machine.Config{
		DB: a.DB, Resources: a.Resources, Native: a.Monitor, Audit: a.Audit, Clock: clk, Gate: a.Maintenance,
		Observer: observer, Power: backend, MaintenancePreflight: controllerUpdatePending, Log: log.With("component", "machine"),
		OnEvent: func(e machine.Event) { publishObservation(a.Events, a.Resources, e) },
	})
	if err != nil {
		return nil, err
	}
	a.Routers, err = router.New(router.Config{
		DB: a.DB, Resources: a.Resources, Audit: a.Audit, Gate: a.Maintenance, Connections: a.Connections,
		Secrets: a.Secrets, Native: a.Monitor, Clock: clk, Log: log.With("component", "router"), HTTP: o.routerHTTP,
		OnEvent: func(e resource.Event) { publishObservation(a.Events, a.Resources, e) },
	})
	if err != nil {
		return nil, err
	}
	a.syncer = newMonitorSyncer(a.Resources, a.Monitor, log.With("component", "monitor"))
	// Subscribed before anything writes. A committed resource change is
	// announced on the event stream before the control events the machine
	// type's hooks staged for it; it also wakes the router loops and the
	// monitor sync, which first forgets a resource that left observation.
	a.Resources.Subscribe(func(c resource.Change) {
		a.Events.ResourceChanged(c)
		a.Machines.Changed(c)
	})
	a.Resources.Subscribe(a.Routers.Changed)
	a.Resources.Subscribe(a.syncer.Changed)
	ready("connections", "count", len(a.Connections.List()))
	ready("secrets", "dir", cfg.SecretsPath())

	helper := o.deployHelper
	if helper == nil {
		helper = deployHelper{client: deployhelper.Client{Runner: o.runner}, wrapper: deployhelper.WrapperPath, workDir: workDir}
	}
	a.Runtimes = rt.NewRegistry(o.runner, rt.Options{
		Systemctl: cfg.Systemctl, Privilege: cfg.Privilege(), Docker: cfg.Docker,
		StatusTimeout: cfg.RuntimeStatusTimeout, ActionTimeout: cfg.RuntimeActionTimeout, Deploy: helper,
	})
	a.Catalog, err = catalog.New(catalog.Config{
		Gate: a.Maintenance, DB: a.DB, Audit: a.Audit, Resources: a.Resources, Bus: a.Bus, Clock: clk,
		Log: log.With("component", "catalog"), OnTimeline: a.Events.Timeline,
	})
	if err != nil {
		return nil, err
	}
	ready("runtimes", "systemctl", cfg.Systemctl, "docker", cfg.Docker, "privilege", cfg.Privilege())
	a.Manager, err = catalog.NewManager(catalog.ManagerConfig{
		Catalog: a.Catalog, Runtimes: a.Runtimes, Runner: o.runner, Locker: lockAdapter{m: a.Locks},
		Log: log.With("component", "lifecycle"), LockTTL: cfg.LockTTL, Git: cfg.Git, WorkDir: workDir, Monitor: a.Monitor,
		Deploy: helper, RestoreSettle: o.restoreSettle, OnInstances: func(string) { a.syncer.request() },
	})
	if err != nil {
		return nil, err
	}
	a.syncer.stopped = a.Manager.StoppedInstances
	// Hooks before anything writes: every save and phase change of a
	// service or machine passes through its type.
	a.Resources.RegisterHooks(model.ResourceService, a.Manager.Hooks())
	a.Resources.RegisterHooks(model.ResourceMachine, a.Machines.Hooks())
	a.Resources.RegisterHooks(model.ResourceRouter, a.Routers.Hooks())
	// A saved service document whose route changed is sent to the host in
	// the background (Run); the write itself never waits for the helper.
	a.Routes = catalog.NewRouteSync(a.Manager)
	a.Resources.Subscribe(a.Routes.Changed)
	a.follower = a.Catalog.FollowSignals(a.ctx, a.Bus)
	ready("catalog", "work", workDir)
	if cfg.SelfBackupKeep > 0 {
		if a.SelfBackup, err = selfbackup.New(selfbackup.Config{
			DB: a.DB, Dir: filepath.Join(cfg.DataDir, selfBackupDirName), SecretsDir: cfg.SecretsPath(), Keep: cfg.SelfBackupKeep,
			MinInterval: selfBackupInterval, Recorder: a.Catalog, Audit: a.Audit, Clock: clk, Log: log.With("component", "selfbackup"),
		}); err != nil {
			return nil, err
		}
	}

	if err = a.Machines.Reconcile(ctx); err != nil {
		return nil, fmt.Errorf("machine recovery: %w", err)
	}
	if err = a.Routers.Reconcile(ctx); err != nil {
		return nil, fmt.Errorf("router recovery: %w", err)
	}
	securityCfg := security.Config{DB: a.DB, Audit: a.Audit, Clock: clk, Enabled: cfg.SecurityEnabled, MachineID: cfg.SecurityMachineID}
	if cfg.SecurityEnabled && cfg.SecurityKonataURL != "" {
		securityCfg.Send = func(ctx context.Context, i security.Incident) error {
			status := "failed"
			if i.State == "resolved" {
				status = "succeeded"
			}
			_, sendErr := notify.SendKonata(ctx, notify.Deps{Client: o.client, Secrets: a.Secrets}, clk, cfg.SecurityKonataURL, cfg.SecurityTokenSecret, i.ID, notify.KonataMessage{
				Title:   "LCM security: " + i.Source + " / " + i.Severity,
				Summary: i.State + " | Machine " + i.MachineID + " | " + i.Service + " | " + i.Summary,
				Status:  status, OccurredAt: i.ObservedAt,
			})
			return sendErr
		}
	}
	if a.Security, err = security.New(securityCfg); err != nil {
		return nil, err
	}
	if cfg.SecurityMachineID != "" {
		if a.SecurityHost, err = securityhost.New(securityhost.Config{DB: a.DB, Audit: a.Audit, Clock: clk, MachineID: cfg.SecurityMachineID}); err != nil {
			return nil, err
		}
	}
	// One registration per resource type: its state, actions and phase
	// changes, and the optional observation and operation backends.
	resourceTypes := []api.ResourceType{
		{Type: a.Manager.Type()},
		{Type: a.Machines.Type(), Observations: machineObservations{m: a.Machines}, Operations: machineOperations{m: a.Machines}},
		{Type: a.Routers.Type(), Observations: routerObservations{m: a.Routers}, Operations: routerOperations{m: a.Routers}},
	}

	if a.Modules, err = modules.New(a.DB, a.Audit, clk, modules.WithGate(a.Maintenance)); err != nil {
		return nil, err
	}
	a.Registry = node.NewRegistry()
	core.Register(a.Registry)
	lifecycle.Register(a.Registry, a.Manager)
	trigger.Register(a.Registry, trigger.Deps{WorkDir: workDir})
	flow.Register(a.Registry, flow.Deps{Client: o.client, Secrets: a.Secrets})
	guard.Register(a.Registry)
	network.Register(a.Registry, network.Deps{Client: o.client, Secrets: a.Secrets})
	notify.Register(a.Registry, notify.Deps{Client: o.client, Secrets: a.Secrets, State: a.State})
	stateful.Register(a.Registry, stateful.Deps{State: a.State, Locks: a.Locks})

	ready("nodes", "types", len(a.Registry.Schemas()))
	a.releaser = stateful.NewReleaser(a.Locks, clk, log.With("component", "locks"))
	a.Engine, err = engine.New(engine.Config{
		Gate: a.Maintenance,
		DB:   a.DB, Registry: a.Registry, Modules: a.Modules, Workers: cfg.EngineWorkers, Clock: clk, Audit: a.Audit,
		Logger:    log.With("component", "engine"),
		Observers: []engine.Observer{a.releaser, a.Events.Observer()},
		Vars:      []engine.Var{a.State.Var(), resourceVar(a.Resources, stateTypes(resourceTypes), a.Monitor)},
	})
	if err != nil {
		return nil, err
	}
	git := o.git
	if git == nil {
		git = trigger.ExecGit{Binary: cfg.Git, Runner: o.runner, Clock: clk}
	}
	a.Triggers, err = trigger.NewManager(trigger.Config{
		Engine: a.Engine, DB: a.DB, Bus: a.Bus, Clock: clk, Git: git, Secrets: a.Secrets,
		CalendarDir: cfg.CalendarPath(), Logger: log.With("component", "trigger"),
	})
	if err != nil {
		return nil, err
	}
	// The trigger manager needs the engine, so it observes deployments from
	// here on; nothing has been deployed or recovered yet. Observers passed
	// as options come last, after every component's observer.
	a.Engine.AddObserver(a.Triggers)
	for _, ob := range o.observers {
		a.Engine.AddObserver(ob)
	}
	ready("engine", "policies", len(a.Engine.Plans()))

	// The sync loop starts in Run, after recovery, so that a pulled
	// document never races a recovering run.
	a.ConfigSync, err = configsync.New(configsync.Config{
		Resources: a.Resources, Gate: a.Maintenance,
		DB: a.DB, Audit: a.Audit, Catalog: a.Catalog, Modules: a.Modules, Engine: a.Engine,
		Checks: a.Monitor, Bus: a.Bus, Runner: o.runner, Git: cfg.Git, Dir: cfg.SourcesPath(),
		Clock: clk, Log: log.With("component", "configsync"),
	})
	if err != nil {
		return nil, err
	}
	ready("configsync", "dir", cfg.SourcesPath())

	// Reconcile first: it corrects lifecycle states that a crash left
	// untrue, and runs recovered by the engine act on those states at once.
	// Neither failure keeps the server down; both are logged and the rest
	// of the process stays usable.
	if changes, err := a.Manager.Reconcile(ctx); err != nil {
		log.Error("service reconciliation incomplete", "error", err)
	} else if len(changes) > 0 {
		log.Info("reconciled service states", "changes", len(changes))
	}
	commit := o.commit
	if commit == "" {
		commit = buildinfo.Get().Commit
	}
	reconcileHostUpgrade(ctx, log, a.Catalog, cfg.DataDir, commit)
	a.automationReady = make(chan struct{})
	if cfg.AutomationOff {
		// Neither interrupted runs nor deployed policies are restored, and
		// automationReady stays open, so triggers and config sync (and with
		// them self-deploy) never start.
		log.Warn("automation is off (" + config.EnvAutomation + "=off): policies, triggers and config sync do not run")
	} else if err := a.Engine.Recover(ctx); errors.Is(err, engine.ErrRecoveryDeferred) {
		a.recoveryDeferred.Store(true)
		log.Info("automation recovery deferred until maintenance exits")
	} else if err != nil {
		log.Error("engine recovery incomplete", "error", err)
		a.automationOnce.Do(func() { close(a.automationReady) })
	} else {
		a.automationOnce.Do(func() { close(a.automationReady) })
	}

	proxies, err := cfg.TrustedProxyPrefixes()
	if err != nil {
		return nil, fmt.Errorf("invalid configuration: %w", err)
	}
	info := buildinfo.Get()
	a.api = api.New(api.Deps{
		TrustedProxies: proxies,
		Machines:       a.Machines,
		Connections:    a.Connections,
		HostStatus:     withIngressStatus(o.hostStatus, helper),
		Resources:      a.Resources,
		ResourceTypes:  resourceTypes,
		Security:       a.Security,
		SecurityHost:   a.SecurityHost,
		DB:             a.DB, Auth: a.Auth, Audit: a.Audit, Logger: log,
		Version: info.Version, CookieSecure: cfg.CookieSecure, Now: clk.Now, Clock: clk,
		Catalog: a.Catalog, Manager: a.Manager, Monitor: a.Monitor, Engine: a.Engine, Triggers: a.Triggers,
		Modules: a.Modules, State: a.State, Locks: a.Locks, Registry: a.Registry, Secrets: a.Secrets,
		ConfigSync: a.ConfigSync, Events: a.Events, Context: a.ctx, GitHubWebhookSecret: cfg.GitHubWebhookSecret,
	})
	// The API keeps its own middleware chain; the console is static files
	// and needs none of it.
	root := http.NewServeMux()
	root.Handle("/api/", a.api.Handler())
	root.Handle("/", webui.Handler())
	a.server = &http.Server{
		Handler:           root,
		ReadHeaderTimeout: 10 * time.Second,
		ReadTimeout:       30 * time.Second,
		WriteTimeout:      60 * time.Second,
		IdleTimeout:       120 * time.Second,
		ErrorLog:          slog.NewLogLogger(log.Handler(), slog.LevelError),
	}
	// Event streams never go idle on their own, so a graceful shutdown would
	// wait for them until its timeout; closing the hub ends them at once.
	a.server.RegisterOnShutdown(a.Events.Close)
	return a, nil
}

// Handler returns the HTTP handler of the whole server (API and web console),
// for tests that drive it without a listener.
func (a *App) Handler() http.Handler { return a.server.Handler }

// Addr returns the bound listen address.
func (a *App) Addr() net.Addr { return a.listener.Addr() }

// PprofAddr returns the address of the profiling listener, or nil when
// profiling is not enabled.
func (a *App) PprofAddr() net.Addr {
	if a.pprofListener == nil {
		return nil
	}
	return a.pprofListener.Addr()
}

// SyncMonitor applies the managed resources' checks to the monitor now. The
// resource change subscription does this in the background; callers that
// need the checks scheduled before they continue use this.
func (a *App) SyncMonitor(ctx context.Context) error {
	// Committed changes are delivered first, so that the monitor has
	// forgotten a resource that left observation before the sync.
	if err := a.Resources.Flush(ctx); err != nil {
		return err
	}
	return a.syncer.Sync(ctx)
}

// SyncRoutes sends the route options of changed service documents to the
// deploy helper now. The resource change subscription does this in the
// background; callers that need the outcome before they continue use this.
func (a *App) SyncRoutes(ctx context.Context) error {
	if err := a.Resources.Flush(ctx); err != nil {
		return err
	}
	a.Routes.Drain(ctx)
	return nil
}

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
	a.log.Info("starting background loops", "loops", []string{"triggers", "machines", "routers", "lock releaser", "monitor sync", "config sync", "retention", "reconcile", "ingress routes"})
	afterRecovery := func(fn func(context.Context)) {
		select {
		case <-runCtx.Done():
			return
		case <-a.automationReady:
			fn(runCtx)
		}
	}
	spawn("triggers", func() { afterRecovery(a.Triggers.Run) })
	spawn("machines", func() { a.Machines.Run(runCtx) })
	spawn("routers", func() { a.Routers.Run(runCtx) })
	if a.Config.SecurityEnabled {
		spawn("security observations", func() {
			every(runCtx, a.Clock, time.Minute, nil, func(ctx context.Context) {
				actorCtx := auth.WithActor(ctx, auth.Actor{Kind: auth.KindSystem, ID: "security", Name: "security"})
				if err := a.Security.Poll(actorCtx, a.Machines, a.Monitor); err != nil && ctx.Err() == nil {
					a.log.Warn("security observation reconciliation failed", "error", err)
				}
			})
		})
		spawn("security delivery", func() {
			every(runCtx, a.Clock, time.Minute, nil, func(ctx context.Context) {
				actorCtx := auth.WithActor(ctx, auth.Actor{Kind: auth.KindSystem, ID: "security", Name: "security"})
				if err := a.Security.Deliver(actorCtx); err != nil && ctx.Err() == nil {
					a.log.Warn("security delivery processing failed", "error", err)
				}
			})
		})
	}
	spawn("lock releaser", func() { a.releaser.Run(runCtx, a.Engine) })
	spawn("monitor sync", func() {
		every(runCtx, a.Clock, monitorSyncInterval, a.syncer.kick, a.syncer.syncQuietly)
	})
	spawn("config sync", func() { afterRecovery(a.ConfigSync.Run) })
	spawn("retention", func() {
		every(runCtx, a.Clock, a.Config.PruneInterval, nil, a.retentionJobs)
	})
	spawn("reconcile", func() {
		every(runCtx, a.Clock, reconcileInterval, nil, a.reconcileStates)
	})
	spawn("ingress routes", func() {
		// The first pass also retries routes the host refused or could not
		// take before this start.
		if err := a.Routes.EnqueueAll(runCtx); err != nil && runCtx.Err() == nil {
			a.log.Warn("queue routes failed", "error", err)
		}
		every(runCtx, a.Clock, reconcileInterval, a.Routes.Kick(), a.Routes.Drain)
	})

	serveErr := make(chan error, 1)
	go func() { serveErr <- a.server.Serve(a.listener) }()
	if a.pprofServer != nil {
		spawn("pprof", func() {
			if err := a.pprofServer.Serve(a.pprofListener); err != nil && !errors.Is(err, http.ErrServerClosed) {
				a.log.Error("pprof server stopped", "error", err)
			}
		})
	}
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

	if a.pprofServer != nil {
		_ = a.pprofServer.Close()
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
		if a.pprofServer != nil {
			_ = a.pprofServer.Close()
		}
		if a.pprofListener != nil {
			_ = a.pprofListener.Close()
		}
		if a.listener != nil {
			// Serve closes the listener; closing it again is only needed when
			// Run never started, and its error then is harmless.
			_ = a.listener.Close()
		}
		// Service actions started through the API were cancelled with a.ctx;
		// wait for them while the database is still open. Event streams end
		// when the hub closes.
		if a.api != nil {
			a.api.Close()
		}
		if a.Events != nil {
			a.Events.Close()
		}
		if a.Triggers != nil {
			a.Triggers.Close()
		}
		if a.ConfigSync != nil {
			a.ConfigSync.Close()
		}
		if a.Engine != nil {
			if err := a.Engine.Close(ctx); err != nil {
				errs = append(errs, fmt.Errorf("close engine: %w", err))
			}
		}
		// Queued resource changes still reach the monitor and the machine
		// type, which need the database.
		if a.Resources != nil {
			a.Resources.Close()
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
		if a.controllerLock != nil {
			if err := a.controllerLock.Close(); err != nil {
				a.closeErr = errors.Join(a.closeErr, err)
			}
		}
	})
	return a.closeErr
}
