package nodes

import (
	f "nodes/Functions"
	t "nodes/Triggers"
	i "interfaces"
)

func NewTrigger(nodeConfig i.NodeConfig) (*i.IPolicyNode) {
	info := i.TriggerNode{
		Description: t.Description[nodeConfig.NodeType],
		Config: nodeConfig,
	}
	trigger := t.InitNode(info)

	return trigger
}

func NewNode(nodeConfig i.NodeConfig) (*i.IPolicyNode, i.NumPort) {
	info := i.FunctionNode{
		Description: f.Description[nodeConfig.NodeType],
		Config: nodeConfig,
	}
	node, numPort := f.InitNode(info)

	return node, numPort
}