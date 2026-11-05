package securityhost

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/ACaccel/lcm/internal/audit"
	"github.com/ACaccel/lcm/internal/store/storetest"
)

// shellRunner runs script with /bin/sh in place of the privileged helper;
// args become $1, $2, ...
func shellRunner(script string, args ...string) commandRunner {
	return commandRunner{argv: append([]string{"/bin/sh", "-c", script, "helper"}, args...)}
}

func TestRunnerPassesRequestInFixedEnvironment(t *testing.T) {
	t.Setenv("LCM_RUNNER_TEST_SECRET", "must-not-reach-the-helper")
	dir := t.TempDir()
	stdin, env, cwd := filepath.Join(dir, "stdin"), filepath.Join(dir, "env"), filepath.Join(dir, "cwd")
	runner := shellRunner(`cat > "$1"; env > "$2"; pwd > "$3"
printf '%s' '{"version":1,"status":{"available":true,"machineId":"machine-a"}}'`, stdin, env, cwd)
	request := Request{Version: ProtocolVersion, Command: "status", MachineID: "machine-a"}

	response, err := runner.Run(context.Background(), request)
	if err != nil {
		t.Fatalf("run: %v", err)
	}
	if response.Status == nil || !response.Status.Available || response.Status.MachineID != "machine-a" {
		t.Fatalf("response = %+v", response)
	}
	want, _ := json.Marshal(request)
	if got := readFile(t, stdin); got != string(want) {
		t.Fatalf("helper read %q, want %q", got, want)
	}
	environment := readFile(t, env)
	if strings.Contains(environment, "LCM_RUNNER_TEST_SECRET") || !strings.Contains(environment, "PATH=/usr/sbin:/usr/bin:/sbin:/bin\n") {
		t.Fatalf("helper environment:\n%s", environment)
	}
	if got := strings.TrimSpace(readFile(t, cwd)); got != "/" {
		t.Fatalf("helper ran in %q", got)
	}
}

func TestRunnerMapsHelperReplies(t *testing.T) {
	cases := []struct {
		name   string
		script string
		want   error
	}{
		{"conflict", `printf '%s' '{"version":1,"error":"conflict"}'`, ErrConflict},
		{"not found", `printf '%s' '{"version":1,"error":"not-found"}'`, ErrNotFound},
		{"other error", `printf '%s' '{"version":1,"error":"disk full at /secret/path"}'`, ErrUnavailable},
		{"wrong version", `printf '%s' '{"version":2}'`, ErrUnavailable},
		{"unknown field", `printf '%s' '{"version":1,"debug":"x"}'`, ErrUnavailable},
		{"trailing document", `printf '%s' '{"version":1}{"version":1}'`, ErrUnavailable},
		{"not json", `echo 'Traceback: secret detail'`, ErrUnavailable},
		{"no output", `:`, ErrUnavailable},
		{"failed exit", `echo 'secret detail' >&2; printf '%s' '{"version":1}'; exit 3`, ErrUnavailable},
		{"oversized output", `head -c 70000 /dev/zero`, ErrUnavailable},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			_, err := shellRunner(tc.script).Run(context.Background(), Request{Version: ProtocolVersion, Command: "status"})
			// The sentinel itself, never wrapped: helper output may contain
			// local data and must not reach callers.
			if err != tc.want { //nolint:errorlint // the error must be the bare sentinel
				t.Fatalf("err = %v, want %v", err, tc.want)
			}
		})
	}
	// Trailing whitespace after the single document is accepted.
	if _, err := shellRunner(`printf '%s\n' '{"version":1}'`).Run(context.Background(), Request{Command: "status"}); err != nil {
		t.Fatalf("newline-terminated reply: %v", err)
	}
}

func TestRunnerRejectsOversizedRequestWithoutRunning(t *testing.T) {
	marker := filepath.Join(t.TempDir(), "ran")
	runner := shellRunner(`touch "$1"`, marker)
	request := Request{Command: "preview", Reason: strings.Repeat("x", maxRequestBytes)}
	if _, err := runner.Run(context.Background(), request); !errors.Is(err, ErrUnavailable) {
		t.Fatalf("err = %v", err)
	}
	if _, err := os.Stat(marker); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("helper ran for an oversized request: %v", err)
	}
}

func TestRunnerStopsHelperWhenContextEnds(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 200*time.Millisecond)
	defer cancel()
	done := make(chan error, 1)
	go func() {
		_, err := shellRunner(`exec sleep 60`).Run(ctx, Request{Command: "status"})
		done <- err
	}()
	select {
	case err := <-done:
		if !errors.Is(err, ErrUnavailable) {
			t.Fatalf("err = %v", err)
		}
	case <-time.After(20 * time.Second):
		t.Fatal("runner did not stop the helper when its context ended")
	}
}

func TestDefaultRunnerIsFixedHelper(t *testing.T) {
	db := storetest.Open(t)
	c, err := New(Config{DB: db, Audit: audit.New(db), MachineID: "machine-a"})
	if err != nil {
		t.Fatal(err)
	}
	r, ok := c.cfg.Runner.(commandRunner)
	want := "/usr/bin/sudo -n /usr/bin/python3 -I /usr/local/libexec/lcm/security-host/helper.py"
	if !ok || strings.Join(r.argv, " ") != want {
		t.Fatalf("default runner = %#v", c.cfg.Runner)
	}
}

func readFile(t *testing.T, path string) string {
	t.Helper()
	b, err := os.ReadFile(path) //nolint:gosec // paths inside the test temp dir
	if err != nil {
		t.Fatal(err)
	}
	return string(b)
}
