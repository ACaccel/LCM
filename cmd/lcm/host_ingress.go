package main

import (
	"context"
	"flag"
	"fmt"

	"github.com/ACaccel/lcm/internal/hostsetup"
)

// hostCert runs "host cert issue": the domain's first certificate.
func (a *app) hostCert(ctx context.Context, fs *flag.FlagSet, args []string) error {
	if len(args) == 0 || args[0] != "issue" {
		return usagef("host cert: expected subcommand issue")
	}
	path := fs.String("config", hostsetup.ConfigPath, "host configuration file")
	pos, err := parseArgs(fs, args[1:])
	if err != nil {
		return err
	}
	if len(pos) > 0 {
		return usagef("host cert issue: unexpected argument %q", pos[0])
	}
	h, err := a.hostEnv()
	if err != nil {
		return err
	}
	return hostsetup.IssueCertificate(ctx, h, hostsetup.CertOptions{ConfigPath: *path, Out: a.stdout})
}

// hostIngress runs "host ingress revert": back to the adopted hand-made
// site.
func (a *app) hostIngress(ctx context.Context, fs *flag.FlagSet, args []string) error {
	if len(args) == 0 || args[0] != "revert" {
		return usagef("host ingress: expected subcommand revert")
	}
	path := fs.String("config", hostsetup.ConfigPath, "host configuration file")
	check := fs.Bool("check", false, "report what would change without changing anything (exit status 3 when something would)")
	pos, err := parseArgs(fs, args[1:])
	if err != nil {
		return err
	}
	if len(pos) > 0 {
		return usagef("host ingress revert: unexpected argument %q", pos[0])
	}
	h, err := a.hostEnv()
	if err != nil {
		return err
	}
	res, err := hostsetup.RevertIngress(ctx, h, hostsetup.RevertOptions{ConfigPath: *path, Check: *check, Out: a.stdout})
	if err != nil {
		return err
	}
	if *check && res.Changes > 0 {
		return problemError{fmt.Sprintf("%d changes pending", res.Changes)}
	}
	return nil
}
