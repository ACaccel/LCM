package policy

import (
	i "interfaces"
	"log"
	"nodes"
)

/*
 * Struct
 */

type Policy struct {
	Description struct {
		PolicyName string
		PolicyType string
	}
	Triggers   []i.NodeConfig
	PolicyList *PolicyList
}

/*
 * Method: Policy
 */

func (w *Policy) Init(pConfig i.PolicyConfig) {
	w.Description.PolicyName = pConfig.PolicyName
	w.Description.PolicyType = pConfig.PolicyType
	w.Triggers = pConfig.PolicyTrigger

	var policyList *PolicyList = new(PolicyList)
	policyList.init()

	for _, triggerConfig := range pConfig.PolicyTrigger {
		trigger := nodes.NewTrigger(triggerConfig)
		policyList.initTrigger(trigger, triggerConfig.NodeName)
	}

	for _, nodeConfig := range pConfig.PolicyNode {
		functionNode, numPort := nodes.NewNode(nodeConfig)
		policyList.initNode(functionNode, nodeConfig.NodeName, numPort)
	}

	for _, path := range pConfig.PolicyPath {
		policyList.connectNode(path)
	}

	w.PolicyList = policyList
}

func (w *Policy) Execute(triggerNode *ListNode, content any) {
	log.Println("Policy: Execute policy", w.Description.PolicyName)
	policyTask(triggerNode, content)
}

func policyTask(node *ListNode, content any) {
	// check if all input is ready
	for _, input := range node.packet.input {
		if input != nil && !input.ready {
			return
		}
	}

	// execute node and update the status
	if node.description.NumInput == 0 {
		node.packet.output.content, node.nextPath = (*node.baseNode).Execute([]any{content})
	} else {
		var inputContent = make([]any, node.description.NumInput)
		for i, input := range node.packet.input {
			if input != nil {
				inputContent[i] = (*input.content)
			}
		}
		node.packet.output.content, node.nextPath = (*node.baseNode).Execute(inputContent)
	}

	node.packet.output.ready = true

	// execute next node
	for _, port := range node.nextPath {
		if node.next[port] != nil {
			policyTask(node.next[port], content)
		}
	}
}