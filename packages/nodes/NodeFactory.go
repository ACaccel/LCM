package nodes

import (
	f "nodes/Functions"
	t "nodes/Triggers"
	i "interfaces"
)

func NewTrigger(nodeConfig i.NodeConfig) (*i.IPolicyNode) {
	var trigger i.IPolicyNode

	switch nodeConfig.NodeType {
	case "Webhook":
		trigger = new(t.Webhook).Init(nodeConfig)
	default:
		trigger = nil
	}

	return &trigger
}

func NewNode(nodeConfig i.NodeConfig) (*i.IPolicyNode, i.NumPort) {
	var node i.IPolicyNode
	var numPort i.NumPort

	switch nodeConfig.NodeType {
	case "Function":
		node, numPort = new(f.Function).Init(nodeConfig)
	default:
		node = nil
	}

	return &node, numPort
}