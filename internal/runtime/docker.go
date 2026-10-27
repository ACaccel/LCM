package runtime

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"

	"github.com/ACaccel/lcm/internal/execx"
	"github.com/ACaccel/lcm/internal/model"
)

type docker struct {
	run  execx.Runner
	opts Options
}

// inspectFormat prints "status|exitCode|pid|startedAt|image"; none of the
// values contain a pipe.
const inspectFormat = "{{.State.Status}}|{{.State.ExitCode}}|{{.State.Pid}}|{{.State.StartedAt}}|{{.Config.Image}}"

func (d *docker) Status(ctx context.Context, svc *model.Service) (Status, error) {
	name := svc.Spec.Runtime.Container
	res, err := run(ctx, d.run, execx.Cmd{
		Argv:    []string{d.opts.Docker, "inspect", "--type", "container", "--format", inspectFormat, name},
		Timeout: d.opts.StatusTimeout,
	})
	if err != nil {
		if strings.Contains(res.Output, "No such") {
			return Status{State: StateUnknown}, fmt.Errorf("%w: container %s", ErrNotFound, name)
		}
		return Status{State: StateUnknown}, err
	}
	return parseDockerInspect(name, res.Output)
}

func parseDockerInspect(name, out string) (Status, error) {
	f := strings.Split(strings.TrimSpace(out), "|")
	if len(f) != 5 {
		return Status{State: StateUnknown}, fmt.Errorf("runtime: unexpected docker inspect output for %s: %q", name, trim(out))
	}
	status, exit, pid, since, image := f[0], f[1], f[2], f[3], f[4]
	st := Status{Detail: map[string]string{"status": status, "image": image}}
	if pid != "0" {
		st.Detail["pid"] = pid
	}
	if status == "running" {
		st.Detail["since"] = since
	}
	switch status {
	case "running":
		st.State = StateActive
	case "created":
		st.State = StateInactive
	case "exited":
		if exit != "0" {
			st.State = StateFailed
			st.Detail["exitCode"] = exit
		} else {
			st.State = StateInactive
		}
	case "dead":
		st.State = StateFailed
	default: // paused, restarting, removing
		st.State = StateUnknown
	}
	return st, nil
}

func (d *docker) action(ctx context.Context, svc *model.Service, verb string) error {
	_, err := run(ctx, d.run, execx.Cmd{
		Argv:    []string{d.opts.Docker, verb, svc.Spec.Runtime.Container},
		Timeout: d.opts.ActionTimeout,
	})
	return err
}

func (d *docker) Start(ctx context.Context, svc *model.Service) error {
	return d.action(ctx, svc, "start")
}

func (d *docker) Stop(ctx context.Context, svc *model.Service) error {
	return d.action(ctx, svc, "stop")
}

func (d *docker) Restart(ctx context.Context, svc *model.Service) error {
	return d.action(ctx, svc, "restart")
}

type compose struct {
	run  execx.Runner
	opts Options
}

func (c *compose) argv(rt model.Runtime, args ...string) []string {
	base := []string{c.opts.Docker, "compose", "-p", rt.Project, "-f", rt.File}
	return append(base, args...)
}

func (c *compose) Status(ctx context.Context, svc *model.Service) (Status, error) {
	rt := svc.Spec.Runtime
	res, err := run(ctx, c.run, execx.Cmd{
		Argv:    c.argv(rt, "ps", "-a", "--format", "json"),
		Timeout: c.opts.StatusTimeout,
	})
	if err != nil {
		return Status{State: StateUnknown}, err
	}
	return parseComposePS(rt.Project, res.Output)
}

type composeContainer struct {
	Service  string `json:"Service"`
	State    string `json:"State"`
	ExitCode int    `json:"ExitCode"`
}

// parseComposePS accepts both output shapes of `docker compose ps --format
// json`: one JSON array (older releases) or one object per line.
func parseComposePS(project, out string) (Status, error) {
	out = strings.TrimSpace(out)
	var items []composeContainer
	switch {
	case out == "":
	case strings.HasPrefix(out, "["):
		if err := json.Unmarshal([]byte(out), &items); err != nil {
			return Status{State: StateUnknown}, fmt.Errorf("runtime: parse compose ps for %s: %w", project, err)
		}
	default:
		for line := range strings.SplitSeq(out, "\n") {
			line = strings.TrimSpace(line)
			if line == "" {
				continue
			}
			var c composeContainer
			if err := json.Unmarshal([]byte(line), &c); err != nil {
				return Status{State: StateUnknown}, fmt.Errorf("runtime: parse compose ps for %s: %w", project, err)
			}
			items = append(items, c)
		}
	}

	running, failed := 0, 0
	for _, c := range items {
		switch {
		case c.State == "running":
			running++
		case c.State == "exited" && c.ExitCode != 0, c.State == "dead":
			failed++
		}
	}
	st := Status{Detail: map[string]string{
		"containers": fmt.Sprint(len(items)), "running": fmt.Sprint(running), "failed": fmt.Sprint(failed),
	}}
	switch {
	case failed > 0:
		st.State = StateFailed
	case running > 0:
		st.State = StateActive
	default:
		// No containers, or only one-shot containers that exited cleanly.
		st.State = StateInactive
	}
	return st, nil
}

func (c *compose) action(ctx context.Context, svc *model.Service, args ...string) error {
	_, err := run(ctx, c.run, execx.Cmd{Argv: c.argv(svc.Spec.Runtime, args...), Timeout: c.opts.ActionTimeout})
	return err
}

func (c *compose) Start(ctx context.Context, svc *model.Service) error {
	return c.action(ctx, svc, "up", "-d")
}

func (c *compose) Stop(ctx context.Context, svc *model.Service) error {
	return c.action(ctx, svc, "stop")
}

func (c *compose) Restart(ctx context.Context, svc *model.Service) error {
	return c.action(ctx, svc, "restart")
}
