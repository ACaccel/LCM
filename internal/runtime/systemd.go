package runtime

import (
	"context"
	"fmt"
	"strings"

	"github.com/ACaccel/lcm/internal/execx"
	"github.com/ACaccel/lcm/internal/model"
)

type systemd struct {
	run  execx.Runner
	opts Options
}

// base returns the systemctl argv prefix for the service's scope.
func (s *systemd) base(rt model.Runtime, privileged bool) []string {
	var argv []string
	if rt.EffectiveScope() == model.ScopeUser {
		return append(argv, s.opts.Systemctl, "--user")
	}
	if privileged {
		argv = append(argv, s.opts.Privilege...)
	}
	return append(argv, s.opts.Systemctl)
}

func (s *systemd) Status(ctx context.Context, svc *model.Service) (Status, error) {
	rt := svc.Spec.Runtime
	argv := append(s.base(rt, false), "show",
		"-p", "LoadState,ActiveState,SubState,MainPID,ActiveEnterTimestamp", rt.Unit)
	res, err := run(ctx, s.run, execx.Cmd{Argv: argv, Timeout: s.opts.StatusTimeout})
	if err != nil {
		return Status{State: StateUnknown}, err
	}
	return parseSystemdShow(rt.Unit, res.Output)
}

func parseSystemdShow(unit, out string) (Status, error) {
	props := map[string]string{}
	for line := range strings.SplitSeq(out, "\n") {
		if k, v, ok := strings.Cut(line, "="); ok {
			props[k] = strings.TrimSpace(v)
		}
	}
	if props["LoadState"] == "not-found" {
		return Status{State: StateUnknown}, fmt.Errorf("%w: unit %s", ErrNotFound, unit)
	}
	st := Status{Detail: map[string]string{}}
	for _, k := range []string{"ActiveState", "SubState", "MainPID", "ActiveEnterTimestamp"} {
		if v := props[k]; v != "" {
			st.Detail[detailKey(k)] = v
		}
	}
	if pid := props["MainPID"]; pid == "0" {
		delete(st.Detail, "pid")
	}
	switch props["ActiveState"] {
	case "active", "reloading":
		st.State = StateActive
	case "inactive":
		st.State = StateInactive
	case "failed":
		st.State = StateFailed
	case "activating", "deactivating", "maintenance":
		st.State = StateUnknown
	default:
		return Status{State: StateUnknown}, fmt.Errorf("runtime: unexpected systemctl output for %s: %q", unit, trim(out))
	}
	return st, nil
}

func detailKey(k string) string {
	switch k {
	case "ActiveState":
		return "activeState"
	case "SubState":
		return "subState"
	case "MainPID":
		return "pid"
	case "ActiveEnterTimestamp":
		return "since"
	}
	return k
}

func (s *systemd) action(ctx context.Context, svc *model.Service, verb string) error {
	rt := svc.Spec.Runtime
	argv := append(s.base(rt, true), verb, rt.Unit)
	_, err := run(ctx, s.run, execx.Cmd{Argv: argv, Timeout: s.opts.ActionTimeout})
	return err
}

func (s *systemd) Start(ctx context.Context, svc *model.Service) error {
	return s.action(ctx, svc, "start")
}

func (s *systemd) Stop(ctx context.Context, svc *model.Service) error {
	return s.action(ctx, svc, "stop")
}

func (s *systemd) Restart(ctx context.Context, svc *model.Service) error {
	return s.action(ctx, svc, "restart")
}
