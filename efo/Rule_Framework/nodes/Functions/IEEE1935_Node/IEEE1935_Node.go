package IEEE1935_Node

import (
	"fmt"

	i "1935/efo/Rule_Framework/interfaces"
	h "1935/efo/Rule_Framework/nodes/Functions/httpnode"
	u "1935/efo/Rule_Framework/nodes/utils"
)

type Property struct {
	Request      string `yaml:"request"`
	Cluster_Name string `yaml:"cluster_Name"`
	Node_Name    string `yaml:"node_Name"`
	Machine_Name string `yaml:"machine_Name"`
}

type OutputFormat struct {
	Status_code int
	Response    string
}

var Description = i.NodeDescription{
	NodeType:      "IEEE1935_Node",
	NumInputPort:  1,
	NumOutputPort: 1,
}

type IEEE1935_Node struct {
	i.FunctionNode
}

func (n *IEEE1935_Node) Execute(input []any, info i.ExecInfo) i.NodeExecResult {
	var property Property
	u.DecodeProperty(Description.NodeType, n.Config, input, &property)

	var result h.Result
	switch property.Request {
	case "List_Node":
		result = h.Get(fmt.Sprintf("%s/api/clusters/%s/nodes", info.ControlPlaneBaseURL, property.Cluster_Name))
	case "Get_Node":
		result = h.Get(fmt.Sprintf("%s/api/clusters/%s/nodes/%s", info.ControlPlaneBaseURL, property.Cluster_Name, property.Node_Name))
	case "Join_Node":
		result = h.PostJSON(fmt.Sprintf("%s/api/clusters/%s/nodes", info.ControlPlaneBaseURL, property.Cluster_Name), map[string]string{
			"Machine_Name": property.Machine_Name,
			"Node_Name":    property.Node_Name,
		})
	case "Delete_Node":
		result = h.Delete(fmt.Sprintf("%s/api/clusters/%s/nodes/%s", info.ControlPlaneBaseURL, property.Cluster_Name, property.Node_Name))
	default:
		result = h.UnsupportedRequest()
	}

	output := OutputFormat{Status_code: result.StatusCode, Response: result.Body}
	fmt.Println("IEEE1935_Node output:", output)
	encoded_output, err := u.EncodeOutput(output, Description)
	if err != nil {
		fmt.Println("IEEE1935_Node: Encode output error,", err)
	}
	return i.NodeExecResult{Output: encoded_output}
}
