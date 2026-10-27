package monitor

import (
	"context"
	"errors"
	"fmt"
	"io"
	"log"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/ACaccel/lcm/internal/model"
)

// fakeRunner returns scripted results and records the commands it saw.
type fakeRunner struct {
	res   CommandResult
	err   error
	calls []Command
}

func (f *fakeRunner) Run(_ context.Context, c Command) (CommandResult, error) {
	f.calls = append(f.calls, c)
	return f.res, f.err
}

func buildCheck(t *testing.T, typ string, props map[string]any, env Env) Built {
	t.Helper()
	b, err := DefaultRegistry().Build(model.Check{Name: "c", Type: typ, Props: props}.WithDefaults(), env)
	if err != nil {
		t.Fatalf("build %s: %v", typ, err)
	}
	return b
}

func probe(t *testing.T, typ string, props map[string]any, env Env) Result {
	t.Helper()
	b := buildCheck(t, typ, props, env)
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	return b.Probe(ctx)
}

func TestHTTPProbe(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/ok":
			_, _ = w.Write([]byte("hello world"))
		case "/redirect":
			http.Redirect(w, r, "/ok", http.StatusFound)
		case "/boom":
			http.Error(w, "boom", http.StatusInternalServerError)
		case "/auth":
			if r.Header.Get("X-Token") != "s3cret" {
				w.WriteHeader(http.StatusForbidden)
				return
			}
			w.WriteHeader(http.StatusNoContent)
		case "/post":
			if r.Method != http.MethodPost {
				w.WriteHeader(http.StatusMethodNotAllowed)
			}
		}
	}))
	defer srv.Close()

	tests := []struct {
		name  string
		props map[string]any
		ok    bool
		msg   string
	}{
		{"ok", map[string]any{"url": srv.URL + "/ok"}, true, ""},
		{"redirect accepted by default range", map[string]any{"url": srv.URL + "/redirect"}, true, ""},
		{"server error", map[string]any{"url": srv.URL + "/boom"}, false, "unexpected status 500"},
		{"expected status override", map[string]any{"url": srv.URL + "/boom", "expectStatus": []any{500}}, true, ""},
		{"body match", map[string]any{"url": srv.URL + "/ok", "bodyContains": "world"}, true, ""},
		{"body mismatch", map[string]any{"url": srv.URL + "/ok", "bodyContains": "nope"}, false, "does not contain"},
		{"headers", map[string]any{"url": srv.URL + "/auth", "headers": map[string]any{"X-Token": "s3cret"}}, true, ""},
		{"missing header", map[string]any{"url": srv.URL + "/auth"}, false, "unexpected status 403"},
		{"method", map[string]any{"url": srv.URL + "/post", "method": "post"}, true, ""},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			r := probe(t, "http", tt.props, Env{})
			if r.OK != tt.ok || !strings.Contains(r.Message, tt.msg) {
				t.Errorf("result = %+v, want ok=%v message containing %q", r, tt.ok, tt.msg)
			}
			if r.OK && r.Latency <= 0 {
				t.Errorf("latency = %v", r.Latency)
			}
		})
	}
}

func TestHTTPProbeHidesURLInErrors(t *testing.T) {
	srv := httptest.NewServer(http.NotFoundHandler())
	url := srv.URL
	srv.Close()
	r := probe(t, "http", map[string]any{"url": url + "/?token=hunter2"}, Env{})
	if r.OK || strings.Contains(r.Message, "hunter2") || strings.Contains(r.Message, url) {
		t.Errorf("result = %+v; message must not echo the URL", r)
	}
}

func TestHTTPProbeTimeout(t *testing.T) {
	release := make(chan struct{})
	entered := make(chan struct{}, 1)
	srv := httptest.NewServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) {
		entered <- struct{}{}
		<-release
	}))
	defer srv.Close()
	defer close(release)
	b := buildCheck(t, "http", map[string]any{"url": srv.URL}, Env{})
	ctx, cancel := context.WithCancelCause(context.Background())
	go func() {
		<-entered
		cancel(errCheckTimeout)
	}()
	if r := b.Probe(ctx); r.OK || r.Message != "timed out" {
		t.Errorf("result = %+v", r)
	}
}

func TestHTTPInsecureSkipVerify(t *testing.T) {
	srv := httptest.NewUnstartedServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) {}))
	// The rejected handshake of the first probe is expected; keep it out of
	// the test output.
	srv.Config.ErrorLog = log.New(io.Discard, "", 0)
	srv.StartTLS()
	defer srv.Close()
	if r := probe(t, "http", map[string]any{"url": srv.URL}, Env{}); r.OK {
		t.Errorf("self-signed certificate accepted by default: %+v", r)
	}
	if r := probe(t, "http", map[string]any{"url": srv.URL, "insecureSkipVerify": true}, Env{}); !r.OK {
		t.Errorf("insecureSkipVerify result = %+v", r)
	}
}

func TestTCPProbe(t *testing.T) {
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	addr := ln.Addr().String()
	if r := probe(t, "tcp", map[string]any{"address": addr}, Env{}); !r.OK {
		t.Errorf("open port: %+v", r)
	}
	_ = ln.Close()
	if r := probe(t, "tcp", map[string]any{"address": addr}, Env{}); r.OK {
		t.Errorf("closed port passed: %+v", r)
	}
}

func TestSystemdProbe(t *testing.T) {
	tests := []struct {
		name  string
		props map[string]any
		res   CommandResult
		args  string
		ok    bool
		msg   string
	}{
		{"active", map[string]any{"unit": "nginx.service"}, CommandResult{Stdout: "active\n"}, "is-active nginx.service", true, ""},
		{"user scope", map[string]any{"unit": "app", "scope": "user"}, CommandResult{Stdout: "active\n"}, "--user is-active app", true, ""},
		{"failed", map[string]any{"unit": "app"}, CommandResult{ExitCode: 3, Stdout: "failed\n"}, "is-active app", false, "unit is failed"},
		{"no output", map[string]any{"unit": "app"}, CommandResult{ExitCode: 4}, "is-active app", false, "exited with 4"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			fr := &fakeRunner{res: tt.res}
			r := probe(t, "systemd", tt.props, Env{Runner: fr})
			if r.OK != tt.ok || !strings.Contains(r.Message, tt.msg) {
				t.Errorf("result = %+v", r)
			}
			if got := fr.calls[0].Name + " " + strings.Join(fr.calls[0].Args, " "); got != "systemctl "+tt.args {
				t.Errorf("command = %q", got)
			}
		})
	}
}

func TestDockerProbe(t *testing.T) {
	tests := []struct {
		name string
		res  CommandResult
		ok   bool
		msg  string
	}{
		{"running without healthcheck", CommandResult{Stdout: "running \n"}, true, ""},
		{"healthy", CommandResult{Stdout: "running healthy\n"}, true, ""},
		{"starting", CommandResult{Stdout: "running starting\n"}, false, "health is starting"},
		{"unhealthy", CommandResult{Stdout: "running unhealthy\n"}, false, "health is unhealthy"},
		{"exited", CommandResult{Stdout: "exited \n"}, false, "container is exited"},
		{"missing", CommandResult{ExitCode: 1, Stderr: "Error: No such object: web\n"}, false, "No such object"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			r := probe(t, "docker", map[string]any{"container": "web"}, Env{Runner: &fakeRunner{res: tt.res}})
			if r.OK != tt.ok || !strings.Contains(r.Message, tt.msg) {
				t.Errorf("result = %+v", r)
			}
		})
	}
}

func TestCommandProbe(t *testing.T) {
	fr := &fakeRunner{res: CommandResult{ExitCode: 2, Stderr: "disk full\n"}}
	props := map[string]any{"command": []any{"/usr/bin/check", "--fast"}, "expectExit": 2}
	if r := probe(t, "command", props, Env{Runner: fr}); !r.OK {
		t.Errorf("matching exit code: %+v", r)
	}
	if c := fr.calls[0]; c.Name != "/usr/bin/check" || len(c.Args) != 1 || c.Args[0] != "--fast" {
		t.Errorf("command = %+v", c)
	}
	fr.res.ExitCode = 0
	r := probe(t, "command", map[string]any{"command": []any{"x"}, "expectExit": 1}, Env{Runner: fr})
	if r.OK || !strings.Contains(r.Message, "exit code 0, want 1") {
		t.Errorf("mismatch: %+v", r)
	}
	fr.err = errors.New("exec: not found")
	if r := probe(t, "command", map[string]any{"command": []any{"x"}}, Env{Runner: fr}); r.OK || !strings.Contains(r.Message, "not found") {
		t.Errorf("start error: %+v", r)
	}
}

func TestExecRunner(t *testing.T) {
	ctx := context.Background()
	res, err := execRunner{}.Run(ctx, Command{Name: "sh", Args: []string{"-c", "echo out; echo err >&2; exit 3"}})
	if err != nil || res.ExitCode != 3 || strings.TrimSpace(res.Stdout) != "out" || strings.TrimSpace(res.Stderr) != "err" {
		t.Errorf("result = %+v, err = %v", res, err)
	}
	res, err = execRunner{}.Run(ctx, Command{Name: "sh", Args: []string{"-c", "head -c 100000 /dev/zero | tr '\\0' x"}})
	if err != nil || len(res.Stdout) != maxCommandOutput {
		t.Errorf("output length = %d, err = %v; want cap %d", len(res.Stdout), err, maxCommandOutput)
	}
	if _, err := (execRunner{}).Run(ctx, Command{Name: "definitely-not-a-command-lcm"}); err == nil {
		t.Error("missing program must be an error")
	}
}

func TestExecRunnerKillsProcessGroup(t *testing.T) {
	pidFile := t.TempDir() + "/child.pid"
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() {
		// The grandchild sleeps in the same group as the shell.
		_, err := execRunner{}.Run(ctx, Command{Name: "sh", Args: []string{"-c", "sleep 60 & echo $! > " + pidFile + "; wait"}})
		done <- err
	}()
	var pid string
	deadline := time.Now().Add(10 * time.Second)
	for time.Now().Before(deadline) {
		if b, err := os.ReadFile(pidFile); err == nil && strings.TrimSpace(string(b)) != "" { //nolint:gosec // path is under t.TempDir
			pid = strings.TrimSpace(string(b))
			break
		}
		time.Sleep(10 * time.Millisecond)
	}
	if pid == "" {
		t.Fatal("child did not start")
	}
	cancel()
	if err := <-done; !errors.Is(err, context.Canceled) {
		t.Errorf("error = %v, want context.Canceled", err)
	}
	for time.Now().Before(deadline) {
		if _, err := os.Stat("/proc/" + pid); err != nil { //nolint:gosec // pid was written by the test's own child process
			return
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Errorf("grandchild %s survived cancellation", pid)
}

func TestPrometheusProbe(t *testing.T) {
	var body string
	var gotQuery string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotQuery = r.URL.Query().Get("query")
		if r.URL.Path != "/api/v1/query" {
			http.NotFound(w, r)
			return
		}
		_, _ = w.Write([]byte(body))
	}))
	defer srv.Close()
	vector := func(v string) string {
		return fmt.Sprintf(`{"status":"success","data":{"resultType":"vector","result":[{"metric":{},"value":[1760000000.1,%q]}]}}`, v)
	}
	props := func(op string, th float64) map[string]any {
		return map[string]any{"url": srv.URL, "query": `rate(errors[5m])`, "op": op, "threshold": th}
	}
	tests := []struct {
		name string
		body string
		op   string
		th   float64
		ok   bool
		msg  string
	}{
		{"satisfied", vector("0.02"), "<", 0.05, true, "value 0.02"},
		{"violated", vector("0.2"), "<", 0.05, false, "does not satisfy < 0.05"},
		{"equals", vector("1"), "==", 1, true, ""},
		{"scalar", `{"status":"success","data":{"resultType":"scalar","result":[1760000000,"7"]}}`, ">=", 7, true, ""},
		{"empty vector", `{"status":"success","data":{"resultType":"vector","result":[]}}`, ">", 0, false, "0 samples"},
		{"two samples", `{"status":"success","data":{"resultType":"vector","result":[{"metric":{},"value":[1,"1"]},{"metric":{},"value":[1,"2"]}]}}`, ">", 0, false, "2 samples"},
		{"query error", `{"status":"error","errorType":"bad_data","error":"parse error"}`, ">", 0, false, "parse error"},
		{"not json", `<html>`, ">", 0, false, "unexpected response"},
		{"NaN", vector("NaN"), ">", 0, false, "does not satisfy"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			body = tt.body
			r := probe(t, "prometheus", props(tt.op, tt.th), Env{})
			if r.OK != tt.ok || !strings.Contains(r.Message, tt.msg) {
				t.Errorf("result = %+v", r)
			}
		})
	}
	if gotQuery != "rate(errors[5m])" {
		t.Errorf("query = %q", gotQuery)
	}
}

func TestHeartbeatBuild(t *testing.T) {
	b, err := DefaultRegistry().Build(model.Check{Name: "c", Type: "heartbeat", Interval: model.Duration(time.Minute)}.WithDefaults(), Env{})
	if err != nil || b.Probe != nil || b.Grace != time.Minute {
		t.Errorf("default grace: %+v %v", b, err)
	}
	b, err = DefaultRegistry().Build(model.Check{Name: "c", Type: "heartbeat", Props: map[string]any{"grace": "10s"}}.WithDefaults(), Env{})
	if err != nil || b.Grace != 10*time.Second {
		t.Errorf("explicit grace: %+v %v", b, err)
	}
}

func TestBuildValidation(t *testing.T) {
	tests := []struct {
		typ   string
		props map[string]any
		path  string
	}{
		{"nope", nil, "type"},
		{"http", map[string]any{}, "props.url"},
		{"http", map[string]any{"url": "ftp://x"}, "props.url"},
		{"http", map[string]any{"url": "http://x", "method": "TRACE"}, "props.method"},
		{"http", map[string]any{"url": "http://x", "expectStatus": []any{99}}, "props.expectStatus"},
		{"http", map[string]any{"url": "http://x", "bogus": 1}, "props"},
		{"tcp", map[string]any{"address": "no-port"}, "props.address"},
		{"systemd", map[string]any{"unit": "--help"}, "props.unit"},
		{"systemd", map[string]any{"unit": "a", "scope": "global"}, "props.scope"},
		{"docker", map[string]any{"container": "-v"}, "props.container"},
		{"command", map[string]any{"command": []any{}}, "props.command"},
		{"prometheus", map[string]any{"url": "http://p", "query": "up", "op": "~", "threshold": 1}, "props.op"},
		{"prometheus", map[string]any{"url": "http://p", "query": "up", "op": ">"}, "props.threshold"},
		{"heartbeat", map[string]any{"grace": "soon"}, "props"},
	}
	for _, tt := range tests {
		_, err := DefaultRegistry().Build(model.Check{Name: "c", Type: tt.typ, Props: tt.props}.WithDefaults(), Env{Runner: &fakeRunner{}})
		var is model.Issues
		if !errors.As(err, &is) || len(is) == 0 || is[0].Path != tt.path {
			t.Errorf("%s %v: error = %v, want issue at %s", tt.typ, tt.props, err, tt.path)
		}
	}
}

func TestRegistryPanicsOnDuplicate(t *testing.T) {
	defer func() {
		if recover() == nil {
			t.Error("duplicate registration must panic")
		}
	}()
	r := DefaultRegistry()
	Register(r, Spec[tcpProps]{Type: "tcp", Build: buildTCP})
}
