package IEEE1935_Deployment

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"net/http"

	i "1935/efo/LCM_Enabler/interfaces"
	u "1935/efo/LCM_Enabler/nodes/utils"
)

type Property struct {
	Request            string `yaml:"request"`
	Cluster_name       string `yaml:"cluster_name"`
	App_name           string `yaml:"app_name"`
	Allocated_CPU      string `yaml:"allocated_cpu"`
	Allocated_Memory   string `yaml:"allocated_memory"`
	Replicas string `yaml:"replicas"`
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
	err := u.GetProperty(n.Config.Property, &property)
	if err != nil {
		fmt.Println("IEEE1935_Deployment: Get property error,", err)
	}

	client := &http.Client{}
	var res *http.Response
	var output OutputFormat
	var is_error bool = false

	switch property.Request {
	case "List_Deployment":
		res, err = client.Get(fmt.Sprintf("http://localhost:8000/api/clusters/%s/deployments", property.Cluster_name))
	case "Get_Deployment":
		res, err = client.Get(fmt.Sprintf("http://localhost:8000/api/clusters/%s/deployments/%s", property.Cluster_name, property.App_name))
	case "Deploy_Deployment":
		// Build payload using encoding/json and bytes
		payload := map[string]string{
			"App_Name":         property.App_name,
			"Allocated_CPU":    property.Allocated_CPU,
			"Allocated_Memory": property.Allocated_Memory,
			"Replicas":         property.Replicas,
		}
		b, errMarshal := json.Marshal(payload)
		if errMarshal != nil {
			output = OutputFormat{Status_code: 500, Response: fmt.Sprintf("JSON marshal error: %v", errMarshal)}
			is_error = true
			break
		}
		res, err = client.Post(
			fmt.Sprintf("http://localhost:8000/api/clusters/%s/deployments", property.Cluster_name),
			"application/json",
			bytes.NewBuffer(b),
		)
	case "Update_Deployment":
		// Build payload using encoding/json and bytes
		payload := map[string]string{
			"Allocated_CPU":    property.Allocated_CPU,
			"Allocated_Memory": property.Allocated_Memory,
			"Replicas":         property.Replicas,
		}
		b, errMarshal := json.Marshal(payload)
		if errMarshal != nil {
			output = OutputFormat{Status_code: 500, Response: fmt.Sprintf("JSON marshal error: %v", errMarshal)}
			is_error = true
			break
		}
		req, _ := http.NewRequest(http.MethodPut,
			fmt.Sprintf("http://localhost:8000/api/clusters/%s/deployments/%s", property.Cluster_name, property.App_name),
			bytes.NewBuffer(b),
		)
		req.Header.Set("Content-Type", "application/json")
		res, err = client.Do(req)
	case "Remove_Deployment":
		req, _ := http.NewRequest(http.MethodDelete,
			fmt.Sprintf("http://localhost:8000/api/clusters/%s/deployments/%s", property.Cluster_name, property.App_name),
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

	fmt.Println("IEEE1935_Deployment output:", output)

	encoded_output, err := u.EncodeOutput(output, Description)
	if err != nil {
		fmt.Println("IEEE1935_Deployment: Encode output error,", err)
	}
	return i.NodeExecResult{
		Output: encoded_output,
	}
}
