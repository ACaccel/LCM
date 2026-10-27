package api

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"log/slog"
	"net"
	"net/http"
	"runtime/debug"
	"strings"
	"time"

	"github.com/ACaccel/lcm/internal/audit"
	"github.com/ACaccel/lcm/internal/auth"
)

const (
	headerRequestID = "X-Request-Id"
	sessionCookie   = "lcm_session"
)

type ctxKey int

const (
	requestIDKey ctxKey = iota
	requestStateKey
)

// requestState lets inner middleware report facts to the access log.
type requestState struct {
	actor auth.Actor
	known bool
}

func requestID(ctx context.Context) string {
	id, _ := ctx.Value(requestIDKey).(string)
	return id
}

// statusRecorder captures the response status for logging and panic recovery.
type statusRecorder struct {
	http.ResponseWriter
	status int
}

func (w *statusRecorder) WriteHeader(code int) {
	if w.status == 0 {
		w.status = code
	}
	w.ResponseWriter.WriteHeader(code)
}

func (w *statusRecorder) Write(b []byte) (int, error) {
	if w.status == 0 {
		w.status = http.StatusOK
	}
	return w.ResponseWriter.Write(b)
}

// Unwrap exposes the underlying writer to http.ResponseController.
func (w *statusRecorder) Unwrap() http.ResponseWriter { return w.ResponseWriter }

// withRequestID tags the request with a client-supplied or generated ID.
func withRequestID(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		id := r.Header.Get(headerRequestID)
		if !validRequestID(id) {
			var b [8]byte
			// crypto/rand.Read does not fail on supported platforms.
			_, _ = rand.Read(b[:])
			id = hex.EncodeToString(b[:])
		}
		w.Header().Set(headerRequestID, id)
		next.ServeHTTP(w, r.WithContext(context.WithValue(r.Context(), requestIDKey, id)))
	})
}

func validRequestID(id string) bool {
	if id == "" || len(id) > 64 {
		return false
	}
	for _, c := range id {
		switch {
		case c >= 'a' && c <= 'z', c >= 'A' && c <= 'Z', c >= '0' && c <= '9', c == '-', c == '_', c == '.':
		default:
			return false
		}
	}
	return true
}

// accessLog emits one structured line per request. It sits outside recovery
// so that recovered panics are logged with their 500 status.
func (s *Server) accessLog(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		start := time.Now()
		state := &requestState{}
		rec := &statusRecorder{ResponseWriter: w}
		next.ServeHTTP(rec, r.WithContext(context.WithValue(r.Context(), requestStateKey, state)))

		status := rec.status
		if status == 0 {
			status = http.StatusOK
		}
		attrs := []any{
			"method", r.Method,
			"path", r.URL.Path,
			"status", status,
			"duration", time.Since(start),
			"request_id", requestID(r.Context()),
		}
		if state.known {
			attrs = append(attrs, "actor_kind", string(state.actor.Kind), "actor", state.actor.Name)
		}
		level := slog.LevelInfo
		if status >= 500 {
			level = slog.LevelError
		}
		s.log.Log(r.Context(), level, "request", attrs...)
	})
}

// recoverPanic turns a handler panic into a 500 envelope.
func (s *Server) recoverPanic(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		defer func() {
			rec := recover()
			if rec == nil {
				return
			}
			if rec == http.ErrAbortHandler { //nolint:errorlint // recover() returns a value, not a wrapped error
				panic(rec)
			}
			s.log.Error("panic in handler",
				"panic", rec, "stack", string(debug.Stack()),
				"method", r.Method, "path", r.URL.Path, "request_id", requestID(r.Context()))
			if sr, ok := w.(*statusRecorder); ok && sr.status != 0 {
				return // response already started; nothing sensible left to send
			}
			writeError(w, http.StatusInternalServerError, codeInternal, "internal server error", nil)
		}()
		next.ServeHTTP(w, r)
	})
}

// crossOrigin rejects cross-origin browser writes (CSRF). Non-browser
// clients, including those sending bearer tokens, send no Origin or
// Sec-Fetch-Site headers and pass.
func (s *Server) crossOrigin(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if err := s.cop.Check(r); err != nil {
			writeError(w, http.StatusForbidden, codeCrossOrigin, "cross-origin request rejected", nil)
			return
		}
		next.ServeHTTP(w, r)
	})
}

// authenticate resolves the caller from a bearer token or session cookie and
// stores the actor in the request context. Missing or invalid credentials
// leave the request anonymous; handlers decide whether that is acceptable.
func (s *Server) authenticate(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		actor, ok, err := s.resolveActor(r)
		if err != nil {
			s.internalError(w, r, err)
			return
		}
		if ok {
			if st, _ := r.Context().Value(requestStateKey).(*requestState); st != nil {
				st.actor, st.known = actor, true
			}
			r = r.WithContext(auth.WithActor(r.Context(), actor))
		}
		next.ServeHTTP(w, r)
	})
}

func (s *Server) resolveActor(r *http.Request) (auth.Actor, bool, error) {
	if h := r.Header.Get("Authorization"); h != "" {
		scheme, token, found := strings.Cut(h, " ")
		if !found || !strings.EqualFold(scheme, "Bearer") {
			return auth.Actor{}, false, nil
		}
		actor, err := s.auth.VerifyToken(r.Context(), strings.TrimSpace(token))
		switch {
		case err == nil:
			return actor, true, nil
		case errors.Is(err, auth.ErrUnauthenticated):
			return auth.Actor{}, false, nil
		}
		return auth.Actor{}, false, err
	}
	if c, err := r.Cookie(sessionCookie); err == nil {
		user, err := s.auth.Session(r.Context(), c.Value)
		switch {
		case err == nil:
			return user.Actor(), true, nil
		case errors.Is(err, auth.ErrUnauthenticated):
			return auth.Actor{}, false, nil
		}
		return auth.Actor{}, false, err
	}
	return auth.Actor{}, false, nil
}

// requireAny allows any authenticated actor.
func (s *Server) requireAny(h http.HandlerFunc) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if _, ok := auth.ActorFrom(r.Context()); !ok {
			writeError(w, http.StatusUnauthorized, codeUnauthenticated, "authentication required", nil)
			return
		}
		h(w, r)
	}
}

// requireSession allows only logged-in users, not API tokens. A token
// attempting the action is audited under action when it is non-empty.
func (s *Server) requireSession(action string, h http.HandlerFunc) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		actor, ok := auth.ActorFrom(r.Context())
		if !ok {
			writeError(w, http.StatusUnauthorized, codeUnauthenticated, "authentication required", nil)
			return
		}
		if actor.Kind != auth.KindUser {
			if action != "" {
				s.record(r.Context(), action, "", audit.OutcomeDenied, map[string]any{"reason": "session required"})
			}
			writeError(w, http.StatusForbidden, codeForbidden, "this endpoint requires a logged-in session", nil)
			return
		}
		h(w, r)
	}
}

// clientIP returns the peer address. Forwarding headers are not trusted
// because LCM listens on loopback by default and has no proxy configuration.
func clientIP(r *http.Request) string {
	host, _, err := net.SplitHostPort(r.RemoteAddr)
	if err != nil {
		return r.RemoteAddr
	}
	return host
}
