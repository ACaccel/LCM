package Logic

import (
	i "1935/efo/LCM_Enabler/interfaces"
	u "1935/efo/LCM_Enabler/nodes/utils"
	"fmt"
)

type InputFormat struct {
	Value float64
}

type OutputFormat struct {
	Result bool
}

type Property struct {
	Operation string
}

var Description = i.NodeDescription{
	NodeType:      "Logic",
	NumInputPort:  2,
	NumOutputPort: 2,
}

type Logic struct {
	i.FunctionNode
}

func (n *Logic) Execute(input []any, info i.ExecInfo) i.NodeExecResult {
	var property Property
	u.GetProperty(n.Config.Property, &property)

	var newInput []InputFormat = make([]InputFormat, len(input))
	for i := 0; i < len(input); i++ {
		err := u.DecodeInput(input[i], &newInput[i])
		if err != nil {
			fmt.Println("Logic: Get input error,", err)
		}
	}

	var output OutputFormat
	var port []int

	switch property.Operation {
	case "==":
		output.Result = newInput[0].Value == newInput[1].Value
	case "!=":
		output.Result = newInput[0].Value != newInput[1].Value
	case ">":
		output.Result = newInput[0].Value > newInput[1].Value
	case "<":
		output.Result = newInput[0].Value < newInput[1].Value
	case ">=":
		output.Result = newInput[0].Value >= newInput[1].Value
	case "<=":
		output.Result = newInput[0].Value <= newInput[1].Value
	default:
		fmt.Println("Logic: Invalid operation")
	}

	if output.Result {
		port = []int{0}
	} else {
		port = []int{1}
	}

	encoded_output, err := u.EncodeOutput(output, Description)
	if err != nil {
		fmt.Println("Logic: Encode output error,", err)
	}

	return i.NodeExecResult{
		Output: encoded_output,
		Ports:  port,
	}
}
