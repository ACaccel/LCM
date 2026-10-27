package engine

import (
	"context"
	"fmt"
	"regexp"
	"slices"
	"strings"
	"time"

	"github.com/ACaccel/lcm/internal/expr"
	"github.com/ACaccel/lcm/internal/model"
	"github.com/ACaccel/lcm/internal/node"
)

var portRe = regexp.MustCompile(`^[a-z][a-z0-9_-]*$`)

// defaultMaxBackoff caps retry backoff when the policy sets no maxBackoff.
const defaultMaxBackoff = 5 * time.Minute

// CompileOptions configures Compile.
type CompileOptions struct {
	Registry *node.Registry
	// Modules resolves module references; nil rejects every module node.
	Modules ModuleResolver
	// Vars are extra top-level expression variables (see Config.Vars).
	Vars []string
}

// Compile turns a policy into an immutable Plan: modules are expanded into
// scoped ids, node types, ports, edges and props are checked, expressions
// are compiled and their node references checked against the graph, and a
// topological order is computed. Errors are model.Issues; non-fatal
// findings are in Plan.Warnings.
func Compile(ctx context.Context, p *model.Policy, opts CompileOptions) (*Plan, error) {
	if opts.Registry == nil {
		return nil, fmt.Errorf("engine: compile: no node registry")
	}
	c := &compiler{ctx: ctx, opts: opts, used: map[string]*model.Module{}}
	top := &scope{input: -1, outputs: map[string][]int{}}
	c.expand(top, p.Spec.Nodes, "", nil)
	plan := &Plan{
		name:        p.Metadata.Name,
		labels:      p.Metadata.Labels,
		mode:        p.Spec.Concurrency.Mode,
		limit:       p.Spec.Concurrency.Limit,
		timeout:     p.Spec.Timeout.D(),
		onInterrupt: p.Spec.OnInterrupt,
		params:      p.Spec.Params,
		nodes:       c.nodes,
		index:       make(map[string]int, len(c.nodes)),
	}
	if plan.mode == "" {
		plan.mode = model.ConcurrencyQueue
	}
	if plan.onInterrupt == "" {
		plan.onInterrupt = model.OnInterruptResume
	}
	if plan.params == nil {
		plan.params = map[string]any{}
	}
	for _, n := range c.nodes {
		plan.index[n.id] = n.idx
	}
	if len(c.is) == 0 {
		c.checkTriggers(plan, p)
	}
	if len(c.is) == 0 {
		c.sort(plan)
	}
	if len(c.is) == 0 {
		c.resolveRefs(plan)
	}
	if len(c.is) > 0 {
		return nil, c.is
	}
	plan.warnings = unreachable(plan)

	doc, err := model.Encode(p)
	if err != nil {
		return nil, err
	}
	plan.document = doc
	plan.modules = make(map[string][]byte, len(c.used))
	for name, m := range c.used {
		b, err := model.Encode(m)
		if err != nil {
			return nil, err
		}
		plan.modules[name] = b
	}
	return plan, nil
}

type compiler struct {
	ctx   context.Context
	opts  CompileOptions
	nodes []*planNode
	is    model.Issues
	used  map[string]*model.Module
	stack []string
}

type endpoint struct {
	idx  int
	port string
}

// moduleCall carries the caller's side of a module expansion.
type moduleCall struct {
	params map[string]any
	caller *scope
	path   string
}

type scopeResult struct {
	input   int
	outputs map[string]int // module port -> module.output node
}

func (c *compiler) addf(path, format string, args ...any) {
	c.is = append(c.is, model.Issue{Path: path, Message: fmt.Sprintf(format, args...)})
}

func (c *compiler) addIssues(base string, is model.Issues) {
	for _, x := range is {
		c.is = append(c.is, model.Issue{Path: joinIssuePath(base, x.Path), Message: x.Message})
	}
}

// expand adds the nodes of one scope and wires their edges. call is nil for
// the policy scope.
func (c *compiler) expand(sc *scope, docNodes []model.Node, base string, call *moduleCall) scopeResult {
	res := scopeResult{input: -1, outputs: map[string]int{}}
	entry := map[string]int{}
	exits := map[string]map[string][]endpoint{}
	for i := range docNodes {
		n := &docNodes[i]
		path := fmt.Sprintf("%sspec.nodes[%d]", base, i)
		if n.Type == model.TypeModule {
			in, outs, ok := c.expandModule(sc, n, path)
			if !ok {
				continue
			}
			entry[n.ID] = in
			ex := map[string][]endpoint{}
			ports := sortedKeys(outs)
			for _, port := range ports {
				ex[port] = []endpoint{{idx: outs[port], port: node.PortOut}}
				sc.outputs[n.ID] = append(sc.outputs[n.ID], outs[port])
			}
			exits[n.ID] = ex
			continue
		}
		pn, ok := c.addNode(sc, n, path, call)
		if !ok {
			continue
		}
		entry[n.ID] = pn.idx
		ex := map[string][]endpoint{}
		for port := range pn.ports {
			ex[port] = []endpoint{{idx: pn.idx, port: port}}
		}
		exits[n.ID] = ex
		sc.outputs[n.ID] = []int{pn.idx}
		switch n.Type {
		case model.TypeModuleInput:
			res.input = pn.idx
			sc.input = pn.idx
		case model.TypeModuleOutput:
			port, _ := n.Props["port"].(string)
			if !portRe.MatchString(port) || port == node.PortError {
				c.addf(path+".props.port", "must be a static port name other than error")
				continue
			}
			if len(n.Next) > 0 {
				c.addf(path+".next", "module.output nodes have no outgoing edges; connect the module node instead")
				continue
			}
			if prev, dup := res.outputs[port]; dup {
				c.addf(path+".props.port", "port %q is already used by %s", port, c.nodes[prev].id)
			}
			res.outputs[port] = pn.idx
		}
	}
	for i := range docNodes {
		n := &docNodes[i]
		ex, ok := exits[n.ID]
		if !ok || n.Type == model.TypeModuleOutput {
			continue // already reported
		}
		path := fmt.Sprintf("%sspec.nodes[%d]", base, i)
		for _, port := range sortedKeys(n.Next) {
			eps, ok := ex[port]
			if !ok {
				switch {
				case n.Type == model.TypeModule && port == node.PortError:
					c.addf(path+".next.error", "module nodes have no error port; route errors inside the module to a module.output")
				default:
					c.addf(path+".next."+port, "unknown port %q (ports: %s)", port, strings.Join(sortedKeys(ex), ", "))
				}
				continue
			}
			for _, t := range n.Next[port] {
				to, ok := entry[t]
				if !ok {
					continue // target failed to compile or validation reported it
				}
				for _, ep := range eps {
					c.addEdge(ep.idx, ep.port, to)
				}
			}
		}
	}
	return res
}

func (c *compiler) addNode(sc *scope, n *model.Node, path string, call *moduleCall) (*planNode, bool) {
	def, ok := c.opts.Registry.Lookup(n.Type)
	if !ok {
		c.addf(path+".type", "unknown node type %q", n.Type)
		return nil, false
	}
	pn := &planNode{
		idx: len(c.nodes), id: sc.prefix + n.ID, typ: n.Type, def: def, path: path,
		effect: def.Effect(), trigger: def.IsTrigger(), evalScope: sc,
		timeout: n.Timeout.D(), joinMode: n.JoinMode,
	}
	if pn.joinMode == "" {
		pn.joinMode = model.JoinAll
	}
	if r := n.Retry; r != nil {
		pn.retry = retryPolicy{max: r.Max, backoff: r.Backoff.D(), maxBackoff: r.MaxBackoff.D()}
		if pn.retry.maxBackoff == 0 {
			pn.retry.maxBackoff = max(defaultMaxBackoff, pn.retry.backoff)
		}
	}
	if pn.trigger && call != nil {
		c.addf(path+".type", "trigger nodes are not allowed in modules")
		return nil, false
	}
	raw, propsPath := n.Props, path+".props"
	if n.Type == model.TypeModuleInput {
		if len(n.Props) > 0 {
			c.addf(path+".props", "module.input takes no props; parameters come from the module node")
		}
		raw, propsPath = call.params, call.path+".props.params"
		pn.evalScope = call.caller
	}
	cp, is := compileProps(def, raw, c.opts.Vars)
	if len(is) > 0 {
		c.addIssues(propsPath, is)
		return nil, false
	}
	pn.props = cp
	if pn.trigger && len(cp.leaves) > 0 {
		c.addf(propsPath, "trigger props must be static; expressions are evaluated only inside runs")
		return nil, false
	}
	ports, err := def.Ports(cp.partial)
	if err != nil {
		c.addf(propsPath, "%v", err)
		return nil, false
	}
	pn.ports = make(map[string]bool, len(ports)+1)
	for _, p := range ports {
		pn.ports[p] = true
	}
	pn.ports[node.PortError] = true
	for _, v := range cp.vars {
		if !slices.Contains(expr.BuiltinVars, v) {
			pn.extraVars = append(pn.extraVars, v)
		}
	}
	c.nodes = append(c.nodes, pn)
	return pn, true
}

func (c *compiler) expandModule(sc *scope, n *model.Node, path string) (int, map[string]int, bool) {
	ok := true
	for _, k := range sortedKeys(n.Props) {
		if k != "module" && k != "params" {
			c.addf(path+".props."+k, "unknown field")
			ok = false
		}
	}
	name, isStr := n.Props["module"].(string)
	if !isStr || name == "" || expr.HasTemplate(name) {
		c.addf(path+".props.module", "must be a module name")
		return 0, nil, false
	}
	params := map[string]any{}
	if raw, present := n.Props["params"]; present {
		m, isMap := raw.(map[string]any)
		if !isMap {
			c.addf(path+".props.params", "must be an object")
			return 0, nil, false
		}
		params = m
	}
	if n.Timeout != 0 || n.Retry != nil {
		c.addf(path, "timeout and retry are not supported on module nodes; set them on the nodes inside the module")
		ok = false
	}
	if !ok {
		return 0, nil, false
	}
	if slices.Contains(c.stack, name) {
		c.addf(path+".props.module", "module recursion: %s -> %s", strings.Join(c.stack, " -> "), name)
		return 0, nil, false
	}
	if c.opts.Modules == nil {
		c.addf(path+".props.module", "modules are not available")
		return 0, nil, false
	}
	mod, err := c.opts.Modules.ResolveModule(c.ctx, name)
	if err != nil {
		c.addf(path+".props.module", "%v", err)
		return 0, nil, false
	}
	if mod.Metadata.Name != name {
		c.addf(path+".props.module", "resolver returned module %q for %q", mod.Metadata.Name, name)
		return 0, nil, false
	}
	c.used[name] = mod

	merged := map[string]any{}
	declared := map[string]bool{}
	for _, p := range mod.Spec.Params {
		declared[p.Name] = true
		switch v, given := params[p.Name]; {
		case given:
			merged[p.Name] = v
		case p.HasDefault:
			merged[p.Name] = p.Default
		default:
			c.addf(path+".props.params", "missing required parameter %q of module %s", p.Name, name)
			ok = false
		}
	}
	for _, k := range sortedKeys(params) {
		if !declared[k] {
			c.addf(path+".props.params."+k, "module %s has no parameter %q", name, k)
			ok = false
		}
	}
	if !ok {
		return 0, nil, false
	}

	inner := &scope{prefix: sc.prefix + n.ID + "/", module: name, parent: sc, input: -1, outputs: map[string][]int{}}
	c.stack = append(c.stack, name)
	before := len(c.is)
	res := c.expand(inner, mod.Spec.Nodes, path+"/"+name+":", &moduleCall{params: merged, caller: sc, path: path})
	c.stack = c.stack[:len(c.stack)-1]
	if len(c.is) > before || res.input < 0 || len(res.outputs) == 0 {
		return 0, nil, false
	}
	in := c.nodes[res.input]
	in.joinMode = n.JoinMode
	if in.joinMode == "" {
		in.joinMode = model.JoinAll
	}
	return res.input, res.outputs, true
}

func (c *compiler) addEdge(from int, port string, to int) {
	f := c.nodes[from]
	found := false
	for gi := range f.out {
		if f.out[gi].port == port {
			f.out[gi].targets = append(f.out[gi].targets, to)
			found = true
			break
		}
	}
	if !found {
		f.out = append(f.out, edgeGroup{port: port, targets: []int{to}})
	}
	if port == node.PortError {
		f.hasErrorEdge = true
	}
	t := c.nodes[to]
	t.inDegree++
	t.preds = append(t.preds, from)
}

func (c *compiler) checkTriggers(plan *Plan, p *model.Policy) {
	for _, n := range plan.nodes {
		if !n.trigger {
			continue
		}
		plan.triggers = append(plan.triggers, n.idx)
		if n.inDegree > 0 {
			c.addf(n.path, "trigger nodes cannot have inbound edges")
		}
	}
	if len(plan.triggers) == 0 && len(p.Spec.Nodes) > 0 {
		c.addf("spec.nodes", "at least one trigger node is required")
	}
}

// sort computes a topological order (Kahn's algorithm, stable by document
// order) and reports cycles.
func (c *compiler) sort(plan *Plan) {
	deg := make([]int, len(plan.nodes))
	queue := make([]int, 0, len(plan.nodes))
	for i, n := range plan.nodes {
		deg[i] = n.inDegree
		if deg[i] == 0 {
			queue = append(queue, i)
		}
	}
	order := make([]int, 0, len(plan.nodes))
	for len(queue) > 0 {
		i := queue[0]
		queue = queue[1:]
		order = append(order, i)
		for _, g := range plan.nodes[i].out {
			for _, t := range g.targets {
				deg[t]--
				if deg[t] == 0 {
					queue = append(queue, t)
				}
			}
		}
	}
	if len(order) < len(plan.nodes) {
		var cyc []string
		for i, d := range deg {
			if d > 0 {
				cyc = append(cyc, plan.nodes[i].id)
			}
		}
		c.addf("spec.nodes", "cycle among nodes %s", strings.Join(cyc, ", "))
		return
	}
	plan.topo = order
}

// resolveRefs checks that every nodes.<id> reference names a node of the
// expression's scope that is a strict ancestor of the referencing node, so
// its output can exist when the expression is evaluated.
func (c *compiler) resolveRefs(plan *Plan) {
	words := (len(plan.nodes) + 63) / 64
	anc := make([][]uint64, len(plan.nodes))
	for _, i := range plan.topo {
		set := make([]uint64, words)
		for _, p := range plan.nodes[i].preds {
			for w := range set {
				set[w] |= anc[p][w]
			}
			set[p/64] |= 1 << (uint(p) % 64)
		}
		anc[i] = set
	}
	isAnc := func(of, a int) bool { return anc[of][a/64]&(1<<(uint(a)%64)) != 0 }
	for _, n := range plan.nodes {
		for _, name := range n.props.refs {
			cands, ok := n.evalScope.outputs[name]
			if !ok {
				c.addf(n.path+".props", "nodes.%s: unknown node", name)
				continue
			}
			var up []int
			for _, t := range cands {
				if isAnc(n.idx, t) {
					up = append(up, t)
				}
			}
			if len(up) == 0 {
				c.addf(n.path+".props", "nodes.%s is not upstream of %s", name, n.id)
				continue
			}
			n.refs = append(n.refs, nodeRef{name: name, targets: up})
		}
	}
}

func unreachable(plan *Plan) model.Issues {
	seen := make([]bool, len(plan.nodes))
	stack := slices.Clone(plan.triggers)
	for len(stack) > 0 {
		i := stack[len(stack)-1]
		stack = stack[:len(stack)-1]
		if seen[i] {
			continue
		}
		seen[i] = true
		for _, g := range plan.nodes[i].out {
			stack = append(stack, g.targets...)
		}
	}
	var is model.Issues
	for _, i := range plan.topo {
		if !seen[i] {
			is = append(is, model.Issue{Path: plan.nodes[i].path, Message: fmt.Sprintf("node %s is unreachable from any trigger", plan.nodes[i].id)})
		}
	}
	return is
}

func sortedKeys[V any](m map[string]V) []string {
	keys := make([]string, 0, len(m))
	for k := range m {
		keys = append(keys, k)
	}
	slices.Sort(keys)
	return keys
}
