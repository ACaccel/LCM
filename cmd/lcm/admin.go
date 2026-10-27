package main

import (
	"bufio"
	"context"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"strings"

	"golang.org/x/term"

	"github.com/ACaccel/lcm/internal/audit"
	"github.com/ACaccel/lcm/internal/auth"
	"github.com/ACaccel/lcm/internal/config"
	"github.com/ACaccel/lcm/internal/store"
)

// openStore opens the database for a CLI command, validating the same
// configuration as serve. The returned context carries the CLI actor.
func (a *app) openStore(ctx context.Context, fs *flag.FlagSet, cfg *config.Config, args []string) (context.Context, *store.DB, error) {
	cfg.RegisterDataDirFlag(fs)
	if err := fs.Parse(args); err != nil {
		return nil, nil, err
	}
	if err := cfg.Validate(); err != nil {
		return nil, nil, fmt.Errorf("invalid configuration: %w", err)
	}
	if err := cfg.EnsureDataDir(); err != nil {
		return nil, nil, fmt.Errorf("invalid configuration: %w", err)
	}
	db, err := store.Open(ctx, cfg.DBPath())
	if err != nil {
		return nil, nil, err
	}
	return auth.WithActor(ctx, auth.CLI), db, nil
}

func (a *app) newFlagSet(name string) (*flag.FlagSet, *config.Config, error) {
	cfg, err := config.FromEnv(a.getenv)
	if err != nil {
		return nil, nil, err
	}
	fs := flag.NewFlagSet(name, flag.ContinueOnError)
	fs.SetOutput(a.stderr)
	return fs, &cfg, nil
}

func (a *app) admin(ctx context.Context, args []string) error {
	if len(args) == 0 || args[0] != "set-password" {
		return usagef("admin: expected subcommand set-password")
	}
	fs, cfg, err := a.newFlagSet("admin set-password")
	if err != nil {
		return err
	}
	username := fs.String("username", "admin", "user to create or update")
	ctx, db, err := a.openStore(ctx, fs, cfg, args[1:])
	if err != nil {
		return err
	}
	defer func() { _ = db.Close() }()
	if fs.NArg() > 0 {
		return usagef("admin set-password: unexpected argument %q", fs.Arg(0))
	}

	password, err := a.readPassword()
	if err != nil {
		return err
	}
	svc, err := auth.NewService(db, cfg.SessionTTL)
	if err != nil {
		return err
	}
	created, err := svc.SetPassword(ctx, *username, password)
	switch {
	case errors.Is(err, auth.ErrWeakPassword), errors.Is(err, auth.ErrInvalidUsername):
		return err
	case err != nil:
		recordCLI(ctx, db, "admin.set_password", "user:"+*username, audit.OutcomeError, nil)
		return err
	}
	if err := audit.New(db).Record(ctx, "admin.set_password", "user:"+*username, audit.OutcomeOK,
		map[string]any{"created": created}); err != nil {
		return err
	}
	verb := "updated"
	if created {
		verb = "created"
	}
	_, err = fmt.Fprintf(a.stdout, "User %q %s.\n", *username, verb)
	return err
}

// recordCLI writes a best-effort audit entry for a failed command.
func recordCLI(ctx context.Context, db *store.DB, action, target string, outcome audit.Outcome, details map[string]any) {
	_ = audit.New(db).Record(ctx, action, target, outcome, details)
}

// readPassword reads the password without echo from a terminal (asking for
// confirmation), or as a single line from a pipe.
func (a *app) readPassword() (string, error) {
	if f, ok := a.stdin.(*os.File); ok && term.IsTerminal(int(f.Fd())) { //nolint:gosec // file descriptors fit in int
		return a.promptPassword(f)
	}
	line, err := bufio.NewReader(a.stdin).ReadString('\n')
	if err != nil && (!errors.Is(err, io.EOF) || line == "") {
		return "", errors.New("read password from stdin: no input")
	}
	return strings.TrimRight(line, "\r\n"), nil
}

func (a *app) promptPassword(f *os.File) (string, error) {
	fd := int(f.Fd()) //nolint:gosec // file descriptors fit in int
	_, _ = fmt.Fprintf(a.stderr, "New password (min %d characters): ", auth.MinPasswordLength)
	first, err := term.ReadPassword(fd)
	_, _ = fmt.Fprintln(a.stderr)
	if err != nil {
		return "", fmt.Errorf("read password: %w", err)
	}
	_, _ = fmt.Fprint(a.stderr, "Confirm password: ")
	second, err := term.ReadPassword(fd)
	_, _ = fmt.Fprintln(a.stderr)
	if err != nil {
		return "", fmt.Errorf("read password: %w", err)
	}
	if string(first) != string(second) {
		return "", errors.New("passwords do not match")
	}
	return string(first), nil
}
