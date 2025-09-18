package If

import (
	i "1935/efo/LCM_Enabler/interfaces"
	u "1935/efo/LCM_Enabler/nodes/utils"
	"fmt"
)

type InputFormat struct {
	Value float64
}

type OutputFormat struct {
	Value  float64
	Result bool
}

type Property struct {
	Condition string
	Value     float64
}

var Description = i.NodeDescription{
	NodeType:      "If",
	NumInputPort:  1,
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
		err := u.DecodeInput(input[i], &inputarr[i])
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

	encoded_output, err := u.EncodeOutput(output, Description)
	if err != nil {
		fmt.Println("If: Encode output error,", err)
	}

	return i.NodeExecResult{
		Output: encoded_output,
		Ports:  port,
	}
}
