// Package runtime controls the process behind a service through adapters
// for systemd, Docker, Docker Compose, operator-defined commands and
// externally managed services. Every adapter talks to the system through
// execx, never through a shell it did not receive from the operator.
package runtime

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/ACaccel/lcm/internal/execx"
	"github.com/ACaccel/lcm/internal/model"
)

// State is the runtime's view of a service.
type State string

// States.
const (
	StateActive   State = "active"
	StateInactive State = "inactive"
	StateFailed   State = "failed"
	// StateUnknown covers external services, transitional states such as
	// activating, and anything that could not be determined.
	StateUnknown State = "unknown"
)

// Status is the result of Adapter.Status.
type Status struct {
	State State
	// Detail carries adapter specific facts such as pid, since, image and
	// substate. Values are plain strings for easy display.
	Detail map[string]string
}

// Errors returned by adapters.
var (
	// ErrUnsupported is returned for actions the runtime type cannot do.
	ErrUnsupported = errors.New("runtime: action not supported")
	// ErrNotFound is returned when the unit or container does not exist.
	ErrNotFound = errors.New("runtime: not found")
)

// Adapter controls one runtime type. svc is a validated service document.
type Adapter interface {
	Status(ctx context.Context, svc *model.Service) (Status, error)
	Start(ctx context.Context, svc *model.Service) error
	Stop(ctx context.Context, svc *model.Service) error
	Restart(ctx context.Context, svc *model.Service) error
}

// Capabilities lists the process actions a service's runtime can perform.
// Status is always available; it reports unknown when it cannot tell.
type Capabilities struct {
	Start   bool `json:"start"`
	Stop    bool `json:"stop"`
	Restart bool `json:"restart"`
}

// CapabilitiesOf derives the capabilities from the runtime configuration
// alone. External services support nothing; a command runtime supports the
// actions that have a command, and restart also when it can stop and start.
func CapabilitiesOf(r model.Runtime) Capabilities {
	switch r.Type {
	case model.RuntimeExternal:
		return Capabilities{}
	case model.RuntimeCommand:
		start, stop := r.Start != "", r.Stop != ""
		return Capabilities{Start: start, Stop: stop, Restart: r.Restart != "" || (start && stop)}
	}
	return Capabilities{Start: true, Stop: true, Restart: true}
}

// Options configure the adapters.
type Options struct {
	// Systemctl is the systemctl binary; default "systemctl".
	Systemctl string
	// Privilege is prepended to systemctl actions in system scope. The
	// default is sudo -n, matching a sudoers rule that allows only
	// start, stop and restart of the listed units. User scope never uses
	// it. Set it to an empty non-nil slice to run without a prefix.
	Privilege []string
	// Docker is the docker binary; default "docker".
	Docker string
	// StatusTimeout bounds status queries; default 15s.
	StatusTimeout time.Duration
	// ActionTimeout bounds start, stop and restart; default 5m.
	ActionTimeout time.Duration
}

func (o Options) withDefaults() Options {
	if o.Systemctl == "" {
		o.Systemctl = "systemctl"
	}
	if o.Privilege == nil {
		o.Privilege = []string{"sudo", "-n"}
	}
	if o.Docker == "" {
		o.Docker = "docker"
	}
	if o.StatusTimeout <= 0 {
		o.StatusTimeout = 15 * time.Second
	}
	if o.ActionTimeout <= 0 {
		o.ActionTimeout = 5 * time.Minute
	}
	return o
}

// Registry maps runtime types to adapters.
type Registry struct {
	adapters map[model.RuntimeType]Adapter
}

// NewRegistry returns a registry with every built-in adapter on runner.
func NewRegistry(runner execx.Runner, opts Options) *Registry {
	opts = opts.withDefaults()
	return &Registry{adapters: map[model.RuntimeType]Adapter{
		model.RuntimeSystemd:  &systemd{run: runner, opts: opts},
		model.RuntimeDocker:   &docker{run: runner, opts: opts},
		model.RuntimeCompose:  &compose{run: runner, opts: opts},
		model.RuntimeCommand:  &command{run: runner, opts: opts},
		model.RuntimeExternal: external{},
	}}
}

// For returns the adapter of svc's runtime type.
func (r *Registry) For(svc *model.Service) (Adapter, error) {
	a, ok := r.adapters[svc.Spec.Runtime.Type]
	if !ok {
		return nil, fmt.Errorf("runtime: no adapter for type %q", svc.Spec.Runtime.Type)
	}
	return a, nil
}

// Override replaces the adapter of a type, for tests.
func (r *Registry) Override(t model.RuntimeType, a Adapter) { r.adapters[t] = a }

// run executes argv and returns a trimmed error that includes the command
// output, which is where systemctl and docker explain failures.
func run(ctx context.Context, runner execx.Runner, c execx.Cmd) (execx.Result, error) {
	res, err := runner.Run(ctx, c)
	if err != nil {
		if out := trim(res.Output); out != "" {
			return res, fmt.Errorf("%s: %w: %s", c.Display(), err, out)
		}
		return res, fmt.Errorf("%s: %w", c.Display(), err)
	}
	return res, nil
}

func trim(s string) string {
	const limit = 512
	s = strings.TrimSpace(s)
	if len(s) > limit {
		s = "..." + s[len(s)-limit:]
	}
	return s
}

type external struct{}

func (external) Status(context.Context, *model.Service) (Status, error) {
	return Status{State: StateUnknown, Detail: map[string]string{"reason": "externally managed"}}, nil
}

func (external) Start(context.Context, *model.Service) error   { return errExternal("start") }
func (external) Stop(context.Context, *model.Service) error    { return errExternal("stop") }
func (external) Restart(context.Context, *model.Service) error { return errExternal("restart") }

func errExternal(action string) error {
	return fmt.Errorf("%w: %s on an external service (LCM does not control its process)", ErrUnsupported, action)
}
