package api

import (
	"net/http"
	"testing"
)

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
