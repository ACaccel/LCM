// Command lcm is the resource lifecycle control plane: HTTP server and
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
	"time"

	"github.com/ACaccel/lcm/internal/buildinfo"
	"github.com/ACaccel/lcm/internal/hostsetup"
	"github.com/ACaccel/lcm/internal/store"
)

const usage = `Usage: lcm <command> [flags]

Commands:
  serve                       Run the HTTP server
  admin set-password          Create the admin user or change its password
  token create --name <name>  Create an API token (shown once)
  token list                  List API tokens
  token revoke <id>           Revoke an API token
  version                     Print version information
  storage-schema              Print the supported storage version
  convert -f <file|dir>       Print lcm/v1 documents converted to lcm/v2 (offline)
  host init --operator <user> Write the host configuration /etc/lcm/host.json (root)
  host apply [--check]        Install or converge LCM's host components (root)
  host allow <service> --port N [--runtime R] [--input NAME=PATH] [--check]
                              Add a service to the deploy allowlist and create
                              its account and directories (root)
  host status                 Compare installed host files with this build

Commands that talk to a running server (see the README):
  apply -f <file|dir> [--dry-run] [--force]   Apply Resource, Policy and Module documents
  export [kind[/name]]                        Print documents as YAML (kind: resource, policy, module)
  validate -f <file|dir>                      Check documents on the server
  get resources|policies|runs|locks|sources|connections [name] [--type t] [-o table|json|yaml]
  resource <command> <name> [flags]           Act on a resource: action, observe, maintenance,
                                              reboot, retire, impact, group-action, ... (see -h)
  run <policy> [--trigger id] [--event json] [--dry-run] [--wait]

Run "lcm <command> -h" for the flags of a command.
Configuration is read from LCM_* environment variables; flags override them.
The server commands use LCM_SERVER (default http://127.0.0.1:7800) and an API
token from LCM_TOKEN, or from the file named by --token-file or LCM_TOKEN_FILE.
Exit status: 0 success, 1 error, 2 usage, 3 documents with problems or,
for host apply --check, pending changes.
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

	// poll is the interval of --wait; zero selects one second.
	poll time.Duration

	// terminal, when set, replaces the check whether stdin is a terminal
	// that can answer a confirmation question.
	terminal func() bool

	// hostSetup, when set, replaces the real host of the host commands.
	hostSetup func() (hostsetup.Host, error)
}

func main() {
	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer stop()
	a := &app{stdin: os.Stdin, stdout: os.Stdout, stderr: os.Stderr, getenv: os.Getenv}
	os.Exit(a.run(ctx, os.Args[1:]))
}

func (a *app) run(ctx context.Context, args []string) int {
	err := a.dispatch(ctx, args)
	var (
		ue usageError
		pe problemError
	)
	switch {
	case err == nil, errors.Is(err, flag.ErrHelp):
		return 0
	case errors.As(err, &pe):
		_, _ = fmt.Fprintf(a.stderr, "lcm: %v\n", err)
		return 3
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
	case "storage-schema":
		if len(rest) != 0 {
			return usagef("storage-schema accepts no arguments")
		}
		v, err := store.SchemaVersion()
		if err != nil {
			return err
		}
		_, err = fmt.Fprintln(a.stdout, v)
		return err
	case "serve":
		return a.serve(ctx, rest)
	case "convert":
		return a.convert(ctx, rest)
	case "admin":
		return a.admin(ctx, rest)
	case "token":
		return a.token(ctx, rest)
	case "apply":
		return a.apply(ctx, rest)
	case "export":
		return a.export(ctx, rest)
	case "validate":
		return a.validate(ctx, rest)
	case "get":
		return a.get(ctx, rest)
	case "run":
		return a.runPolicy(ctx, rest)
	case "resource":
		return a.resource(ctx, rest)
	case "machine-helper":
		return a.machineHelper(ctx, rest)
	case "deploy-helper":
		return a.deployHelper(ctx, rest)
	case "host":
		return a.host(ctx, rest)
	case "version", "--version":
		_, err := fmt.Fprintf(a.stdout, "lcm %s\n", buildinfo.Get())
		return err
	case "help", "-h", "--help":
		_, err := io.WriteString(a.stdout, usage)
		return err
	}
	return usagef("unknown command %q", cmd)
}
