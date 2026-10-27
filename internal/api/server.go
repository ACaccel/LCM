// Package api serves the HTTP API under /api/v1.
package api

import (
	"context"
	"log/slog"
	"net/http"
	"sort"
	"strings"
	"time"

	"github.com/ACaccel/lcm/internal/audit"
	"github.com/ACaccel/lcm/internal/auth"
	"github.com/ACaccel/lcm/internal/store"
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
}

// New builds a Server.
func New(d Deps) *Server {
	s := &Server{
		db: d.DB, auth: d.Auth, audit: d.Audit, log: d.Logger, version: d.Version,
		cookieSecure: d.CookieSecure, now: d.Now, loginFailDelay: d.LoginFailDelay,
		cop: http.NewCrossOriginProtection(),
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

func (s *Server) routes() http.Handler {
	mux := http.NewServeMux()
	methods := map[string][]string{}
	handle := func(method, path string, h http.HandlerFunc) {
		mux.HandleFunc(method+" "+path, h)
		methods[path] = append(methods[path], method)
	}

	handle("GET", "/api/v1/health", s.handleHealth)
	handle("POST", "/api/v1/auth/login", s.handleLogin)
	handle("POST", "/api/v1/auth/logout", s.requireSession("", s.handleLogout))
	handle("GET", "/api/v1/auth/me", s.requireAny(s.handleMe))
	handle("GET", "/api/v1/tokens", s.requireSession("", s.handleListTokens))
	handle("POST", "/api/v1/tokens", s.requireSession("token.create", s.handleCreateToken))
	handle("DELETE", "/api/v1/tokens/{id}", s.requireSession("token.revoke", s.handleRevokeToken))
	handle("GET", "/api/v1/audit", s.requireAny(s.handleListAudit))

	// A method-less pattern on the same path is less specific than the
	// method patterns above, so it only catches the remaining methods.
	for path, ms := range methods {
		if contains(ms, "GET") {
			ms = append(ms, "HEAD")
		}
		sort.Strings(ms)
		mux.HandleFunc(path, methodNotAllowedHandler(strings.Join(ms, ", ")))
	}
	mux.HandleFunc("/api/v1/", notFoundHandler)
	return mux
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
