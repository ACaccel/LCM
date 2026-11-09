package main

import (
	"context"
	"flag"
	"fmt"

	"github.com/ACaccel/lcm/internal/hostsetup"
)

func (a *app) hostUpgrade(ctx context.Context, fs *flag.FlagSet, args []string) error {
	check := fs.Bool("check", false, "report what the upgrade would do without changing anything (exit status 3 when it would run)")
	path := fs.String("config", hostsetup.ConfigPath, "host configuration file")
	older := fs.Bool("allow-older", false, "install this build even if its commit is older than the installed binary's")
	pos, err := parseArgs(fs, args)
	if err != nil {
		return err
	}
	if len(pos) > 0 {
		return usagef("host upgrade: unexpected argument %q", pos[0])
	}
	h, err := a.hostEnv()
	if err != nil {
		return err
	}
	res, err := hostsetup.Upgrade(ctx, h, hostsetup.UpgradeOptions{ConfigPath: *path, Check: *check, AllowOlder: *older, Out: a.stdout})
	if err != nil {
		return err
	}
	if *check && res.Changes > 0 {
		return problemError{fmt.Sprintf("an upgrade would run (%d steps)", res.Changes)}
	}
	return nil
}
