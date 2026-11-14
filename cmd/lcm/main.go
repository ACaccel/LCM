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
	"time"

	"github.com/ACaccel/lcm/internal/buildinfo"
	"github.com/ACaccel/lcm/internal/hostsetup"
)

const usage = `Usage: lcm <command> [flags]

Commands:
  serve                       Run the HTTP server
  admin set-password          Create the admin user or change its password
  token create --name <name>  Create an API token (shown once)
  token list                  List API tokens
  token revoke <id>           Revoke an API token
  version                     Print version information
  storage-schema [--data-dir] Print the supported storage version (or the database's)
  convert -f <file|dir>       Print lcm/v1 documents converted to lcm/v2 (offline)
  host init --operator <user> Write the host configuration /etc/lcm/host.json (root)
  host apply [--check]        Install or converge LCM's host components (root)
  host upgrade [--check]      Upgrade a schema-changing build with backup and rollback (root)
  host allow <service> --port N [--runtime R] [--input NAME=PATH] [--check]
                              Add a service to the deploy allowlist and create
                              its account and directories (root); --route P
                              [--static DIR] [--external] publish it through ingress,
                              --publish serves its /opt/<service>/www;
                              --instance NAME[:PORT] (instead of --port),
                              --instance-group NAME=GROUP, --group G,
                              --after UNIT, --wants UNIT (repeatable)
  host status                 Compare installed host files with this build
  host cert issue             Obtain the ingress domain's first certificate (root)
  host ingress revert         Remove the ingress site, enable the adopted one (root; --check)

Commands that talk to a running server (see the README):
  apply -f <file|dir> [--dry-run] [--force]   Apply Resource, Policy and Module documents
  export [kind[/name]]                        Print documents as YAML (kind: resource, policy, module)
  validate -f <file|dir>                      Check documents on the server
  get resources|policies|runs|locks|sources|connections [name] [--type t] [-o table|json|yaml]
  get backups <resource> [-o table|json|yaml]  List the backup sets of a service
  resource <command> <name> [flags]           Act on a resource: action, observe, maintenance,
                                              reboot, retire, impact, group-action, ... (see -h)
  policy pause|resume <name>                  Stop or allow the triggers of a policy to start runs
  run <policy> [--trigger id] [--event json] [--dry-run] [--wait]

Run "lcm <command> -h" for the flags of a command.
Configuration is read from LCM_* environment variables; flags override them.
The server commands use LCM_SERVER (default http://127.0.0.1:7800) and an API
token from LCM_TOKEN, or from the file named by --token-file or LCM_TOKEN_FILE,
or else from ~/.config/lcm/token when that file exists.
Exit status: 0 success, 1 error, 2 usage, 3 documents with problems or,
for host apply|upgrade --check, pending changes.
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
	protectHostChange(os.Args[1:])
	ctx, stop := signal.NotifyContext(context.Background(), shutdownSignals(os.Args[1:])...)
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
		return a.storageSchema(ctx, rest)
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
	case "policy":
		return a.policy(ctx, rest)
	case "machine-helper":
		return a.machineHelper(ctx, rest)
	case "deploy-helper":
		return a.deployHelper(ctx, rest)
	case "backup-worker":
		return a.backupWorker(ctx, rest)
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
