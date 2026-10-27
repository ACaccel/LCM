package main

import (
	"bytes"
	"context"
	"encoding/json"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"go.yaml.in/yaml/v3"

	lcmapp "github.com/ACaccel/lcm/internal/app"
	"github.com/ACaccel/lcm/internal/auth"
	"github.com/ACaccel/lcm/internal/config"
	"github.com/ACaccel/lcm/internal/execx"
)

// server is a real app.App behind an httptest server.
type server struct {
	t      *testing.T
	app    *lcmapp.App
	url    string
	token  string
	runner *execx.Fake
}

func newServer(t *testing.T) *server {
	t.Helper()
	cfg := config.Default()
	cfg.DataDir = filepath.Join(t.TempDir(), "data")
	cfg.Listen = "127.0.0.1:0"
	cfg.SystemctlPrivilege = config.NoPrivilege
	runner := &execx.Fake{}
	client := &http.Client{Transport: &http.Transport{DisableKeepAlives: true}}
	ctx := auth.WithActor(context.Background(), auth.CLI)
	a, err := lcmapp.New(ctx, cfg, lcmapp.WithRunner(runner), lcmapp.WithHTTPClient(client),
		lcmapp.WithLogger(slog.New(slog.DiscardHandler)))
	if err != nil {
		t.Fatal(err)
	}
	ts := httptest.NewServer(a.Handler())
	t.Cleanup(func() {
		ts.Close()
		cctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
		defer cancel()
		if err := a.Close(cctx); err != nil {
			t.Errorf("close app: %v", err)
		}
		client.CloseIdleConnections()
	})
	_, secret, err := a.Auth.CreateToken(ctx, "cli-test", auth.ScopeAdmin)
	if err != nil {
		t.Fatal(err)
	}
	return &server{t: t, app: a, url: ts.URL, token: secret, runner: runner}
}

// cli runs the lcm command against the server and returns its result.
func (s *server) cli(stdin string, args ...string) result {
	s.t.Helper()
	return s.cliEnv(nil, stdin, args...)
}

func (s *server) cliEnv(env map[string]string, stdin string, args ...string) result {
	s.t.Helper()
	var out, errb bytes.Buffer
	vars := map[string]string{"LCM_SERVER": s.url, "LCM_TOKEN": s.token}
	for k, v := range env {
		vars[k] = v
	}
	a := &app{
		stdin: strings.NewReader(stdin), stdout: &out, stderr: &errb,
		getenv: func(k string) string { return vars[k] }, poll: time.Millisecond,
	}
	code := a.run(context.Background(), args)
	return result{code, out.String(), errb.String()}
}

func (r result) want(t *testing.T, code int, stdout ...string) result {
	t.Helper()
	if r.code != code {
		t.Fatalf("exit code = %d, want %d\nstdout:\n%s\nstderr:\n%s", r.code, code, r.stdout, r.stderr)
	}
	for _, s := range stdout {
		if !strings.Contains(r.stdout, s) {
			t.Fatalf("stdout lacks %q:\n%s\nstderr:\n%s", s, r.stdout, r.stderr)
		}
	}
	return r
}

func writeFile(t *testing.T, dir, name, content string) string {
	t.Helper()
	p := filepath.Join(dir, name)
	if err := os.MkdirAll(filepath.Dir(p), 0o750); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(p, []byte(content), 0o600); err != nil {
		t.Fatal(err)
	}
	return p
}

func svcDoc(name, extra string) string {
	return "apiVersion: lcm/v1\nkind: Service\nmetadata: { name: " + name + " }\nspec:\n" +
		"  runtime: { type: command, start: " + name + "-start, stop: " + name + "-stop, restart: " + name + "-restart, status: " + name + "-status }\n" + extra
}

func polDoc(name, nodes string) string {
	return "apiVersion: lcm/v1\nkind: Policy\nmetadata: { name: " + name + " }\nspec:\n  nodes:\n" + nodes
}

const manualEcho = `    - { id: start, type: trigger.manual, next: { out: [echo] } }
    - { id: echo, type: data.set, props: { values: { got: "{{ trigger.name }}" } } }
`

func TestApplyCommand(t *testing.T) {
	s := newServer(t)
	dir := t.TempDir()
	writeFile(t, dir, "services/web.yaml", svcDoc("web", ""))
	writeFile(t, dir, "policies/echo.yaml", polDoc("echo", manualEcho))
	writeFile(t, dir, "policies/echo.test.yaml", "kind: Test\n")
	writeFile(t, dir, "notes.txt", "ignored")

	// A dry run lists what would happen, with diffs, and writes nothing.
	r := s.cli("", "apply", "-f", dir, "--dry-run").want(t, 0, "create    Service/web  "+filepath.Join(dir, "services/web.yaml"),
		"create    Policy/echo", "    +kind: Service", "2 documents: 2 create (dry run)")
	if strings.Contains(r.stdout, "echo.test.yaml") {
		t.Fatalf("a test file was applied:\n%s", r.stdout)
	}
	s.cli("", "get", "services").want(t, 0)
	if got := s.cli("", "get", "services"); strings.Contains(got.stdout, "web") {
		t.Fatalf("dry run created a service:\n%s", got.stdout)
	}

	s.cli("", "apply", "-f", dir).want(t, 0, "2 documents: 2 create")
	s.cli("", "get", "services").want(t, 0, "web", "registered")
	s.cli("", "apply", "-f", dir).want(t, 0, "unchanged", "2 documents: 2 unchanged")

	// An edited file shows its diff and updates.
	writeFile(t, dir, "services/web.yaml", svcDoc("web", "  dependsOn: [db]\n"))
	s.cli("", "apply", "-f", filepath.Join(dir, "services")).want(t, 0, "update    Service/web", "    +    - db", "1 documents: 1 update")

	// Several -f and standard input.
	extra := writeFile(t, t.TempDir(), "api.yaml", svcDoc("api", ""))
	s.cli(svcDoc("cache", ""), "apply", "-f", extra, "-f", "-").want(t, 0, "create    Service/api", "create    Service/cache  -")
}

func TestApplyProblems(t *testing.T) {
	s := newServer(t)
	dir := t.TempDir()
	good := writeFile(t, dir, "good.yaml", svcDoc("good", ""))
	bad := writeFile(t, dir, "bad.yaml", "apiVersion: lcm/v1\nkind: Service\nmetadata: { name: bad }\nspec:\n  host: mars\n  runtime: { type: command, start: a, stop: b, restart: c, status: d }\n")
	broken := writeFile(t, dir, "broken.yaml", "kind: Service\nmetadata: [\n")

	r := s.cli("", "apply", "-f", good, "-f", bad).want(t, 3, "create    Service/good", "failed    Service/bad  "+bad, "error: spec.host: only \"local\" is supported")
	if !strings.Contains(r.stderr, "1 of 2 documents were not applied") {
		t.Fatalf("stderr = %q", r.stderr)
	}
	// The good document was applied anyway.
	s.cli("", "get", "services", "good").want(t, 0, "good")

	// A file that is not YAML at all is rejected by the server as a whole.
	r = s.cli("", "apply", "-f", broken).want(t, 1)
	if !strings.Contains(r.stderr, "validation_failed") {
		t.Fatalf("stderr = %q", r.stderr)
	}

	s.cli("", "apply").want(t, 2)
	s.cli("", "apply", "-f", filepath.Join(dir, "missing.yaml")).want(t, 1)
	s.cli("", "apply", "-f", t.TempDir()).want(t, 1)
	s.cli("", "apply", "-f", good, "extra").want(t, 2)
	s.cli("", "apply", "-f", good, "--bogus").want(t, 2)
}

func TestExportCommand(t *testing.T) {
	s := newServer(t)
	dir := t.TempDir()
	s.cli("", "apply", "-f", writeFile(t, dir, "all.yaml", svcDoc("web", "")+"---\n"+svcDoc("api", "  dependsOn: [web]\n")+"---\n"+polDoc("echo", manualEcho))).want(t, 0)

	all := s.cli("", "export").want(t, 0, "kind: Service", "kind: Policy")
	one := s.cli("", "export", "service/web").want(t, 0, "name: web")
	if strings.Contains(one.stdout, "name: api") {
		t.Fatalf("export service/web =\n%s", one.stdout)
	}
	s.cli("", "export", "policies").want(t, 0, "name: echo")
	s.cli("", "export", "service/ghost").want(t, 1)
	s.cli("", "export", "host").want(t, 2)
	s.cli("", "export", "a", "b").want(t, 2)

	// What was exported applies back unchanged.
	file := writeFile(t, dir, "export.yaml", all.stdout)
	s.cli("", "apply", "-f", file).want(t, 0, "3 documents: 3 unchanged")
}

func TestValidateCommand(t *testing.T) {
	s := newServer(t)
	dir := t.TempDir()
	good := writeFile(t, dir, "good.yaml", svcDoc("good", "")+"---\n"+polDoc("echo", manualEcho))
	bad := writeFile(t, dir, "bad.yaml", polDoc("loop", "    - { id: start, type: trigger.manual, next: { out: [ghost] } }\n"))

	s.cli("", "validate", "-f", good).want(t, 0, "valid     Service/good", "valid     Policy/echo", "2 documents, 2 valid")
	r := s.cli("", "validate", "-f", good, "-f", bad).want(t, 3, "invalid   Policy/loop", "error:")
	if !strings.Contains(r.stderr, "1 of 3 documents are invalid") {
		t.Fatalf("stderr = %q", r.stderr)
	}
	// Validating never writes.
	if got := s.cli("", "get", "services"); strings.Contains(got.stdout, "good") {
		t.Fatalf("validate created a service:\n%s", got.stdout)
	}
	s.cli("", "validate").want(t, 2)
}

func TestGetCommand(t *testing.T) {
	s := newServer(t)
	dir := t.TempDir()
	s.cli("", "apply", "-f", writeFile(t, dir, "all.yaml", svcDoc("web", "")+"---\n"+svcDoc("api", "")+"---\n"+polDoc("echo", manualEcho))).want(t, 0)
	s.cli("", "run", "echo", "--wait", "--event", `{"name":"x"}`).want(t, 0)

	table := s.cli("", "get", "services").want(t, 0, "NAME", "STATE", "MANAGED BY", "api", "web", "registered")
	if strings.Contains(table.stdout, "\x1b") {
		t.Fatal("table output has colour codes")
	}
	// Columns are aligned: every row starts the second column at the same offset.
	head := strings.Split(table.stdout, "\n")
	col := strings.Index(head[0], "STATE")
	for _, l := range head[1:3] {
		if col < 0 || len(l) <= col || l[col-1] != ' ' {
			t.Fatalf("misaligned table:\n%s", table.stdout)
		}
	}
	s.cli("", "get", "services", "web").want(t, 0, "web")
	if got := s.cli("", "get", "services", "web"); strings.Contains(got.stdout, "api") {
		t.Fatalf("named get listed another service:\n%s", got.stdout)
	}
	s.cli("", "get", "policies").want(t, 0, "echo", "manual", "succeeded")
	s.cli("", "get", "runs").want(t, 0, "echo", "succeeded", "ID")
	s.cli("", "get", "locks").want(t, 0, "NAME")
	s.cli("", "get", "sources").want(t, 0, "NAME", "REPO")

	var list []map[string]any
	out := s.cli("", "get", "services", "-o", "json").want(t, 0)
	if err := json.Unmarshal([]byte(out.stdout), &list); err != nil || len(list) != 2 || list[0]["name"] != "api" {
		t.Fatalf("json output: %v\n%s", err, out.stdout)
	}
	var one map[string]any
	out = s.cli("", "get", "services", "web", "-o", "json").want(t, 0)
	if err := json.Unmarshal([]byte(out.stdout), &one); err != nil || one["name"] != "web" || one["document"] == nil {
		t.Fatalf("json detail: %v\n%s", err, out.stdout)
	}
	var y []map[string]any
	out = s.cli("", "get", "policies", "-o", "yaml").want(t, 0)
	if err := yaml.Unmarshal([]byte(out.stdout), &y); err != nil || len(y) != 1 || y[0]["name"] != "echo" {
		t.Fatalf("yaml output: %v\n%s", err, out.stdout)
	}

	// --limit shows a prefix and says so.
	r := s.cli("", "get", "services", "--limit", "1").want(t, 0, "api")
	if strings.Contains(r.stdout, "web") || !strings.Contains(r.stderr, "showing the first 1") {
		t.Fatalf("limit: %+v", r)
	}

	s.cli("", "get", "services", "ghost").want(t, 1)
	s.cli("", "get", "locks", "ghost").want(t, 1)
	s.cli("", "get", "hosts").want(t, 2)
	s.cli("", "get").want(t, 2)
	s.cli("", "get", "services", "-o", "xml").want(t, 2)
}

func TestRunCommand(t *testing.T) {
	s := newServer(t)
	dir := t.TempDir()
	failing := polDoc("boom", "    - { id: start, type: trigger.manual, next: { out: [bad] } }\n    - { id: bad, type: data.set, props: { values: { n: \"{{ int('abc') }}\" } } }\n")
	s.cli("", "apply", "-f", writeFile(t, dir, "p.yaml", polDoc("echo", manualEcho)+"---\n"+failing)).want(t, 0)

	s.cli("", "run", "echo", "--wait", "--event", `{"name":"x"}`).want(t, 0, "started (echo, trigger start, live)", "succeeded", "echo")
	s.cli("", "run", "--dry-run", "echo", "--wait", "--event", `{"name":"x"}`).want(t, 0, ", dry)")
	s.cli("", "run", "echo", "--event", `{"name":"x"}`).want(t, 0, "started")

	r := s.cli("", "run", "boom", "--wait").want(t, 1)
	if !strings.Contains(r.stderr, "failed") || !strings.Contains(r.stdout, "bad") {
		t.Fatalf("failing run: %+v", r)
	}
	s.cli("", "run", "ghost").want(t, 1)
	s.cli("", "run", "echo", "--trigger", "nope").want(t, 1)
	s.cli("", "run", "echo", "--event", "{").want(t, 2)
	s.cli("", "run").want(t, 2)
	s.cli("", "run", "a", "b").want(t, 2)
}

func TestActionCommand(t *testing.T) {
	s := newServer(t)
	s.cli("", "apply", "-f", writeFile(t, t.TempDir(), "s.yaml", svcDoc("web", "  jobs:\n    reindex: { command: reindex.sh }\n"))).want(t, 0)

	s.cli("", "action", "web", "start", "--dry-run").want(t, 0, "plan: start web (registered -> running)", "1. ")
	if n := len(s.runner.Calls()); n != 0 {
		t.Fatalf("dry run ran %d commands", n)
	}
	s.cli("", "action", "web", "start", "--wait").want(t, 0, "started (start web)", "succeeded", "registered -> running")
	s.cli("", "get", "services", "web").want(t, 0, "running")
	s.cli("", "action", "web", "run-job", "--job", "reindex", "--wait").want(t, 0, "succeeded")
	s.cli("", "action", "web", "restart")

	s.cli("", "action", "web", "explode").want(t, 1)
	s.cli("", "action", "ghost", "start").want(t, 1)
	s.cli("", "action", "web", "start").want(t, 1) // already running
	s.cli("", "action", "web").want(t, 2)
	s.cli("", "action", "web", "run-job").want(t, 1)

	// A failing command is reported by --wait.
	s.runner.Reply("web-stop", "boom", 1)
	r := s.cli("", "action", "web", "stop", "--wait").want(t, 1)
	if !strings.Contains(r.stderr, "failed") {
		t.Fatalf("failed action: %+v", r)
	}
}

func TestClientCredentials(t *testing.T) {
	s := newServer(t)
	dir := t.TempDir()
	run := func(env map[string]string, args ...string) result {
		t.Helper()
		return s.cliEnv(env, "", args...)
	}
	none := map[string]string{"LCM_TOKEN": ""}

	r := run(none, "get", "services").want(t, 2)
	if !strings.Contains(r.stderr, "no API token") {
		t.Fatalf("stderr = %q", r.stderr)
	}

	file := writeFile(t, dir, "token", "\n"+s.token+"\n")
	run(none, "get", "services", "--token-file", file).want(t, 0)
	run(map[string]string{"LCM_TOKEN": "", "LCM_TOKEN_FILE": file}, "get", "services").want(t, 0)
	// The flag wins over the environment.
	bad := writeFile(t, dir, "bad-token", "lcm_wrong\n")
	run(map[string]string{"LCM_TOKEN_FILE": file}, "get", "services", "--token-file", bad).want(t, 1)

	// A token file that others can read is refused, and says how to fix it.
	open := writeFile(t, dir, "open-token", s.token)
	if err := os.Chmod(open, 0o644); err != nil { //nolint:gosec // the test needs a readable file to prove it is refused
		t.Fatal(err)
	}
	r = run(none, "get", "services", "--token-file", open).want(t, 1)
	if !strings.Contains(r.stderr, "chmod 600") {
		t.Fatalf("stderr = %q", r.stderr)
	}
	run(none, "get", "services", "--token-file", filepath.Join(dir, "absent")).want(t, 1)
	empty := writeFile(t, dir, "empty", " \n")
	run(none, "get", "services", "--token-file", empty).want(t, 1)

	// A wrong token is the server's 401.
	r = run(map[string]string{"LCM_TOKEN": "lcm_nope"}, "get", "services").want(t, 1)
	if !strings.Contains(r.stderr, "HTTP 401") {
		t.Fatalf("stderr = %q", r.stderr)
	}
	// The token never appears in output.
	if strings.Contains(r.stdout+r.stderr, "lcm_nope") {
		t.Fatal("token leaked into the output")
	}

	// Server URL: flag and environment, and a bad value.
	run(map[string]string{"LCM_SERVER": "http://127.0.0.1:1"}, "get", "services").want(t, 1)
	run(map[string]string{"LCM_SERVER": "http://127.0.0.1:1"}, "get", "services", "--server", s.url).want(t, 0)
	run(map[string]string{"LCM_SERVER": "not a url"}, "get", "services").want(t, 2)
	run(map[string]string{"LCM_SERVER": "ftp://x"}, "get", "services").want(t, 2)

	// A server that redirects must not receive the token elsewhere.
	redirector := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Redirect(w, r, "http://127.0.0.1:1/", http.StatusFound)
	}))
	defer redirector.Close()
	r = run(map[string]string{"LCM_SERVER": redirector.URL}, "get", "services").want(t, 1)
	if !strings.Contains(r.stderr, "redirected") {
		t.Fatalf("stderr = %q", r.stderr)
	}
}

func TestClientCommandsInUsage(t *testing.T) {
	a, out, errb, _ := newApp(t, "")
	r := runCLI(t, a, out, errb, "help")
	for _, want := range []string{"apply -f", "export [kind", "validate -f", "get services", "run <policy>", "action <service>", "LCM_TOKEN", "Exit status"} {
		if !strings.Contains(r.stdout, want) {
			t.Errorf("usage lacks %q", want)
		}
	}
	// Help output of a client command goes to stderr and succeeds.
	if r := runCLI(t, a, out, errb, "apply", "-h"); r.code != 0 || !strings.Contains(r.stderr, "-dry-run") {
		t.Errorf("apply -h: %+v", r)
	}
}
