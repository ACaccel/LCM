package api_test

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/ACaccel/lcm/internal/app"
	"github.com/ACaccel/lcm/internal/auth"
	"github.com/ACaccel/lcm/internal/catalog"
	"github.com/ACaccel/lcm/internal/clock"
	"github.com/ACaccel/lcm/internal/config"
	"github.com/ACaccel/lcm/internal/events"
	"github.com/ACaccel/lcm/internal/execx"
)

var epoch = time.Date(2026, 3, 1, 12, 0, 0, 0, time.UTC)

// waitTimeout only guards against hangs; no assertion depends on it.
const waitTimeout = 20 * time.Second

// site is a real app.App on a temp data dir with a fake clock and command
// runner, driven through its HTTP handler.
type site struct {
	t       *testing.T
	app     *app.App
	handler http.Handler
	clk     *clock.Fake
	runner  *execx.Fake
	git     *fakeGit
	dir     string
	// admin and ingest are bearer tokens of the matching scope.
	admin, ingest string
	ctx           context.Context
}

type fakeGit struct {
	mu    sync.Mutex
	heads map[string]string
}

func (g *fakeGit) RemoteHead(_ context.Context, repo, branch string) (string, error) {
	g.mu.Lock()
	defer g.mu.Unlock()
	return g.heads[repo+"#"+branch], nil
}

func (g *fakeGit) set(repo, branch, sha string) {
	g.mu.Lock()
	defer g.mu.Unlock()
	g.heads[repo+"#"+branch] = sha
}

func newSite(t *testing.T, configure ...func(*config.Config)) *site {
	t.Helper()
	x := &site{
		t: t, clk: clock.NewFake(epoch), runner: &execx.Fake{}, git: &fakeGit{heads: map[string]string{}},
		dir: filepath.Join(t.TempDir(), "data"), ctx: auth.WithActor(context.Background(), auth.CLI),
	}
	cfg := config.Default()
	cfg.DataDir = x.dir
	cfg.Listen = "127.0.0.1:0"
	cfg.SystemctlPrivilege = config.NoPrivilege
	for _, c := range configure {
		c(&cfg)
	}
	client := &http.Client{Transport: &http.Transport{DisableKeepAlives: true}}
	a, err := app.New(x.ctx, cfg,
		app.WithClock(x.clk), app.WithRunner(x.runner), app.WithHTTPClient(client), app.WithGitRunner(x.git),
		app.WithLogger(slog.New(slog.NewTextHandler(testWriter{t}, &slog.HandlerOptions{Level: slog.LevelWarn}))))
	if err != nil {
		t.Fatalf("app.New: %v", err)
	}
	t.Cleanup(func() {
		ctx, cancel := context.WithTimeout(context.Background(), waitTimeout)
		defer cancel()
		if err := a.Close(ctx); err != nil {
			t.Errorf("Close: %v", err)
		}
		client.CloseIdleConnections()
	})
	x.app, x.handler = a, a.Handler()
	_, x.admin = x.token("admin-token", auth.ScopeAdmin)
	_, x.ingest = x.token("ingest-token", auth.ScopeIngest)
	return x
}

type testWriter struct{ t *testing.T }

func (w testWriter) Write(p []byte) (int, error) {
	w.t.Log(strings.TrimRight(string(p), "\n"))
	return len(p), nil
}

func (x *site) token(name string, scope auth.Scope) (int64, string) {
	x.t.Helper()
	tok, secret, err := x.app.Auth.CreateToken(x.ctx, name, scope)
	if err != nil {
		x.t.Fatal(err)
	}
	return tok.ID, secret
}

// reply is a recorded response.
type reply struct {
	t    *testing.T
	Code int
	Body []byte
	Head http.Header
}

func (r reply) String() string { return string(r.Body) }

// decode parses the body into v.
func (r reply) decode(v any) {
	r.t.Helper()
	if err := json.Unmarshal(r.Body, v); err != nil {
		r.t.Fatalf("decode %q: %v", r.Body, err)
	}
}

// obj parses the body as a JSON object.
func (r reply) obj() map[string]any {
	r.t.Helper()
	var m map[string]any
	r.decode(&m)
	return m
}

// want fails unless the status is code.
func (r reply) want(code int) reply {
	r.t.Helper()
	if r.Code != code {
		r.t.Fatalf("status = %d, want %d; body: %s", r.Code, code, r.Body)
	}
	return r
}

// wantError checks status and error code of the envelope and returns details.
func (r reply) wantError(status int, code string) map[string]any {
	r.t.Helper()
	r.want(status)
	var env struct {
		Error struct {
			Code    string         `json:"code"`
			Message string         `json:"message"`
			Details map[string]any `json:"details"`
		} `json:"error"`
	}
	r.decode(&env)
	if env.Error.Code != code || env.Error.Message == "" || env.Error.Details == nil {
		r.t.Fatalf("error = %+v, want code %s", env.Error, code)
	}
	return env.Error.Details
}

// items returns the items of a list response.
func (r reply) items() []map[string]any {
	r.t.Helper()
	var l struct {
		Items []map[string]any `json:"items"`
	}
	r.decode(&l)
	return l.Items
}

type opt func(*http.Request)

func header(k, v string) opt { return func(r *http.Request) { r.Header.Set(k, v) } }
func bearer(tok string) opt  { return header("Authorization", "Bearer "+tok) }
func yamlBody() opt          { return header("Content-Type", "application/yaml") }
func ifMatch(v string) opt   { return header("If-Match", v) }

// call performs a request as the admin token unless another credential is
// given. body is a string, []byte or a value to marshal as JSON.
func (x *site) call(method, path string, body any, opts ...opt) reply {
	x.t.Helper()
	var rd io.Reader
	switch b := body.(type) {
	case nil:
	case string:
		rd = strings.NewReader(b)
	case []byte:
		rd = bytes.NewReader(b)
	default:
		raw, err := json.Marshal(b)
		if err != nil {
			x.t.Fatal(err)
		}
		rd = bytes.NewReader(raw)
	}
	r := httptest.NewRequest(method, path, rd)
	bearer(x.admin)(r)
	for _, o := range opts {
		o(r)
	}
	if r.Header.Get("Authorization") == "Bearer " {
		r.Header.Del("Authorization")
	}
	w := httptest.NewRecorder()
	x.handler.ServeHTTP(w, r)
	loadSpec(x.t).checkResponse(x.t, method, path, w.Code, w.Header().Get("Content-Type"), w.Body.Bytes())
	return reply{t: x.t, Code: w.Code, Body: w.Body.Bytes(), Head: w.Header()}
}

// anon is call without credentials.
func (x *site) anon(method, path string, body any, opts ...opt) reply {
	return x.call(method, path, body, append([]opt{bearer("")}, opts...)...)
}

// serviceDoc returns a command-runtime service document.
func serviceDoc(name, extra string) string {
	return "apiVersion: lcm/v1\nkind: Service\nmetadata: { name: " + name + ", labels: { tier: web } }\nspec:\n" +
		"  runtime: { type: command, start: " + name + "-start, stop: " + name + "-stop, restart: " + name + "-restart, status: " + name + "-status }\n" + extra
}

const v1 = "/api/v1"

func (x *site) createService(doc string) reply {
	x.t.Helper()
	return x.call("POST", v1+"/services", doc, yamlBody()).want(http.StatusCreated)
}

func policyDoc(name, nodes string) string {
	return "apiVersion: lcm/v1\nkind: Policy\nmetadata: { name: " + name + " }\nspec:\n  nodes:\n" + nodes
}

const manualPolicyNodes = `    - { id: start, type: trigger.manual, next: { out: [echo] } }
    - { id: echo, type: data.set, props: { values: { got: "{{ trigger.name }}" } } }
`

func (x *site) putPolicy(name, nodes string) reply {
	x.t.Helper()
	return x.call("PUT", v1+"/policies/"+name, policyDoc(name, nodes), yamlBody()).want(http.StatusCreated)
}

// collector receives hub events of the given types.
type collector struct {
	t   *testing.T
	sub *events.Subscription
}

func (x *site) collect(types ...events.Type) *collector {
	c := &collector{t: x.t, sub: x.app.Events.Subscribe(events.Filter{Types: types}, 1024)}
	x.t.Cleanup(c.sub.Close)
	return c
}

// until waits for the first event satisfying pred.
func (c *collector) until(what string, pred func(events.Event) bool) events.Event {
	c.t.Helper()
	timeout := time.After(waitTimeout)
	for {
		select {
		case e, ok := <-c.sub.C:
			if !ok {
				c.t.Fatalf("subscription closed while waiting for %s", what)
			}
			if pred(e) {
				return e
			}
		case <-timeout:
			c.t.Fatalf("timed out waiting for %s", what)
		}
	}
}

// actionDone waits until the action finished and returns its tracker entry.
func (x *site) actionDone(c *collector, id string) map[string]any {
	x.t.Helper()
	c.until("action "+id, func(e events.Event) bool {
		b, _ := json.Marshal(e.Data)
		var a struct{ ID, Status string }
		_ = json.Unmarshal(b, &a)
		return a.ID == id && a.Status != "running"
	})
	return x.call("GET", v1+"/actions/"+id, nil).want(http.StatusOK).obj()
}

// startAction posts an action and returns its id.
func (x *site) startAction(service, action string, body any) string {
	x.t.Helper()
	r := x.call("POST", v1+"/services/"+service+"/actions/"+action, body).want(http.StatusAccepted)
	return r.obj()["actionId"].(string)
}

func writeSecret(t *testing.T, x *site, name, value string) {
	t.Helper()
	if err := os.WriteFile(filepath.Join(x.app.Config.SecretsPath(), name), []byte(value), 0o600); err != nil {
		t.Fatal(err)
	}
}

func startOpts() catalog.ActionOptions { return catalog.ActionOptions{} }

func deployment(service, version string) catalog.Deployment {
	return catalog.Deployment{Service: service, Action: "deploy", Version: version, Actor: auth.CLI}
}
