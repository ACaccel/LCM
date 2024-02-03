package Function

import (
	"fmt"
	i "interfaces"
)

var Description = i.NodeDescription{
	NodeType: "Function",
	NumInputPort: 2,
	NumOutputPort: 2,
}

type Function struct {
	i.FunctionNode
}

func (n *Function) Execute(input []any) (*any, []int) {
	output := input[0]
	fmt.Println("Function: Execute function", n.Config.NodeName)
	return &output, []int{0, 1}
}