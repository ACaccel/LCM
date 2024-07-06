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
	Triggers   		[] *ListNode
	WebhookChMap 	map[string]chan any
	TriggerCh 		chan i.NodeOutput
	Status 			bool
	StatusCh 		chan bool
	PolicyList 		*PolicyList
}

/*
 * Method: Policy
 */

func (p *Policy) Init(pConfig i.PolicyConfig, modules map[string]i.ModuleConfig) {
	p.Description.PolicyName = pConfig.PolicyName
	p.Description.PolicyType = pConfig.PolicyType
	p.WebhookChMap = make(map[string]chan any)
	p.TriggerCh = make(chan i.NodeOutput)
	p.Status = false
	p.StatusCh = make(chan bool)
	var policyList *PolicyList = new(PolicyList)
	policyList.init()

	for _, triggerConfig := range pConfig.PolicyTrigger {
		trigger := nodes.NewTrigger(triggerConfig)
		triggerNode := policyList.initTrigger(trigger, triggerConfig.NodeName, triggerConfig.NodeType)
		if triggerConfig.NodeType == "Webhook" {
			p.WebhookChMap[triggerConfig.NodeName] = make(chan any)
		}
		p.Triggers = append(p.Triggers, triggerNode)
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

func (p *Policy) Execute(triggerNode *ListNode) {
	log.Println("Policy: Execute policy", p.Description.PolicyName)

	for p.Status {
		select {
		case status := <-p.StatusCh: // todo: broadcast to all triggers to terminate
			p.Status = status
		case content := <-p.TriggerCh:
			p.Cleaner()	// todo: 1. clean channel and start cleaning after triggers; 2. clean ticker inside interval trigger
			triggerNode.setTriggerReady(content)
			info := i.ExecInfo{}
			go policyTask(triggerNode.Next[0], info)
		}
	}
}

func (p *Policy) Cleaner() {
	for _, trigger := range p.Triggers {
		policyCleaner(p.PolicyList.NodeSet[trigger.Description.NodeName])
	}
}

func policyCleaner(node *ListNode) {
	node.clean()
	for _, node := range node.Next {
		if node != nil {
			policyCleaner(node)
		}
	}
}

func policyTask(node *ListNode, info i.ExecInfo) {
	// check if all input is ready
	for _, input := range node.packet.input {
		if input != nil && !input.isReady() {
			log.Println("Policy: Execute node", node.Description.NodeName, "input not ready")
			return
		}
	}

	// execute node and update the status
	node.execute(info)

	// execute next node
	for _, port := range node.nextPath {
		if node.Next[port] != nil {
			var newInfo = i.ExecInfo{
				ActiveInPort: node.packet.output[port].outPort(),
			}
			go policyTask(node.Next[port], newInfo)
		}
	}
}