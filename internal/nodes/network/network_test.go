package network_test

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/ACaccel/lcm/internal/clock"
	"github.com/ACaccel/lcm/internal/model"
	"github.com/ACaccel/lcm/internal/node"
	"github.com/ACaccel/lcm/internal/nodes/network"
	"github.com/ACaccel/lcm/internal/secret"
)

var epoch = time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)

type secrets map[string]string

func (s secrets) Get(name string) (secret.Value, error) {
	if v, ok := s[name]; ok {
		return secret.NewValue(v), nil
	}
	return secret.Value{}, fmt.Errorf("%w: %q", secret.ErrNotFound, name)
}

type fixture struct {
	reg *node.Registry
	clk *clock.Fake
}

func newFixture(t *testing.T, srv *httptest.Server, s secrets) fixture {
	t.Helper()
	reg := node.NewRegistry()
	deps := network.Deps{Secrets: s}
	if srv != nil {
		deps.Client = srv.Client()
		deps.GitHubAPI = srv.URL
	}
	network.Register(reg, deps)
	return fixture{reg: reg, clk: clock.NewFake(epoch)}
}

func (f fixture) rc() *node.RunContext {
	return &node.RunContext{
		RunID: "run1", Policy: "pol", NodeID: "n", Attempt: 1, IdempotencyKey: "run1/n",
		Mode: node.ModeLive, Clock: f.clk, Logger: slog.New(slog.DiscardHandler),
	}
}

func (f fixture) run(t *testing.T, rc *node.RunContext, typ string, props map[string]any) (node.Result, error) {
	t.Helper()
	d, ok := f.reg.Lookup(typ)
	if !ok {
		t.Fatalf("%s not registered", typ)
	}
	p, err := d.Decode(props)
	if err != nil {
		t.Fatalf("decode %s: %v", typ, err)
	}
	if rc.Mode == node.ModeDry && d.Effect() == node.EffectWrite {
		if d.HasDryRun() {
			return d.DryRun(context.Background(), rc, p)
		}
		return node.Result{Output: map[string]any{"planned": true}}, nil
	}
	return d.Run(context.Background(), rc, p)
}

func out(t *testing.T, res node.Result) map[string]any {
	t.Helper()
	m, ok := res.Output.(map[string]any)
	if !ok {
		t.Fatalf("output = %#v", res.Output)
	}
	return m
}

func TestHTTPRequestGetDecodesJSON(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json; charset=utf-8")
		w.Header().Set("Set-Cookie", "session=abc")
		_, _ = io.WriteString(w, `{"version":"1.2.3","n":7}`)
	}))
	defer srv.Close()
	f := newFixture(t, srv, nil)

	res, err := f.run(t, f.rc(), "http.request", map[string]any{"method": "GET", "url": srv.URL + "/v"})
	if err != nil {
		t.Fatal(err)
	}
	o := out(t, res)
	body := o["body"].(map[string]any)
	if o["status"] != int64(200) || o["ok"] != true || body["version"] != "1.2.3" || body["n"] != int64(7) {
		t.Fatalf("output = %v", o)
	}
	if _, leaked := o["headers"].(map[string]any)["set-cookie"]; leaked {
		t.Fatal("set-cookie copied into the output")
	}
}

func TestHTTPRequestPostSendsBodySecretsAndIdempotencyKey(t *testing.T) {
	var got struct {
		method, auth, key, apiKey, ctype, body string
	}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		b, _ := io.ReadAll(r.Body)
		got.method, got.auth, got.key, got.apiKey, got.ctype, got.body =
			r.Method, r.Header.Get("Authorization"), r.Header.Get("Idempotency-Key"), r.Header.Get("X-Api-Key"), r.Header.Get("Content-Type"), string(b)
		w.WriteHeader(http.StatusCreated)
	}))
	defer srv.Close()
	f := newFixture(t, srv, secrets{"tok": "bearer-value", "key": "api-value"})

	res, err := f.run(t, f.rc(), "http.request", map[string]any{
		"method": "POST", "url": srv.URL, "body": map[string]any{"replicas": 3}, "expectStatus": []any{"201"},
		"bearerSecret": "tok", "headerSecrets": map[string]any{"X-Api-Key": "key"},
	})
	if err != nil {
		t.Fatal(err)
	}
	if got.method != "POST" || got.auth != "Bearer bearer-value" || got.apiKey != "api-value" ||
		got.key != "run1/n" || got.ctype != "application/json" || got.body != `{"replicas":3}` {
		t.Fatalf("server saw %+v", got)
	}
	if out(t, res)["status"] != int64(201) {
		t.Fatalf("output = %v", res.Output)
	}
}

func TestHTTPRequestStringBodyAndStatusRanges(t *testing.T) {
	var body string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		b, _ := io.ReadAll(r.Body)
		body = string(b)
		w.WriteHeader(http.StatusAccepted)
	}))
	defer srv.Close()
	f := newFixture(t, srv, nil)
	if _, err := f.run(t, f.rc(), "http.request", map[string]any{"method": "PUT", "url": srv.URL, "body": "raw text", "expectStatus": []any{"200", "202-204"}}); err != nil {
		t.Fatal(err)
	}
	if body != "raw text" {
		t.Fatalf("body = %q", body)
	}
}

func TestHTTPRequestFailureClassification(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/500":
			w.WriteHeader(500)
		case "/404":
			w.WriteHeader(404)
		case "/429":
			w.WriteHeader(429)
		}
	}))
	defer srv.Close()
	f := newFixture(t, srv, nil)
	for path, permanent := range map[string]bool{"/500": false, "/404": true, "/429": false} {
		_, err := f.run(t, f.rc(), "http.request", map[string]any{"method": "GET", "url": srv.URL + path + "?token=hunter2"})
		if err == nil || node.IsPermanent(err) != permanent {
			t.Fatalf("%s: err=%v permanent=%v", path, err, node.IsPermanent(err))
		}
		if strings.Contains(err.Error(), "hunter2") {
			t.Fatalf("error leaks the query string: %v", err)
		}
	}
	// 404 is fine when it is the expected answer.
	if _, err := f.run(t, f.rc(), "http.request", map[string]any{"method": "GET", "url": srv.URL + "/404", "expectStatus": []any{"404"}}); err != nil {
		t.Fatal(err)
	}
}

func TestHTTPRequestMissingSecretIsPermanent(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) {}))
	defer srv.Close()
	f := newFixture(t, srv, secrets{})
	_, err := f.run(t, f.rc(), "http.request", map[string]any{"method": "GET", "url": srv.URL, "bearerSecret": "absent"})
	if err == nil || !node.IsPermanent(err) {
		t.Fatalf("err = %v", err)
	}
}

func TestHTTPRequestSizeCapAndNonJSON(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = io.WriteString(w, `{"big":"`+strings.Repeat("x", 5000)+`"}`)
	}))
	defer srv.Close()
	f := newFixture(t, srv, nil)
	res, err := f.run(t, f.rc(), "http.request", map[string]any{"method": "GET", "url": srv.URL, "maxBytes": 100})
	if err != nil {
		t.Fatal(err)
	}
	o := out(t, res)
	s, isString := o["body"].(string)
	if o["truncated"] != true || !isString || len(s) != 100 {
		t.Fatalf("truncated=%v body type %T len %d", o["truncated"], o["body"], len(s))
	}
}

func TestHTTPRequestDoesNotFollowCrossHostRedirect(t *testing.T) {
	var hits atomic.Int32
	other := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { hits.Add(1) }))
	defer other.Close()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Redirect(w, r, other.URL, http.StatusFound)
	}))
	defer srv.Close()
	// The redirect policy lives in the default client, so use it directly.
	reg := node.NewRegistry()
	network.Register(reg, network.Deps{})
	f := fixture{reg: reg, clk: clock.NewFake(epoch)}
	_, err := f.run(t, f.rc(), "http.request", map[string]any{"method": "GET", "url": srv.URL, "headerSecrets": map[string]any{}})
	if err == nil {
		t.Fatal("302 accepted as success")
	}
	if hits.Load() != 0 {
		t.Fatal("redirect to another host was followed")
	}
}

func TestHTTPRequestTimeoutUsesInjectedClock(t *testing.T) {
	release := make(chan struct{})
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		select {
		case <-release:
		case <-r.Context().Done():
		}
	}))
	defer srv.Close()
	defer close(release)
	f := newFixture(t, srv, nil)
	errc := make(chan error, 1)
	go func() {
		_, err := f.run(t, f.rc(), "http.request", map[string]any{"method": "GET", "url": srv.URL, "timeout": "5s"})
		errc <- err
	}()
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	if err := f.clk.WaitForTimers(ctx, 1); err != nil {
		t.Fatal(err)
	}
	f.clk.Advance(5 * time.Second)
	if err := <-errc; err == nil || !strings.Contains(err.Error(), "timed out after 5s") {
		t.Fatalf("err = %v", err)
	}
}

func TestHTTPRequestDryRun(t *testing.T) {
	var hits atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { hits.Add(1) }))
	defer srv.Close()
	f := newFixture(t, srv, nil)
	dry := f.rc()
	dry.Mode = node.ModeDry

	if _, err := f.run(t, dry, "http.request", map[string]any{"method": "GET", "url": srv.URL}); err != nil || hits.Load() != 1 {
		t.Fatalf("GET in dry run: hits=%d err=%v", hits.Load(), err)
	}
	res, err := f.run(t, dry, "http.request", map[string]any{"method": "POST", "url": srv.URL})
	if err != nil || hits.Load() != 1 || out(t, res)["planned"] != true {
		t.Fatalf("POST in dry run: hits=%d out=%v err=%v", hits.Load(), res.Output, err)
	}
	// effect overrides the method both ways.
	if _, err := f.run(t, dry, "http.request", map[string]any{"method": "POST", "url": srv.URL, "effect": "read"}); err != nil || hits.Load() != 2 {
		t.Fatalf("effect=read: hits=%d err=%v", hits.Load(), err)
	}
	res, _ = f.run(t, dry, "http.request", map[string]any{"method": "GET", "url": srv.URL, "effect": "write"})
	if hits.Load() != 2 || out(t, res)["planned"] != true {
		t.Fatalf("effect=write: hits=%d", hits.Load())
	}
}

func TestHTTPGetIsReadNode(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { _, _ = io.WriteString(w, "ok") }))
	defer srv.Close()
	f := newFixture(t, srv, nil)
	d, _ := f.reg.Lookup("http.get")
	if d.Effect() != node.EffectRead {
		t.Fatalf("effect = %s", d.Effect())
	}
	res, err := f.run(t, f.rc(), "http.get", map[string]any{"url": srv.URL})
	if err != nil || out(t, res)["body"] != "ok" {
		t.Fatalf("res=%v err=%v", res.Output, err)
	}
}

func TestHTTPRequestValidation(t *testing.T) {
	f := newFixture(t, nil, nil)
	d, _ := f.reg.Lookup("http.request")
	bad := []map[string]any{
		{"method": "GET", "url": "ftp://x"},
		{"method": "GET", "url": "/relative"},
		{"method": "GET", "url": "http://x", "body": "x"},
		{"method": "GET", "url": "http://x", "timeout": "10m"},
		{"method": "GET", "url": "http://x", "maxBytes": 1 << 30},
		{"method": "GET", "url": "http://x", "headers": map[string]any{"bad header": "v"}},
		{"method": "GET", "url": "http://x", "bearerSecret": "../x"},
		{"method": "GET", "url": "http://x", "expectStatus": []any{"20x"}},
		{"method": "TRACE", "url": "http://x"},
	}
	for _, props := range bad {
		if _, err := d.Decode(props); err == nil {
			t.Errorf("accepted %v", props)
		}
	}
}

func checkRuns(runs ...map[string]string) string {
	items := make([]map[string]string, len(runs))
	copy(items, runs)
	b, _ := json.Marshal(map[string]any{"total_count": len(runs), "check_runs": items})
	return string(b)
}

func run(name, status, conclusion string) map[string]string {
	return map[string]string{"name": name, "status": status, "conclusion": conclusion, "html_url": "https://ci/" + name}
}

func TestCIStatusStates(t *testing.T) {
	var body, auth, path atomic.Value
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		auth.Store(r.Header.Get("Authorization"))
		path.Store(r.URL.Path)
		w.Header().Set("Content-Type", "application/json")
		_, _ = io.WriteString(w, body.Load().(string))
	}))
	defer srv.Close()
	f := newFixture(t, srv, secrets{"gh": "ghp_token"})

	tests := []struct {
		name string
		body string
		port string
	}{
		{"all green", checkRuns(run("build", "completed", "success"), run("lint", "completed", "skipped"), run("x", "completed", "neutral")), "success"},
		{"one failed", checkRuns(run("build", "completed", "success"), run("test", "completed", "failure")), "failure"},
		{"failure beats pending", checkRuns(run("test", "completed", "timed_out"), run("slow", "in_progress", "")), "failure"},
		{"pending", checkRuns(run("build", "completed", "success"), run("test", "queued", "")), "pending"},
		{"no runs yet", checkRuns(), "pending"},
		{"cancelled counts as failure", checkRuns(run("build", "completed", "cancelled")), "failure"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			body.Store(tt.body)
			res, err := f.run(t, f.rc(), "ci.status", map[string]any{"repo": "acme/app", "commit": "abc1234", "tokenSecret": "gh"})
			if err != nil {
				t.Fatal(err)
			}
			if res.Port != tt.port || out(t, res)["state"] != tt.port {
				t.Fatalf("port=%q out=%v", res.Port, res.Output)
			}
		})
	}
	if auth.Load() != "Bearer ghp_token" || path.Load() != "/repos/acme/app/commits/abc1234/check-runs" {
		t.Fatalf("request: auth=%v path=%v", auth.Load(), path.Load())
	}
}

func TestCIStatusWaitForSuspendsAndPolls(t *testing.T) {
	var calls atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		if calls.Add(1) < 3 {
			_, _ = io.WriteString(w, checkRuns(run("test", "in_progress", "")))
			return
		}
		_, _ = io.WriteString(w, checkRuns(run("test", "completed", "success")))
	}))
	defer srv.Close()
	f := newFixture(t, srv, nil)
	props := map[string]any{"repo": "acme/app", "commit": "abc1234", "waitFor": "30m", "interval": "1m"}

	rc := f.rc()
	res, err := f.run(t, rc, "ci.status", props)
	if err != nil || res.Suspend == nil || !res.Suspend.ResumeAt.Equal(epoch.Add(time.Minute)) {
		t.Fatalf("first run: %+v %v", res, err)
	}
	saved := res.Suspend.Output
	f.clk.Advance(time.Minute)
	rc = f.rc()
	rc.Resumed = &node.Resumed{Timer: true, Output: roundTrip(t, saved)}
	res, err = f.run(t, rc, "ci.status", props)
	if err != nil || res.Suspend == nil || !res.Suspend.ResumeAt.Equal(epoch.Add(2*time.Minute)) {
		t.Fatalf("second run: %+v %v", res, err)
	}
	f.clk.Advance(time.Minute)
	rc = f.rc()
	rc.Resumed = &node.Resumed{Timer: true, Output: roundTrip(t, res.Suspend.Output)}
	res, err = f.run(t, rc, "ci.status", props)
	if err != nil || res.Port != "success" || out(t, res)["polls"] != int64(3) {
		t.Fatalf("third run: %+v %v", res, err)
	}
}

func TestCIStatusWaitForTimesOutAsPending(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = io.WriteString(w, checkRuns(run("test", "queued", "")))
	}))
	defer srv.Close()
	f := newFixture(t, srv, nil)
	props := map[string]any{"repo": "acme/app", "commit": "abc1234", "waitFor": "3m", "interval": "2m"}
	res, _ := f.run(t, f.rc(), "ci.status", props)
	// The second wake-up is capped at the deadline, not at 4 minutes.
	f.clk.Advance(2 * time.Minute)
	rc := f.rc()
	rc.Resumed = &node.Resumed{Timer: true, Output: roundTrip(t, res.Suspend.Output)}
	res, _ = f.run(t, rc, "ci.status", props)
	if res.Suspend == nil || !res.Suspend.ResumeAt.Equal(epoch.Add(3*time.Minute)) {
		t.Fatalf("wake-up = %+v", res)
	}
	f.clk.Advance(time.Minute)
	rc = f.rc()
	rc.Resumed = &node.Resumed{Timer: true, Output: roundTrip(t, res.Suspend.Output)}
	res, err := f.run(t, rc, "ci.status", props)
	if err != nil || res.Port != "pending" || res.Suspend != nil {
		t.Fatalf("after deadline: %+v %v", res, err)
	}
}

func TestCIStatusAPIErrors(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/repos/acme/gone/commits/abc1234/check-runs" {
			w.WriteHeader(http.StatusNotFound)
			return
		}
		w.WriteHeader(http.StatusBadGateway)
	}))
	defer srv.Close()
	f := newFixture(t, srv, nil)
	_, err := f.run(t, f.rc(), "ci.status", map[string]any{"repo": "acme/gone", "commit": "abc1234"})
	if err == nil || !node.IsPermanent(err) {
		t.Fatalf("404: %v", err)
	}
	_, err = f.run(t, f.rc(), "ci.status", map[string]any{"repo": "acme/app", "commit": "abc1234"})
	if err == nil || node.IsPermanent(err) {
		t.Fatalf("502 must be retryable: %v", err)
	}
	d, _ := f.reg.Lookup("ci.status")
	for _, props := range []map[string]any{
		{"repo": "noslash", "commit": "abc1234"},
		{"repo": "a/b", "commit": "x"},
		{"repo": "a/../b", "commit": "abc1234"},
	} {
		if _, err := d.Decode(props); err == nil {
			t.Errorf("accepted %v", props)
		}
	}
}

// roundTrip passes a value through JSON like the engine does when it stores
// a suspension output.
func roundTrip(t *testing.T, v any) any {
	t.Helper()
	out, _, err := model.NormalizeJSON(v)
	if err != nil {
		t.Fatal(err)
	}
	return out
}

func wfRun(file string, number int, status, conclusion string) map[string]any {
	return map[string]any{
		"name": "workflow " + file, "path": ".github/workflows/" + file, "run_number": number,
		"status": status, "conclusion": conclusion, "event": "push", "html_url": "https://ci/" + file,
	}
}

func TestCIStatusWorkflowsMode(t *testing.T) {
	var body atomic.Value
	var gotQuery, gotPath atomic.Value
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotQuery.Store(r.URL.RawQuery)
		gotPath.Store(r.URL.Path)
		w.Header().Set("Content-Type", "application/json")
		_, _ = io.WriteString(w, body.Load().(string))
	}))
	defer srv.Close()
	f := newFixture(t, srv, nil)
	set := func(runs ...map[string]any) {
		b, _ := json.Marshal(map[string]any{"total_count": len(runs), "workflow_runs": runs})
		body.Store(string(b))
	}
	props := map[string]any{"repo": "acme/app", "commit": "abc1234", "workflows": []string{"ci.yml", "secret-scan.yml"}}

	tests := []struct {
		name string
		runs []map[string]any
		port string
	}{
		{"both succeeded", []map[string]any{wfRun("ci.yml", 3, "completed", "success"), wfRun("secret-scan.yml", 2, "completed", "success")}, "success"},
		{"unrelated workflow is ignored", []map[string]any{
			wfRun("ci.yml", 3, "completed", "success"), wfRun("secret-scan.yml", 2, "completed", "success"), wfRun("pages.yml", 1, "completed", "failure"),
		}, "success"},
		{"one failed", []map[string]any{wfRun("ci.yml", 3, "completed", "failure"), wfRun("secret-scan.yml", 2, "completed", "success")}, "failure"},
		{"a rerun supersedes the failed run", []map[string]any{
			wfRun("ci.yml", 3, "completed", "failure"), wfRun("ci.yml", 4, "completed", "success"), wfRun("secret-scan.yml", 2, "completed", "success"),
		}, "success"},
		{"latest run failed after an earlier success", []map[string]any{
			wfRun("ci.yml", 4, "completed", "failure"), wfRun("ci.yml", 3, "completed", "success"), wfRun("secret-scan.yml", 2, "completed", "success"),
		}, "failure"},
		{"still running", []map[string]any{wfRun("ci.yml", 3, "in_progress", ""), wfRun("secret-scan.yml", 2, "completed", "success")}, "pending"},
		{"workflow has not started", []map[string]any{wfRun("ci.yml", 3, "completed", "success")}, "pending"},
		{"no runs", nil, "pending"},
		{"failure beats a missing workflow", []map[string]any{wfRun("ci.yml", 3, "completed", "failure")}, "failure"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			set(tt.runs...)
			res, err := f.run(t, f.rc(), "ci.status", props)
			if err != nil {
				t.Fatal(err)
			}
			if res.Port != tt.port {
				t.Fatalf("port = %q, want %q (%v)", res.Port, tt.port, res.Output)
			}
		})
	}
	if gotPath.Load() != "/repos/acme/app/actions/runs" || gotQuery.Load() != "head_sha=abc1234&event=push&per_page=100&page=1" {
		t.Fatalf("request = %v ?%v", gotPath.Load(), gotQuery.Load())
	}
}

func TestCIStatusWorkflowsValidation(t *testing.T) {
	f := newFixture(t, nil, nil)
	d, _ := f.reg.Lookup("ci.status")
	for _, wf := range []string{".github/workflows/ci.yml", "ci", "../ci.yml"} {
		if _, err := d.Decode(map[string]any{"repo": "acme/app", "commit": "abc1234", "workflows": []string{wf}}); err == nil {
			t.Errorf("workflow %q accepted", wf)
		}
	}
}
