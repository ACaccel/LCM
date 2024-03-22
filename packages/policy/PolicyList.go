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
		nodeName	string
		i.NumPort
	}
	baseNode *i.IPolicyNode
	next	[]*ListNode
	prev	[]*ListNode

	packet struct {
		input	[]*dataPacket
		output	[]*dataPacket
	}
	nextPath []int
}

type PolicyList struct {
	NodeSet map[string]*ListNode
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
	node.packet.output = make([]*dataPacket, numOutput)
	for i := 0; i < numOutput; i++ {
		node.packet.output[i] = newDataPacket()
	}
	node.nextPath = make([]int, 0)
}

func (node *ListNode) pushNextNode(next *ListNode, portPair [2]int) {
	node.next[portPair[0]] = next
	next.prev[portPair[1]] = node
}

// func (node *ListNode) popNextNode(port int) {
// 	node.next[port] = nil
// }

// func (node *ListNode) popPrevNode(port int) {
// 	node.prev[port] = nil
// }

func (node *ListNode) getInputContent(info i.ExecInfo) []any {
	// content for trigger node
	if info.UseShared {
		return []any{info.SharedContent}
	}

	// extract content from input packet array
	var inputContent []any = make([]any, node.description.NumInput)
	for i, input := range node.packet.input {
		if input != nil {
			err := input.transform()
			if err != nil {
				log.Println("Error: transform input packet,", err)
				inputContent[i] = nil
			} else {
				inputContent[i] = input.content()
			}
		} else {
			inputContent[i] = nil
		}
	}
	return inputContent
}

func (node *ListNode) execute(content []any, info i.ExecInfo) {
	// update output packet and nextPath
	res := (*node.baseNode).Execute(content, info)
	output, ok := res.Output.([]any)
	if ok {
		for i, out := range output {
			node.packet.output[i].setContent(out)
		}
	} else {
		for i := 0; i < node.description.NumOutput; i++ {
			node.packet.output[i].setContent(res.Output)
		}
	}
	node.nextPath = res.Ports

	node.setPacketReady()
}

func (node *ListNode) setPacketReady() {
	for _, output := range node.packet.output {
		output.setReady(true)
	}
}

func (node *ListNode) clean() {
	for _, input := range node.packet.input {
		input.clean()
	}
	for _, output := range node.packet.output {
		output.clean()
	}
	node.nextPath = make([]int, 0)
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
	node2.packet.input[policyPath.PortPair[1]] = node1.packet.output[policyPath.PortPair[0]]
	node1.packet.output[policyPath.PortPair[0]].setInPort(policyPath.PortPair[0])
	node1.packet.output[policyPath.PortPair[0]].setOutPort(policyPath.PortPair[1])

	// transformation setup
	node1.packet.output[policyPath.PortPair[0]].setTransFormat(policyPath.TransFormat)
}