package policy

import (
	"log"

	i "interfaces"
)

/*
 * Struct
 */

type ListNode struct {
	description struct {
		nodeName   string
		i.NumPort
	}
	baseNode *i.IPolicyNode
	next       []*ListNode
	prev       []*ListNode

	packet struct {
		input 	[]*dataPacket
		output 	*dataPacket
	}
	nextPath []int
}

type PolicyList struct {
	NodeSet map[string]*ListNode
}

type dataPacket struct {
	content *any
	ready bool
}

/*
 * Method: ListNode
 */

func (node *ListNode) init(name string, numInput int, numOutput int, baseNode *i.IPolicyNode) {
	node.description.nodeName = name
	node.description.NumInput = numInput
	node.description.NumOutput = numOutput
	node.baseNode = baseNode
	node.next = make([]*ListNode, numOutput)
	node.prev = make([]*ListNode, numInput)

	node.packet.input = make([]*dataPacket, numInput)
	node.packet.output = new(dataPacket)
	node.nextPath = make([]int, 0)
}

func (node *ListNode) pushNextNode(next *ListNode, portPair [2]int) {
	node.next[portPair[0]] = next
	next.prev[portPair[1]] = node
}

func (node *ListNode) popNextNode(port int) {
	node.next[port] = nil
}

func (node *ListNode) popPrevNode(port int) {
	node.prev[port] = nil
}

func (node *ListNode) getInputContent() []any {
	var inputContent []any = make([]any, node.description.NumInput)
	for i, input := range node.packet.input {
		inputContent[i] = input.content
	}
	return inputContent
}

func (node *ListNode) execute(content []any) {
	node.packet.output.content, node.nextPath = (*node.baseNode).Execute(content)
}

/*
 * Method: PolicyList
 */

func (list *PolicyList) init() {
	list.NodeSet = make(map[string]*ListNode)
}

func (list *PolicyList) initNode(policyNode *i.IPolicyNode, nodeName string, numPort i.NumPort) {
	var node *ListNode = new(ListNode)
	node.init(
		nodeName,
		numPort.NumInput,
		numPort.NumOutput,
		policyNode,
	)
	
	list.NodeSet[node.description.nodeName] = node
}

func (list *PolicyList) initTrigger(policyNode *i.IPolicyNode, triggerName string) {
	var node *ListNode = new(ListNode)
	node.init(
		triggerName,
		0,
		1,
		policyNode,
	)

	list.NodeSet[node.description.nodeName] = node
}

func (list *PolicyList) connectNode(policyPath i.ConnectionUnit) {
	node1, found1 := list.NodeSet[policyPath.NodePair[0]]
	node2, found2 := list.NodeSet[policyPath.NodePair[1]]

	// list connection
	if !found1 || !found2 {
		log.Println("Error: Node not found", policyPath.NodePair)
	} else {
		node1.pushNextNode(node2, policyPath.PortPair)
	}

	// packet connection
	node2.packet.input[policyPath.PortPair[1]] = node1.packet.output
}