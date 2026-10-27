// Package execx runs external commands for the runtime adapters and the
// deployment pipeline. Commands run with a bounded lifetime, a minimal
// environment, capped output and, on Unix, in their own process group so
// that cancellation also stops the children of a shell.
package execx

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"
)

// DefaultMaxOutput is the number of output bytes kept when Cmd.MaxOutput is
// zero. Only the tail is kept because failures are reported at the end.
const DefaultMaxOutput = 64 << 10

// Cmd describes one command. Exactly one of Argv and Shell must be set.
type Cmd struct {
	// Argv is executed directly, without a shell.
	Argv []string
	// Shell is a script run as `sh -c Shell`. Use it only for commands
	// written by the operator, never for values taken from untrusted input.
	Shell string
	// Dir is the working directory; empty means the process's directory.
	Dir string
	// Env holds additional KEY=VALUE entries. They are added to the allowlisted
	// base environment and win over it.
	Env []string
	// Timeout bounds the run; zero means no limit beyond ctx.
	Timeout time.Duration
	// MaxOutput bounds the captured output; zero means DefaultMaxOutput.
	MaxOutput int
}

// Display returns a short, log-safe description of the command.
func (c Cmd) Display() string {
	if c.Shell != "" {
		return "sh -c " + quoteIfNeeded(firstLine(c.Shell))
	}
	parts := make([]string, len(c.Argv))
	for i, a := range c.Argv {
		parts[i] = quoteIfNeeded(a)
	}
	return strings.Join(parts, " ")
}

func firstLine(s string) string {
	line, rest, multi := strings.Cut(s, "\n")
	if multi && rest != "" {
		line += " ..."
	}
	return line
}

func quoteIfNeeded(s string) string {
	if s == "" || strings.ContainsAny(s, " \t\"'\\$`") {
		return fmt.Sprintf("%q", s)
	}
	return s
}

func (c Cmd) validate() error {
	switch {
	case len(c.Argv) > 0 && c.Shell != "":
		return errors.New("execx: set either Argv or Shell, not both")
	case len(c.Argv) == 0 && strings.TrimSpace(c.Shell) == "":
		return errors.New("execx: empty command")
	case len(c.Argv) > 0 && c.Argv[0] == "":
		return errors.New("execx: empty program name")
	}
	for _, e := range c.Env {
		if k, _, ok := strings.Cut(e, "="); !ok || k == "" {
			return fmt.Errorf("execx: invalid environment entry %q", e)
		}
	}
	return nil
}

// Result is the outcome of a command that was started.
type Result struct {
	// ExitCode is the process exit status, or -1 when the process did not
	// exit normally (not started, killed by a signal or timed out).
	ExitCode int
	// Output is the combined stdout and stderr, limited to the last
	// MaxOutput bytes.
	Output string
	// Truncated reports that earlier output was discarded.
	Truncated bool
	Duration  time.Duration
}

// ExitError reports a command that ran and exited with a non-zero status.
type ExitError struct {
	Code int
}

func (e *ExitError) Error() string { return fmt.Sprintf("exit status %d", e.Code) }

// ErrTimeout is wrapped by the error of a command stopped by Cmd.Timeout.
var ErrTimeout = errors.New("command timed out")

// Runner runs commands. Run returns a nil error only when the command exited
// with status 0. A non-zero exit yields an *ExitError together with the
// Result, so callers that treat exit codes as data can use errors.As.
type Runner interface {
	Run(ctx context.Context, c Cmd) (Result, error)
}

// ExitCode returns the exit status carried by err, or -1 if err is not an
// *ExitError.
func ExitCode(err error) int {
	var ee *ExitError
	if errors.As(err, &ee) {
		return ee.Code
	}
	return -1
}

// tail keeps the last max bytes written to it.
type tail struct {
	max       int
	buf       []byte
	truncated bool
}

func (t *tail) Write(p []byte) (int, error) {
	n := len(p)
	if len(p) >= t.max {
		if len(p) > t.max || len(t.buf) > 0 {
			t.truncated = true
		}
		t.buf = append(t.buf[:0], p[len(p)-t.max:]...)
		return n, nil
	}
	t.buf = append(t.buf, p...)
	// Trim lazily so that a chatty process costs amortized O(1) per byte.
	if len(t.buf) > 2*t.max {
		t.truncated = true
		t.buf = append(t.buf[:0], t.buf[len(t.buf)-t.max:]...)
	}
	return n, nil
}

func (t *tail) result() (string, bool) {
	b := t.buf
	if len(b) > t.max {
		t.truncated = true
		b = b[len(b)-t.max:]
	}
	s := string(b)
	if t.truncated {
		// The cut may fall inside a multi-byte character.
		s = strings.ToValidUTF8(s, "")
	}
	return s, t.truncated
}
