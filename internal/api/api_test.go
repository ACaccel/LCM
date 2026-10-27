package api

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/ACaccel/lcm/internal/audit"
	"github.com/ACaccel/lcm/internal/auth"
	"github.com/ACaccel/lcm/internal/store"
	"github.com/ACaccel/lcm/internal/store/storetest"
)

const testPassword = "correct horse battery"

type env struct {
	t       *testing.T
	srv     *Server
	handler http.Handler
	auth    *auth.Service
	audit   *audit.Log
	logs    *bytes.Buffer
}

func newEnv(t *testing.T, mutate ...func(*Deps)) *env {
	t.Helper()
	db := storetest.Open(t)
	svc, err := auth.NewService(db, time.Hour)
	if err != nil {
		t.Fatal(err)
	}
	logs := &bytes.Buffer{}
	d := Deps{
		DB: db, Auth: svc, Audit: audit.New(db), Version: "test",
		Logger:         slog.New(slog.NewTextHandler(logs, nil)),
		LoginFailDelay: time.Nanosecond,
	}
	for _, m := range mutate {
		m(&d)
	}
	if _, err := svc.SetPassword(context.Background(), "admin", testPassword); err != nil {
		t.Fatal(err)
	}
	srv := New(d)
	return &env{t: t, srv: srv, handler: srv.Handler(), auth: svc, audit: d.Audit, logs: logs}
}

type reqOpt func(*http.Request)

func withHeader(k, v string) reqOpt { return func(r *http.Request) { r.Header.Set(k, v) } }
func withBearer(tok string) reqOpt  { return withHeader("Authorization", "Bearer "+tok) }
func withSession(c *http.Cookie) reqOpt {
	return func(r *http.Request) { r.AddCookie(c) }
}

func (e *env) do(method, path string, body any, opts ...reqOpt) *httptest.ResponseRecorder {
	e.t.Helper()
	var rd io.Reader
	switch b := body.(type) {
	case nil:
	case string:
		rd = strings.NewReader(b)
	default:
		raw, err := json.Marshal(b)
		if err != nil {
			e.t.Fatal(err)
		}
		rd = bytes.NewReader(raw)
	}
	r := httptest.NewRequest(method, path, rd)
	for _, o := range opts {
		o(r)
	}
	w := httptest.NewRecorder()
	e.handler.ServeHTTP(w, r)
	return w
}

func decode[T any](t *testing.T, w *httptest.ResponseRecorder) T {
	t.Helper()
	var v T
	if err := json.Unmarshal(w.Body.Bytes(), &v); err != nil {
		t.Fatalf("decode %q: %v", w.Body.String(), err)
	}
	return v
}

func wantStatus(t *testing.T, w *httptest.ResponseRecorder, status int) {
	t.Helper()
	if w.Code != status {
		t.Fatalf("status = %d, want %d; body: %s", w.Code, status, w.Body.String())
	}
}

func wantError(t *testing.T, w *httptest.ResponseRecorder, status int, code string) {
	t.Helper()
	wantStatus(t, w, status)
	if ct := w.Header().Get("Content-Type"); ct != "application/json" {
		t.Errorf("Content-Type = %q", ct)
	}
	var raw map[string]map[string]any
	if err := json.Unmarshal(w.Body.Bytes(), &raw); err != nil {
		t.Fatalf("error body is not an envelope: %v: %s", err, w.Body.String())
	}
	body, ok := raw["error"]
	if !ok || len(raw) != 1 {
		t.Fatalf("envelope = %v", raw)
	}
	if body["code"] != code {
		t.Errorf("code = %v, want %s", body["code"], code)
	}
	if msg, _ := body["message"].(string); msg == "" {
		t.Error("empty message")
	}
	if _, ok := body["details"].(map[string]any); !ok {
		t.Errorf("details missing or not an object: %v", body["details"])
	}
}

type actorBody struct {
	Kind string `json:"kind"`
	ID   string `json:"id"`
	Name string `json:"name"`
}

// login performs a successful login and returns the session cookie.
func (e *env) login() *http.Cookie {
	e.t.Helper()
	w := e.do("POST", "/api/v1/auth/login", map[string]string{"username": "admin", "password": testPassword})
	wantStatus(e.t, w, http.StatusOK)
	for _, c := range w.Result().Cookies() {
		if c.Name == sessionCookie {
			return c
		}
	}
	e.t.Fatal("no session cookie")
	return nil
}

func (e *env) createToken(c *http.Cookie, name string) (id int64, secret string) {
	e.t.Helper()
	w := e.do("POST", "/api/v1/tokens", map[string]string{"name": name}, withSession(c))
	wantStatus(e.t, w, http.StatusCreated)
	body := decode[struct {
		ID    int64  `json:"id"`
		Token string `json:"token"`
	}](e.t, w)
	return body.ID, body.Token
}

func (e *env) auditEntries() []audit.Entry {
	e.t.Helper()
	entries, err := e.audit.List(context.Background(), 1000, 0)
	if err != nil {
		e.t.Fatal(err)
	}
	return entries
}

// findAudit returns the newest entry with the given action.
func (e *env) findAudit(action string) audit.Entry {
	e.t.Helper()
	for _, en := range e.auditEntries() {
		if en.Action == action {
			return en
		}
	}
	e.t.Fatalf("no audit entry for %s", action)
	return audit.Entry{}
}

func TestHealth(t *testing.T) {
	e := newEnv(t)
	w := e.do("GET", "/api/v1/health", nil)
	wantStatus(t, w, http.StatusOK)
	got := decode[map[string]string](t, w)
	if got["status"] != "ok" || got["version"] != "test" {
		t.Fatalf("body = %v", got)
	}
}

func TestHealthReportsUnavailableDB(t *testing.T) {
	var db *store.DB
	e := newEnv(t, func(d *Deps) { db = d.DB })
	if err := db.Close(); err != nil {
		t.Fatal(err)
	}
	wantError(t, e.do("GET", "/api/v1/health", nil), http.StatusServiceUnavailable, codeUnavailable)
}

func TestLoginOK(t *testing.T) {
	e := newEnv(t)
	w := e.do("POST", "/api/v1/auth/login", map[string]string{"username": "admin", "password": testPassword})
	wantStatus(t, w, http.StatusOK)
	body := decode[struct {
		Actor     actorBody `json:"actor"`
		ExpiresAt time.Time `json:"expiresAt"`
	}](t, w)
	if body.Actor.Kind != "user" || body.Actor.Name != "admin" || body.ExpiresAt.IsZero() {
		t.Fatalf("body = %+v", body)
	}
	var cookie *http.Cookie
	for _, c := range w.Result().Cookies() {
		if c.Name == "lcm_session" {
			cookie = c
		}
	}
	if cookie == nil {
		t.Fatal("no lcm_session cookie")
	}
	if !cookie.HttpOnly || cookie.SameSite != http.SameSiteStrictMode || cookie.Path != "/" || cookie.Secure || cookie.Value == "" {
		t.Errorf("cookie = %+v", cookie)
	}
	if got := e.findAudit("auth.login"); got.Outcome != audit.OutcomeOK || got.Actor.Kind != auth.KindUser || got.Actor.Name != "admin" {
		t.Errorf("audit = %+v", got)
	}
}

func TestLoginCookieSecure(t *testing.T) {
	e := newEnv(t, func(d *Deps) { d.CookieSecure = true })
	if c := e.login(); !c.Secure {
		t.Error("cookie must be Secure when configured")
	}
}

func TestLoginDenied(t *testing.T) {
	e := newEnv(t)
	for _, creds := range []map[string]string{
		{"username": "admin", "password": "wrong password!!"},
		{"username": "ghost", "password": testPassword},
	} {
		w := e.do("POST", "/api/v1/auth/login", creds)
		wantError(t, w, http.StatusUnauthorized, codeInvalidCredentials)
		if len(w.Result().Cookies()) != 0 {
			t.Error("cookie set on failed login")
		}
	}
	got := e.findAudit("auth.login")
	if got.Outcome != audit.OutcomeDenied || got.Actor.Kind != auth.KindUser || got.Actor.Name != "ghost" {
		t.Errorf("audit = %+v", got)
	}
	if strings.Contains(e.logs.String(), testPassword) {
		t.Error("password leaked into logs")
	}
}

func TestLoginBadRequests(t *testing.T) {
	e := newEnv(t)
	for _, body := range []string{``, `{`, `{"username":"admin"}`, `{"username":"a","password":"b","extra":1}`, `{"username":"a","password":"b"} x`} {
		wantError(t, e.do("POST", "/api/v1/auth/login", body), http.StatusBadRequest, codeBadRequest)
	}
}

func TestLoginRateLimited(t *testing.T) {
	e := newEnv(t)
	bad := map[string]string{"username": "admin", "password": "wrong password!!"}
	for range defaultLoginMaxFailures {
		wantError(t, e.do("POST", "/api/v1/auth/login", bad), http.StatusUnauthorized, codeInvalidCredentials)
	}
	// Even correct credentials are refused while the window is exhausted.
	w := e.do("POST", "/api/v1/auth/login", map[string]string{"username": "admin", "password": testPassword})
	wantError(t, w, http.StatusTooManyRequests, codeRateLimited)
	if n, err := strconv.Atoi(w.Header().Get("Retry-After")); err != nil || n <= 0 {
		t.Errorf("Retry-After = %q", w.Header().Get("Retry-After"))
	}
}

func TestLoginRateLimitWindowAndReset(t *testing.T) {
	now := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	e := newEnv(t, func(d *Deps) { d.Now = func() time.Time { return now } })
	bad := map[string]string{"username": "admin", "password": "wrong password!!"}
	for range defaultLoginMaxFailures {
		e.do("POST", "/api/v1/auth/login", bad)
	}
	wantStatus(t, e.do("POST", "/api/v1/auth/login", bad), http.StatusTooManyRequests)
	now = now.Add(defaultLoginWindow)
	wantStatus(t, e.do("POST", "/api/v1/auth/login", bad), http.StatusUnauthorized)

	// A successful login clears the counter.
	for range defaultLoginMaxFailures - 2 {
		e.do("POST", "/api/v1/auth/login", bad)
	}
	e.login()
	for range defaultLoginMaxFailures - 1 {
		wantStatus(t, e.do("POST", "/api/v1/auth/login", bad), http.StatusUnauthorized)
	}
}

func TestMe(t *testing.T) {
	e := newEnv(t)
	cookie := e.login()
	_, secret := e.createToken(cookie, "ci")

	w := e.do("GET", "/api/v1/auth/me", nil, withSession(cookie))
	wantStatus(t, w, http.StatusOK)
	if a := decode[struct{ Actor actorBody }](t, w).Actor; a.Kind != "user" || a.Name != "admin" || a.ID == "" {
		t.Errorf("session actor = %+v", a)
	}

	w = e.do("GET", "/api/v1/auth/me", nil, withBearer(secret))
	wantStatus(t, w, http.StatusOK)
	if a := decode[struct{ Actor actorBody }](t, w).Actor; a.Kind != "token" || a.Name != "ci" {
		t.Errorf("token actor = %+v", a)
	}

	wantError(t, e.do("GET", "/api/v1/auth/me", nil), http.StatusUnauthorized, codeUnauthenticated)
	wantError(t, e.do("GET", "/api/v1/auth/me", nil, withBearer("lcm_bogus")), http.StatusUnauthorized, codeUnauthenticated)
	wantError(t, e.do("GET", "/api/v1/auth/me", nil, withHeader("Authorization", "Basic abc")), http.StatusUnauthorized, codeUnauthenticated)
	wantError(t, e.do("GET", "/api/v1/auth/me", nil, withHeader("Cookie", sessionCookie+"=nope")), http.StatusUnauthorized, codeUnauthenticated)
}

func TestLogout(t *testing.T) {
	e := newEnv(t)
	cookie := e.login()
	w := e.do("POST", "/api/v1/auth/logout", nil, withSession(cookie))
	wantStatus(t, w, http.StatusNoContent)
	cleared := false
	for _, c := range w.Result().Cookies() {
		cleared = cleared || (c.Name == sessionCookie && c.MaxAge < 0)
	}
	if !cleared {
		t.Error("session cookie not cleared")
	}
	wantError(t, e.do("GET", "/api/v1/auth/me", nil, withSession(cookie)), http.StatusUnauthorized, codeUnauthenticated)
	wantError(t, e.do("POST", "/api/v1/auth/logout", nil), http.StatusUnauthorized, codeUnauthenticated)
	if got := e.findAudit("auth.logout"); got.Actor.Kind != auth.KindUser || got.Outcome != audit.OutcomeOK {
		t.Errorf("audit = %+v", got)
	}
}

func TestTokenCRUD(t *testing.T) {
	e := newEnv(t)
	cookie := e.login()

	wantError(t, e.do("GET", "/api/v1/tokens", nil), http.StatusUnauthorized, codeUnauthenticated)

	id, secret := e.createToken(cookie, "deploy")
	if !strings.HasPrefix(secret, "lcm_") {
		t.Fatalf("secret = %q", secret)
	}

	w := e.do("GET", "/api/v1/tokens", nil, withSession(cookie))
	wantStatus(t, w, http.StatusOK)
	if strings.Contains(w.Body.String(), secret) {
		t.Fatal("list leaks the token secret")
	}
	list := decode[struct {
		Items []struct {
			ID         int64      `json:"id"`
			Name       string     `json:"name"`
			LastUsedAt *time.Time `json:"lastUsedAt"`
			RevokedAt  *time.Time `json:"revokedAt"`
		} `json:"items"`
	}](t, w)
	if len(list.Items) != 1 || list.Items[0].ID != id || list.Items[0].Name != "deploy" || list.Items[0].RevokedAt != nil {
		t.Fatalf("list = %+v", list)
	}
	var raw map[string][]map[string]any
	if err := json.Unmarshal(w.Body.Bytes(), &raw); err != nil {
		t.Fatal(err)
	}
	if _, ok := raw["items"][0]["token"]; ok {
		t.Error("list items contain a token field")
	}

	// Token works, then stops after revocation.
	wantStatus(t, e.do("GET", "/api/v1/auth/me", nil, withBearer(secret)), http.StatusOK)
	wantStatus(t, e.do("DELETE", "/api/v1/tokens/"+strconv.FormatInt(id, 10), nil, withSession(cookie)), http.StatusNoContent)
	wantError(t, e.do("GET", "/api/v1/auth/me", nil, withBearer(secret)), http.StatusUnauthorized, codeUnauthenticated)

	list = decode[struct {
		Items []struct {
			ID         int64      `json:"id"`
			Name       string     `json:"name"`
			LastUsedAt *time.Time `json:"lastUsedAt"`
			RevokedAt  *time.Time `json:"revokedAt"`
		} `json:"items"`
	}](t, e.do("GET", "/api/v1/tokens", nil, withSession(cookie)))
	if list.Items[0].RevokedAt == nil || list.Items[0].LastUsedAt == nil {
		t.Errorf("after revoke: %+v", list.Items[0])
	}

	wantError(t, e.do("DELETE", "/api/v1/tokens/9999", nil, withSession(cookie)), http.StatusNotFound, codeNotFound)
	wantError(t, e.do("DELETE", "/api/v1/tokens/abc", nil, withSession(cookie)), http.StatusNotFound, codeNotFound)
	wantError(t, e.do("POST", "/api/v1/tokens", map[string]string{"name": " "}, withSession(cookie)), http.StatusUnprocessableEntity, codeValidation)
	wantError(t, e.do("POST", "/api/v1/tokens", `{"nom":"x"}`, withSession(cookie)), http.StatusBadRequest, codeBadRequest)
}

func TestTokenCannotManageTokens(t *testing.T) {
	e := newEnv(t)
	cookie := e.login()
	id, secret := e.createToken(cookie, "ci")

	wantError(t, e.do("POST", "/api/v1/tokens", map[string]string{"name": "evil"}, withBearer(secret)), http.StatusForbidden, codeForbidden)
	wantError(t, e.do("GET", "/api/v1/tokens", nil, withBearer(secret)), http.StatusForbidden, codeForbidden)
	wantError(t, e.do("DELETE", "/api/v1/tokens/"+strconv.FormatInt(id, 10), nil, withBearer(secret)), http.StatusForbidden, codeForbidden)
	wantError(t, e.do("POST", "/api/v1/auth/logout", nil, withBearer(secret)), http.StatusForbidden, codeForbidden)

	got := e.findAudit("token.create")
	if got.Actor.Kind != auth.KindToken || got.Actor.Name != "ci" || got.Outcome != audit.OutcomeDenied {
		// findAudit returns the newest; the denied attempt is newest.
		t.Errorf("audit = %+v", got)
	}
	// Only the legitimate token was created.
	list := decode[struct{ Items []struct{} }](t, e.do("GET", "/api/v1/tokens", nil, withSession(cookie)))
	if len(list.Items) != 1 {
		t.Errorf("tokens = %d, want 1", len(list.Items))
	}
}

func TestCrossOriginProtection(t *testing.T) {
	e := newEnv(t)
	cookie := e.login()
	body := map[string]string{"name": "x"}

	for name, opt := range map[string]reqOpt{
		"foreign Origin":         withHeader("Origin", "https://evil.example"),
		"Sec-Fetch-Site cross":   withHeader("Sec-Fetch-Site", "cross-site"),
		"Sec-Fetch-Site sameste": withHeader("Sec-Fetch-Site", "same-site"),
	} {
		w := e.do("POST", "/api/v1/tokens", body, withSession(cookie), opt)
		if w.Code != http.StatusForbidden {
			t.Errorf("%s: status = %d", name, w.Code)
			continue
		}
		wantError(t, w, http.StatusForbidden, codeCrossOrigin)
	}
	if got := len(decode[struct{ Items []struct{} }](t, e.do("GET", "/api/v1/tokens", nil, withSession(cookie))).Items); got != 0 {
		t.Errorf("cross-origin request created %d tokens", got)
	}

	for _, opt := range []reqOpt{
		withHeader("Origin", "http://example.com"),
		withHeader("Sec-Fetch-Site", "same-origin"),
		func(*http.Request) {},
	} {
		wantStatus(t, e.do("POST", "/api/v1/tokens", body, withSession(cookie), opt), http.StatusCreated)
	}

	// Safe methods are never blocked.
	wantStatus(t, e.do("GET", "/api/v1/tokens", nil, withSession(cookie), withHeader("Origin", "https://evil.example")), http.StatusOK)
}

func TestBearerRequestsPassCrossOriginProtection(t *testing.T) {
	e := newEnv(t)
	cookie := e.login()
	_, secret := e.createToken(cookie, "ci")
	// A non-browser client sends no Origin/Sec-Fetch-Site. The request must
	// get past CSRF protection and be judged by authorization instead.
	w := e.do("POST", "/api/v1/tokens", map[string]string{"name": "x"}, withBearer(secret))
	wantError(t, w, http.StatusForbidden, codeForbidden)
}

func TestErrorEnvelopeForUnknownRoutesAndMethods(t *testing.T) {
	e := newEnv(t)
	wantError(t, e.do("GET", "/api/v1/nope", nil), http.StatusNotFound, codeNotFound)
	wantError(t, e.do("GET", "/api/v1/tokens/1/extra", nil), http.StatusNotFound, codeNotFound)

	w := e.do("POST", "/api/v1/health", nil)
	wantError(t, w, http.StatusMethodNotAllowed, codeMethodNotAllowed)
	if got := w.Header().Get("Allow"); got != "GET, HEAD" {
		t.Errorf("Allow = %q", got)
	}
	w = e.do("PUT", "/api/v1/tokens/1", nil)
	wantError(t, w, http.StatusMethodNotAllowed, codeMethodNotAllowed)
	if got := w.Header().Get("Allow"); got != "DELETE" {
		t.Errorf("Allow = %q", got)
	}
}

func TestAuditEndpointAndActorKinds(t *testing.T) {
	e := newEnv(t)
	cookie := e.login()
	_, secret := e.createToken(cookie, "ci")                                           // actor: user
	e.do("POST", "/api/v1/tokens", map[string]string{"name": "x"}, withBearer(secret)) // actor: token (denied)
	if err := e.audit.Record(auth.WithActor(context.Background(), auth.CLI), "admin.set_password", "user:admin", audit.OutcomeOK, nil); err != nil {
		t.Fatal(err)
	}

	wantError(t, e.do("GET", "/api/v1/audit", nil), http.StatusUnauthorized, codeUnauthenticated)

	w := e.do("GET", "/api/v1/audit", nil, withBearer(secret))
	wantStatus(t, w, http.StatusOK)
	var resp struct {
		Items []struct {
			ID      int64          `json:"id"`
			Time    time.Time      `json:"time"`
			Actor   actorBody      `json:"actor"`
			Action  string         `json:"action"`
			Target  string         `json:"target"`
			Details map[string]any `json:"details"`
			Outcome string         `json:"outcome"`
		} `json:"items"`
		NextBefore *int64 `json:"nextBefore"`
	}
	if err := json.Unmarshal(w.Body.Bytes(), &resp); err != nil {
		t.Fatal(err)
	}
	if resp.NextBefore != nil {
		t.Errorf("nextBefore = %d on a short page", *resp.NextBefore)
	}
	kinds := map[string]string{}
	for _, it := range resp.Items {
		kinds[it.Action+"/"+it.Outcome] = it.Actor.Kind
	}
	want := map[string]string{
		"auth.login/ok":         "user",
		"token.create/ok":       "user",
		"token.create/denied":   "token",
		"admin.set_password/ok": "system",
	}
	for k, v := range want {
		if kinds[k] != v {
			t.Errorf("%s: actor kind = %q, want %q (all: %v)", k, kinds[k], v, kinds)
		}
	}
	for i := 1; i < len(resp.Items); i++ {
		if resp.Items[i].ID >= resp.Items[i-1].ID {
			t.Fatal("entries are not newest first")
		}
	}
	if strings.Contains(w.Body.String(), secret) {
		t.Error("audit output leaks a token secret")
	}
}

func TestAuditPaginationAndValidation(t *testing.T) {
	e := newEnv(t)
	cookie := e.login()
	for i := range 4 {
		e.createToken(cookie, "t"+strconv.Itoa(i))
	}
	type page struct {
		Items []struct {
			ID int64 `json:"id"`
		} `json:"items"`
		NextBefore *int64 `json:"nextBefore"`
	}
	p1 := decode[page](t, e.do("GET", "/api/v1/audit?limit=2", nil, withSession(cookie)))
	if len(p1.Items) != 2 || p1.NextBefore == nil || *p1.NextBefore != p1.Items[1].ID {
		t.Fatalf("page 1 = %+v", p1)
	}
	p2 := decode[page](t, e.do("GET", "/api/v1/audit?limit=2&before="+strconv.FormatInt(*p1.NextBefore, 10), nil, withSession(cookie)))
	if len(p2.Items) != 2 || p2.Items[0].ID >= p1.Items[1].ID {
		t.Fatalf("page 2 = %+v", p2)
	}
	for _, q := range []string{"limit=0", "limit=1001", "limit=x", "before=0", "before=-1", "before=x"} {
		wantError(t, e.do("GET", "/api/v1/audit?"+q, nil, withSession(cookie)), http.StatusBadRequest, codeBadRequest)
	}
}

func TestRequestID(t *testing.T) {
	e := newEnv(t)
	w := e.do("GET", "/api/v1/health", nil)
	generated := w.Header().Get("X-Request-Id")
	if len(generated) != 16 {
		t.Errorf("generated id = %q", generated)
	}
	w = e.do("GET", "/api/v1/health", nil, withHeader("X-Request-Id", "abc-123"))
	if got := w.Header().Get("X-Request-Id"); got != "abc-123" {
		t.Errorf("id = %q", got)
	}
	w = e.do("GET", "/api/v1/health", nil, withHeader("X-Request-Id", "bad id\twith space"))
	if got := w.Header().Get("X-Request-Id"); got == "bad id\twith space" {
		t.Error("unsafe request id echoed")
	}
}

func TestAccessLog(t *testing.T) {
	e := newEnv(t)
	cookie := e.login()
	e.logs.Reset()
	e.do("GET", "/api/v1/auth/me", nil, withSession(cookie), withHeader("X-Request-Id", "rid-1"))
	out := e.logs.String()
	for _, want := range []string{"method=GET", "path=/api/v1/auth/me", "status=200", "request_id=rid-1", "actor=admin", "actor_kind=user", "duration="} {
		if !strings.Contains(out, want) {
			t.Errorf("access log %q lacks %q", out, want)
		}
	}
	if strings.Contains(out, cookie.Value) {
		t.Error("session token leaked into logs")
	}
}

func TestPanicRecovery(t *testing.T) {
	s := New(Deps{Logger: slog.New(slog.NewTextHandler(io.Discard, nil))})
	h := withRequestID(s.accessLog(s.recoverPanic(http.HandlerFunc(func(http.ResponseWriter, *http.Request) {
		panic("boom")
	}))))
	w := httptest.NewRecorder()
	h.ServeHTTP(w, httptest.NewRequest("GET", "/x", nil))
	wantError(t, w, http.StatusInternalServerError, codeInternal)
	if strings.Contains(w.Body.String(), "boom") {
		t.Error("panic value leaked to the client")
	}
}
