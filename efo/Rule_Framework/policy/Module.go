package policy

import (
	i "1935/efo/LCM_Enabler/interfaces"
	abi "1935/efo/Rule_Framework/interfaces"
	u "1935/efo/Rule_Framework/nodes/utils"
	"errors"
	"sort"
	"strconv"
	"strings"
	"sync"
)

var Description = abi.NodeDescription{
	NodeType:      "Module",
	NumInputPort:  1,
	NumOutputPort: 2,
}

// moduleRef is the property naming which module a Module node instantiates.
// It is optional: a node that sets no module falls back to matching its own
// name, which is how every config written before the property existed works.
type moduleRef struct {
	Module string `yaml:"module"`
}

type modulePort struct {
	nodePort int
	node     *ListNode
}

type Module struct {
	config  i.ModuleConfig
	policy  *Policy
	inPort  []modulePort
	outPort []modulePort
}

// ModuleNameOf resolves which module a node instantiates.
//
// Reading the property first is what lets one module be used more than once in
// a policy: node names are unique within a policy, so name matching alone caps
// every module at a single instance and forbids naming the instance for what it
// does.
func ModuleNameOf(nodeConfig i.NodeConfig) string {
	var ref moduleRef
	if err := u.GetProperty(nodeConfig.Property, &ref); err == nil && ref.Module != "" {
		return ref.Module
	}
	return nodeConfig.NodeName
}

// ValidateModule reports whether a module can actually be instantiated, by
// instantiating it and throwing the result away.
//
// Building it is the point: every rule the engine enforces at deploy time -- the
// node types exist, the paths connect, the boundary is complete, no module
// reaches itself -- is enforced by the same code here, so the design-time answer
// cannot drift from what deployment will do. Construction starts no goroutines
// and a module declares no triggers, so there is nothing to undo.
func ValidateModule(moduleName string, modules map[string]i.ModuleConfig) error {
	_, _, err := initModule(
		i.NodeConfig{NodeName: moduleName, NodeType: "Module"}, modules, nil)
	return err
}

// initModule builds a Module node and the nested policy it hosts.
//
// modulePath carries the module names already being built further up the stack.
// Without it a module that reaches itself recurses until the goroutine stack
// overflows, and a Go stack overflow is a fatal error that recover cannot catch:
// the whole EFO process dies at deploy time.
func initModule(
	nodeConfig i.NodeConfig,
	modules map[string]i.ModuleConfig,
	modulePath []string,
) (*abi.IPolicyNode, abi.NumPort, error) {
	moduleName := ModuleNameOf(nodeConfig)

	config, found := modules[moduleName]
	if !found {
		return nil, abi.NumPort{}, errors.New(
			"LCM: node " + nodeConfig.NodeName + " references unknown module " + moduleName)
	}

	for _, ancestor := range modulePath {
		if ancestor == moduleName {
			return nil, abi.NumPort{}, errors.New(
				"LCM: module cycle: " + strings.Join(append(modulePath, moduleName), " -> "))
		}
	}

	var module = Module{config: config}

	var modulePolicy = Policy{}
	err := modulePolicy.initWithModulePath(i.PolicyConfig{
		PolicyName:    config.ModuleName,
		PolicyTrigger: []i.NodeConfig{},
		PolicyNode:    config.ModuleNode,
		PolicyPath:    config.ModulePath,
	}, modules, append(modulePath, moduleName))
	if err != nil {
		return nil, abi.NumPort{}, err
	}
	module.policy = &modulePolicy

	module.inPort, err = bindPorts(
		config.InPort, config.NumInPort, moduleName, "input", modulePolicy.PolicyList,
		func(node *ListNode) int { return node.Description.NumInput })
	if err != nil {
		return nil, abi.NumPort{}, err
	}
	module.outPort, err = bindPorts(
		config.OutPort, config.NumOutPort, moduleName, "output", modulePolicy.PolicyList,
		func(node *ListNode) int { return node.Description.NumOutput })
	if err != nil {
		return nil, abi.NumPort{}, err
	}

	var node abi.IPolicyNode = &module
	return &node, abi.NumPort{
		NumInput:  config.NumInPort,
		NumOutput: config.NumOutPort,
	}, nil
}

// bindPorts resolves one boundary side into the inner nodes it addresses.
//
// Every slot must be filled with a node that exists and a port that node has.
// An unfilled slot leaves a nil node that Execute dereferences from a walker
// goroutine, where nothing recovers, so an incomplete boundary has to be
// rejected here rather than discovered at run time.
func bindPorts(
	configured []i.ModulePortConfig,
	numPorts int,
	moduleName string,
	side string,
	list *PolicyList,
	arityOf func(*ListNode) int,
) ([]modulePort, error) {
	fail := func(reason string) error {
		return errors.New("LCM: module " + moduleName + " " + side + " port: " + reason)
	}

	ports := make([]modulePort, numPorts)
	bound := make([]bool, numPorts)
	for _, port := range configured {
		if port.ModulePort < 0 || port.ModulePort >= numPorts {
			return nil, fail(strconv.Itoa(port.ModulePort) + " is outside 0.." + strconv.Itoa(numPorts-1))
		}
		node, ok := list.NodeSet[port.NodeName]
		if !ok {
			return nil, fail(strconv.Itoa(port.ModulePort) + " names unknown node " + port.NodeName)
		}
		if port.NodePort < 0 || port.NodePort >= arityOf(node) {
			return nil, fail(strconv.Itoa(port.ModulePort) + " addresses " + port.NodeName +
				" port " + strconv.Itoa(port.NodePort) + ", which that node does not have")
		}
		ports[port.ModulePort] = modulePort{nodePort: port.NodePort, node: node}
		bound[port.ModulePort] = true
	}

	for index, isBound := range bound {
		if !isBound {
			return nil, fail(strconv.Itoa(index) + " is declared but never wired to an inner node")
		}
	}
	return ports, nil
}

// Execute runs the module's inner graph and reports which of its output ports
// the walk reached.
//
// Every wired input port is entered, not just the one the arriving walker came
// in on. Entering only that one meant a module could declare any number of
// inputs but read exactly one: the inner node behind every other port kept a nil
// packet and never ran, so the data on those ports reached the module node and
// stopped there.
func (m *Module) Execute(input []any, info abi.ExecInfo) abi.NodeExecResult {
	// Safe to reset here because the previous Execute drained its own walk
	// before returning, and the enclosing run's reset guarantees that previous
	// Execute happened at all.
	m.policy.Clean()
	q := m.policy.PolicyList.quiesce
	q.reset()

	// Which ports actually carry something is settled before anything starts,
	// so a module nothing arrived at returns without leaving a goroutine behind
	// waiting for a walk that will never happen.
	entries := []int{}
	for port := range m.inPort {
		// A JoinAny node upstream leaves ports that never arrived holding nil,
		// and most of them never will.
		if port < len(input) && input[port] != nil {
			entries = append(entries, port)
		}
	}
	if len(entries) == 0 {
		return abi.NodeExecResult{Output: m.exitContent(), Ports: []int{}}
	}

	// The inner walk ends either by going quiet or by the enclosing run being
	// retired, whichever comes first. Linking the two is what keeps a module
	// from holding a run open after undeploy.
	go func() {
		select {
		case <-q.idleCh():
		case <-info.RunDone:
		}
		q.cancel()
	}()

	exits := &exitPorts{}
	for _, port := range entries {
		entry := m.inPort[port]
		entryInfo := abi.ExecInfo{
			Service_Name:        info.Service_Name,
			File_Path:           info.File_Path,
			ControlPlaneBaseURL: info.ControlPlaneBaseURL,
			RunID:               info.RunID,
			RunDone:             q.doneCh(),
			ActiveInPort:        entry.nodePort,
			Content:             input[port],
			// The boundary flattens in both directions, so what arrives has no
			// namespaces of its own. Naming it per input port is what keeps two
			// ports from overwriting each other where their chains rejoin, and
			// gives an inner condition something to qualify with. The name
			// matches the boundary pseudo-node the designer draws, and the "__"
			// prefix is reserved there so it cannot collide with a real node.
			EntryNamespace: entryNamespace(port),
		}
		node := entry.node
		q.spawn(func() { m.walk(node, entryInfo, exits) })
	}

	// Parked rather than counted as running: the inner DAG runs synchronously
	// here, so a stalled inner branch must not keep the whole run reported as
	// still executing.
	rec := m.inPort[0].node.rec
	rec.Park(info.RunID)
	<-q.idleCh()
	q.cancel()
	q.drain()
	rec.Unpark(info.RunID)

	return abi.NodeExecResult{Output: m.exitContent(), Ports: exits.sorted()}
}

// hostModulePolicy marks a policy list as hosting a module's inner graph.
//
// Every node built into the list takes a copy of the counter, so this has to
// happen before the nodes do. A module needs it because the enclosing walker
// blocks inside Module.Execute waiting for a result, so the module must be able
// to tell when its walk is finished; a top-level policy never asks, and its
// list leaves the field nil. See Quiesce.go.
func hostModulePolicy(list *PolicyList) {
	list.quiesce = newQuiesce()
}

// entryNamespace names what arrived at one of the module's input ports. It
// mirrors the designer's boundary node id (see graphModel.boundaryNodeId), so a
// qualified reference typed against the canvas resolves at run time.
func entryNamespace(port int) string {
	return "__in:" + strconv.Itoa(port)
}

// exitContent reads what each module output port carries after the walk.
//
// A module reports its own flat result, not the namespaces its internals built
// up. Flattening at the boundary is what keeps inner node names from leaking
// into the enclosing policy, where two modules could otherwise collide on an
// inner name neither author knew about. The enclosing walker then files this
// under the module node's own name like any other node.
func (m *Module) exitContent() i.NodeOutput {
	output := make(i.NodeOutput, m.config.NumOutPort)
	for port := 0; port < m.config.NumOutPort; port++ {
		exit := m.outPort[port]
		inner := exit.node.packet.output[exit.nodePort].rawContent()
		output[port] = flattenNamespaces(inner, flattenOrder(inner, nil, exit.node.Description.NodeName))
	}
	return output
}

// exitPorts collects the module output ports the walk reached. Several walkers
// write to it, so it carries its own lock; it is new state, not shared engine
// state given a lock after the fact.
type exitPorts struct {
	mu    sync.Mutex
	taken map[int]bool
}

func (e *exitPorts) add(port int) {
	e.mu.Lock()
	defer e.mu.Unlock()
	if e.taken == nil {
		e.taken = map[int]bool{}
	}
	e.taken[port] = true
}

// sorted returns the ports in ascending order. Map iteration is randomized, and
// the enclosing walker spawns one successor per entry here, so an unordered
// result would make the same policy fan out in a different order every run.
func (e *exitPorts) sorted() []int {
	e.mu.Lock()
	defer e.mu.Unlock()
	ports := make([]int, 0, len(e.taken))
	for port := range e.taken {
		ports = append(ports, port)
	}
	sort.Ints(ports)
	return ports
}

// walk is one walker inside a module.
//
// It mirrors the top-level walker with one addition: a node backing a module
// output port records that the walk reached it. A node can be both an exit and
// an ordinary node -- one port leaves the module while another continues inside
// it -- so it goes on to its successors either way.
func (m *Module) walk(node *ListNode, info abi.ExecInfo, exits *exitPorts) {
	defer node.rec.Done(info.RunID)

	if !node.inputsSatisfied(info) {
		return
	}

	node.execute(info)

	// Collect every module output port this node reached. One inner node can
	// back several of them, one per node port -- an If inside a module is
	// exactly that shape -- so stopping at the first entry naming this node
	// would silently drop whichever branch is not listed first.
	for modulePort, outport := range m.outPort {
		if outport.node != node {
			continue
		}
		for _, path := range node.nextPath {
			if path == outport.nodePort {
				exits.add(modulePort)
			}
		}
	}

	for _, port := range node.nextPath {
		if node.Next[port] == nil {
			continue
		}
		next := node.Next[port]
		nextInfo := abi.ExecInfo{
			Service_Name:        info.Service_Name,
			File_Path:           info.File_Path,
			ControlPlaneBaseURL: info.ControlPlaneBaseURL,
			RunID:               info.RunID,
			RunDone:             info.RunDone,
			ActiveInPort:        node.packet.output[port].outPort(),
		}
		node.rec.Spawn(info.RunID)
		node.quiesce.spawn(func() { m.walk(next, nextInfo, exits) })
	}
}
