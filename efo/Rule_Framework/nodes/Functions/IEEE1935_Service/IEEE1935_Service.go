package IEEE1935_Service

import (
	"fmt"

	i "1935/efo/Rule_Framework/interfaces"
	h "1935/efo/Rule_Framework/nodes/Functions/httpnode"
	u "1935/efo/Rule_Framework/nodes/utils"
)

type InputFormat struct {
}

type OutputFormat struct {
	Status_code int
	Response    string
}

type Property struct {
	Request            string   `yaml:"request"`
	Cluster_name       string   `yaml:"cluster_name"`
	App_name           string   `yaml:"app_name"`
	Selected_nodes     []string `yaml:"selected_nodes"`
	Allocated_cpu      string   `yaml:"allocated_cpu"`
	Allocated_memory   string   `yaml:"allocated_memory"`
	Allocated_replicas string   `yaml:"allocated_replicas"`
}

var Description = i.NodeDescription{
	NodeType:      "IEEE1935_Service",
	NumInputPort:  1,
	NumOutputPort: 1,
}

type IEEE1935_Service struct {
	i.FunctionNode
}

func (n *IEEE1935_Service) Execute(input []any, info i.ExecInfo) i.NodeExecResult {
	var property Property
	u.DecodeProperty(Description.NodeType, n.Config, input, &property)
	fmt.Println("IEEE1935_Service input:", property)

	var result h.Result
	switch property.Request {
	case "List_Service":
		result = h.Get(fmt.Sprintf("%s/api/clusters/%s/services", info.ControlPlaneBaseURL, property.Cluster_name))
	case "Get_Service":
		result = h.Get(fmt.Sprintf("%s/api/clusters/%s/services/%s", info.ControlPlaneBaseURL, property.Cluster_name, property.App_name))
	case "Instantiate_Service":
		result = h.PostJSON(fmt.Sprintf("%s/api/clusters/%s/services", info.ControlPlaneBaseURL, property.Cluster_name), map[string]interface{}{
			"App_Name":       property.App_name,
			"Selected_Nodes": property.Selected_nodes,
		})
	case "Service_Scaling":
		result = h.PatchJSON(fmt.Sprintf("%s/api/clusters/%s/services/%s", info.ControlPlaneBaseURL, property.Cluster_name, property.App_name), map[string]string{
			"CPU":      property.Allocated_cpu,
			"Memory":   property.Allocated_memory,
			"Replicas": property.Allocated_replicas,
		})
	case "Terminate_Service":
		result = h.Delete(fmt.Sprintf("%s/api/clusters/%s/services/%s", info.ControlPlaneBaseURL, property.Cluster_name, property.App_name))
	default:
		result = h.UnsupportedRequest()
	}

	output := OutputFormat{Status_code: result.StatusCode, Response: result.Body}
	fmt.Println("IEEE1935_Service output:", output)
	encoded_output, err := u.EncodeOutput(output, Description)
	if err != nil {
		fmt.Println("IEEE1935_Service: Encode output error,", err)
	}
	return i.NodeExecResult{
		Output: encoded_output,
	}
}
