package policy

import (
	i "lcm/packages/interfaces"
)

type Property struct {
	
}

var Description = i.NodeDescription{
	NodeType: "If",
	NumInputPort: 1,
	NumOutputPort: 2,
}

type Module struct {
	config i.ModuleConfig
	policy Policy
	inPort []struct {
		nodePort int
		node *ListNode
	}
	outPort []struct {
		nodePort int
		node *ListNode
	}
}

func (n *Module) Execute(input []any, info i.ExecInfo) i.NodeExecResult {
	var newInfo = i.ExecInfo{
		ActiveInPort: n.inPort[info.ActiveInPort].nodePort,
		UseShared: true,
		SharedContent: input[info.ActiveInPort],
	}

	nextPath := n.moduleTask(n.inPort[info.ActiveInPort].node, newInfo)
	output := make([]any, n.config.NumOutPort)
	for i := 0; i < n.config.NumOutPort; i++ {
		output[i] = n.outPort[i].node.packet.output[n.outPort[i].nodePort].rawContent()
	}
	
	return i.NodeExecResult{
		Output: output,
		Ports: nextPath,
	}
}

func initModule(nodeConfig i.NodeConfig, modulesConfig []i.ModuleConfig) (*i.IPolicyNode, i.NumPort) {
	var module = Module{}

	// check if the node is a module
	for _, moduleConfig := range modulesConfig {
		if nodeConfig.NodeName == moduleConfig.ModuleName {
			module.config = moduleConfig
			break
		}
	}
	if module.config.ModuleName == "" {
		return nil, i.NumPort{}
	}

	// module policy setup
	var modulePolicy = Policy{}
	var policyConfig = i.PolicyConfig{
		PolicyName: module.config.ModuleName,
		PolicyType: "Module",
		PolicyTrigger: []i.NodeConfig{},
		PolicyNode: module.config.ModuleNode,
		PolicyPath: module.config.ModulePath,
	}
	modulePolicy.Init(policyConfig, modulesConfig)
	module.policy = modulePolicy
	
	// module port setup
	module.inPort = make([]struct{nodePort int; node *ListNode}, module.config.NumInPort)
	module.outPort = make([]struct{nodePort int; node *ListNode}, module.config.NumOutPort)
	for _, inPort := range module.config.InPort {
		module.inPort[inPort.ModulePort] = struct{nodePort int; node *ListNode}{
			nodePort: inPort.NodePort,
			node: module.policy.PolicyList.NodeSet[inPort.NodeName],
		}
	}
	for _, outPort := range module.config.OutPort {
		module.outPort[outPort.ModulePort] = struct{nodePort int; node *ListNode}{
			nodePort: outPort.NodePort,
			node: module.policy.PolicyList.NodeSet[outPort.NodeName],
		}
	}

	var node i.IPolicyNode = &module
	var numPort i.NumPort = i.NumPort{
		NumInput: module.config.NumInPort,
		NumOutput: module.config.NumOutPort,
	}

	return &node, numPort
}

func (n *Module) moduleTask(node *ListNode, info i.ExecInfo) ([]int) {
	// check if all input is ready
	for _, input := range node.packet.input {
		if input != nil && !input.isReady() {
			return []int{}
		}
	}

	// execute node and update the status
	node.execute(node.getInputContent(info), info)

	// return when reaching the end of the policy
	for i, outport := range n.outPort {
		if outport.node == node {
			nextPath := []int{}
			for _, path := range node.nextPath {
				if path == n.outPort[i].nodePort {
					nextPath = append(nextPath, i)
				}
			}
			return nextPath
		}
	}
	
	// execute next node
	nextPath := []int{}
	for _, port := range node.nextPath {
		if node.next[port] != nil {
			var newInfo = i.ExecInfo{
				ActiveInPort: node.packet.output[port].outPort(),
				UseShared: false,
				SharedContent: nil,
			}
			nextPath = append(nextPath, n.moduleTask(node.next[port], newInfo)...)
		}
	}

	return nextPath
}