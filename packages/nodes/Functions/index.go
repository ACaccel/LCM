package Functions

import (
	i "lcm/packages/interfaces"
	"lcm/packages/nodes/Functions/If"
	"lcm/packages/nodes/Functions/Logic"
	"lcm/packages/nodes/Functions/Reqforward"
)

var Description = make(map[string]i.NodeDescription)

func init() {
	Description["If"] = If.Description
	Description["Logic"] = logic.Description
	Description["Reqforward"] = reqforward.Description
}

func InitNode(info i.FunctionNode) (*i.IPolicyNode, i.NumPort) {
	var node i.IPolicyNode
	var numPort i.NumPort

	switch info.Description.NodeType {
	case "If":
		node = &If.If{FunctionNode: info}
	case "Logic":
		node = &logic.Logic{FunctionNode: info}
	case "Reqforward":
		node = &reqforward.Reqforward{FunctionNode: info}
	default:
		node = nil
	}

	numPort = i.NumPort{
		NumInput: info.Description.NumInputPort,
		NumOutput: info.Description.NumOutputPort,
	}

	return &node, numPort
}