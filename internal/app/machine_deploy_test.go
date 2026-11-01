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

	"github.com/ACaccel/lcm/internal/machine/power"
)

// checkedMachinePowerWrapper verifies every executable line before any test
// runs the script, so a changed guard cannot reach the real privileged helper.
func checkedMachinePowerWrapper(t *testing.T) []byte {
	t.Helper()
	script := readDeploy(t, "machine-power")
	if !bytes.HasPrefix(script, []byte("#!/bin/sh\n")) {
		t.Fatal("machine-power must use the fixed /bin/sh interpreter")
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
		`printf '%s\n' 'machine-power accepts no arguments' >&2`,
		"exit 2",
		"fi",
	}
	if len(lines) != len(wantGuard)+1 || !slices.Equal(lines[:len(wantGuard)], wantGuard) {
		t.Fatalf("wrapper must contain only the argument guard and fixed exec, got:\n%s", script)
	}
	// Exact tokens exclude expansions, redirects, shell operators, caller paths,
	// extra arguments and environment/config sourcing at the privilege boundary.
	wantExec := []string{
		"exec", "/usr/bin/env", "-i",
		"PATH=/usr/sbin:/usr/bin:/sbin:/bin", "LC_ALL=C",
		"/usr/local/libexec/lcm-machine-helper", "machine-helper",
	}
	if got := strings.Fields(lines[len(wantGuard)]); !slices.Equal(got, wantExec) {
		t.Fatalf("wrapper exec = %q, want %q", got, wantExec)
	}
	return script
}

func TestMachinePowerWrapperShellSyntax(t *testing.T) {
	script := checkedMachinePowerWrapper(t)
	ctx, cancel := context.WithTimeout(t.Context(), 5*time.Second)
	defer cancel()
	cmd := exec.CommandContext(ctx, "/bin/sh", "-n")
	cmd.Stdin = bytes.NewReader(script)
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("wrapper shell syntax: %v: %s", err, out)
	}
}

func TestMachinePowerWrapperRefusesArguments(t *testing.T) {
	script := checkedMachinePowerWrapper(t)
	cases := []struct {
		name string
		args []string
	}{
		{"empty argument", []string{""}},
		{"option", []string{"--help"}},
		{"option separator", []string{"--"}},
		{"helper path", []string{"/tmp/caller-helper"}},
		{"subcommand", []string{"machine-helper"}},
		{"multiple arguments", []string{"reboot", "--force"}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			ctx, cancel := context.WithTimeout(t.Context(), 5*time.Second)
			defer cancel()
			cmd := exec.CommandContext(ctx, "/bin/sh", "-s", "--")
			cmd.Args = append(cmd.Args, tc.args...)
			cmd.Env = []string{
				"PATH=/caller-controlled",
				"LCM_MACHINE_HELPER=/tmp/caller-helper",
				"LCM_CONFIG=/tmp/caller-config",
			}
			cmd.Stdin = bytes.NewReader(script)
			var stdout, stderr bytes.Buffer
			cmd.Stdout, cmd.Stderr = &stdout, &stderr
			var exit *exec.ExitError
			if err := cmd.Run(); !errors.As(err, &exit) || exit.ExitCode() != 2 {
				t.Fatalf("wrapper error = %v, want exit 2; stderr: %s", err, &stderr)
			}
			if stdout.Len() != 0 || stderr.String() != "machine-power accepts no arguments\n" {
				t.Fatalf("unexpected output: stdout %q, stderr %q", &stdout, &stderr)
			}
		})
	}
}

func TestMachinePowerSudoersAllowsOnlyNoArgumentWrapper(t *testing.T) {
	var rules []string
	for line := range strings.SplitSeq(string(readDeploy(t, "machine-power-sudoers")), "\n") {
		line = strings.TrimSpace(line)
		if line != "" && !strings.HasPrefix(line, "#") {
			rules = append(rules, line)
		}
	}
	want := []string{`lcm ALL=(root) NOPASSWD: ` + power.WrapperPath + ` ""`}
	if !slices.Equal(rules, want) {
		t.Fatalf("sudoers rules = %q, want only %q", rules, want)
	}
}
