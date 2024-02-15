package If

import (
	"fmt"
	i "interfaces"
	u "nodes/utils"
)

type InputFormat struct {
	Value int
}

type Property struct {
	Condition string
	Value int
}

var Description = i.NodeDescription{
	NodeType: "If",
	NumInputPort: 1,
	NumOutputPort: 2,
}

type If struct {
	i.FunctionNode
}

func (n *If) Execute(input []any) i.NodeExecResult {
	var property Property
	var input1 InputFormat
	u.GetProperty(n.Config.Property, &property)
	err := u.GetInputContent(input[0], &input1)
	if err != nil {
		fmt.Println("If: Execute function", n.Config.NodeName, "Error:", err)
	}

	output := property
	
	return i.NodeExecResult{
		Output: output,
		Ports: []int{0},
	}
}