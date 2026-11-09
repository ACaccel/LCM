package main

import (
	"context"
	"flag"
	"fmt"
	"strings"

	"github.com/ACaccel/lcm/internal/hostsetup"
)

// listFlag collects a repeated flag.
type listFlag []string

func (l *listFlag) String() string { return strings.Join(*l, ",") }

func (l *listFlag) Set(v string) error {
	*l = append(*l, v)
	return nil
}

func (a *app) hostAllow(ctx context.Context, fs *flag.FlagSet, args []string) error {
	port := fs.Int("port", 0, "port the service listens on, on 127.0.0.1 (1024-65535)")
	var runtimes, inputs listFlag
	fs.Var(&runtimes, "runtime", "runtime of the host configuration the service may use (repeatable)")
	fs.Var(&inputs, "input", "read-only build input as NAME=PATH (repeatable)")
	check := fs.Bool("check", false, "report what would change without changing anything (exit status 3 when something would)")
	path := fs.String("config", hostsetup.ConfigPath, "host configuration file")
	pos, err := parseArgs(fs, args)
	if err != nil {
		return err
	}
	if len(pos) != 1 {
		return usagef("host allow: expected one service name")
	}
	if *port == 0 {
		return usagef("host allow: --port is required")
	}
	grant := hostsetup.ServiceGrant{Port: *port, Runtimes: runtimes}
	for _, in := range inputs {
		name, dir, ok := strings.Cut(in, "=")
		if !ok || name == "" || dir == "" {
			return usagef("host allow: --input must be NAME=PATH (got %q)", in)
		}
		if grant.Inputs == nil {
			grant.Inputs = map[string]string{}
		}
		if _, dup := grant.Inputs[name]; dup {
			return usagef("host allow: input %q is given twice", name)
		}
		grant.Inputs[name] = dir
	}
	h, err := a.hostEnv()
	if err != nil {
		return err
	}
	res, err := hostsetup.Allow(ctx, h, hostsetup.AllowOptions{ConfigPath: *path, Name: pos[0], Grant: grant, Check: *check, Out: a.stdout})
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
