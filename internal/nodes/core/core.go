// Package core provides the built-in flow and data nodes: trigger.manual,
// flow.if, flow.switch, flow.merge, data.set, and the module markers
// module, module.input and module.output.
package core

import (
	"context"
	"errors"
	"fmt"
	"regexp"

	"github.com/ACaccel/lcm/internal/model"
	"github.com/ACaccel/lcm/internal/node"
)

// Categories used by the core nodes.
const (
	CategoryFlow   = "flow"
	CategoryData   = "data"
	CategoryModule = "module"
)

// Ports of flow.if and flow.switch.
const (
	PortTrue    = "true"
	PortFalse   = "false"
	PortDefault = "default"
)

var portRe = regexp.MustCompile(`^[a-z][a-z0-9_-]*$`)

// ManualProps configures trigger.manual.
type ManualProps struct {
	Description string `json:"description" desc:"shown when starting the policy by hand"`
}

// IfProps configures flow.if.
type IfProps struct {
	Condition bool `json:"condition" lcm:"required,expr" desc:"boolean expression, e.g. trigger.status == 'down'"`
}

// SwitchCase is one branch of flow.switch.
type SwitchCase struct {
	Port string `json:"port" lcm:"required" desc:"output port taken when the case matches"`
	When bool   `json:"when" lcm:"required,expr" desc:"boolean expression"`
}

// SwitchProps configures flow.switch.
type SwitchProps struct {
	Cases []SwitchCase `json:"cases" lcm:"required" desc:"evaluated in order; the first true case wins, otherwise default"`
}

// MergeProps configures flow.merge.
type MergeProps struct {
	Value any `json:"value" desc:"optional output, usually an expression over the merged branches"`
}

// SetProps configures data.set.
type SetProps struct {
	Values map[string]any `json:"values" lcm:"required" desc:"output object; values may be expressions"`
}

// ModuleProps configures a module reference. Module nodes are expanded at
// compile time and never executed.
type ModuleProps struct {
	Module string         `json:"module" lcm:"required" desc:"module name"`
	Params map[string]any `json:"params" desc:"module parameters; values may be expressions"`
}

// OutputProps configures module.output.
type OutputProps struct {
	Port  string `json:"port" lcm:"required" desc:"module output port"`
	Value any    `json:"value" desc:"output of the module node in the calling policy"`
}

// Register adds the core node types to r.
func Register(r *node.Registry) {
	node.Register(r, node.Spec[ManualProps]{
		Type: "trigger.manual", Category: node.CategoryTrigger,
		Description: "Started by hand from the UI, API or CLI; the supplied parameters become trigger.",
	})
	node.Register(r, node.Spec[IfProps]{
		Type: "flow.if", Category: CategoryFlow,
		Description: "Takes the true or false port depending on a condition.",
		Ports:       []string{PortTrue, PortFalse},
		Run: func(_ context.Context, _ *node.RunContext, p IfProps) (node.Result, error) {
			port := PortFalse
			if p.Condition {
				port = PortTrue
			}
			return node.Result{Port: port, Output: p.Condition}, nil
		},
	})
	node.Register(r, node.Spec[SwitchProps]{
		Type: "flow.switch", Category: CategoryFlow,
		Description: "Takes the port of the first matching case, or default.",
		Ports:       []string{PortDefault},
		PortsOf: func(p SwitchProps) []string {
			ports := make([]string, 0, len(p.Cases)+1)
			for _, c := range p.Cases {
				ports = append(ports, c.Port)
			}
			return append(ports, PortDefault)
		},
		Validate: validateSwitch,
		Run: func(_ context.Context, _ *node.RunContext, p SwitchProps) (node.Result, error) {
			for _, c := range p.Cases {
				if c.When {
					return node.Result{Port: c.Port, Output: c.Port}, nil
				}
			}
			return node.Result{Port: PortDefault, Output: PortDefault}, nil
		},
	})
	node.Register(r, node.Spec[MergeProps]{
		Type: "flow.merge", Category: CategoryFlow,
		Description: "Join point for branches; with dead-path elimination it waits only for live branches.",
		Run: func(_ context.Context, _ *node.RunContext, p MergeProps) (node.Result, error) {
			return node.Result{Output: p.Value}, nil
		},
	})
	node.Register(r, node.Spec[SetProps]{
		Type: "data.set", Category: CategoryData,
		Description: "Outputs an object built from static values and expressions.",
		Run: func(_ context.Context, _ *node.RunContext, p SetProps) (node.Result, error) {
			return node.Result{Output: p.Values}, nil
		},
	})
	node.Register(r, node.Spec[ModuleProps]{
		Type: model.TypeModule, Category: CategoryModule,
		Description: "Uses a module; its ports are the module's output ports.",
		Run: func(context.Context, *node.RunContext, ModuleProps) (node.Result, error) {
			return node.Result{}, node.Permanent(errors.New("module nodes are expanded at compile time"))
		},
	})
	node.Register(r, node.Spec[map[string]any]{
		Type: model.TypeModuleInput, Category: CategoryModule,
		Description: "Entry of a module; outputs the resolved module parameters.",
		Run: func(_ context.Context, _ *node.RunContext, p map[string]any) (node.Result, error) {
			return node.Result{Output: p}, nil
		},
	})
	node.Register(r, node.Spec[OutputProps]{
		Type: model.TypeModuleOutput, Category: CategoryModule,
		Description: "Exit of a module through the given port.",
		Validate: func(p OutputProps) error {
			if !portRe.MatchString(p.Port) || p.Port == node.PortError {
				return fmt.Errorf("port %q: must be a port name other than error", p.Port)
			}
			return nil
		},
		Run: func(_ context.Context, _ *node.RunContext, p OutputProps) (node.Result, error) {
			return node.Result{Output: p.Value}, nil
		},
	})
}

func validateSwitch(p SwitchProps) error {
	if len(p.Cases) == 0 {
		return errors.New("cases: at least one case is required")
	}
	seen := map[string]bool{}
	for i, c := range p.Cases {
		switch {
		case !portRe.MatchString(c.Port):
			return fmt.Errorf("cases[%d].port: invalid port name %q", i, c.Port)
		case c.Port == PortDefault || c.Port == node.PortError:
			return fmt.Errorf("cases[%d].port: %q is reserved", i, c.Port)
		case seen[c.Port]:
			return fmt.Errorf("cases[%d].port: duplicate port %q", i, c.Port)
		}
		seen[c.Port] = true
	}
	return nil
}
