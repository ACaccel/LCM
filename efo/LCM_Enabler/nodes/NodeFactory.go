package nodes

import (
	i "1935/efo/LCM_Enabler/interfaces"
	f "1935/efo/LCM_Enabler/nodes/Functions"
	t "1935/efo/LCM_Enabler/nodes/Triggers"
)

func NewTrigger(nodeConfig i.NodeConfig) (*i.IPolicyNode, error) {
	info := i.TriggerNode{
		Description: t.Description[nodeConfig.NodeType],
		Config:      nodeConfig,
	}
	trigger, err := t.InitNode(info)
	if err != nil {
		return nil, err
	}

	return trigger, nil
}

func NewNode(nodeConfig i.NodeConfig) (*i.IPolicyNode, i.NumPort, error) {
	info := i.FunctionNode{
		Description: f.Description[nodeConfig.NodeType],
		Config:      nodeConfig,
	}
	node, numPort, err := f.InitNode(info)
	if err != nil {
		return nil, i.NumPort{}, err
	}

	return node, numPort, nil
}
