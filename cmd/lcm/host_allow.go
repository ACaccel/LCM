package main

import (
	"context"
	"flag"
	"fmt"
	"strconv"
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
	var runtimes, inputs, instances, instanceGroups, groups, after, wants listFlag
	fs.Var(&runtimes, "runtime", "runtime of the host configuration the service may use (repeatable)")
	fs.Var(&inputs, "input", "read-only build input as NAME=PATH (repeatable)")
	fs.Var(&instances, "instance", "instance the service runs as <service>@NAME.service, as NAME or NAME:PORT (repeatable; replaces --port)")
	fs.Var(&instanceGroups, "instance-group", "supplementary group of one instance's unit as NAME=GROUP (repeatable; the instance must be given with --instance)")
	fs.Var(&groups, "group", "supplementary group of every unit of the service: a dedicated system group (repeatable)")
	fs.Var(&after, "after", "system unit the service's units start after, such as db.service (repeatable)")
	fs.Var(&wants, "wants", "system unit the service's units pull in, such as db.service (repeatable)")
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
	case len(instances) > 0 && *port != 0:
		return usagef("host allow: --port and --instance exclude each other: each instance has its own port")
	case len(instances) > 0 && *external:
		return usagef("host allow: --instance is not for an --external service")
	case len(instanceGroups) > 0 && len(instances) == 0:
		return usagef("host allow: --instance-group needs --instance")
	case *port == 0 && len(instances) == 0 && !*publish && (!*external || *static == ""):
		return usagef("host allow: --port or --instance is required (unless --external with --static, or --publish for a service that only publishes static content)")
	}
	grant := hostsetup.ServiceGrant{Port: *port, Runtimes: runtimes, External: *external, Groups: groups, After: after, Wants: wants}
	for _, in := range instances {
		name, p, hasPort := strings.Cut(in, ":")
		ig := hostsetup.InstanceGrant{}
		if hasPort {
			n, err := strconv.Atoi(p)
			if err != nil || n <= 0 {
				return usagef("host allow: --instance must be NAME or NAME:PORT (got %q)", in)
			}
			ig.Port = n
		}
		if grant.Instances == nil {
			grant.Instances = map[string]hostsetup.InstanceGrant{}
		}
		if _, dup := grant.Instances[name]; dup || name == "" {
			return usagef("host allow: instance %q is empty or given twice", name)
		}
		grant.Instances[name] = ig
	}
	for _, ig := range instanceGroups {
		name, group, ok := strings.Cut(ig, "=")
		in, listed := grant.Instances[name]
		switch {
		case !ok || group == "":
			return usagef("host allow: --instance-group must be NAME=GROUP (got %q)", ig)
		case !listed:
			return usagef("host allow: --instance-group names %q, which is not given with --instance", name)
		}
		in.Groups = append(in.Groups, group)
		grant.Instances[name] = in
	}
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
