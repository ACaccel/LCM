package policy

import (
	i "lcm/packages/interfaces"
)

type Property struct {
	
}

var Description = i.NodeDescription{
	NodeType: "Module",
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

func initModule(nodeConfig i.NodeConfig, modules map[string]i.ModuleConfig) (*i.IPolicyNode, i.NumPort) {
	var module = Module{}

	// check if the node is a module
	for moduleName, moduleConfig := range modules {
		if nodeConfig.NodeName == moduleName {
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
	modulePolicy.Init(policyConfig, modules)
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

func (m *Module) Execute(input []any, info i.ExecInfo) i.NodeExecResult {
	m.policy.Cleaner()

	var newInfo = i.ExecInfo{
		ActiveInPort: m.inPort[info.ActiveInPort].nodePort,
		UseContent: true,
		Content: input[info.ActiveInPort],
	}
	chnextPath := make(chan []int)
	go m.moduleTask(m.inPort[info.ActiveInPort].node, newInfo, chnextPath)

	nextPath := <-chnextPath
	output := make(i.NodeOutput, m.config.NumOutPort)
	for i := 0; i < m.config.NumOutPort; i++ {
		output[i] = m.outPort[i].node.packet.output[m.outPort[i].nodePort].rawContent()
	}
	
	return i.NodeExecResult{
		Output: output,
		Ports: nextPath,
	}
}

// change moduleTask to goroutine
func (m *Module) moduleTask(node *ListNode, info i.ExecInfo, chnextPath chan []int) {
	// check if all input is ready
	for _, input := range node.packet.input {
		if input != nil && !input.isReady() {
			chnextPath <- []int{}
			return
		}
	}

	// execute node and update the status
	node.execute(node.getInputContent(info), info)

	// return when reaching the end of the policy
	for i, outport := range m.outPort {
		if outport.node == node {
			nextPath := []int{}
			for _, path := range node.nextPath {
				if path == m.outPort[i].nodePort {
					nextPath = append(nextPath, i)
				}
			}

			chnextPath <- nextPath
			return
		}
	}
	
	// execute next node
	nextPath := []int{}
	for _, port := range node.nextPath {
		if node.next[port] != nil {
			var newInfo = i.ExecInfo{
				ActiveInPort: node.packet.output[port].outPort(),
				UseContent: false,
			}
			var newChnextPath = make(chan []int)
			go m.moduleTask(node.next[port], newInfo, newChnextPath)
			nextPath = append(nextPath, <-newChnextPath...)
		}
	}

	chnextPath <- nextPath
}