package monitor

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"os/exec"
	"time"
)

// Output caps for command-backed checks. Check results are stored for days,
// so nothing larger than a short excerpt is worth keeping.
const (
	maxCommandOutput = 4 << 10
	killGrace        = 2 * time.Second
)

// Command is one external program invocation. Args are passed as an argv
// list; no shell is involved.
type Command struct {
	Name string
	Args []string
}

// CommandResult is the outcome of a program that ran to completion.
type CommandResult struct {
	ExitCode int
	Stdout   string
	Stderr   string
}

// Runner executes external programs for checks. A non-zero exit status is a
// normal result; the error is non-nil only when the program could not be
// started or ctx ended before it finished.
type Runner interface {
	Run(ctx context.Context, cmd Command) (CommandResult, error)
}

// execRunner is the os/exec backed Runner. It kills the whole process group
// on cancellation so that children of a wrapper script do not outlive a
// timed-out check.
type execRunner struct{}

func (execRunner) Run(ctx context.Context, c Command) (CommandResult, error) {
	cmd := exec.CommandContext(ctx, c.Name, c.Args...) //nolint:gosec // argv comes from validated check definitions and never passes through a shell
	var stdout, stderr cappedBuffer
	stdout.max, stderr.max = maxCommandOutput, maxCommandOutput
	cmd.Stdout, cmd.Stderr = &stdout, &stderr
	setProcessGroup(cmd)
	cmd.WaitDelay = killGrace
	err := cmd.Run()
	res := CommandResult{Stdout: stdout.String(), Stderr: stderr.String()}
	var exitErr *exec.ExitError
	switch {
	case err == nil:
		return res, nil
	case ctx.Err() != nil:
		return res, ctx.Err()
	case errors.As(err, &exitErr):
		res.ExitCode = exitErr.ExitCode()
		return res, nil
	default:
		return res, fmt.Errorf("run %s: %w", c.Name, err)
	}
}

// cappedBuffer keeps the first max bytes and silently discards the rest, so
// a chatty program never blocks on a full pipe.
type cappedBuffer struct {
	buf bytes.Buffer
	max int
}

func (b *cappedBuffer) Write(p []byte) (int, error) {
	if room := b.max - b.buf.Len(); room > 0 {
		b.buf.Write(p[:min(len(p), room)])
	}
	return len(p), nil
}

func (b *cappedBuffer) String() string { return b.buf.String() }
