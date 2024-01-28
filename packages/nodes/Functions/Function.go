package Functions

import (
	"fmt"
	i "interfaces"
)

var description = i.NodeDescription{
	NodeType: "Function",
	NumInputPort: 2,
	NumOutputPort: 2,
}

type Function struct {
	i.FunctionNode
}

func (n *Function) Init(config i.NodeConfig) (*Function, i.NumPort) {
	n.Description = description
	n.Config = config

	return n, i.NumPort{
		NumInput: description.NumInputPort,
		NumOutput: description.NumOutputPort,
	}
}

func (n *Function) Execute(input []any) (*any, []int) {
	output := input[0]
	fmt.Println("Function: Execute function", n.Config.NodeName)
	return &output, []int{0, 1}
}