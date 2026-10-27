package engine

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/ACaccel/lcm/internal/model"
	"github.com/ACaccel/lcm/internal/node"
)

// ModuleResolver finds module documents by name. Deploy resolves modules
// through it once; the documents used are stored with the policy version so
// that recovery recompiles exactly what was deployed.
type ModuleResolver interface {
	ResolveModule(ctx context.Context, name string) (*model.Module, error)
}

// ErrModuleNotFound is returned by resolvers for unknown modules.
var ErrModuleNotFound = errors.New("engine: module not found")

// StaticModules is a ModuleResolver over a fixed set of modules.
type StaticModules map[string]*model.Module

// ResolveModule implements ModuleResolver.
func (s StaticModules) ResolveModule(_ context.Context, name string) (*model.Module, error) {
	m, ok := s[name]
	if !ok {
		return nil, fmt.Errorf("%w: %s", ErrModuleNotFound, name)
	}
	return m, nil
}

// Plan is a compiled policy. It is immutable and shared by every run of the
// policy version it was compiled from.
type Plan struct {
	name        string
	version     int
	labels      map[string]string
	mode        model.ConcurrencyMode
	limit       int
	timeout     time.Duration
	onInterrupt model.OnInterrupt
	params      map[string]any

	nodes    []*planNode
	topo     []int
	index    map[string]int
	triggers []int
	warnings model.Issues

	document []byte
	modules  map[string][]byte
}

// planNode is one node of the flattened graph.
type planNode struct {
	idx     int
	id      string // scoped id such as outer/inner/check
	typ     string
	def     *node.Definition
	path    string // document location for messages
	effect  node.Effect
	trigger bool

	// evalScope supplies params and node references for the node's props.
	// It is the node's own scope except for module.input, whose props are
	// the module parameters written in the calling scope.
	evalScope *scope
	props     *compiledProps

	timeout  time.Duration
	retry    retryPolicy
	joinMode model.JoinMode

	ports        map[string]bool
	out          []edgeGroup
	hasErrorEdge bool
	inDegree     int
	preds        []int
	refs         []nodeRef
	extraVars    []string
}

type edgeGroup struct {
	port    string
	targets []int
}

// nodeRef resolves nodes.<name> for a node: the first completed candidate
// supplies the value. A module has one candidate per output node.
type nodeRef struct {
	name    string
	targets []int
}

type scope struct {
	prefix string
	module string
	parent *scope
	// input is the module.input node whose output is params inside the
	// module; -1 for the policy scope.
	input int
	// outputs maps document ids to the nodes whose outputs nodes.<id>
	// refers to.
	outputs map[string][]int
}

type retryPolicy struct {
	max        int
	backoff    time.Duration
	maxBackoff time.Duration
}

// Name returns the policy name.
func (p *Plan) Name() string { return p.name }

// Version returns the stored version, or 0 for a plan that was only
// validated.
func (p *Plan) Version() int { return p.version }

// Labels returns the policy labels.
func (p *Plan) Labels() map[string]string { return p.labels }

// Warnings returns non-fatal compile findings such as unreachable nodes.
func (p *Plan) Warnings() model.Issues { return p.warnings }

// TriggerInfo describes a trigger node, for subsystems that watch the
// outside world and call Engine.Start.
type TriggerInfo struct {
	ID   string
	Type string
	// Props are the decoded props of the trigger type (trigger props are
	// always static).
	Props any
}

// Triggers returns the policy's trigger nodes in document order.
func (p *Plan) Triggers() []TriggerInfo {
	out := make([]TriggerInfo, 0, len(p.triggers))
	for _, i := range p.triggers {
		n := p.nodes[i]
		out = append(out, TriggerInfo{ID: n.id, Type: n.typ, Props: n.props.static})
	}
	return out
}

// NodeIDs returns the scoped ids of all nodes in topological order.
func (p *Plan) NodeIDs() []string {
	out := make([]string, len(p.topo))
	for i, idx := range p.topo {
		out[i] = p.nodes[idx].id
	}
	return out
}

func (p *Plan) concurrencyLimit() int {
	if p.mode == model.ConcurrencyParallel {
		return p.limit
	}
	return 1
}
