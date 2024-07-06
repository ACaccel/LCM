package policy

import (
	"fmt"
	"log"

	i "lcm/packages/interfaces"
)

/*
 * Struct
 */

type ListNode struct {
	Description struct {
		NodeName	string
		NodeType	string
		i.NumPort
	}
	baseNode *i.IPolicyNode
	Next	[]*ListNode
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

func (node *ListNode) init(nodeName string, nodeType string, numInput int, numOutput int, baseNode *i.IPolicyNode) {
	node.Description.NodeName = nodeName
	node.Description.NodeType = nodeType
	node.Description.NumInput = numInput
	node.Description.NumOutput = numOutput
	node.baseNode = baseNode
	node.Next = make([]*ListNode, numOutput)
	node.prev = make([]*ListNode, numInput)

	node.packet.input = make([]*dataPacket, numInput)
	node.packet.output = make([]*dataPacket, numOutput)
	for i := 0; i < numOutput; i++ {
		node.packet.output[i] = newDataPacket(fmt.Sprintf("%s-output-%d", nodeName, i))
	}
	node.nextPath = make([]int, 0)
}

func (node *ListNode) pushNextNode(next *ListNode, portPair [2]int) {
	node.Next[portPair[0]] = next
	next.prev[portPair[1]] = node
}

func (node *ListNode) getInputContent(info i.ExecInfo) []any {
	// content for trigger node
	if info.Content != nil {
		return []any{info.Content}
	}

	// extract content from input packet array
	var inputContent []any = make([]any, node.Description.NumInput)
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

func (node *ListNode) TriggerExecute(info i.ExecInfo) {
	(*node.baseNode).Execute([]any{}, info)
}

func (node *ListNode) execute(info i.ExecInfo) {
	content := node.getInputContent(info)
	res := (*node.baseNode).Execute(content, info)
	
	// update output packet and nextPath
	for i, output := range res.Output {
		node.packet.output[i].setContent(output)
	}
	node.nextPath = res.Ports

	node.setPacketReady()
}

func (node *ListNode) setTriggerReady(content i.NodeOutput) {
	node.packet.output[0].setContent(content[0])
	node.setPacketReady()
}

func (node *ListNode) setPacketReady() {
	for _, output := range node.packet.output {
		output.setReady()
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

func (list *PolicyList) initNode(policyNode *i.IPolicyNode, nodeName string, nodeType string, numPort i.NumPort) {
	var node *ListNode = new(ListNode)
	node.init(
		nodeName,
		nodeType,
		numPort.NumInput,
		numPort.NumOutput,
		policyNode,
	)

	list.NodeSet[node.Description.NodeName] = node
}

func (list *PolicyList) initTrigger(policyNode *i.IPolicyNode, triggerName string, triggerType string) *ListNode {
	var node *ListNode = new(ListNode)
	node.init(
		triggerName,
		triggerType,
		0,
		1,
		policyNode,
	)

	list.NodeSet[node.Description.NodeName] = node
	return node
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