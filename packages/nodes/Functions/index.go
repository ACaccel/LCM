package Functions

import (
	i "interfaces"
	"nodes/Functions/If"
)

var Description = make(map[string]i.NodeDescription)

func init() {
	Description["If"] = If.Description
}

func InitNode(info i.FunctionNode) (*i.IPolicyNode, i.NumPort) {
	var node i.IPolicyNode
	var numPort i.NumPort

	switch info.Description.NodeType {
	case "If":
		node = &If.If{FunctionNode: info}
	default:
		node = nil
	}

	numPort = i.NumPort{
		NumInput: info.Description.NumInputPort,
		NumOutput: info.Description.NumOutputPort,
	}

	return &node, numPort
}