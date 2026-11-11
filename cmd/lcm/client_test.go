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

// svcDoc returns a command service. extra is appended to the spec; lines
// indented by four spaces continue the config.
func svcDoc(name, extra string) string {
	return "apiVersion: lcm/v2\nkind: Resource\nmetadata: { name: " + name + " }\nspec:\n  type: service\n  adapter: command\n  config:\n" +
		"    start: " + name + "-start\n    stop: " + name + "-stop\n    restart: " + name + "-restart\n    status: " + name + "-status\n" + extra
}

func polDoc(name, nodes string) string {
	return "apiVersion: lcm/v2\nkind: Policy\nmetadata: { name: " + name + " }\nspec:\n  nodes:\n" + nodes
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
	r := s.cli("", "apply", "-f", dir, "--dry-run").want(t, 0, "create    Resource/web  "+filepath.Join(dir, "services/web.yaml"),
		"create    Policy/echo", "    +kind: Resource", "2 documents: 2 create (dry run)")
	if strings.Contains(r.stdout, "echo.test.yaml") {
		t.Fatalf("a test file was applied:\n%s", r.stdout)
	}
	s.cli("", "get", "resources").want(t, 0)
	if got := s.cli("", "get", "resources"); strings.Contains(got.stdout, "web") {
		t.Fatalf("dry run created a service:\n%s", got.stdout)
	}

	s.cli("", "apply", "-f", dir).want(t, 0, "2 documents: 2 create")
	s.cli("", "get", "resources").want(t, 0, "web", "registered")
	s.cli("", "apply", "-f", dir).want(t, 0, "unchanged", "2 documents: 2 unchanged")

	// An edited file shows its diff and updates.
	writeFile(t, dir, "services/web.yaml", svcDoc("web", "  relations: [{ type: dependsOn, target: db }]\n"))
	s.cli("", "apply", "-f", filepath.Join(dir, "services")).want(t, 0, "update    Resource/web", "target: db", "1 documents: 1 update")

	// Several -f and standard input.
	extra := writeFile(t, t.TempDir(), "api.yaml", svcDoc("api", ""))
	s.cli(svcDoc("cache", ""), "apply", "-f", extra, "-f", "-").want(t, 0, "create    Resource/api", "create    Resource/cache  -")
}

func TestApplyProblems(t *testing.T) {
	s := newServer(t)
	dir := t.TempDir()
	good := writeFile(t, dir, "good.yaml", svcDoc("good", ""))
	bad := writeFile(t, dir, "bad.yaml", "apiVersion: lcm/v2\nkind: Resource\nmetadata: { name: bad }\nspec:\n  type: service\n  adapter: systemd\n")
	broken := writeFile(t, dir, "broken.yaml", "kind: Resource\nmetadata: [\n")

	r := s.cli("", "apply", "-f", good, "-f", bad).want(t, 3, "create    Resource/good", "failed    Resource/bad  "+bad, "error: spec.config")
	if !strings.Contains(r.stderr, "1 of 2 documents were not applied") {
		t.Fatalf("stderr = %q", r.stderr)
	}
	// The good document was applied anyway.
	s.cli("", "get", "resources", "good").want(t, 0, "good")

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
	s.cli("", "apply", "-f", writeFile(t, dir, "all.yaml", svcDoc("web", "")+"---\n"+svcDoc("api", "  relations: [{ type: dependsOn, target: web }]\n")+"---\n"+polDoc("echo", manualEcho))).want(t, 0)

	all := s.cli("", "export").want(t, 0, "kind: Resource", "kind: Policy")
	one := s.cli("", "export", "resource/web").want(t, 0, "name: web")
	if strings.Contains(one.stdout, "name: api") {
		t.Fatalf("export resource/web =\n%s", one.stdout)
	}
	s.cli("", "export", "policies").want(t, 0, "name: echo")
	s.cli("", "export", "resource/ghost").want(t, 1)
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

	s.cli("", "validate", "-f", good).want(t, 0, "valid     Resource/good", "valid     Policy/echo", "2 documents, 2 valid")
	r := s.cli("", "validate", "-f", good, "-f", bad).want(t, 3, "invalid   Policy/loop", "error:")
	if !strings.Contains(r.stderr, "1 of 3 documents are invalid") {
		t.Fatalf("stderr = %q", r.stderr)
	}
	// Validating never writes.
	if got := s.cli("", "get", "resources"); strings.Contains(got.stdout, "good") {
		t.Fatalf("validate created a service:\n%s", got.stdout)
	}
	s.cli("", "validate").want(t, 2)
}

func TestGetCommand(t *testing.T) {
	s := newServer(t)
	dir := t.TempDir()
	s.cli("", "apply", "-f", writeFile(t, dir, "all.yaml", svcDoc("web", "")+"---\n"+svcDoc("api", "")+"---\n"+polDoc("echo", manualEcho))).want(t, 0)
	s.cli("", "run", "echo", "--wait", "--event", `{"name":"x"}`).want(t, 0)

	table := s.cli("", "get", "resources").want(t, 0, "NAME", "TYPE", "STATE", "HEALTH", "api", "web", "service", "command", "registered")
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
	s.cli("", "get", "resources", "web").want(t, 0, "web")
	if got := s.cli("", "get", "resources", "web"); strings.Contains(got.stdout, "api") {
		t.Fatalf("named get listed another service:\n%s", got.stdout)
	}
	s.cli("", "get", "policies").want(t, 0, "echo", "manual", "succeeded")
	s.cli("", "get", "runs").want(t, 0, "echo", "succeeded", "ID")
	s.cli("", "get", "locks").want(t, 0, "NAME")
	s.cli("", "get", "sources").want(t, 0, "NAME", "REPO")
	s.cli("", "get", "connections").want(t, 0, "NAME", "ALLOW ACTIONS", "USED BY")

	var list []map[string]any
	out := s.cli("", "get", "resources", "-o", "json").want(t, 0)
	if err := json.Unmarshal([]byte(out.stdout), &list); err != nil || len(list) != 2 || list[0]["name"] != "api" {
		t.Fatalf("json output: %v\n%s", err, out.stdout)
	}
	var one map[string]any
	out = s.cli("", "get", "resources", "web", "-o", "json").want(t, 0)
	if err := json.Unmarshal([]byte(out.stdout), &one); err != nil || one["name"] != "web" || one["document"] == nil {
		t.Fatalf("json detail: %v\n%s", err, out.stdout)
	}
	var y []map[string]any
	out = s.cli("", "get", "policies", "-o", "yaml").want(t, 0)
	if err := yaml.Unmarshal([]byte(out.stdout), &y); err != nil || len(y) != 1 || y[0]["name"] != "echo" {
		t.Fatalf("yaml output: %v\n%s", err, out.stdout)
	}

	// --limit shows a prefix and says so.
	r := s.cli("", "get", "resources", "--limit", "1").want(t, 0, "api")
	if strings.Contains(r.stdout, "web") || !strings.Contains(r.stderr, "showing the first 1") {
		t.Fatalf("limit: %+v", r)
	}

	s.cli("", "get", "resources", "ghost").want(t, 1)
	s.cli("", "get", "locks", "ghost").want(t, 1)
	s.cli("", "get", "hosts").want(t, 2)
	s.cli("", "get", "services").want(t, 2)
	s.cli("", "get", "policies", "--type", "service").want(t, 2)
	s.cli("", "get", "resources", "--type", "service").want(t, 0, "api", "web")
	s.cli("", "get", "resources", "--type", "bogus").want(t, 1)
	s.cli("", "get").want(t, 2)
	s.cli("", "get", "resources", "-o", "xml").want(t, 2)
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

func TestResourceActionCommand(t *testing.T) {
	s := newServer(t)
	s.cli("", "apply", "-f", writeFile(t, t.TempDir(), "s.yaml", svcDoc("web", "    jobs:\n      reindex: { command: reindex.sh }\n"))).want(t, 0)

	s.cli("", "resource", "action", "web", "start", "--dry-run").want(t, 0, "plan: start web (registered -> running)", "1. ")
	if n := len(s.runner.Calls()); n != 0 {
		t.Fatalf("dry run ran %d commands", n)
	}
	s.cli("", "resource", "action", "web", "start", "--wait").want(t, 0, "started (start web)", "succeeded", "registered -> running")
	s.cli("", "get", "resources", "web").want(t, 0, "running")
	s.cli("", "resource", "action", "web", "run-job", "--job", "reindex", "--wait").want(t, 0, "succeeded")
	s.cli("", "resource", "action", "web", "restart")

	s.cli("", "resource", "action", "web", "explode").want(t, 1)
	s.cli("", "resource", "action", "ghost", "start").want(t, 1)
	s.cli("", "resource", "action", "web", "start").want(t, 1) // already running
	s.cli("", "resource", "action", "web").want(t, 2)
	s.cli("", "resource", "action", "web", "run-job").want(t, 1)
	// The server refuses an instance of a service without instances, and
	// restoring users needs the name typed before anything is asked.
	s.cli("", "resource", "action", "web", "restart", "--instance", "a").want(t, 1)
	s.cli("", "resource", "action", "web", "restore", "--restore-users").want(t, 2)
	s.cli("", "resource", "action", "web", "restore", "--restore-users", "--confirm", "other").want(t, 2)
	s.cli("", "resource", "action", "web", "restore", "--restore-users", "--confirm", "web").want(t, 1)

	// A failing command is reported by --wait.
	s.runner.Reply("web-stop", "boom", 1)
	r := s.cli("", "resource", "action", "web", "stop", "--wait").want(t, 1)
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

	r := run(none, "get", "resources").want(t, 2)
	if !strings.Contains(r.stderr, "no API token") {
		t.Fatalf("stderr = %q", r.stderr)
	}

	file := writeFile(t, dir, "token", "\n"+s.token+"\n")
	run(none, "get", "resources", "--token-file", file).want(t, 0)
	run(map[string]string{"LCM_TOKEN": "", "LCM_TOKEN_FILE": file}, "get", "resources").want(t, 0)
	// The flag wins over the environment.
	bad := writeFile(t, dir, "bad-token", "lcm_wrong\n")
	run(map[string]string{"LCM_TOKEN_FILE": file}, "get", "resources", "--token-file", bad).want(t, 1)

	// Without a flag or variable, the file that host setup writes is used
	// when it exists.
	home := t.TempDir()
	byHome := map[string]string{"LCM_TOKEN": "", "HOME": home}
	run(byHome, "get", "resources").want(t, 2)
	writeFile(t, filepath.Join(home, ".config", "lcm"), "token", s.token+"\n")
	run(byHome, "get", "resources").want(t, 0)
	run(map[string]string{"LCM_TOKEN": "", "HOME": home, "LCM_TOKEN_FILE": bad}, "get", "resources").want(t, 1)

	// A token file that others can read is refused, and says how to fix it.
	open := writeFile(t, dir, "open-token", s.token)
	if err := os.Chmod(open, 0o644); err != nil { //nolint:gosec // the test needs a readable file to prove it is refused
		t.Fatal(err)
	}
	r = run(none, "get", "resources", "--token-file", open).want(t, 1)
	if !strings.Contains(r.stderr, "chmod 600") {
		t.Fatalf("stderr = %q", r.stderr)
	}
	run(none, "get", "resources", "--token-file", filepath.Join(dir, "absent")).want(t, 1)
	empty := writeFile(t, dir, "empty", " \n")
	run(none, "get", "resources", "--token-file", empty).want(t, 1)

	// A wrong token is the server's 401.
	r = run(map[string]string{"LCM_TOKEN": "lcm_nope"}, "get", "resources").want(t, 1)
	if !strings.Contains(r.stderr, "HTTP 401") {
		t.Fatalf("stderr = %q", r.stderr)
	}
	// The token never appears in output.
	if strings.Contains(r.stdout+r.stderr, "lcm_nope") {
		t.Fatal("token leaked into the output")
	}

	// Server URL: flag and environment, and a bad value.
	run(map[string]string{"LCM_SERVER": "http://127.0.0.1:1"}, "get", "resources").want(t, 1)
	run(map[string]string{"LCM_SERVER": "http://127.0.0.1:1"}, "get", "resources", "--server", s.url).want(t, 0)
	run(map[string]string{"LCM_SERVER": "not a url"}, "get", "resources").want(t, 2)
	run(map[string]string{"LCM_SERVER": "ftp://x"}, "get", "resources").want(t, 2)

	// A server that redirects must not receive the token elsewhere.
	redirector := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Redirect(w, r, "http://127.0.0.1:1/", http.StatusFound)
	}))
	defer redirector.Close()
	r = run(map[string]string{"LCM_SERVER": redirector.URL}, "get", "resources").want(t, 1)
	if !strings.Contains(r.stderr, "redirected") {
		t.Fatalf("stderr = %q", r.stderr)
	}
}

func TestClientCommandsInUsage(t *testing.T) {
	a, out, errb, _ := newApp(t, "")
	r := runCLI(t, a, out, errb, "help")
	for _, want := range []string{"apply -f", "export [kind", "validate -f", "get resources", "run <policy>", "resource <command>", "LCM_TOKEN", "Exit status"} {
		if !strings.Contains(r.stdout, want) {
			t.Errorf("usage lacks %q", want)
		}
	}
	// Help output of a client command goes to stderr and succeeds.
	if r := runCLI(t, a, out, errb, "apply", "-h"); r.code != 0 || !strings.Contains(r.stderr, "-dry-run") {
		t.Errorf("apply -h: %+v", r)
	}
}

func TestResourcePhaseCommand(t *testing.T) {
	s := newServer(t)
	s.cli("", "apply", "-f", writeFile(t, t.TempDir(), "s.yaml", svcDoc("web", ""))).want(t, 0)
	write := func(command string) []string {
		return []string{"resource", command, "web", "--reason", "decommission", "--confirm", "web", "--yes"}
	}
	s.cli("", write("retire")...).want(t, 0, `"phase": "retired"`)
	s.cli("", "get", "resources", "web").want(t, 0, "retired")
	s.cli("", write("resume")...).want(t, 0, `"phase": "managed"`)
	s.cli("", write("retire")...).want(t, 0)
	s.cli("", write("unmanage")...).want(t, 0, `"phase": "unmanaged"`)
	s.cli("", "get", "resources", "web").want(t, 1)
	// A stale version is refused by the server, not retried.
	s.cli("", "apply", "-f", writeFile(t, t.TempDir(), "s.yaml", svcDoc("api", ""))).want(t, 0)
	r := s.cli("", "resource", "retire", "api", "--version", "9", "--reason", "x", "--confirm", "api", "--yes").want(t, 1)
	if !strings.Contains(r.stderr, "version") {
		t.Fatalf("stale version: %+v", r)
	}
}

func TestResourceImpactAndGroupCommands(t *testing.T) {
	s := newServer(t)
	dir := t.TempDir()
	labeled := func(name, extra string) string {
		return strings.Replace(svcDoc(name, extra), "metadata: { name: "+name+" }", "metadata: { name: "+name+", labels: { tier: web } }", 1)
	}
	s.cli("", "apply", "-f", writeFile(t, dir, "db.yaml", labeled("db", ""))).want(t, 0)
	s.cli("", "apply", "-f", writeFile(t, dir, "web.yaml", labeled("web", "  relations: [{ type: dependsOn, target: db }]\n"))).want(t, 0)

	s.cli("", "resource", "impact", "db").want(t, 0, `"name": "web"`, `"relation": "dependsOn"`)
	s.cli("", "resource", "group-action", "start").want(t, 2)
	s.cli("", "resource", "group-action", "--type", "machine", "--label", "tier=web", "start").want(t, 2)
	s.cli("", "resource", "group-action", "--type", "service", "--label", "tier=web", "start", "--dry-run").
		want(t, 0, "1. start db (registered -> running)", "2. start web (registered -> running)")
	if n := len(s.runner.Calls()); n != 0 {
		t.Fatalf("dry run ran %d commands", n)
	}
	s.cli("", "resource", "group-action", "--label", "tier=web", "start", "--wait").want(t, 0, "db: succeeded", "web: succeeded", "succeeded")

	// Stopping db alone needs the impact acknowledged.
	s.cli("", "resource", "action", "db", "stop", "--dry-run").want(t, 0, "impact: web (dependsOn db)", "requires --acknowledge-impact")
	r := s.cli("", "resource", "action", "db", "stop").want(t, 1)
	if !strings.Contains(r.stderr, "acknowledge") {
		t.Fatalf("unacknowledged stop: %+v", r)
	}
	s.cli("", "resource", "action", "db", "stop", "--acknowledge-impact", "--wait").want(t, 0, "succeeded")

	// A failing step stops the group: web stops first, db is not run.
	s.runner.Reply("web-stop", "boom", 1)
	s.cli("", "resource", "action", "db", "start", "--wait").want(t, 0)
	r = s.cli("", "resource", "group-action", "--label", "tier=web", "stop", "--acknowledge-impact", "--wait").want(t, 1, "web: failed", "db: notRun")
	if !strings.Contains(r.stderr, "failed") {
		t.Fatalf("group failure: %+v", r)
	}
}
