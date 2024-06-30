package policy

import (
	i "lcm/packages/interfaces"
	"log"
	"lcm/packages/nodes"
)

/*
 * Struct
 */

type Policy struct {
	Description struct {
		PolicyName string
		PolicyType string
	}
	Triggers   		[]i.NodeConfig
	WebhookChMap 	map[string]chan any
	PolicyList 		*PolicyList
}

/*
 * Method: Policy
 */

func (p *Policy) Init(pConfig i.PolicyConfig, modules map[string]i.ModuleConfig) {
	p.Description.PolicyName = pConfig.PolicyName
	p.Description.PolicyType = pConfig.PolicyType
	p.Triggers = pConfig.PolicyTrigger
	p.WebhookChMap = make(map[string]chan any)

	var policyList *PolicyList = new(PolicyList)
	policyList.init()

	for _, triggerConfig := range pConfig.PolicyTrigger {
		trigger := nodes.NewTrigger(triggerConfig)
		policyList.initTrigger(trigger, triggerConfig.NodeName, triggerConfig.NodeType)
		if triggerConfig.NodeType == "Webhook" {
			p.WebhookChMap[triggerConfig.NodeName] = make(chan any)
		}
	}

	for _, nodeConfig := range pConfig.PolicyNode {
		if nodeConfig.NodeType == "Module" {
			functionNode, numPort := initModule(nodeConfig, modules)
			if functionNode == nil {
				log.Println("Policy: Module not found", nodeConfig.NodeName)
				continue
			}
			policyList.initNode(functionNode, nodeConfig.NodeName, nodeConfig.NodeType, numPort)
		} else {
			functionNode, numPort := nodes.NewNode(nodeConfig)
			if functionNode == nil {
				log.Println("Policy: Node not found", nodeConfig.NodeName)
				continue
			}
			policyList.initNode(functionNode, nodeConfig.NodeName, nodeConfig.NodeType, numPort)
		}
	}

	for _, path := range pConfig.PolicyPath {
		policyList.connectNode(path)
	}

	p.PolicyList = policyList
}

func (p *Policy) Execute(triggerNode *ListNode, content any) {
	log.Println("Policy: Execute policy", p.Description.PolicyName)

	p.Cleaner()

	var newInfo i.ExecInfo
	if triggerNode.description.nodeType == "Webhook" {
		newInfo = i.ExecInfo{
			UseContent: false,
			WebhookChannel: p.WebhookChMap[triggerNode.description.nodeName],
		}
	} else {
		newInfo = i.ExecInfo{
			UseContent: false,
		}
	}
	go policyTask(triggerNode, newInfo)
}

func (p *Policy) Cleaner() {
	for _, trigger := range p.Triggers {
		policyCleaner(p.PolicyList.NodeSet[trigger.NodeName])
	}
}

func policyCleaner(node *ListNode) {
	node.clean()
	for _, node := range node.next {
		if node != nil {
			policyCleaner(node)
		}
	}
}

func policyTask(node *ListNode, info i.ExecInfo) {
	// check if all input is ready
	for _, input := range node.packet.input {
		if input != nil && !input.isReady() {
			return
		}
	}

	// execute node and update the status
	node.execute(node.getInputContent(info), info)

	// execute next node
	for _, port := range node.nextPath {
		if node.next[port] != nil {
			var newInfo = i.ExecInfo{
				ActiveInPort: node.packet.output[port].outPort(),
				UseContent: false,
			}
			go policyTask(node.next[port], newInfo)
		}
	}
}