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
	StatusCh 		chan bool
	TriggerStatusCh map[string]chan bool
	PolicyList 		*PolicyList
	
	status 			bool
}

/*
 * Method: Policy
 */

func (p *Policy) Init(pConfig i.PolicyConfig, modules map[string]i.ModuleConfig) {
	p.Description.PolicyName = pConfig.PolicyName
	p.Description.PolicyType = pConfig.PolicyType
	p.WebhookChMap = make(map[string]chan any)
	p.TriggerCh = make(chan i.NodeOutput)
	p.status = false
	p.StatusCh = make(chan bool)
	p.TriggerStatusCh = make(map[string]chan bool)
	var policyList *PolicyList = new(PolicyList)
	policyList.init()

	for _, triggerConfig := range pConfig.PolicyTrigger {
		trigger := nodes.NewTrigger(triggerConfig)
		triggerNode := policyList.initTrigger(trigger, triggerConfig.NodeName, triggerConfig.NodeType)
		if triggerConfig.NodeType == "Webhook" {
			p.WebhookChMap[triggerConfig.NodeName] = make(chan any)
		}
		p.TriggerStatusCh[triggerConfig.NodeName] = make(chan bool)
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

	p.status = <-p.StatusCh
	for p.status {
		select {
		case status := <-p.StatusCh: // todo: broadcast to all triggers to terminate
			for _, trigger := range p.Triggers {
				p.TriggerStatusCh[trigger.Description.NodeName] <- status
			}
			p.status = status
			p.Clean()
		case content := <-p.TriggerCh:
			p.Clean()
			triggerNode.setTriggerReady(content)
			info := i.ExecInfo{}
			
			if triggerNode.Next[0] != nil {
				go policyTask(triggerNode.Next[0], info)
			}
		}
	}

	log.Println("Policy: Terminate policy", p.Description.PolicyName)
}

func (p *Policy) Clean() {
	for _, node := range p.PolicyList.NodeSet {
		node.clean()
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