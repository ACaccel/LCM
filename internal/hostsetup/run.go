package hostsetup

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
	"time"
	"unicode"
	"unicode/utf8"
)

// SafePath is the PATH of every command host setup runs; nothing from the
// caller's environment reaches them.
const SafePath = "/usr/local/sbin:/usr/local/bin:/usr/sbin:/usr/bin:/sbin:/bin"

// Account is a local user account.
type Account struct {
	Name string
	UID  int
	GID  int
	Home string
	// GECOS is the comment field; Shell is the login shell.
	GECOS, Shell string
}

// Command is one program run by host setup.
type Command struct {
	// Path is an absolute program path or a tool name looked up in SafePath.
	Path string
	Args []string
	// As runs the program with this account's user and primary group, no
	// supplementary groups and in a new session without a controlling
	// terminal; nil runs it as the calling process.
	As *Account
	// Env is added to the fixed environment (PATH, LC_ALL and, with As,
	// HOME).
	Env []string
	// Stdin is copied to the program through a pipe; nil reads
	// /dev/null. Output is always captured, never connected to a
	// terminal.
	Stdin io.Reader
	// MaxOutput bounds the captured stdout; zero selects 1 MiB.
	MaxOutput int
	// Timeout bounds the program; zero selects two minutes.
	Timeout time.Duration
}

// String is the command line for messages.
func (c Command) String() string {
	return strings.Join(append([]string{c.Path}, c.Args...), " ")
}

// ExitError is a program that ran and exited with a non-zero status.
type ExitError struct {
	Command string
	Code    int
	Stderr  string
}

func (e *ExitError) Error() string {
	msg := fmt.Sprintf("%s exited with status %d", e.Command, e.Code)
	if e.Stderr != "" {
		msg += ": " + e.Stderr
	}
	return msg
}

// exitCode returns the status of an ExitError, or -1 for other errors.
func exitCode(err error) int {
	var ee *ExitError
	if errors.As(err, &ee) {
		return ee.Code
	}
	return -1
}

// Runner runs programs.
type Runner interface {
	// LookPath finds a tool in SafePath.
	LookPath(name string) (string, error)
	// Run runs a command and returns what it wrote to stdout.
	Run(ctx context.Context, c Command) ([]byte, error)
}

// OSRunner runs programs on the host.
type OSRunner struct{}

// LookPath implements Runner.
func (OSRunner) LookPath(name string) (string, error) {
	if strings.Contains(name, "/") {
		return "", fmt.Errorf("tool name %q contains a slash", name)
	}
	for _, dir := range filepath.SplitList(SafePath) {
		p := filepath.Join(dir, name)
		if fi, err := os.Stat(p); err == nil && fi.Mode().IsRegular() && fi.Mode().Perm()&0o111 != 0 {
			return p, nil
		}
	}
	return "", fmt.Errorf("%s not found in %s", name, SafePath)
}

const (
	maxStdout = 1 << 20
	maxStderr = 4 << 10
)

// Run implements Runner.
func (r OSRunner) Run(ctx context.Context, c Command) ([]byte, error) {
	path := c.Path
	if !filepath.IsAbs(path) {
		p, err := r.LookPath(path)
		if err != nil {
			return nil, err
		}
		path = p
	}
	timeout := c.Timeout
	if timeout == 0 {
		timeout = 2 * time.Minute
	}
	ctx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()
	cmd := exec.CommandContext(ctx, path, c.Args...) //nolint:gosec // fixed programs and argv chosen by host setup, never from input
	cmd.Dir = "/"
	cmd.Env = []string{"PATH=" + SafePath, "LC_ALL=C"}
	if c.As != nil {
		cmd.Env = append(cmd.Env, "HOME="+c.As.Home)
	}
	cmd.SysProcAttr = sysProcAttr(c)
	cmd.Env = append(cmd.Env, c.Env...)
	if _, isFile := c.Stdin.(*os.File); isFile {
		// exec would pass the descriptor itself, which may be root's
		// terminal.
		return nil, fmt.Errorf("%s: stdin must not be a file", c)
	}
	cmd.Stdin = c.Stdin
	cmd.WaitDelay = 5 * time.Second
	stdout := &limitedBuffer{max: maxStdout}
	if c.MaxOutput > 0 {
		stdout.max = c.MaxOutput
	}
	stderr := &tailBuffer{max: maxStderr}
	cmd.Stdout, cmd.Stderr = stdout, stderr
	err := cmd.Run()
	var ee *exec.ExitError
	switch {
	case err == nil:
		return stdout.Bytes(), nil
	case ctx.Err() != nil:
		return nil, fmt.Errorf("%s: %w", c, ctx.Err())
	case errors.As(err, &ee) && ee.Exited():
		return stdout.Bytes(), &ExitError{Command: c.String(), Code: ee.ExitCode(), Stderr: Excerpt(stderr.String())}
	}
	return nil, fmt.Errorf("%s: %w", c, err)
}

// sysProcAttr switches to c.As in a new session. Without a controlling
// terminal the program cannot open root's terminal through /dev/tty or
// push input into it with TIOCSTI, and terminal signals do not reach it;
// a terminal passed as Stdin still answers its prompts.
func sysProcAttr(c Command) *syscall.SysProcAttr {
	if c.As == nil {
		return nil
	}
	return &syscall.SysProcAttr{Setsid: true, Credential: &syscall.Credential{
		Uid: uint32(c.As.UID), Gid: uint32(c.As.GID), Groups: []uint32{}, //nolint:gosec // account IDs come from getent and fit in uint32
	}}
}

// excerptLines and excerptBytes bound what a failed program's output adds
// to an error message.
const (
	excerptLines = 6
	excerptBytes = 800
)

// Excerpt is the tail of a program's output made fit for an error
// message: at most the last few non-empty lines joined by " | ", with
// control characters replaced, because programs run as lcm or the
// operator choose what they write. The tail is kept because tools such as
// nginx -t and apt print the cause first and a summary last.
func Excerpt(s string) string {
	var lines []string
	for l := range strings.SplitSeq(strings.ReplaceAll(s, "\r", "\n"), "\n") {
		if l = strings.TrimSpace(l); l != "" {
			lines = append(lines, l)
		}
	}
	if len(lines) > excerptLines {
		lines = lines[len(lines)-excerptLines:]
	}
	return printable(strings.Join(lines, "\n"), excerptBytes)
}

func firstLine(s string) string {
	line, _, _ := strings.Cut(strings.TrimSpace(s), "\n")
	return line
}

// printable makes s safe to print on one line: control and format
// characters (escape sequence introducers, bidirectional overrides) and
// invalid UTF-8 become "?", tabs become spaces, line breaks become " | "
// (empty lines vanish), and s is shortened to at most max bytes.
func printable(s string, max int) string {
	s = strings.ToValidUTF8(s, "?")
	s = strings.ReplaceAll(s, "\r", "\n")
	if strings.Contains(s, "\n") {
		var lines []string
		for l := range strings.SplitSeq(s, "\n") {
			if l = strings.TrimSpace(l); l != "" {
				lines = append(lines, l)
			}
		}
		s = strings.Join(lines, " | ")
	}
	s = strings.Map(func(r rune) rune {
		if r == '\t' {
			return ' '
		}
		if unicode.IsControl(r) || unicode.Is(unicode.Cf, r) || r == '\u2028' || r == '\u2029' {
			return '?'
		}
		return r
	}, s)
	if len(s) > max {
		cut := max
		for cut > 0 && !utf8.RuneStart(s[cut]) {
			cut--
		}
		s = s[:cut] + "..."
	}
	return s
}

// safeWriter writes text with control characters other than newlines
// replaced.
type safeWriter struct{ w io.Writer }

func (s safeWriter) Write(p []byte) (int, error) {
	lines := strings.Split(string(p), "\n")
	for i, l := range lines {
		lines[i] = printable(l, len(l)+1)
	}
	if _, err := io.WriteString(s.w, strings.Join(lines, "\n")); err != nil {
		return 0, err
	}
	return len(p), nil
}

// safeError prints an error with control characters replaced; errors.Is
// and errors.As still see the original.
type safeError struct{ err error }

func (e safeError) Error() string { return printable(e.err.Error(), 4096) }
func (e safeError) Unwrap() error { return e.err }

// limitedBuffer keeps the first max bytes written to it.
type limitedBuffer struct {
	bytes.Buffer
	max int
}

func (b *limitedBuffer) Write(p []byte) (int, error) {
	if room := b.max - b.Len(); room > 0 {
		if len(p) > room {
			b.Buffer.Write(p[:room])
		} else {
			b.Buffer.Write(p)
		}
	}
	return len(p), nil
}

// tailBuffer keeps the last max bytes written to it: a failing program
// names its cause near the end of what it printed.
type tailBuffer struct {
	bytes.Buffer
	max int
}

func (b *tailBuffer) Write(p []byte) (int, error) {
	if len(p) >= b.max {
		b.Reset()
		b.Buffer.Write(p[len(p)-b.max:])
		return len(p), nil
	}
	if over := b.Len() + len(p) - b.max; over > 0 {
		b.Next(over)
	}
	b.Buffer.Write(p)
	return len(p), nil
}

// lookupAccount reads an account with getent, so that every name service
// the host uses applies. A missing account returns found false.
func lookupAccount(ctx context.Context, r Runner, name string) (Account, bool, error) {
	out, err := r.Run(ctx, Command{Path: "getent", Args: []string{"passwd", name}, Timeout: 10 * time.Second})
	if exitCode(err) == 2 {
		return Account{}, false, nil
	}
	if err != nil {
		return Account{}, false, err
	}
	return ParsePasswd(string(out), name)
}

// ParsePasswd parses the output of "getent passwd name" into an Account,
// including its comment (GECOS), which identifies the accounts host setup
// created.
func ParsePasswd(out, name string) (Account, bool, error) {
	f := strings.Split(strings.TrimSpace(out), ":")
	if len(f) != 7 || f[0] != name {
		return Account{}, false, fmt.Errorf("getent passwd %s: unexpected output", name)
	}
	uid, err1 := strconv.Atoi(f[2])
	gid, err2 := strconv.Atoi(f[3])
	if err1 != nil || err2 != nil || uid < 0 || gid < 0 || !filepath.IsAbs(f[5]) {
		return Account{}, false, fmt.Errorf("getent passwd %s: unexpected output", name)
	}
	return Account{Name: name, UID: uid, GID: gid, Home: filepath.Clean(f[5]), GECOS: f[4], Shell: f[6]}, true, nil
}
