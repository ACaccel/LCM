package policy

import (
	"errors"
	"log"
	"strconv"
	"time"

	i "1935/efo/LCM_Enabler/interfaces"
	abi "1935/efo/Rule_Framework/interfaces"
)

/*
 * Struct
 */

type ListNode struct {
	Description struct {
		NodeName string
		NodeType string
		abi.NumPort
	}
	baseNode *abi.IPolicyNode
	Next     []*ListNode
	prev     []*ListNode

	packet struct {
		input  []*dataPacket
		output []*dataPacket
	}
	nextPath []int

	// join is fixed at init and read-only afterwards. firstArrival holds a
	// single token so that exactly one walker executes a JoinAny node; it is
	// rebuilt by clean() the way packet.ready is.
	join         abi.JoinMode
	firstArrival chan struct{}

	// rec and scope are set once by Policy.AttachRecorder before any walker
	// goroutine starts, so they are immutable at runtime. scope is "" for a
	// top-level node and the module path for a module-internal one.
	rec   *Recorder
	scope string

	// quiesce is the module walk this node belongs to, or nil for a top-level
	// node. Set once when the list is built, so walkers share it without
	// synchronization of their own.
	quiesce *quiesce
}

type PolicyList struct {
	NodeSet map[string]*ListNode
	// quiesce is non-nil only for the list a module hosts; see Quiesce.go.
	quiesce *quiesce
}

/*
 * Method: ListNode
 */

func (node *ListNode) init(nodeName string, nodeType string, numInput int, numOutput int, join abi.JoinMode, baseNode *abi.IPolicyNode) {
	node.Description.NodeName = nodeName
	node.Description.NodeType = nodeType
	node.Description.NumInput = numInput
	node.Description.NumOutput = numOutput
	node.baseNode = baseNode
	node.join = join
	node.firstArrival = newArrivalToken()
	node.Next = make([]*ListNode, numOutput)
	node.prev = make([]*ListNode, numInput)

	node.packet.input = make([]*dataPacket, numInput)
	node.packet.output = make([]*dataPacket, numOutput)
	for i := 0; i < numOutput; i++ {
		newPacket := newDataPacket(nodeName, i)
		node.packet.output[i] = newPacket
	}
	node.nextPath = make([]int, 0)
}

// newArrivalToken returns a primed single-token channel.
func newArrivalToken() chan struct{} {
	token := make(chan struct{}, 1)
	token <- struct{}{}
	return token
}

// claimFirstArrival reports whether this walker is the one that runs a JoinAny
// node. Taking the token is non-blocking on purpose: a later arrival returns
// immediately rather than parking, because there is nothing left to wait for.
func (node *ListNode) claimFirstArrival() bool {
	select {
	case <-node.firstArrival:
		return true
	default:
		return false
	}
}

// getInputContent collects what each input port hands to the node. The second
// return value carries the per-port transform error, empty when there was none;
// the engine ignores it and only the execution trace consumes it, so the content
// slice is unchanged from before.
func (node *ListNode) getInputContent(info abi.ExecInfo) ([]any, []i.DataPacketContent, []string) {
	// content for trigger node
	if info.Content != nil {
		raw, _ := info.Content.(i.DataPacketContent)
		return []any{info.Content}, []i.DataPacketContent{raw}, []string{""}
	}

	// extract content from input packet array
	var inputContent []any = make([]any, node.Description.NumInput)
	var inputRaw []i.DataPacketContent = make([]i.DataPacketContent, node.Description.NumInput)
	var inputErrors []string = make([]string, node.Description.NumInput)
	for i, input := range node.packet.input {
		if input == nil {
			inputContent[i] = nil
			continue
		}
		inputRaw[i] = input.rawContent()
		// A JoinAny node runs on the first arrival, so its other ports are
		// normally still untouched and most of them never fill in. That is the
		// expected state, not a transform failure, and reporting it as an error
		// would put a red mark on every healthy rejoin in the trace.
		if node.join == abi.JoinAny && !input.isValid() {
			inputContent[i] = nil
			continue
		}
		if err := input.transform(); err != nil {
			log.Println("Error: transform input packet,", err)
			inputContent[i] = nil
			inputErrors[i] = err.Error()
			continue
		}
		inputContent[i] = input.content()
	}
	return inputContent, inputRaw, inputErrors
}

// inputNamespaces is what arrived on each port, still grouped by producer, for
// a node that needs to name a field's owner. Parallel to the content slice.
func (node *ListNode) inputNamespaces() []i.DataPacketContent {
	namespaces := make([]i.DataPacketContent, node.Description.NumInput)
	for port, input := range node.packet.input {
		if input == nil {
			continue
		}
		namespaces[port] = input.carriedNamespaces()
	}
	return namespaces
}

// carriedNamespaces merges what every connected input forwards, in port order.
// A node with no connected input -- a trigger, or a module entry driven from
// ExecInfo.Content -- starts a fresh namespace set.
func (node *ListNode) carriedNamespaces() i.DataPacketContent {
	carried := i.DataPacketContent{}
	for _, input := range node.packet.input {
		if input == nil {
			continue
		}
		for name, content := range input.carriedNamespaces() {
			carried[name] = content
		}
	}
	return carried
}

// inputsSatisfied blocks until the node is allowed to run, and reports false
// when this walker must give up instead.
//
// JoinAll takes one readiness token per connected port, so the node runs once
// every input has been produced. JoinAny runs on the first arrival: the walker
// that gets here was spawned by a producer that had already written and
// published its packet, so the data it carries is in place and there is nothing
// to wait for. The single-token latch is what keeps a second arrival from
// running the node again.
func (node *ListNode) inputsSatisfied(info abi.ExecInfo) bool {
	if node.join == abi.JoinAny {
		return node.claimFirstArrival()
	}

	for port, input := range node.packet.input {
		if input == nil {
			continue
		}
		node.rec.BlockOn(info.RunID, node.scope, node.Description.NodeName, port)
		ready, got := input.tryReady()
		if !got {
			node.quiesce.enterWait()
			ready = input.isReady(info.RunDone)
			node.quiesce.leaveWait()
		}
		node.rec.Unblock(info.RunID, node.scope, node.Description.NodeName, port)
		if !ready {
			log.Println("Policy: Execute node", node.Description.NodeName, "input not ready")
			return false
		}
	}
	return true
}

func (node *ListNode) TriggerExecute(info abi.ExecInfo) {
	(*node.baseNode).Execute([]any{}, info)
}

func (node *ListNode) execute(info abi.ExecInfo) {
	log.Println("Policy: Execute node", node.Description.NodeName)

	content, inputRaw, inputErrors := node.getInputContent(info)
	// info is a value, so filling this in here reaches the node without any of
	// the places that build an ExecInfo having to know about it.
	info.Namespaces = node.inputNamespaces()
	started := time.Now()
	res := (*node.baseNode).Execute(content, info)
	elapsed := time.Since(started)

	// A node emits only its own fields; the engine is what files them under the
	// node's name and carries the upstream namespaces along. Doing it here
	// rather than in each node is what keeps every node reading and writing
	// plain flat maps.
	carried := node.carriedNamespaces()
	// A boundary entry starts the namespaces rather than continuing them.
	if len(carried) == 0 && info.EntryNamespace != "" {
		if entry, ok := asContent(info.Content); ok {
			carried = accumulate(i.DataPacketContent{}, info.EntryNamespace, entry)
		}
	}
	for i, output := range res.Output {
		node.packet.output[i].setContent(accumulate(carried, node.Description.NodeName, output))
	}
	resolvedPorts := res.Ports
	if resolvedPorts == nil {
		resolvedPorts = []int{0}
	}
	node.nextPath = resolvedPorts

	// Record before releasing downstream walkers: setPacketReady is the only
	// way a successor can start, so recording first guarantees that for every
	// data edge u->v the trace holds seq(u) < seq(v). Everything handed over is
	// a local of this goroutine, so tracing reads no shared engine state.
	node.recordExec(info, started, elapsed, content, inputRaw, inputErrors, res.Output, resolvedPorts)

	node.setPacketReady(info.RunDone)
}

// recordExec adds this execution to the trace. Every argument is a local of the
// calling goroutine and the recorder clones them, so tracing adds no shared
// access to engine state.
func (node *ListNode) recordExec(
	info abi.ExecInfo,
	started time.Time,
	elapsed time.Duration,
	content []any,
	inputRaw []i.DataPacketContent,
	inputErrors []string,
	output i.NodeOutput,
	ports []int,
) {
	if node.rec == nil {
		return
	}
	node.rec.Record(info.RunID, i.NodeEvent{
		Node:          node.Description.NodeName,
		NodeType:      node.Description.NodeType,
		Scope:         node.scope,
		InPort:        info.ActiveInPort,
		StartOffsetMs: started.UnixMilli(),
		DurationMs:    elapsed.Milliseconds(),
		Input:         content,
		InputRaw:      inputRaw,
		InputErrors:   inputErrors,
		Output:        output,
		Ports:         ports,
		Status:        "executed",
	})
}

// setTriggerReady publishes the event that started the run.
//
// The payload is filed under the trigger's name here rather than left flat, so
// that every packet in the policy holds namespaces and nothing downstream has to
// guess which form it is looking at. Guessing would be wrong for a webhook body
// whose values all happen to be objects.
func (node *ListNode) setTriggerReady(content i.NodeOutput, done <-chan struct{}) {
	node.packet.output[0].setContent(
		accumulate(i.DataPacketContent{}, node.Description.NodeName, content[0]))
	node.setPacketReady(done)
}

func (node *ListNode) setPacketReady(done <-chan struct{}) {
	for _, output := range node.packet.output {
		output.setReady(done)
	}
}

func (node *ListNode) clean() {
	for _, input := range node.packet.input {
		if input != nil {
			input.clean()
		}
	}
	for _, output := range node.packet.output {
		if output != nil {
			output.clean()
		}
	}
	node.nextPath = make([]int, 0)
	node.firstArrival = newArrivalToken()
}

/*
 * Method: PolicyList
 */

func (list *PolicyList) init() {
	list.NodeSet = make(map[string]*ListNode)
}

func (list *PolicyList) initNode(policyNode *abi.IPolicyNode, nodeName string, nodeType string, numPort abi.NumPort, join abi.JoinMode) {
	if _, found := list.NodeSet[nodeName]; found {
		log.Println("Error: Node already exists", nodeName)
		return
	}
	var node *ListNode = new(ListNode)
	node.init(
		nodeName,
		nodeType,
		numPort.NumInput,
		numPort.NumOutput,
		join,
		policyNode,
	)

	node.quiesce = list.quiesce
	list.NodeSet[node.Description.NodeName] = node
}

func (list *PolicyList) initTrigger(policyNode *abi.IPolicyNode, triggerName string, triggerType string) *ListNode {
	if _, found := list.NodeSet[triggerName]; found {
		log.Println("Error: Node already exists", triggerName)
		return nil
	}
	var node *ListNode = new(ListNode)
	node.init(
		triggerName,
		triggerType,
		0,
		1,
		abi.JoinAll,
		policyNode,
	)

	list.NodeSet[node.Description.NodeName] = node
	return node
}

func (list *PolicyList) connectNode(policyPath i.ConnectionUnit) error {
	if len(policyPath.NodePair) != 2 || len(policyPath.PortPair) != 2 {
		return errors.New("LCM: Invalid connection unit")
	}
	node1, found1 := list.NodeSet[policyPath.NodePair[0]]
	node2, found2 := list.NodeSet[policyPath.NodePair[1]]

	// list connection
	if !found1 || !found2 {
		return errors.New("LCM: Node not found")
	}

	// A port holds exactly one connection. A second one would overwrite Next and
	// repoint the shared data packet, leaving two consumers racing for the single
	// readiness token: one proceeds, the other blocks forever and the run never
	// completes. Reject it at deploy time instead of hanging at run time.
	if node1.Next[policyPath.PortPair[0]] != nil {
		return errors.New("LCM: output port already connected: " +
			policyPath.NodePair[0] + " port " + strconv.Itoa(policyPath.PortPair[0]))
	}
	if node2.packet.input[policyPath.PortPair[1]] != nil {
		return errors.New("LCM: input port already connected: " +
			policyPath.NodePair[1] + " port " + strconv.Itoa(policyPath.PortPair[1]))
	}

	node1.Next[policyPath.PortPair[0]] = node2
	node2.prev[policyPath.PortPair[1]] = node1

	// packet connection
	node2.packet.input[policyPath.PortPair[1]] = node1.packet.output[policyPath.PortPair[0]]
	node1.packet.output[policyPath.PortPair[0]].setInPort(policyPath.PortPair[0])
	node1.packet.output[policyPath.PortPair[0]].setOutPort(policyPath.PortPair[1])

	// transformation setup
	node1.packet.output[policyPath.PortPair[0]].setTransFormat(policyPath.TransFormat)
	node1.packet.output[policyPath.PortPair[0]].setCarry(policyPath.Carry)
	return nil
}
