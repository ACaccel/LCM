package If

import (
	"fmt"
	i "interfaces"
	u "nodes/utils"
)

type InputFormat struct {
	Value float64
}

type OutputFormat struct {
	Value float64
	Result bool
}

type Property struct {
	Condition string
	Value float64
}

var Description = i.NodeDescription{
	NodeType: "If",
	NumInputPort: 1,
	NumOutputPort: 2,
}

type If struct {
	i.FunctionNode
}

func (n *If) Execute(input []any, info i.ExecInfo) i.NodeExecResult {
	var property Property
	u.GetProperty(n.Config.Property, &property)
	
	var inputarr []InputFormat = make([]InputFormat, len(input))
	for i := 0; i < len(input); i++ {
		err := u.GetInput(input[i], &inputarr[i])
		if err != nil {
			fmt.Println("If: Get input error,", err)
		}
	}

	var output OutputFormat
	var port []int
	output.Value = inputarr[0].Value
	switch property.Condition {
		case "==":
			output.Result = inputarr[0].Value == property.Value
		case "!=":
			output.Result = inputarr[0].Value != property.Value
		case ">":
			output.Result = inputarr[0].Value > property.Value
		case "<":
			output.Result = inputarr[0].Value < property.Value
		case ">=":
			output.Result = inputarr[0].Value >= property.Value
		case "<=":
			output.Result = inputarr[0].Value <= property.Value
		default:
			fmt.Println("If: Invalid operation")
	}

	if output.Result {
		port = []int{0}
	} else {
		port = []int{1}
	}

	fmt.Println("If: Execute function", n.Config.NodeName, "Input:", inputarr)
	fmt.Println("If: Execute function", n.Config.NodeName, "Output:", output)
	
	return i.NodeExecResult{
		Output: output,
		Ports: port,
	}
}