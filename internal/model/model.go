// Package model defines the document types for apiVersion lcm/v1 (kinds
// Policy, Module and Service), their strict decoding from YAML or JSON, and the
// structural validation that needs no knowledge of node types.
package model

import (
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"

	"go.yaml.in/yaml/v3"
)

// APIVersion is the only supported document version.
const APIVersion = "lcm/v1"

// Kind is the document kind.
type Kind string

// Document kinds.
const (
	KindPolicy Kind = "Policy"
	KindModule Kind = "Module"
)

// Node types with structural meaning that the model validates itself.
const (
	TypeModule       = "module"
	TypeModuleInput  = "module.input"
	TypeModuleOutput = "module.output"
)

// Well-known port names.
const (
	PortOut   = "out"
	PortError = "error"
)

// ConcurrencyMode selects what happens when an event arrives while runs of
// the same policy are active.
type ConcurrencyMode string

// Concurrency modes.
const (
	ConcurrencyQueue    ConcurrencyMode = "queue"
	ConcurrencyDrop     ConcurrencyMode = "drop"
	ConcurrencyReplace  ConcurrencyMode = "replace"
	ConcurrencyParallel ConcurrencyMode = "parallel"
)

// OnInterrupt selects how startup recovery treats runs that were active when
// the process stopped.
type OnInterrupt string

// Interrupt policies.
const (
	OnInterruptResume OnInterrupt = "resume"
	OnInterruptAbort  OnInterrupt = "abort"
)

// JoinMode selects when a node with several inbound edges becomes ready.
type JoinMode string

// Join modes.
const (
	JoinAll JoinMode = "all"
	JoinAny JoinMode = "any"
)

// Document is a decoded Policy or Module.
type Document interface {
	DocKind() Kind
	DocName() string
}

// Header holds the fields common to every document.
type Header struct {
	APIVersion string   `yaml:"apiVersion" json:"apiVersion"`
	Kind       Kind     `yaml:"kind" json:"kind"`
	Metadata   Metadata `yaml:"metadata" json:"metadata"`
}

// Metadata names and labels a document.
type Metadata struct {
	Name   string            `yaml:"name" json:"name"`
	Labels map[string]string `yaml:"labels,omitempty" json:"labels,omitempty"`
}

// Policy is an operational policy: a graph of nodes started by a trigger.
type Policy struct {
	Header `yaml:",inline"`
	Spec   PolicySpec `yaml:"spec" json:"spec"`
}

// DocKind implements Document.
func (p *Policy) DocKind() Kind { return KindPolicy }

// DocName implements Document.
func (p *Policy) DocName() string { return p.Metadata.Name }

// PolicySpec is the body of a Policy.
type PolicySpec struct {
	Concurrency Concurrency    `yaml:"concurrency,omitempty" json:"concurrency,omitzero"`
	Timeout     Duration       `yaml:"timeout,omitempty" json:"timeout,omitempty"`
	OnInterrupt OnInterrupt    `yaml:"onInterrupt,omitempty" json:"onInterrupt,omitempty"`
	Params      map[string]any `yaml:"params,omitempty" json:"params,omitempty"`
	Nodes       []Node         `yaml:"nodes" json:"nodes"`
}

// Concurrency configures the policy's concurrency mode. Limit applies to
// parallel mode only.
type Concurrency struct {
	Mode  ConcurrencyMode `yaml:"mode,omitempty" json:"mode,omitempty"`
	Limit int             `yaml:"limit,omitempty" json:"limit,omitempty"`
}

// Module is a reusable subgraph expanded into the policies that use it.
type Module struct {
	Header `yaml:",inline"`
	Spec   ModuleSpec `yaml:"spec" json:"spec"`
}

// DocKind implements Document.
func (m *Module) DocKind() Kind { return KindModule }

// DocName implements Document.
func (m *Module) DocName() string { return m.Metadata.Name }

// ModuleSpec is the body of a Module.
type ModuleSpec struct {
	Params []Param `yaml:"params,omitempty" json:"params,omitempty"`
	Nodes  []Node  `yaml:"nodes" json:"nodes"`
}

// Param declares a module parameter. A parameter without a default is
// required.
type Param struct {
	Name        string
	Default     any
	HasDefault  bool
	Description string
}

// Required reports whether callers must supply the parameter.
func (p Param) Required() bool { return !p.HasDefault }

var paramKeys = map[string]bool{"name": true, "default": true, "description": true}

// UnmarshalYAML decodes a parameter, recording whether default was present
// so that an explicit null default is distinguishable from none.
func (p *Param) UnmarshalYAML(value *yaml.Node) error {
	if value.Kind != yaml.MappingNode {
		return fmt.Errorf("line %d: parameter must be a mapping", value.Line)
	}
	*p = Param{}
	for i := 0; i+1 < len(value.Content); i += 2 {
		k, v := value.Content[i], value.Content[i+1]
		if !paramKeys[k.Value] {
			return fmt.Errorf("line %d: field %s not found in type model.Param", k.Line, k.Value)
		}
		var err error
		switch k.Value {
		case "name":
			err = v.Decode(&p.Name)
		case "description":
			err = v.Decode(&p.Description)
		case "default":
			p.HasDefault = true
			err = v.Decode(&p.Default)
		}
		if err != nil {
			return err
		}
	}
	return nil
}

// MarshalJSON emits default only when the parameter has one.
func (p Param) MarshalJSON() ([]byte, error) {
	m := map[string]any{"name": p.Name}
	if p.HasDefault {
		m["default"] = p.Default
	}
	if p.Description != "" {
		m["description"] = p.Description
	}
	return json.Marshal(m)
}

// Node is one step of a policy or module graph.
type Node struct {
	ID       string              `yaml:"id" json:"id"`
	Type     string              `yaml:"type" json:"type"`
	Props    map[string]any      `yaml:"props,omitempty" json:"props,omitempty"`
	Next     map[string][]string `yaml:"next,omitempty" json:"next,omitempty"`
	Timeout  Duration            `yaml:"timeout,omitempty" json:"timeout,omitempty"`
	Retry    *Retry              `yaml:"retry,omitempty" json:"retry,omitempty"`
	JoinMode JoinMode            `yaml:"joinMode,omitempty" json:"joinMode,omitempty"`
}

// Retry configures additional attempts after retryable errors.
type Retry struct {
	Max        int      `yaml:"max" json:"max"`
	Backoff    Duration `yaml:"backoff,omitempty" json:"backoff,omitempty"`
	MaxBackoff Duration `yaml:"maxBackoff,omitempty" json:"maxBackoff,omitempty"`
}

// Duration is a time.Duration written in Go syntax ("2s", "30m").
type Duration time.Duration

// D returns the value as a time.Duration.
func (d Duration) D() time.Duration { return time.Duration(d) }

// UnmarshalYAML parses a Go duration string.
func (d *Duration) UnmarshalYAML(value *yaml.Node) error {
	if value.Kind != yaml.ScalarNode {
		return fmt.Errorf("line %d: duration must be a string such as \"30s\"", value.Line)
	}
	v, err := time.ParseDuration(value.Value)
	if err != nil {
		return fmt.Errorf("line %d: invalid duration %q: want Go syntax such as \"30s\"", value.Line, value.Value)
	}
	*d = Duration(v)
	return nil
}

// MarshalJSON writes the duration as a Go duration string.
func (d Duration) MarshalJSON() ([]byte, error) {
	return json.Marshal(time.Duration(d).String())
}

// UnmarshalJSON parses a Go duration string.
func (d *Duration) UnmarshalJSON(b []byte) error {
	var s string
	if err := json.Unmarshal(b, &s); err != nil {
		return errors.New("duration must be a string such as \"30s\"")
	}
	v, err := time.ParseDuration(s)
	if err != nil {
		return fmt.Errorf("invalid duration %q", s)
	}
	*d = Duration(v)
	return nil
}

// Issue is one problem found in a document, located by a path such as
// spec.nodes[3].next.true[0].
type Issue struct {
	Path    string `json:"path"`
	Message string `json:"message"`
}

func (i Issue) String() string {
	if i.Path == "" {
		return i.Message
	}
	return i.Path + ": " + i.Message
}

// Issues is a list of problems; it implements error.
type Issues []Issue

func (is Issues) Error() string {
	parts := make([]string, len(is))
	for i, x := range is {
		parts[i] = x.String()
	}
	return strings.Join(parts, "; ")
}

// Err returns is as an error, or nil when it is empty.
func (is Issues) Err() error {
	if len(is) == 0 {
		return nil
	}
	return is
}

func (is *Issues) addf(path, format string, args ...any) {
	*is = append(*is, Issue{Path: path, Message: fmt.Sprintf(format, args...)})
}
