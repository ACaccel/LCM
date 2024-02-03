package Functions

import (
	i "interfaces"
	"nodes/Functions/Function"
)

var Description = make(map[string]i.NodeDescription)

func init() {
	Description["Function"] = Function.Description
}

func InitNode(info i.FunctionNode) (*i.IPolicyNode, i.NumPort) {
	var node i.IPolicyNode
	var numPort i.NumPort

	switch info.Description.NodeType {
	case "Function":
		node = &Function.Function{FunctionNode: info}
	default:
		node = nil
	}

	numPort = i.NumPort{
		NumInput: info.Description.NumInputPort,
		NumOutput: info.Description.NumOutputPort,
	}

	return &node, numPort
}