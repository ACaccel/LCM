// Package api serves the HTTP API under /api/v1.
package api

import (
	"context"
	"log/slog"
	"net/http"
	"sync"
	"time"

	"github.com/ACaccel/lcm/internal/audit"
	"github.com/ACaccel/lcm/internal/auth"
	"github.com/ACaccel/lcm/internal/catalog"
	"github.com/ACaccel/lcm/internal/configsync"
	"github.com/ACaccel/lcm/internal/engine"
	"github.com/ACaccel/lcm/internal/events"
	"github.com/ACaccel/lcm/internal/lock"
	"github.com/ACaccel/lcm/internal/modules"
	"github.com/ACaccel/lcm/internal/monitor"
	"github.com/ACaccel/lcm/internal/node"
	"github.com/ACaccel/lcm/internal/secret"
	"github.com/ACaccel/lcm/internal/state"
	"github.com/ACaccel/lcm/internal/store"
	"github.com/ACaccel/lcm/internal/trigger"
)

// Defaults for the login hardening settings.
const (
	defaultLoginFailDelay   = 500 * time.Millisecond
	defaultLoginMaxFailures = 10
	defaultLoginWindow      = 10 * time.Minute
)

// Deps are the collaborators and settings of a Server.
type Deps struct {
	DB      *store.DB
	Auth    *auth.Service
	Audit   *audit.Log
	Logger  *slog.Logger
	Version string

	// CookieSecure sets the Secure attribute on the session cookie.
	CookieSecure bool

	// Now is the time source; defaults to time.Now.
	Now func() time.Time

	// Login hardening. Zero values select the defaults (500ms delay after a
	// failure, 10 failures per client IP per 10 minutes).
	LoginFailDelay   time.Duration
	LoginMaxFailures int
	LoginWindow      time.Duration

	// Components behind the resource endpoints. Handlers of a resource
	// need its component; the foundation endpoints (health, auth, tokens,
	// audit) work without them.
	Catalog  *catalog.Catalog
	Manager  *catalog.Manager
	Monitor  *monitor.Monitor
	Engine   *engine.Engine
	Triggers *trigger.Manager
	Modules  *modules.Store
	State    *state.Store
	Locks    *lock.Manager
	Registry *node.Registry
	Secrets  secret.Source
	Events   *events.Hub
	// ConfigSync serves apply, export and the config source endpoints.
	ConfigSync *configsync.Syncer

	// Context is the application lifetime. Service actions started through
	// the API run on it rather than on the request, so they outlive the
	// HTTP call and stop when the application closes.
	Context context.Context

	// EventHeartbeat is the interval of keep-alive comments on the event
	// stream; zero selects 15 seconds.
	EventHeartbeat time.Duration
	// EventBuffer is the per-client event queue; zero selects 256.
	EventBuffer int

	// GitHubWebhookSecret names the secret that verifies GitHub push
	// webhooks; empty requires an API token for them instead.
	GitHubWebhookSecret string
}

// Server is the HTTP API.
type Server struct {
	db      *store.DB
	auth    *auth.Service
	audit   *audit.Log
	log     *slog.Logger
	version string

	cookieSecure   bool
	now            func() time.Time
	loginFailDelay time.Duration
	limiter        *failureLimiter
	cop            *http.CrossOriginProtection

	catalog  *catalog.Catalog
	manager  *catalog.Manager
	monitor  *monitor.Monitor
	engine   *engine.Engine
	triggers *trigger.Manager
	modules  *modules.Store
	state    *state.Store
	locks    *lock.Manager
	registry *node.Registry
	secrets  secret.Source
	hub      *events.Hub
	sync     *configsync.Syncer
	ctx      context.Context

	eventHeartbeat time.Duration
	eventBuffer    int
	githubSecret   string
	actions        *actionTracker
	// docMu serializes policy and module writes so that the If-Match check
	// and the save form one step.
	docMu sync.Mutex
	// patterns lists the registered route patterns, for the OpenAPI
	// coverage test.
	patterns []string
}

// New builds a Server.
func New(d Deps) *Server {
	s := &Server{
		db: d.DB, auth: d.Auth, audit: d.Audit, log: d.Logger, version: d.Version,
		cookieSecure: d.CookieSecure, now: d.Now, loginFailDelay: d.LoginFailDelay,
		cop:     http.NewCrossOriginProtection(),
		catalog: d.Catalog, manager: d.Manager, monitor: d.Monitor, engine: d.Engine, triggers: d.Triggers,
		modules: d.Modules, state: d.State, locks: d.Locks, registry: d.Registry, secrets: d.Secrets,
		hub: d.Events, sync: d.ConfigSync, ctx: d.Context, githubSecret: d.GitHubWebhookSecret,
		actions: newActionTracker(maxTrackedActions),
	}
	s.eventBuffer = d.EventBuffer
	if s.eventBuffer <= 0 {
		s.eventBuffer = defaultEventBuffer
	}
	s.eventHeartbeat = d.EventHeartbeat
	if s.eventHeartbeat <= 0 {
		s.eventHeartbeat = defaultEventHeartbeat
	}
	if s.ctx == nil {
		s.ctx = context.Background()
	}
	if s.log == nil {
		s.log = slog.New(slog.DiscardHandler)
	}
	if s.now == nil {
		s.now = time.Now
	}
	if s.loginFailDelay == 0 {
		s.loginFailDelay = defaultLoginFailDelay
	}
	maxFailures, window := d.LoginMaxFailures, d.LoginWindow
	if maxFailures == 0 {
		maxFailures = defaultLoginMaxFailures
	}
	if window == 0 {
		window = defaultLoginWindow
	}
	s.limiter = newFailureLimiter(s.now, maxFailures, window)
	return s
}

// Handler returns the full handler chain.
func (s *Server) Handler() http.Handler {
	h := s.routes()
	h = s.authenticate(h)
	h = s.crossOrigin(h)
	h = s.recoverPanic(h)
	h = s.accessLog(h)
	return withRequestID(h)
}

func contains(list []string, s string) bool {
	for _, v := range list {
		if v == s {
			return true
		}
	}
	return false
}

// record writes an audit entry. A failure is logged rather than failing the
// request because the audited action has already happened.
func (s *Server) record(ctx context.Context, action, target string, outcome audit.Outcome, details map[string]any) {
	if err := s.audit.Record(ctx, action, target, outcome, details); err != nil {
		s.log.Error("audit record failed", "action", action, "target", target, "error", err, "request_id", requestID(ctx))
	}
}

func (s *Server) internalError(w http.ResponseWriter, r *http.Request, err error) {
	s.log.Error("request failed", "error", err, "method", r.Method, "path", r.URL.Path, "request_id", requestID(r.Context()))
	writeError(w, http.StatusInternalServerError, codeInternal, "internal server error", nil)
}
