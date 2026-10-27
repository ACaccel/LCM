package execx_test

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
	"testing"
	"time"

	"github.com/ACaccel/lcm/internal/execx"
)

func TestRunCapturesOutputAndExit(t *testing.T) {
	r := execx.New()
	res, err := r.Run(context.Background(), execx.Cmd{Shell: "echo out; echo err >&2"})
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(res.Output, "out") || !strings.Contains(res.Output, "err") || res.ExitCode != 0 {
		t.Fatalf("unexpected result %+v", res)
	}

	res, err = r.Run(context.Background(), execx.Cmd{Argv: []string{"sh", "-c", "echo boom; exit 7"}})
	var ee *execx.ExitError
	if !errors.As(err, &ee) || ee.Code != 7 || res.ExitCode != 7 || execx.ExitCode(err) != 7 {
		t.Fatalf("want exit 7, got %+v %v", res, err)
	}
	if !strings.Contains(res.Output, "boom") {
		t.Fatalf("output lost: %q", res.Output)
	}
}

func TestRunValidation(t *testing.T) {
	r := execx.New()
	for name, c := range map[string]execx.Cmd{
		"empty":    {},
		"both":     {Argv: []string{"true"}, Shell: "true"},
		"bad env":  {Argv: []string{"true"}, Env: []string{"NOEQUALS"}},
		"blank":    {Shell: "  "},
		"no prog":  {Argv: []string{""}},
		"bad prog": {Argv: []string{"/nonexistent/program"}},
	} {
		if _, err := r.Run(context.Background(), c); err == nil {
			t.Errorf("%s: want error", name)
		}
	}
}

func TestOutputKeepsTail(t *testing.T) {
	r := execx.New()
	res, err := r.Run(context.Background(), execx.Cmd{
		Shell:     `i=0; while [ $i -lt 500 ]; do echo line-$i; i=$((i+1)); done`,
		MaxOutput: 100,
	})
	if err != nil {
		t.Fatal(err)
	}
	if !res.Truncated || len(res.Output) > 100 || !strings.HasSuffix(res.Output, "line-499\n") {
		t.Fatalf("tail not kept: truncated=%v %q", res.Truncated, res.Output)
	}
}

func TestEnvironmentIsAllowlisted(t *testing.T) {
	t.Setenv("LCM_SECRET_FOR_TEST", "hunter2")
	t.Setenv("LCM_VISIBLE_FOR_TEST", "yes")
	r := execx.New(execx.WithEnvAllow("PATH", "LCM_VISIBLE_FOR_TEST"))
	res, err := r.Run(context.Background(), execx.Cmd{
		Shell: `echo "secret=[$LCM_SECRET_FOR_TEST] visible=[$LCM_VISIBLE_FOR_TEST] extra=[$EXTRA]"`,
		Env:   []string{"EXTRA=1"},
	})
	if err != nil {
		t.Fatal(err)
	}
	if strings.TrimSpace(res.Output) != "secret=[] visible=[yes] extra=[1]" {
		t.Fatalf("unexpected environment: %q", res.Output)
	}
}

func TestDefaultPathWhenNotInherited(t *testing.T) {
	r := execx.New(execx.WithEnvLookup(func(string) (string, bool) { return "", false }))
	res, err := r.Run(context.Background(), execx.Cmd{Shell: `echo "$PATH"`})
	if err != nil || !strings.Contains(res.Output, "/usr/bin") {
		t.Fatalf("want default PATH, got %q %v", res.Output, err)
	}
}

func TestDir(t *testing.T) {
	dir := t.TempDir()
	res, err := execx.New().Run(context.Background(), execx.Cmd{Argv: []string{"pwd"}, Dir: dir})
	if err != nil {
		t.Fatal(err)
	}
	got, _ := os.Readlink(strings.TrimSpace(res.Output))
	if got == "" {
		got = strings.TrimSpace(res.Output)
	}
	if got != dir {
		t.Fatalf("pwd = %q, want %q", got, dir)
	}
}

func TestTimeoutKillsProcessGroup(t *testing.T) {
	pidFile := filepath.Join(t.TempDir(), "child.pid")
	// The background child is in the shell's process group and must die with it.
	script := `sleep 30 & echo $! > ` + pidFile + `; wait`
	start := time.Now()
	_, err := execx.New().Run(context.Background(), execx.Cmd{Shell: script, Timeout: 300 * time.Millisecond})
	if !errors.Is(err, execx.ErrTimeout) {
		t.Fatalf("want ErrTimeout, got %v", err)
	}
	if time.Since(start) > 10*time.Second {
		t.Fatalf("timeout took too long: %s", time.Since(start))
	}
	raw, err := os.ReadFile(pidFile) //nolint:gosec // path is inside the test temp dir
	if err != nil {
		t.Fatalf("child never started: %v", err)
	}
	pid, err := strconv.Atoi(strings.TrimSpace(string(raw)))
	if err != nil {
		t.Fatal(err)
	}
	// The orphan is reaped by init asynchronously; poll briefly.
	deadline := time.Now().Add(5 * time.Second)
	for syscall.Kill(pid, 0) == nil {
		if time.Now().After(deadline) {
			t.Fatalf("background child %d survived the kill", pid)
		}
		time.Sleep(10 * time.Millisecond)
	}
}

func TestContextCancel(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	go cancel()
	_, err := execx.New().Run(ctx, execx.Cmd{Shell: "sleep 30"})
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("want context.Canceled, got %v", err)
	}
	if _, err := execx.New().Run(ctx, execx.Cmd{Shell: "true"}); !errors.Is(err, context.Canceled) {
		t.Fatalf("cancelled ctx must not start: %v", err)
	}
}

func TestDaemonizingCommandDoesNotHang(t *testing.T) {
	start := time.Now()
	res, err := execx.New().Run(context.Background(), execx.Cmd{Shell: "sleep 5 & echo started"})
	if err != nil {
		t.Fatalf("want success, got %v", err)
	}
	if time.Since(start) > 4*time.Second || !strings.Contains(res.Output, "started") {
		t.Fatalf("hung on lingering child: %s %q", time.Since(start), res.Output)
	}
}

func TestFake(t *testing.T) {
	f := (&execx.Fake{}).Reply("git status", " M file\n", 0).Reply("fail", "nope", 3)
	res, err := f.Run(context.Background(), execx.Cmd{Argv: []string{"git", "status"}})
	if err != nil || res.Output != " M file\n" {
		t.Fatalf("got %+v %v", res, err)
	}
	if _, err := f.Run(context.Background(), execx.Cmd{Shell: "fail now"}); execx.ExitCode(err) != 3 {
		t.Fatalf("want exit 3, got %v", err)
	}
	if got := f.Displays(); len(got) != 2 || got[0] != "git status" {
		t.Fatalf("calls = %v", got)
	}
	f.Reset()
	if len(f.Calls()) != 0 {
		t.Fatal("Reset kept calls")
	}
}
