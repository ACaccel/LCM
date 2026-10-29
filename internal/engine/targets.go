package engine

import "github.com/ACaccel/lcm/internal/expr"

// ServiceTarget is a node of a plan that acts on a service, found through
// its type's node.Spec.ServiceProp.
type ServiceTarget struct {
	// Node is the scoped node id, Type its node type.
	Node string
	Type string
	// Service is the service the node acts on, or "" when that is decided
	// only while the policy runs (it depends on the trigger event, another
	// node's output or a runtime variable).
	Service string
}

// ServiceTargets lists the nodes that act on a service, in document order.
// A target is resolved when its prop is a literal or an expression over
// params alone (as the built-in templates write it), including module
// parameters that are themselves static.
func (p *Plan) ServiceTargets() []ServiceTarget {
	var out []ServiceTarget
	for _, n := range p.nodes {
		prop := n.def.ServiceProp()
		if prop == "" {
			continue
		}
		out = append(out, ServiceTarget{Node: n.id, Type: n.typ, Service: p.staticString(n, prop)})
	}
	return out
}

// staticString returns the value of a top-level string prop when it can be
// known without running, and "" otherwise.
func (p *Plan) staticString(n *planNode, prop string) string {
	for _, l := range n.props.leaves {
		if len(l.path) != 1 || l.path[0] != prop {
			continue
		}
		if !paramsOnly(l.ev) {
			return ""
		}
		params, ok := p.staticParams(n.evalScope)
		if !ok {
			return ""
		}
		v, err := l.ev.Eval(expr.NewEnv(expr.Base{Params: params}, nil))
		if err != nil {
			return ""
		}
		s, _ := v.(string)
		return s
	}
	s, _ := n.props.raw[prop].(string)
	return s
}

// staticParams returns the params visible in sc when they are known before
// a run: the policy's params, or a module's parameters when every value the
// module node passes is static or uses params alone.
func (p *Plan) staticParams(sc *scope) (any, bool) {
	if sc == nil || sc.input < 0 {
		return p.params, true
	}
	in := p.nodes[sc.input]
	for _, l := range in.props.leaves {
		if !paramsOnly(l.ev) {
			return nil, false
		}
	}
	params, ok := p.staticParams(in.evalScope)
	if !ok {
		return nil, false
	}
	resolved, _, err := in.props.resolve(expr.NewEnv(expr.Base{Params: params}, nil))
	if err != nil {
		return nil, false
	}
	return resolved, true
}

// paramsOnly reports whether an expression reads nothing but params.
func paramsOnly(ev evaluator) bool {
	if len(ev.Refs()) > 0 {
		return false
	}
	for _, v := range ev.Vars() {
		if v != expr.VarParams {
			return false
		}
	}
	return true
}
