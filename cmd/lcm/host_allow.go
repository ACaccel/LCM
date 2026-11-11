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
	port := fs.Int("port", 0, "port the service listens on, on 127.0.0.1 (1024-65535); optional with --external --static, and with --publish for a service that only publishes static content")
	route := fs.String("route", "", "publish the service through the ingress component at this path prefix (\"/\" or ending with \"/\")")
	static := fs.String("static", "", "root-owned directory the route serves (needs --route)")
	external := fs.Bool("external", false, "LCM does not own the service's process or accounts; only its route is published (needs --route)")
	publish := fs.Bool("publish", false, "the route serves the managed service's publish directory /opt/<service>/www, where the deploy helper publishes its static content (needs --route; the only --static a managed service may have)")
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
	switch {
	case *static != "" && *route == "":
		return usagef("host allow: --static needs --route")
	case *external && *route == "":
		return usagef("host allow: --external needs --route")
	case *publish && (*route == "" || *external):
		return usagef("host allow: --publish needs --route and is only for a managed service, not with --external")
	case *publish && *static != "" && *static != hostsetup.ServicePublishDir(pos[0]):
		return usagef("host allow: --publish serves %s; leave out --static", hostsetup.ServicePublishDir(pos[0]))
	case *port == 0 && !*publish && (!*external || *static == ""):
		return usagef("host allow: --port is required (unless --external with --static, or --publish for a service that only publishes static content)")
	}
	grant := hostsetup.ServiceGrant{Port: *port, Runtimes: runtimes, External: *external}
	if *route != "" {
		grant.Route = &hostsetup.RouteGrant{Prefix: *route, Static: *static}
		if *publish {
			grant.Route.Static = hostsetup.ServicePublishDir(pos[0])
		}
	}
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
