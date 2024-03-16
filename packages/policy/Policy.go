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

func (w *Policy) Init(pConfig i.PolicyConfig, modulesConfig []i.ModuleConfig) {
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
		if nodeConfig.NodeType == "Module" {
			functionNode, numPort := initModule(nodeConfig, modulesConfig)
			if functionNode == nil {
				log.Println("Policy: Module not found", nodeConfig.NodeName)
				continue
			}
			policyList.initNode(functionNode, nodeConfig.NodeName, numPort)
		} else {
			functionNode, numPort := nodes.NewNode(nodeConfig)
			if functionNode == nil {
				log.Println("Policy: Node not found", nodeConfig.NodeName)
				continue
			}
			policyList.initNode(functionNode, nodeConfig.NodeName, numPort)
		}
	}

	for _, path := range pConfig.PolicyPath {
		policyList.connectNode(path)
	}

	w.PolicyList = policyList
}

func (w *Policy) Execute(triggerNode *ListNode, content any) {
	log.Println("Policy: Execute policy", w.Description.PolicyName)

	// Todo: call cleaner to clean all data packets

	newInfo := i.ExecInfo{
		ActiveInPort: -1,
		UseShared: true,
		SharedContent: content,
	}
	policyTask(triggerNode, newInfo)
}

func policyTask(node *ListNode, info i.ExecInfo) {
	// check if all input is ready
	for _, input := range node.packet.input {
		if input != nil && !input.ready {
			// Todo: currently, I let the last subPolicy which is connected with the node to execute the node 
			// (when all packets are ready)
			// The poilcy Task should be replaced by something like threads or go routines
			return
		}
	}

	// execute node and update the status
	node.execute(node.getInputContent(info), info)
	for _, output := range node.packet.output {
		output.ready = true
	}

	// execute next node
	for _, port := range node.nextPath {
		if node.next[port] != nil {
			var newInfo = i.ExecInfo{
				ActiveInPort: node.packet.output[port].outPort,
				UseShared: false,
				SharedContent: nil,
			}
			policyTask(node.next[port], newInfo)
		}
	}
}