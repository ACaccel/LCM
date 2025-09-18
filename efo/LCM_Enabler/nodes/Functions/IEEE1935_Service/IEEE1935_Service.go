package IEEE1935_Service

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"net/http"

	i "1935/efo/LCM_Enabler/interfaces"
	u "1935/efo/LCM_Enabler/nodes/utils"
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
	err := u.GetProperty(n.Config.Property, &property)
	if err != nil {
		fmt.Println("IEEE1935_Service: Get property error,", err)
	}
	fmt.Println("IEEE1935_Service input:", property)

	client := &http.Client{}
	var res *http.Response
	var output OutputFormat
	var is_error bool = false

	switch property.Request {
	case "List_Service":
		res, err = client.Get(fmt.Sprintf("http://localhost:8000/api/clusters/%s/services", property.Cluster_name))
	case "Get_Service":
		res, err = client.Get(fmt.Sprintf("http://localhost:8000/api/clusters/%s/services/%s", property.Cluster_name, property.App_name))
	case "Instantiate_Service":
		// Build payload using encoding/json and bytes
		payload := map[string]interface{}{
			"App_Name":       property.App_name,
			"Selected_Nodes": property.Selected_nodes,
		}
		b, errMarshal := json.Marshal(payload)
		if errMarshal != nil {
			output = OutputFormat{Status_code: 500, Response: fmt.Sprintf("JSON marshal error: %v", errMarshal)}
			is_error = true
			break
		}
		res, err = client.Post(
			fmt.Sprintf("http://localhost:8000/api/clusters/%s/services", property.Cluster_name),
			"application/json",
			bytes.NewBuffer(b),
		)
	case "Service_Scaling":
		// Build payload using encoding/json and bytes
		payload := map[string]string{
			"CPU":      property.Allocated_cpu,
			"Memory":   property.Allocated_memory,
			"Replicas": property.Allocated_replicas,
		}
		b, errMarshal := json.Marshal(payload)
		if errMarshal != nil {
			output = OutputFormat{Status_code: 500, Response: fmt.Sprintf("JSON marshal error: %v", errMarshal)}
			is_error = true
			break
		}
		req, _ := http.NewRequest(
			http.MethodPatch,
			fmt.Sprintf("http://localhost:8000/api/clusters/%s/services/%s", property.Cluster_name, property.App_name),
			bytes.NewBuffer(b),
		)
		req.Header.Set("Content-Type", "application/json")
		res, err = client.Do(req)
	case "Terminate_Service":
		req, _ := http.NewRequest(
			http.MethodDelete,
			fmt.Sprintf("http://localhost:8000/api/clusters/%s/services/%s", property.Cluster_name, property.App_name),
			nil,
		)
		res, err = client.Do(req)
	default:
		output = OutputFormat{
			Status_code: 400,
			Response:    "Unsupported request",
		}
		is_error = true
	}
	if res != nil {
		defer res.Body.Close()
	}

	if err != nil {
		output = OutputFormat{
			Status_code: 500,
			Response:    fmt.Sprintf("Error: %v", err),
		}
		is_error = true
	}

	if !is_error && res != nil {
		body, _ := io.ReadAll(res.Body)
		output = OutputFormat{
			Status_code: res.StatusCode,
			Response:    string(body),
		}
	}
	fmt.Println("IEEE1935_Service output:", output)

	encoded_output, err := u.EncodeOutput(output, Description)
	if err != nil {
		fmt.Println("IEEE1935_Service: Encode output error,", err)
	}
	return i.NodeExecResult{
		Output: encoded_output,
	}
}
