package node

import (
	"context"
	"errors"
	"fmt"
	"reflect"
	"regexp"
	"slices"
	"sort"
	"sync"

	"github.com/ACaccel/lcm/internal/model"
)

// Spec defines a node type with props type P.
type Spec[P any] struct {
	Type        string
	Category    string
	Description string
	// Effect defaults to EffectNone.
	Effect Effect
	// Ports are the output ports; the default is ["out"]. "error" is
	// implicit and must not be listed.
	Ports []string
	// PortsOf, when set, returns the complete port list for the given
	// props. It is called at compile time with props decoded from their
	// static values only: fields that hold expressions are zero. Ports
	// must therefore come from static fields.
	PortsOf func(P) []string
	// Validate checks decoded props; optional.
	Validate func(P) error
	// Run executes the node. It must honour ctx cancellation. Props are
	// shared between runs and must not be modified. Not used for trigger
	// nodes.
	Run func(ctx context.Context, rc *RunContext, props P) (Result, error)
	// DryRun replaces Run for write nodes in dry mode; optional.
	DryRun func(ctx context.Context, rc *RunContext, props P) (Result, error)
	// ServiceProp names the string prop holding the service the node acts
	// on, so that a policy's targets can be listed without running it;
	// optional.
	ServiceProp string
}

// Definition is a registered node type with its props type erased.
type Definition struct {
	typ         string
	category    string
	description string
	effect      Effect
	ports       []string
	props       []Prop
	additional  bool

	portsOf  func(any) []string
	decode   func(map[string]any) (any, error)
	validate func(any) error
	run      func(context.Context, *RunContext, any) (Result, error)
	dryRun   func(context.Context, *RunContext, any) (Result, error)

	serviceProp string
}

var (
	typeRe = regexp.MustCompile(`^[a-z][a-z0-9_]*(\.[a-z][a-z0-9_-]*)*$`)
	portRe = regexp.MustCompile(`^[a-z][a-z0-9_-]*$`)
)

// Registry holds node types. It is safe for concurrent use.
type Registry struct {
	mu   sync.RWMutex
	defs map[string]*Definition
}

// NewRegistry returns an empty registry.
func NewRegistry() *Registry {
	return &Registry{defs: map[string]*Definition{}}
}

// Register adds a node type. It panics on an invalid spec or a duplicate
// type, since both are programming errors detected at startup.
func Register[P any](r *Registry, s Spec[P]) *Definition {
	d, err := newDefinition(s)
	if err != nil {
		panic(fmt.Sprintf("node: register %q: %v", s.Type, err))
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	if _, dup := r.defs[d.typ]; dup {
		panic(fmt.Sprintf("node: register %q: duplicate type", d.typ))
	}
	r.defs[d.typ] = d
	return d
}

func newDefinition[P any](s Spec[P]) (*Definition, error) {
	if !typeRe.MatchString(s.Type) {
		return nil, errors.New("type must be dotted lowercase words such as flow.if")
	}
	if s.Category == "" {
		return nil, errors.New("category is required")
	}
	effect := s.Effect
	if effect == "" {
		effect = EffectNone
	}
	if !effect.Valid() {
		return nil, fmt.Errorf("invalid effect %q", s.Effect)
	}
	trigger := s.Category == CategoryTrigger
	if s.Run == nil && !trigger {
		return nil, errors.New("run is required")
	}
	if s.DryRun != nil && effect != EffectWrite {
		return nil, errors.New("dryRun is only used by write nodes")
	}
	ports := s.Ports
	if ports == nil {
		ports = []string{PortOut}
	}
	if err := checkPorts(ports); err != nil {
		return nil, err
	}
	props, additional, err := reflectProps(reflect.TypeFor[P]())
	if err != nil {
		return nil, err
	}
	if s.ServiceProp != "" && !slices.ContainsFunc(props, func(p Prop) bool { return p.Name == s.ServiceProp && p.Type == "string" }) {
		return nil, fmt.Errorf("serviceProp %q is not a string prop", s.ServiceProp)
	}
	d := &Definition{
		typ: s.Type, category: s.Category, description: s.Description, effect: effect,
		ports: slices.Clone(ports), props: props, additional: additional, serviceProp: s.ServiceProp,
		decode: func(m map[string]any) (any, error) { return decodeInto[P](m) },
	}
	if s.PortsOf != nil {
		d.portsOf = func(p any) []string { return s.PortsOf(p.(P)) }
	}
	if s.Validate != nil {
		d.validate = func(p any) error { return s.Validate(p.(P)) }
	}
	if s.Run != nil {
		d.run = func(ctx context.Context, rc *RunContext, p any) (Result, error) { return s.Run(ctx, rc, p.(P)) }
	}
	if s.DryRun != nil {
		d.dryRun = func(ctx context.Context, rc *RunContext, p any) (Result, error) { return s.DryRun(ctx, rc, p.(P)) }
	}
	return d, nil
}

func checkPorts(ports []string) error {
	seen := map[string]bool{}
	for _, p := range ports {
		switch {
		case p == PortError:
			return errors.New(`port "error" is implicit and must not be declared`)
		case !portRe.MatchString(p):
			return fmt.Errorf("invalid port name %q", p)
		case seen[p]:
			return fmt.Errorf("duplicate port %q", p)
		}
		seen[p] = true
	}
	return nil
}

// Lookup returns the definition of a node type.
func (r *Registry) Lookup(typ string) (*Definition, bool) {
	r.mu.RLock()
	defer r.mu.RUnlock()
	d, ok := r.defs[typ]
	return d, ok
}

// Schemas returns the schemas of all registered types, sorted by type.
func (r *Registry) Schemas() []Schema {
	r.mu.RLock()
	out := make([]Schema, 0, len(r.defs))
	for _, d := range r.defs {
		out = append(out, d.Schema())
	}
	r.mu.RUnlock()
	sort.Slice(out, func(i, j int) bool { return out[i].Type < out[j].Type })
	return out
}

// Type returns the node type name.
func (d *Definition) Type() string { return d.typ }

// Category returns the node category.
func (d *Definition) Category() string { return d.category }

// Effect returns the declared effect.
func (d *Definition) Effect() Effect { return d.effect }

// IsTrigger reports whether the type starts runs.
func (d *Definition) IsTrigger() bool { return d.category == CategoryTrigger }

// ServiceProp returns the prop naming the service the node acts on, or "".
func (d *Definition) ServiceProp() string { return d.serviceProp }

// HasDryRun reports whether the type provides a DryRun function.
func (d *Definition) HasDryRun() bool { return d.dryRun != nil }

// Schema returns the frontend schema.
func (d *Definition) Schema() Schema {
	return Schema{
		Type: d.typ, Category: d.category, Description: d.description, Effect: d.effect,
		Ports: slices.Clone(d.ports), DynamicPorts: d.portsOf != nil,
		Props: d.props, AdditionalProps: d.additional,
	}
}

// Leaves returns the values of raw that must be evaluated at run time.
func (d *Definition) Leaves(raw map[string]any) []Leaf { return d.dynamicLeaves(raw) }

// CheckStatic validates raw props as far as possible without evaluating
// expressions: unknown and required fields, enums and the types of static
// values. It returns the props decoded from static values only (dynamic
// fields zero), suitable for Ports. Issue paths are relative to props.
func (d *Definition) CheckStatic(raw map[string]any) (any, error) {
	var is model.Issues
	var static map[string]any
	if d.additional {
		static, _ = stripAny(raw).(map[string]any)
	} else {
		checkShape(d.props, raw, "", false, &is)
		if len(is) > 0 {
			return nil, is
		}
		static = withDefaults(d.props, strip(d.props, raw))
	}
	p, err := d.decode(static)
	if err != nil {
		return nil, model.Issues{{Message: err.Error()}}
	}
	return p, nil
}

// Decode fully decodes and validates resolved props (every dynamic value
// already evaluated). Issue paths are relative to props.
func (d *Definition) Decode(raw map[string]any) (any, error) {
	if raw == nil {
		raw = map[string]any{}
	}
	if !d.additional {
		var is model.Issues
		checkShape(d.props, raw, "", true, &is)
		if len(is) > 0 {
			return nil, is
		}
		raw = withDefaults(d.props, raw)
	}
	p, err := d.decode(raw)
	if err != nil {
		return nil, model.Issues{{Message: err.Error()}}
	}
	if d.validate != nil {
		if err := d.validate(p); err != nil {
			return nil, model.Issues{{Message: err.Error()}}
		}
	}
	return p, nil
}

// Ports returns the output ports for props decoded by CheckStatic or
// Decode, excluding the implicit "error" port.
func (d *Definition) Ports(props any) ([]string, error) {
	if d.portsOf == nil || props == nil {
		return slices.Clone(d.ports), nil
	}
	ports := d.portsOf(props)
	if err := checkPorts(ports); err != nil {
		return nil, err
	}
	return ports, nil
}

// Run executes the node with props from Decode.
func (d *Definition) Run(ctx context.Context, rc *RunContext, props any) (Result, error) {
	if d.run == nil {
		return Result{}, Permanent(fmt.Errorf("node type %s cannot be executed", d.typ))
	}
	return d.run(ctx, rc, props)
}

// DryRun executes the node's DryRun function; call only when HasDryRun.
func (d *Definition) DryRun(ctx context.Context, rc *RunContext, props any) (Result, error) {
	if d.dryRun == nil {
		return Result{}, Permanent(fmt.Errorf("node type %s has no dry run", d.typ))
	}
	return d.dryRun(ctx, rc, props)
}
