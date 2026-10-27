package api

import (
	"context"
	"errors"
	"math"
	"net/http"
	"strconv"
	"time"

	"github.com/ACaccel/lcm/internal/audit"
	"github.com/ACaccel/lcm/internal/auth"
)

type actorDTO struct {
	Kind string `json:"kind"`
	ID   string `json:"id"`
	Name string `json:"name"`
}

func toActorDTO(a auth.Actor) actorDTO {
	return actorDTO{Kind: string(a.Kind), ID: a.ID, Name: a.Name}
}

// maxLoggedUsername bounds attacker-controlled text written to the audit log.
const maxLoggedUsername = 64

func (s *Server) handleHealth(w http.ResponseWriter, r *http.Request) {
	ctx, cancel := context.WithTimeout(r.Context(), 2*time.Second)
	defer cancel()
	if err := s.db.Ping(ctx); err != nil {
		s.log.Error("health check failed", "error", err, "request_id", requestID(r.Context()))
		writeError(w, http.StatusServiceUnavailable, codeUnavailable, "database unavailable", nil)
		return
	}
	writeJSON(w, http.StatusOK, map[string]string{"status": "ok", "version": s.version})
}

func (s *Server) handleLogin(w http.ResponseWriter, r *http.Request) {
	ip := clientIP(r)
	if retry, blocked := s.limiter.blocked(ip); blocked {
		s.log.Warn("login rate limited", "ip", ip, "request_id", requestID(r.Context()))
		secs := int(retry.Seconds()) + 1
		w.Header().Set("Retry-After", strconv.Itoa(secs))
		writeError(w, http.StatusTooManyRequests, codeRateLimited, "too many failed login attempts",
			map[string]any{"retryAfterSeconds": secs})
		return
	}

	var req struct {
		Username string `json:"username"`
		Password string `json:"password"`
	}
	if !decodeJSON(w, r, &req) {
		return
	}
	if req.Username == "" || req.Password == "" {
		writeError(w, http.StatusBadRequest, codeBadRequest, "username and password are required", nil)
		return
	}

	token, expires, user, err := s.auth.Login(r.Context(), req.Username, req.Password)
	if errors.Is(err, auth.ErrInvalidCredentials) {
		s.limiter.fail(ip)
		name := req.Username
		if len(name) > maxLoggedUsername {
			name = name[:maxLoggedUsername]
		}
		attempt := auth.WithActor(r.Context(), auth.Actor{Kind: auth.KindUser, Name: name})
		s.record(attempt, "auth.login", "", audit.OutcomeDenied, map[string]any{"ip": ip})
		select {
		case <-time.After(s.loginFailDelay):
		case <-r.Context().Done():
			return
		}
		writeError(w, http.StatusUnauthorized, codeInvalidCredentials, "invalid username or password", nil)
		return
	}
	if err != nil {
		s.internalError(w, r, err)
		return
	}

	s.limiter.reset(ip)
	ctx := auth.WithActor(r.Context(), user.Actor())
	s.record(ctx, "auth.login", "", audit.OutcomeOK, map[string]any{"ip": ip})
	http.SetCookie(w, s.newSessionCookie(token, expires, int(expires.Sub(s.now()).Seconds())))
	writeJSON(w, http.StatusOK, map[string]any{
		"actor":     toActorDTO(user.Actor()),
		"expiresAt": expires.UTC(),
	})
}

// newSessionCookie builds the session cookie. Secure follows configuration
// because the default deployment is plain HTTP on loopback.
func (s *Server) newSessionCookie(value string, expires time.Time, maxAge int) *http.Cookie {
	return &http.Cookie{ //nolint:gosec // Secure is configurable via LCM_COOKIE_SECURE
		Name:     sessionCookie,
		Value:    value,
		Path:     "/",
		Expires:  expires,
		MaxAge:   maxAge,
		HttpOnly: true,
		Secure:   s.cookieSecure,
		SameSite: http.SameSiteStrictMode,
	}
}

func (s *Server) handleLogout(w http.ResponseWriter, r *http.Request) {
	if c, err := r.Cookie(sessionCookie); err == nil {
		if err := s.auth.Logout(r.Context(), c.Value); err != nil {
			s.internalError(w, r, err)
			return
		}
	}
	s.record(r.Context(), "auth.logout", "", audit.OutcomeOK, nil)
	http.SetCookie(w, s.newSessionCookie("", time.Time{}, -1))
	w.WriteHeader(http.StatusNoContent)
}

func (s *Server) handleMe(w http.ResponseWriter, r *http.Request) {
	actor, _ := auth.ActorFrom(r.Context())
	writeJSON(w, http.StatusOK, map[string]any{"actor": toActorDTO(actor)})
}

type tokenDTO struct {
	ID         int64      `json:"id"`
	Name       string     `json:"name"`
	Scope      string     `json:"scope"`
	CreatedAt  time.Time  `json:"createdAt"`
	LastUsedAt *time.Time `json:"lastUsedAt"`
	RevokedAt  *time.Time `json:"revokedAt"`
}

func toTokenDTO(t auth.Token) tokenDTO {
	return tokenDTO{ID: t.ID, Name: t.Name, Scope: string(t.Scope), CreatedAt: t.CreatedAt, LastUsedAt: t.LastUsedAt, RevokedAt: t.RevokedAt}
}

func (s *Server) handleListTokens(w http.ResponseWriter, r *http.Request) {
	tokens, err := s.auth.ListTokens(r.Context())
	if err != nil {
		s.internalError(w, r, err)
		return
	}
	items := make([]tokenDTO, 0, len(tokens))
	for _, t := range tokens {
		items = append(items, toTokenDTO(t))
	}
	writeJSON(w, http.StatusOK, map[string]any{"items": items})
}

func (s *Server) handleCreateToken(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Name  string `json:"name"`
		Scope string `json:"scope"`
	}
	if !decodeJSON(w, r, &req) {
		return
	}
	tok, secret, err := s.auth.CreateToken(r.Context(), req.Name, auth.Scope(req.Scope))
	if errors.Is(err, auth.ErrInvalidName) {
		writeError(w, http.StatusUnprocessableEntity, codeValidation, err.Error(), map[string]any{"field": "name"})
		return
	}
	if errors.Is(err, auth.ErrInvalidScope) {
		writeError(w, http.StatusUnprocessableEntity, codeValidation, err.Error(), map[string]any{"field": "scope"})
		return
	}
	if err != nil {
		s.internalError(w, r, err)
		return
	}
	s.record(r.Context(), "token.create", "token:"+strconv.FormatInt(tok.ID, 10), audit.OutcomeOK,
		map[string]any{"name": tok.Name, "scope": string(tok.Scope)})
	resp := struct {
		tokenDTO
		Token string `json:"token"`
	}{toTokenDTO(tok), secret}
	writeJSON(w, http.StatusCreated, resp)
}

func (s *Server) handleRevokeToken(w http.ResponseWriter, r *http.Request) {
	id, err := strconv.ParseInt(r.PathValue("id"), 10, 64)
	if err != nil || id <= 0 {
		writeError(w, http.StatusNotFound, codeNotFound, "token not found", nil)
		return
	}
	err = s.auth.RevokeToken(r.Context(), id)
	if errors.Is(err, auth.ErrNotFound) {
		writeError(w, http.StatusNotFound, codeNotFound, "token not found", nil)
		return
	}
	if err != nil {
		s.internalError(w, r, err)
		return
	}
	s.record(r.Context(), "token.revoke", "token:"+strconv.FormatInt(id, 10), audit.OutcomeOK, nil)
	w.WriteHeader(http.StatusNoContent)
}

type auditDTO struct {
	ID      int64          `json:"id"`
	Time    time.Time      `json:"time"`
	Actor   actorDTO       `json:"actor"`
	Action  string         `json:"action"`
	Target  string         `json:"target"`
	Details map[string]any `json:"details"`
	Outcome string         `json:"outcome"`
}

func (s *Server) handleListAudit(w http.ResponseWriter, r *http.Request) {
	limit64, ok := intParam(w, r, "limit", audit.DefaultLimit, 1, audit.MaxLimit)
	if !ok {
		return
	}
	before, ok := intParam(w, r, "before", 0, 1, math.MaxInt64)
	if !ok {
		return
	}
	limit := int(limit64)
	entries, err := s.audit.List(r.Context(), limit, before)
	if err != nil {
		s.internalError(w, r, err)
		return
	}
	items := make([]auditDTO, 0, len(entries))
	for _, e := range entries {
		items = append(items, auditDTO{
			ID: e.ID, Time: e.Time, Actor: toActorDTO(e.Actor), Action: e.Action,
			Target: e.Target, Details: e.Details, Outcome: string(e.Outcome),
		})
	}
	var next *int64
	if len(entries) == limit {
		next = &entries[len(entries)-1].ID
	}
	writeJSON(w, http.StatusOK, map[string]any{"items": items, "nextBefore": next})
}

// intParam reads an optional integer query parameter within [lo, hi]. On a
// bad value it has already written a 400 response.
func intParam(w http.ResponseWriter, r *http.Request, name string, def, lo, hi int64) (int64, bool) {
	raw := r.URL.Query().Get(name)
	if raw == "" {
		return def, true
	}
	n, err := strconv.ParseInt(raw, 10, 64)
	if err != nil || n < lo || n > hi {
		writeError(w, http.StatusBadRequest, codeBadRequest, "invalid query parameter",
			map[string]any{"param": name, "min": lo, "max": hi})
		return 0, false
	}
	return n, true
}
