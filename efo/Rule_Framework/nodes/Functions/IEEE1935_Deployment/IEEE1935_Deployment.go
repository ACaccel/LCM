package IEEE1935_Deployment

import (
	"fmt"

	i "1935/efo/Rule_Framework/interfaces"
	h "1935/efo/Rule_Framework/nodes/Functions/httpnode"
	u "1935/efo/Rule_Framework/nodes/utils"
)

type Property struct {
	Request          string `yaml:"request"`
	Cluster_name     string `yaml:"cluster_name"`
	App_name         string `yaml:"app_name"`
	Allocated_CPU    string `yaml:"allocated_cpu"`
	Allocated_Memory string `yaml:"allocated_memory"`
	Replicas         string `yaml:"replicas"`
}

type OutputFormat struct {
	Status_code int
	Response    string
}

var Description = i.NodeDescription{
	NodeType:      "IEEE1935_Deployment",
	NumInputPort:  1,
	NumOutputPort: 1,
}

type IEEE1935_Deployment struct {
	i.FunctionNode
}

func (n *IEEE1935_Deployment) Execute(input []any, info i.ExecInfo) i.NodeExecResult {
	var property Property
	u.DecodeProperty(Description.NodeType, n.Config, input, &property)

	var result h.Result
	switch property.Request {
	case "List_Deployment":
		result = h.Get(fmt.Sprintf("%s/api/clusters/%s/deployments", info.ControlPlaneBaseURL, property.Cluster_name))
	case "Get_Deployment":
		result = h.Get(fmt.Sprintf("%s/api/clusters/%s/deployments/%s", info.ControlPlaneBaseURL, property.Cluster_name, property.App_name))
	case "Deploy_Deployment":
		result = h.PostJSON(fmt.Sprintf("%s/api/clusters/%s/deployments", info.ControlPlaneBaseURL, property.Cluster_name), map[string]string{
			"App_Name":         property.App_name,
			"Allocated_CPU":    property.Allocated_CPU,
			"Allocated_Memory": property.Allocated_Memory,
			"Replicas":         property.Replicas,
		})
	case "Update_Deployment":
		result = h.PutJSON(fmt.Sprintf("%s/api/clusters/%s/deployments/%s", info.ControlPlaneBaseURL, property.Cluster_name, property.App_name), map[string]string{
			"Allocated_CPU":    property.Allocated_CPU,
			"Allocated_Memory": property.Allocated_Memory,
			"Replicas":         property.Replicas,
		})
	case "Remove_Deployment":
		result = h.Delete(fmt.Sprintf("%s/api/clusters/%s/deployments/%s", info.ControlPlaneBaseURL, property.Cluster_name, property.App_name))
	default:
		result = h.UnsupportedRequest()
	}

	output := OutputFormat{Status_code: result.StatusCode, Response: result.Body}
	fmt.Println("IEEE1935_Deployment output:", output)
	encoded_output, err := u.EncodeOutput(output, Description)
	if err != nil {
		fmt.Println("IEEE1935_Deployment: Encode output error,", err)
	}
	return i.NodeExecResult{
		Output: encoded_output,
	}
}
