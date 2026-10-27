package api

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/ACaccel/lcm/internal/auth"
)

// ingestRoutes are the only protected routes an ingest-scoped token may use.
var ingestRoutes = map[string]bool{
	"GET /api/v1/auth/me":                   true,
	"POST /api/v1/hooks/alerts/{source}":    true,
	"POST /api/v1/heartbeats/{svc}/{check}": true,
	"GET /api/v1/heartbeats/{svc}/{check}":  true,
	"POST /api/v1/hooks/github":             true,
	"POST /api/v1/hooks/{name}":             true,
}

// publicRoutes need no credentials at all.
var publicRoutes = map[string]bool{
	"GET /api/v1/health":       true,
	"GET /api/v1/openapi.yaml": true,
	"POST /api/v1/auth/login":  true,
}

// authSelfRoutes decide authentication in the handler (signature or token).
// They run no middleware check, so this test cannot probe them without
// components.
var authSelfRoutes = map[string]bool{
	"POST /api/v1/hooks/github": true,
	"POST /api/v1/hooks/{name}": true,
}

func routeRequest(e *env, pattern string, opts ...reqOpt) *httptest.ResponseRecorder {
	method, path, _ := strings.Cut(pattern, " ")
	for _, p := range []string{"{svc}", "{check}", "{action}", "{id}", "{p}", "{v}", "{key}", "{m}", "{name}", "{node}", "{source}"} {
		path = strings.ReplaceAll(path, p, "x")
	}
	return e.do(method, path, nil, opts...)
}

func TestIngestTokenReachesOnlyIngestRoutes(t *testing.T) {
	e := newEnv(t)
	_, ingest, err := e.auth.CreateToken(t.Context(), "hook", auth.ScopeIngest)
	if err != nil {
		t.Fatal(err)
	}
	_, admin, err := e.auth.CreateToken(t.Context(), "ci", auth.ScopeAdmin)
	if err != nil {
		t.Fatal(err)
	}
	cookie := e.login()

	routes := e.srv.Routes()
	if len(routes) < 50 {
		t.Fatalf("only %d routes registered", len(routes))
	}
	for _, pattern := range routes {
		switch {
		case publicRoutes[pattern]:
			continue
		case authSelfRoutes[pattern]:
			// The handlers refuse anonymous callers themselves; the
			// integration tests cover that with real components.
			continue
		}
		if w := routeRequest(e, pattern); w.Code != http.StatusUnauthorized {
			t.Errorf("%s anonymous = %d, want 401", pattern, w.Code)
		}
		w := routeRequest(e, pattern, withBearer(ingest))
		if ingestRoutes[pattern] {
			if w.Code == http.StatusUnauthorized || w.Code == http.StatusForbidden {
				t.Errorf("%s with ingest token = %d, want access", pattern, w.Code)
			}
			continue
		}
		if w.Code != http.StatusForbidden {
			t.Errorf("%s with ingest token = %d, want 403: %s", pattern, w.Code, w.Body.String())
		}
	}

	// Token management stays with sessions, whatever the scope.
	for name, tok := range map[string]string{"admin": admin, "ingest": ingest} {
		wantError(t, e.do("POST", "/api/v1/tokens", map[string]string{"name": "evil"}, withBearer(tok)), http.StatusForbidden, codeForbidden)
		wantError(t, e.do("GET", "/api/v1/tokens", nil, withBearer(tok)), http.StatusForbidden, codeForbidden)
		_ = name
	}
	if w := e.do("GET", "/api/v1/tokens", nil, withSession(cookie)); w.Code != http.StatusOK {
		t.Errorf("session token list = %d", w.Code)
	}
}

func TestTokenScopeOverAPI(t *testing.T) {
	e := newEnv(t)
	cookie := e.login()

	w := e.do("POST", "/api/v1/tokens", map[string]string{"name": "hook", "scope": "ingest"}, withSession(cookie))
	wantStatus(t, w, http.StatusCreated)
	created := decode[struct {
		Scope string `json:"scope"`
		Token string `json:"token"`
	}](t, w)
	if created.Scope != "ingest" {
		t.Fatalf("created = %+v", created)
	}
	// The default scope is admin.
	w = e.do("POST", "/api/v1/tokens", map[string]string{"name": "ci"}, withSession(cookie))
	wantStatus(t, w, http.StatusCreated)
	if s := decode[struct{ Scope string }](t, w).Scope; s != "admin" {
		t.Fatalf("default scope = %q", s)
	}
	wantError(t, e.do("POST", "/api/v1/tokens", map[string]string{"name": "bad", "scope": "root"}, withSession(cookie)),
		http.StatusUnprocessableEntity, codeValidation)

	list := decode[struct {
		Items []struct{ Name, Scope string } `json:"items"`
	}](t, e.do("GET", "/api/v1/tokens", nil, withSession(cookie)))
	if len(list.Items) != 2 || list.Items[0].Scope != "admin" || list.Items[1].Scope != "ingest" {
		t.Fatalf("list = %+v", list)
	}
	if got := e.findAudit("token.create"); got.Details["scope"] != "admin" {
		t.Errorf("audit details = %v", got.Details)
	}

	// The scoped token can identify itself but not read the audit log.
	wantStatus(t, e.do("GET", "/api/v1/auth/me", nil, withBearer(created.Token)), http.StatusOK)
	wantError(t, e.do("GET", "/api/v1/audit", nil, withBearer(created.Token)), http.StatusForbidden, codeForbidden)
}
