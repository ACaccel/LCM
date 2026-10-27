package nodes

import (
	i "1935/efo/LCM_Enabler/interfaces"
	abi "1935/efo/Rule_Framework/interfaces"
	f "1935/efo/Rule_Framework/nodes/Functions"
	t "1935/efo/Rule_Framework/nodes/Triggers"
)

func NewTrigger(nodeConfig i.NodeConfig) (*abi.IPolicyNode, error) {
	info := abi.TriggerNode{
		Description: t.Description[nodeConfig.NodeType],
		Config:      nodeConfig,
	}
	trigger, err := t.InitNode(info)
	if err != nil {
		return nil, err
	}

	return trigger, nil
}

func NewNode(nodeConfig i.NodeConfig) (*abi.IPolicyNode, abi.NumPort, abi.JoinMode, error) {
	info := abi.FunctionNode{
		Description: f.Description[nodeConfig.NodeType],
		Config:      nodeConfig,
	}
	node, numPort, err := f.InitNode(info)
	if err != nil {
		return nil, abi.NumPort{}, abi.JoinAll, err
	}

	return node, numPort, joinModeOf(node, info.Description), nil
}

// joinModeOf takes the node's own answer when it has one, since a node like
// Merge decides from its configuration rather than from its type.
func joinModeOf(node *abi.IPolicyNode, description abi.NodeDescription) abi.JoinMode {
	if decider, ok := (*node).(abi.JoinModeDecider); ok {
		return decider.JoinMode()
	}
	return description.Join
}
