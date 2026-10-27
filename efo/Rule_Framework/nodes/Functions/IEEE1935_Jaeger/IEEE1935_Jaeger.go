package IEEE1935_Jaeger

import (
	"fmt"

	i "1935/efo/Rule_Framework/interfaces"
	h "1935/efo/Rule_Framework/nodes/Functions/httpnode"
	u "1935/efo/Rule_Framework/nodes/utils"
)

type Property struct {
	URL     string `yaml:"url"`
	Request string `yaml:"request"`
}

type OutputFormat struct {
	Status_code int
	Response    string
}

var Description = i.NodeDescription{
	NodeType:      "IEEE1935_Jaeger",
	NumInputPort:  1,
	NumOutputPort: 1,
}

type IEEE1935_Jaeger struct {
	i.FunctionNode
}

func (n *IEEE1935_Jaeger) Execute(input []any, info i.ExecInfo) i.NodeExecResult {
	var property Property
	u.DecodeProperty(Description.NodeType, n.Config, input, &property)

	result := h.Get(fmt.Sprintf("%s/%s", property.URL, property.Request))

	output := OutputFormat{Status_code: result.StatusCode, Response: result.Body}
	fmt.Println("IEEE1935_Jaeger output:", output)
	encoded_output, err := u.EncodeOutput(output, Description)
	if err != nil {
		fmt.Println("IEEE1935_Jaeger: Encode output error,", err)
	}
	return i.NodeExecResult{Output: encoded_output}
}
