package main

import (
	"bytes"
	"context"
	"io"
	"net"
	"net/http"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/ACaccel/lcm/internal/audit"
	"github.com/ACaccel/lcm/internal/auth"
	"github.com/ACaccel/lcm/internal/store"
)

type result struct {
	code           int
	stdout, stderr string
}

func newApp(t *testing.T, stdin string) (*app, *bytes.Buffer, *bytes.Buffer, string) {
	t.Helper()
	dir := filepath.Join(t.TempDir(), "data")
	var out, errb bytes.Buffer
	env := map[string]string{"LCM_DATA_DIR": dir}
	a := &app{
		stdin: strings.NewReader(stdin), stdout: &out, stderr: &errb,
		getenv: func(k string) string { return env[k] },
	}
	return a, &out, &errb, dir
}

func runCLI(t *testing.T, a *app, out, errb *bytes.Buffer, args ...string) result {
	t.Helper()
	out.Reset()
	errb.Reset()
	code := a.run(context.Background(), args)
	return result{code, out.String(), errb.String()}
}

func openDB(t *testing.T, dir string) *store.DB {
	t.Helper()
	db, err := store.Open(context.Background(), filepath.Join(dir, "lcm.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = db.Close() })
	return db
}

func TestVersionAndUsage(t *testing.T) {
	a, out, errb, _ := newApp(t, "")
	if r := runCLI(t, a, out, errb, "version"); r.code != 0 || !strings.HasPrefix(r.stdout, "lcm ") {
		t.Errorf("version: %+v", r)
	}
	for _, args := range [][]string{{}, {"bogus"}, {"token"}, {"token", "bogus"}, {"admin"}, {"token", "create"}, {"token", "revoke"}, {"token", "revoke", "x"}, {"serve", "extra"}} {
		r := runCLI(t, a, out, errb, args...)
		if r.code != 2 || !strings.Contains(r.stderr, "Usage:") {
			t.Errorf("%v: code=%d stderr=%q", args, r.code, r.stderr)
		}
	}
	if r := runCLI(t, a, out, errb, "help"); r.code != 0 || !strings.Contains(r.stdout, "Usage:") {
		t.Errorf("help: %+v", r)
	}
	if r := runCLI(t, a, out, errb, "serve", "-h"); r.code != 0 {
		t.Errorf("serve -h: %+v", r)
	}
}

func TestSetPassword(t *testing.T) {
	a, out, errb, dir := newApp(t, "a very long password\n")
	r := runCLI(t, a, out, errb, "admin", "set-password")
	if r.code != 0 || !strings.Contains(r.stdout, `"admin" created`) {
		t.Fatalf("create: %+v", r)
	}
	a.stdin = strings.NewReader("another long password")
	if r := runCLI(t, a, out, errb, "admin", "set-password", "--username", "root"); r.code != 0 || !strings.Contains(r.stdout, `"root" created`) {
		t.Fatalf("custom user, no trailing newline: %+v", r)
	}
	a.stdin = strings.NewReader("a very long password\n")
	if r := runCLI(t, a, out, errb, "admin", "set-password"); r.code != 0 || !strings.Contains(r.stdout, "updated") {
		t.Fatalf("update: %+v", r)
	}

	a.stdin = strings.NewReader("short\n")
	if r := runCLI(t, a, out, errb, "admin", "set-password"); r.code != 1 || !strings.Contains(r.stderr, "at least 8") {
		t.Errorf("weak password: %+v", r)
	}
	a.stdin = strings.NewReader("")
	if r := runCLI(t, a, out, errb, "admin", "set-password"); r.code != 1 {
		t.Errorf("empty stdin: %+v", r)
	}

	// The password must be usable and every change audited as the CLI.
	db := openDB(t, dir)
	svc, err := auth.NewService(db, time.Hour)
	if err != nil {
		t.Fatal(err)
	}
	if _, _, _, err := svc.Login(context.Background(), "admin", "a very long password"); err != nil {
		t.Fatalf("login with CLI-set password: %v", err)
	}
	entries, err := audit.New(db).List(context.Background(), 100, 0)
	if err != nil {
		t.Fatal(err)
	}
	if len(entries) != 3 {
		t.Fatalf("audit entries = %d, want 3 (failed attempts are not state changes)", len(entries))
	}
	for _, e := range entries {
		if e.Actor != auth.CLI || e.Action != "admin.set_password" || e.Outcome != audit.OutcomeOK {
			t.Errorf("entry = %+v", e)
		}
	}
}

func TestTokenCommands(t *testing.T) {
	a, out, errb, dir := newApp(t, "")

	r := runCLI(t, a, out, errb, "token", "create", "--name", "ci")
	secret := strings.TrimSpace(r.stdout)
	if r.code != 0 || !strings.HasPrefix(secret, "lcm_") || strings.Contains(r.stderr, secret) {
		t.Fatalf("create: %+v", r)
	}

	r = runCLI(t, a, out, errb, "token", "list")
	if r.code != 0 || !strings.Contains(r.stdout, "ci") || !strings.Contains(r.stdout, "active") || strings.Contains(r.stdout, secret) {
		t.Fatalf("list: %+v", r)
	}

	if r := runCLI(t, a, out, errb, "token", "revoke", "1"); r.code != 0 {
		t.Fatalf("revoke: %+v", r)
	}
	if r := runCLI(t, a, out, errb, "token", "list"); !strings.Contains(r.stdout, "revoked") {
		t.Errorf("list after revoke: %+v", r)
	}
	if r := runCLI(t, a, out, errb, "token", "revoke", "99"); r.code != 1 || !strings.Contains(r.stderr, "not found") {
		t.Errorf("revoke unknown: %+v", r)
	}

	db := openDB(t, dir)
	svc, err := auth.NewService(db, time.Hour)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := svc.VerifyToken(context.Background(), secret); err == nil {
		t.Error("revoked token still verifies")
	}
	entries, err := audit.New(db).List(context.Background(), 100, 0)
	if err != nil {
		t.Fatal(err)
	}
	var actions []string
	for _, e := range entries {
		actions = append(actions, e.Action)
		if e.Actor.Kind != auth.KindSystem || e.Actor.Name != "cli" {
			t.Errorf("entry %s actor = %+v", e.Action, e.Actor)
		}
		for _, v := range e.Details {
			if s, _ := v.(string); strings.Contains(s, secret) {
				t.Error("audit details contain the token secret")
			}
		}
	}
	if got := strings.Join(actions, ","); got != "token.revoke,token.create" {
		t.Errorf("actions = %s", got)
	}
}

func TestTokenCreateScope(t *testing.T) {
	a, out, errb, _ := newApp(t, "")

	r := runCLI(t, a, out, errb, "token", "create", "--name", "hook", "--scope", "ingest")
	if r.code != 0 {
		t.Fatalf("create ingest: %+v", r)
	}
	r = runCLI(t, a, out, errb, "token", "list")
	if r.code != 0 || !strings.Contains(r.stdout, "SCOPE") || !strings.Contains(r.stdout, "ingest") {
		t.Fatalf("list: %+v", r)
	}
	if r := runCLI(t, a, out, errb, "token", "create", "--name", "x", "--scope", "root"); r.code != 2 {
		t.Errorf("invalid scope exit code = %d, want 2: %+v", r.code, r)
	}
}

func TestInvalidConfigFailsFast(t *testing.T) {
	a, out, errb, _ := newApp(t, "")
	for _, args := range [][]string{
		{"serve", "--log-level", "loud"},
		{"serve", "--listen", "nonsense"},
		{"serve", "--log-format", "xml"},
		{"token", "list", "--data-dir", " "},
	} {
		r := runCLI(t, a, out, errb, args...)
		if r.code != 1 || !strings.Contains(r.stderr, "invalid configuration") {
			t.Errorf("%v: %+v", args, r)
		}
	}
}

func TestServeAndGracefulShutdown(t *testing.T) {
	a, _, errb, _ := newApp(t, "")
	addrCh := make(chan net.Addr, 1)
	a.onListen = func(addr net.Addr) { addrCh <- addr }

	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { done <- a.serve(ctx, []string{"--listen", "127.0.0.1:0", "--log-format", "json"}) }()

	var addr net.Addr
	select {
	case addr = <-addrCh:
	case err := <-done:
		t.Fatalf("serve exited early: %v; stderr: %s", err, errb.String())
	case <-time.After(10 * time.Second):
		t.Fatal("server did not start")
	}

	resp, err := http.Get("http://" + addr.String() + "/api/v1/health") //nolint:noctx // local test request
	if err != nil {
		t.Fatal(err)
	}
	body, _ := io.ReadAll(resp.Body)
	_ = resp.Body.Close()
	if resp.StatusCode != http.StatusOK || !strings.Contains(string(body), `"status":"ok"`) {
		t.Fatalf("health: %d %s", resp.StatusCode, body)
	}

	cancel()
	select {
	case err := <-done:
		if err != nil {
			t.Fatalf("shutdown: %v", err)
		}
	case <-time.After(15 * time.Second):
		t.Fatal("server did not stop")
	}
}
