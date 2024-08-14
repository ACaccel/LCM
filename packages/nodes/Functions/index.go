package Functions

import (
	i "lcm/packages/interfaces"

	// import your new written functions here
	"lcm/packages/nodes/Functions/If"
	"lcm/packages/nodes/Functions/Logic"
	"lcm/packages/nodes/Functions/Reqforward"
)

type nodeFunc func(info i.FunctionNode) i.IPolicyNode

var Description = make(map[string]i.NodeDescription)
var Function = make(map[string]nodeFunc)

func init() {
	// add your new written functions' descriptions here
	Description = map[string]i.NodeDescription{
		"If": If.Description,
		"Logic": logic.Description,
		"Reqforward": reqforward.Description,
	}

	// add your new written functions here
	Function = map[string]nodeFunc{
		"If": func(info i.FunctionNode) i.IPolicyNode { return &If.If{FunctionNode: info} },
		"Logic": func(info i.FunctionNode) i.IPolicyNode { return &logic.Logic{FunctionNode: info} },
		"Reqforward": func(info i.FunctionNode) i.IPolicyNode { return &reqforward.Reqforward{FunctionNode: info} },
	}
}

func InitNode(info i.FunctionNode) (*i.IPolicyNode, i.NumPort) {
	var node i.IPolicyNode
	var numPort i.NumPort

	if f, ok := Function[info.Description.NodeType]; ok {
		node = f(info)
	} else {
		node = nil
	}

	numPort = i.NumPort{
		NumInput: info.Description.NumInputPort,
		NumOutput: info.Description.NumOutputPort,
	}

	return &node, numPort
}