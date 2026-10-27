package IEEE1935_Cluster

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
	OvaPath      string `yaml:"ovaPath"`
}

type OutputFormat struct {
	Status_code int
	Response    string
}

var Description = i.NodeDescription{
	NodeType:      "IEEE1935_Cluster",
	NumInputPort:  1,
	NumOutputPort: 1,
}

type IEEE1935_Cluster struct {
	i.FunctionNode
}

func (n *IEEE1935_Cluster) Execute(input []any, info i.ExecInfo) i.NodeExecResult {
	var property Property
	u.DecodeProperty(Description.NodeType, n.Config, input, &property)

	var result h.Result
	switch property.Request {
	case "List_Cluster":
		result = h.Get(fmt.Sprintf("%s/api/clusters", info.ControlPlaneBaseURL))
	case "Get_Cluster":
		result = h.Get(fmt.Sprintf("%s/api/clusters/%s", info.ControlPlaneBaseURL, property.Cluster_Name))
	case "Create_Cluster":
		result = h.PostJSON(fmt.Sprintf("%s/api/clusters", info.ControlPlaneBaseURL), map[string]string{
			"Cluster_Name": property.Cluster_Name,
			"Node_Name":    property.Node_Name,
			"Machine_Name": property.Machine_Name,
			"OvaPath":      property.OvaPath,
		})
	case "Get_Cluster_join_cmd":
		result = h.Get(fmt.Sprintf("%s/api/clusters/%s/join_cmd", info.ControlPlaneBaseURL, property.Cluster_Name))
	case "Delete_Cluster":
		result = h.Delete(fmt.Sprintf("%s/api/clusters/%s", info.ControlPlaneBaseURL, property.Cluster_Name))
	default:
		result = h.UnsupportedRequest()
	}

	output := OutputFormat{Status_code: result.StatusCode, Response: result.Body}
	fmt.Println("IEEE1935_Cluster output:", output)
	encoded_output, err := u.EncodeOutput(output, Description)
	if err != nil {
		fmt.Println("IEEE1935_Cluster: Encode output error,", err)
	}
	return i.NodeExecResult{Output: encoded_output}
}
