package app_test

import (
	"bytes"
	"context"
	"errors"
	"os/exec"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/ACaccel/lcm/internal/deployhelper"
)

// checkedDeployWrapper verifies every executable line of the deploy
// wrapper before a test runs it, so that a changed guard can never reach
// the real helper.
func checkedDeployWrapper(t *testing.T) []byte {
	t.Helper()
	script := readDeploy(t, "lcm-deploy")
	if !bytes.HasPrefix(script, []byte("#!/bin/sh\n")) {
		t.Fatal("lcm-deploy must use the fixed /bin/sh interpreter")
	}
	var lines []string
	for line := range strings.SplitSeq(string(script), "\n") {
		line = strings.TrimSpace(line)
		if line != "" && !strings.HasPrefix(line, "#") {
			lines = append(lines, line)
		}
	}
	wantGuard := []string{
		"set -eu",
		`if [ "$#" -ne 0 ]; then`,
		`printf '%s\n' 'lcm-deploy accepts no arguments' >&2`,
		"exit 2",
		"fi",
	}
	if len(lines) != len(wantGuard)+1 || !slices.Equal(lines[:len(wantGuard)], wantGuard) {
		t.Fatalf("wrapper must contain only the argument guard and a fixed exec, got:\n%s", script)
	}
	wantExec := []string{
		"exec", "/usr/bin/env", "-i",
		"PATH=/usr/sbin:/usr/bin:/sbin:/bin", "LC_ALL=C",
		"/usr/local/libexec/lcm-deploy-helper", "deploy-helper",
	}
	if got := strings.Fields(lines[len(wantGuard)]); !slices.Equal(got, wantExec) {
		t.Fatalf("wrapper exec = %q, want %q", got, wantExec)
	}
	return script
}

func TestDeployWrapperRefusesArguments(t *testing.T) {
	script := checkedDeployWrapper(t)
	ctx, cancel := context.WithTimeout(t.Context(), 5*time.Second)
	defer cancel()
	syntax := exec.CommandContext(ctx, "/bin/sh", "-n")
	syntax.Stdin = bytes.NewReader(script)
	if out, err := syntax.CombinedOutput(); err != nil {
		t.Fatalf("wrapper shell syntax: %v: %s", err, out)
	}
	for _, args := range [][]string{{""}, {"--help"}, {"deploy-helper"}, {"build", "web"}} {
		cmd := exec.CommandContext(ctx, "/bin/sh", "-s", "--")
		cmd.Args = append(cmd.Args, args...)
		cmd.Env = []string{"PATH=/caller-controlled"}
		cmd.Stdin = bytes.NewReader(script)
		var stdout, stderr bytes.Buffer
		cmd.Stdout, cmd.Stderr = &stdout, &stderr
		var exit *exec.ExitError
		if err := cmd.Run(); !errors.As(err, &exit) || exit.ExitCode() != 2 {
			t.Fatalf("%q: %v, want exit 2; stderr: %s", args, err, &stderr)
		}
		if stdout.Len() != 0 || stderr.String() != "lcm-deploy accepts no arguments\n" {
			t.Fatalf("%q: stdout %q, stderr %q", args, &stdout, &stderr)
		}
	}
}

func TestDeploySudoersAllowsOnlyTheNoArgumentWrapper(t *testing.T) {
	var rules []string
	for line := range strings.SplitSeq(string(readDeploy(t, "lcm-deploy-sudoers")), "\n") {
		line = strings.TrimSpace(line)
		if line != "" && !strings.HasPrefix(line, "#") {
			rules = append(rules, line)
		}
	}
	want := []string{`lcm ALL=(root) NOPASSWD: ` + deployhelper.WrapperPath + ` ""`}
	if !slices.Equal(rules, want) {
		t.Fatalf("sudoers rules = %q, want only %q", rules, want)
	}
}
