package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"os"
	"os/signal"
	"strings"
	"syscall"
	"text/tabwriter"

	"golang.org/x/term"

	"github.com/ACaccel/lcm/internal/hostsetup"
)

func (a *app) host(ctx context.Context, args []string) error {
	if len(args) == 0 {
		return usagef("host: expected subcommand init, apply, allow, upgrade, status, cert or ingress")
	}
	sub, rest := args[0], args[1:]
	fs := flag.NewFlagSet("host "+sub, flag.ContinueOnError)
	fs.SetOutput(a.stderr)
	switch sub {
	case "init":
		return a.hostInit(ctx, fs, rest)
	case "apply":
		return a.hostApply(ctx, fs, rest)
	case "upgrade":
		return a.hostUpgrade(ctx, fs, rest)
	case "status":
		return a.hostStatus(ctx, fs, rest)
	case "allow":
		return a.hostAllow(ctx, fs, rest)
	case "cert":
		return a.hostCert(ctx, fs, rest)
	case "ingress":
		return a.hostIngress(ctx, fs, rest)
	}
	return usagef("host: unknown subcommand %q", sub)
}

// hostEnv returns the host that the host commands act on.
func (a *app) hostEnv() (hostsetup.Host, error) {
	if a.hostSetup != nil {
		return a.hostSetup()
	}
	return hostsetup.OSHost()
}

func (a *app) hostInit(ctx context.Context, fs *flag.FlagSet, args []string) error {
	operator := fs.String("operator", a.getenv("SUDO_USER"), "administrator account that owns the toolchains and gets the first API token (default: the user who ran sudo)")
	repository := fs.String("repository", hostsetup.DefaultRepository, "repository LCM deploys itself from (https, no credentials)")
	goRoot := fs.String("go-root", "", "Go toolchain to copy (default: ~OPERATOR/.local/go)")
	nodeRoot := fs.String("node-root", "", "Node.js toolchain to copy (default: ~OPERATOR/.local/node)")
	port := fs.Int("ts-port", hostsetup.DefaultTailscalePort, "HTTPS port of tailscale serve")
	power := fs.Bool("machine-power", false, "install the machine power helper component")
	machineID := fs.String("security-host-machine-id", "", "install the security host helper for the Machine with this immutable ID")
	path := fs.String("config", hostsetup.ConfigPath, "host configuration file to write")
	pos, err := parseArgs(fs, args)
	if err != nil {
		return err
	}
	if len(pos) > 0 {
		return usagef("host init: unexpected argument %q", pos[0])
	}
	if *operator == "" {
		return usagef("host init: --operator is required")
	}
	h, err := a.hostEnv()
	if err != nil {
		return err
	}
	if !h.Privileged {
		return errors.New("must run as root, from a reviewed build: sudo bin/lcm host init")
	}
	cfg, err := hostsetup.DefaultConfig(ctx, h, *operator)
	if err != nil {
		return err
	}
	cfg.Repository = *repository
	cfg.Tailscale.Port = *port
	cfg.Components.MachinePower = *power
	if *goRoot != "" {
		cfg.Toolchains.Go = *goRoot
	}
	if *nodeRoot != "" {
		cfg.Toolchains.Node = *nodeRoot
	}
	if *machineID != "" {
		cfg.Components.SecurityHost = &hostsetup.SecurityHost{MachineID: *machineID}
	}
	if err := hostsetup.Init(h, *path, cfg); err != nil {
		return err
	}
	_, err = fmt.Fprintf(a.stdout, "Wrote %s; review it, then run: sudo bin/lcm host apply --check\n", *path)
	return err
}

func (a *app) hostApply(ctx context.Context, fs *flag.FlagSet, args []string) error {
	check := fs.Bool("check", false, "report what would change without changing anything (exit status 3 when something would)")
	path := fs.String("config", hostsetup.ConfigPath, "host configuration file")
	replace := fs.Bool("replace-binary", false, "install this executable as /usr/local/bin/lcm even if one is installed")
	pos, err := parseArgs(fs, args)
	if err != nil {
		return err
	}
	if len(pos) > 0 {
		return usagef("host apply: unexpected argument %q", pos[0])
	}
	h, err := a.hostEnv()
	if err != nil {
		return err
	}
	opts := hostsetup.Options{ConfigPath: *path, Check: *check, ReplaceBinary: *replace, Out: a.stdout}
	if f, ok := a.stdin.(*os.File); ok && a.interactive() {
		opts.Password = func() (string, error) { return a.promptRestoring(f) }
	}
	res, err := hostsetup.Apply(ctx, h, opts)
	if err != nil {
		return err
	}
	switch {
	case *check && res.Changes+res.Manual > 0:
		return problemError{fmt.Sprintf("%d changes pending and %d differences that need manual action", res.Changes, res.Manual)}
	case res.Manual > 0:
		return fmt.Errorf("%d differences need manual action; see the lines above", res.Manual)
	}
	return nil
}

func (a *app) hostStatus(ctx context.Context, fs *flag.FlagSet, args []string) error {
	pos, err := parseArgs(fs, args)
	if err != nil {
		return err
	}
	if len(pos) > 0 {
		return usagef("host status: unexpected argument %q", pos[0])
	}
	h, err := a.hostEnv()
	if err != nil {
		return err
	}
	components, err := hostsetup.Status(h)
	if err != nil {
		return err
	}
	tw := tabwriter.NewWriter(a.stdout, 0, 4, 2, ' ', 0)
	pending := false
	var limits []string
	for _, c := range components {
		_, _ = fmt.Fprintf(tw, "%s\t%s\n", c.Name, c.State)
		for _, f := range c.Files {
			var parts []string
			if f.Installed != "" {
				parts = append(parts, "installed "+short(f.Installed))
			}
			if f.Embedded != "" {
				parts = append(parts, "embedded "+short(f.Embedded))
			}
			detail := strings.Join(parts, ", ")
			if f.Detail != "" {
				detail = strings.TrimSpace(detail + " (" + f.Detail + ")")
			}
			if detail == "" {
				_, _ = fmt.Fprintf(tw, "  %s\t%s\n", f.Path, f.State)
				continue
			}
			_, _ = fmt.Fprintf(tw, "  %s\t%s\t%s\n", f.Path, f.State, detail)
		}
		pending = pending || c.State == hostsetup.StateOutdated || c.State == hostsetup.StateMissing
		if c.Name == hostsetup.ComponentDeploy && c.State != hostsetup.StateNotInstalled {
			limits = hostsetup.SandboxLimits(ctx, h)
		}
	}
	if err := tw.Flush(); err != nil {
		return err
	}
	for _, l := range limits {
		_, _ = fmt.Fprintln(a.stdout, "\nnote: "+l)
	}
	if pending {
		_, err = fmt.Fprintln(a.stdout, "\nTo install the versions of this build: sudo bin/lcm host apply (from a reviewed checkout, after make build)")
	}
	return err
}

// promptRestoring asks for a new password with echo off, like admin
// set-password, and puts the terminal back even when a signal ends the
// command during the prompt.
func (a *app) promptRestoring(f *os.File) (string, error) {
	fd := int(f.Fd()) //nolint:gosec // file descriptors fit in int
	state, err := term.GetState(fd)
	if err != nil {
		return "", fmt.Errorf("read the terminal state: %w", err)
	}
	sigs := make(chan os.Signal, 1)
	signal.Notify(sigs, os.Interrupt, syscall.SIGTERM, syscall.SIGHUP, syscall.SIGQUIT)
	done := make(chan struct{})
	defer func() {
		signal.Stop(sigs)
		close(done)
	}()
	go func() {
		select {
		case <-sigs:
			_ = term.Restore(fd, state)
			_, _ = fmt.Fprintln(a.stderr)
			os.Exit(130)
		case <-done:
		}
	}()
	return a.promptPassword(f)
}
