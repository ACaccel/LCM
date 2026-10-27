// Command lcm is the service lifecycle control plane: HTTP server and
// administration CLI in one binary.
package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"io"
	"net"
	"os"
	"os/signal"
	"syscall"

	"github.com/ACaccel/lcm/internal/buildinfo"
)

const usage = `Usage: lcm <command> [flags]

Commands:
  serve                       Run the HTTP server
  admin set-password          Create the admin user or change its password
  token create --name <name>  Create an API token (shown once)
  token list                  List API tokens
  token revoke <id>           Revoke an API token
  version                     Print version information

Run "lcm <command> -h" for the flags of a command.
Configuration is read from LCM_* environment variables; flags override them.
`

// usageError marks mistakes in the command line, which exit with status 2.
type usageError struct{ msg string }

func (e usageError) Error() string { return e.msg }

func usagef(format string, args ...any) error {
	return usageError{msg: fmt.Sprintf(format, args...)}
}

// app carries the process environment so that commands can be tested.
type app struct {
	stdin  io.Reader
	stdout io.Writer
	stderr io.Writer
	getenv func(string) string

	// onListen, when set, is called with the bound address once the server
	// accepts connections.
	onListen func(net.Addr)
}

func main() {
	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer stop()
	a := &app{stdin: os.Stdin, stdout: os.Stdout, stderr: os.Stderr, getenv: os.Getenv}
	os.Exit(a.run(ctx, os.Args[1:]))
}

func (a *app) run(ctx context.Context, args []string) int {
	err := a.dispatch(ctx, args)
	var ue usageError
	switch {
	case err == nil, errors.Is(err, flag.ErrHelp):
		return 0
	case errors.As(err, &ue):
		_, _ = fmt.Fprintf(a.stderr, "lcm: %v\n\n%s", err, usage)
		return 2
	default:
		_, _ = fmt.Fprintf(a.stderr, "lcm: %v\n", err)
		return 1
	}
}

func (a *app) dispatch(ctx context.Context, args []string) error {
	if len(args) == 0 {
		return usagef("missing command")
	}
	cmd, rest := args[0], args[1:]
	switch cmd {
	case "serve":
		return a.serve(ctx, rest)
	case "admin":
		return a.admin(ctx, rest)
	case "token":
		return a.token(ctx, rest)
	case "version", "--version":
		_, err := fmt.Fprintf(a.stdout, "lcm %s\n", buildinfo.Get())
		return err
	case "help", "-h", "--help":
		_, err := io.WriteString(a.stdout, usage)
		return err
	}
	return usagef("unknown command %q", cmd)
}
