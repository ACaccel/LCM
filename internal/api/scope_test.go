package api_test

import (
	"net/http"
	"net/http/httptest"
	"sort"
	"strings"
	"testing"
)

const testPassword = "correct horse battery staple"

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

// authSelfRoutes decide authentication in the handler (signature or token)
// and run no middleware check, so they are not probed here; their tests
// cover the cases that matter.
var authSelfRoutes = map[string]bool{
	"POST /api/v1/hooks/github": true,
	"POST /api/v1/hooks/{name}": true,
}

// documentedRoutes lists "METHOD /api/v1/path" for every operation in the
// OpenAPI document, which a test keeps equal to the registered routes.
func documentedRoutes(t *testing.T) []string {
	t.Helper()
	var out []string
	for _, p := range loadSpec(t).paths {
		for method := range p.item {
			switch method {
			case "get", "post", "put", "delete", "patch":
				out = append(out, strings.ToUpper(method)+" /api/v1"+p.template)
			}
		}
	}
	sort.Strings(out)
	return out
}

// probe sends a request for a route pattern, with every path parameter
// filled with "x", straight to the handler. It does not check the response
// against the OpenAPI document because the probes deliberately hit error
// paths.
func (x *site) probe(pattern string, opts ...opt) *httptest.ResponseRecorder {
	method, path, _ := strings.Cut(pattern, " ")
	for strings.Contains(path, "{") {
		i, j := strings.Index(path, "{"), strings.Index(path, "}")
		path = path[:i] + "x" + path[j+1:]
	}
	r := httptest.NewRequest(method, path, nil)
	for _, o := range opts {
		o(r)
	}
	w := httptest.NewRecorder()
	x.handler.ServeHTTP(w, r)
	return w
}

// login returns the session cookie of the admin user.
func (x *site) login() *http.Cookie {
	x.t.Helper()
	if _, err := x.app.Auth.SetPassword(x.ctx, "admin", testPassword); err != nil {
		x.t.Fatal(err)
	}
	req := httptest.NewRequest("POST", v1+"/auth/login", strings.NewReader(`{"username":"admin","password":"`+testPassword+`"}`))
	w := httptest.NewRecorder()
	x.handler.ServeHTTP(w, req)
	for _, c := range w.Result().Cookies() {
		if c.Name == "lcm_session" {
			return c
		}
	}
	x.t.Fatal("no session cookie")
	return nil
}

func withSession(c *http.Cookie) opt { return func(r *http.Request) { r.AddCookie(c) } }

func TestIngestTokenReachesOnlyIngestRoutes(t *testing.T) {
	x := newSite(t)

	routes := documentedRoutes(t)
	if len(routes) < 50 {
		t.Fatalf("only %d routes documented", len(routes))
	}
	for _, pattern := range routes {
		switch {
		case publicRoutes[pattern], authSelfRoutes[pattern]:
			continue
		}
		if w := x.probe(pattern); w.Code != http.StatusUnauthorized {
			t.Errorf("%s anonymous = %d, want 401", pattern, w.Code)
		}
		w := x.probe(pattern, bearer(x.ingest))
		if ingestRoutes[pattern] {
			if w.Code == http.StatusUnauthorized || w.Code == http.StatusForbidden || w.Code >= 500 {
				t.Errorf("%s with ingest token = %d, want access", pattern, w.Code)
			}
			continue
		}
		if w.Code != http.StatusForbidden {
			t.Errorf("%s with ingest token = %d, want 403: %s", pattern, w.Code, w.Body.String())
		}
	}

	// Token management stays with sessions, whatever the scope.
	cookie := x.login()
	for _, tok := range []string{x.admin, x.ingest} {
		x.call("POST", v1+"/tokens", map[string]string{"name": "evil"}, bearer(tok)).wantError(http.StatusForbidden, "forbidden")
		x.call("GET", v1+"/tokens", nil, bearer(tok)).wantError(http.StatusForbidden, "forbidden")
	}
	x.call("GET", v1+"/tokens", nil, bearer(""), withSession(cookie)).want(http.StatusOK)
}
