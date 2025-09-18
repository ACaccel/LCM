package IEEE1935_Cluster

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"net/http"

	// ...existing imports...

	i "1935/efo/LCM_Enabler/interfaces"
	u "1935/efo/LCM_Enabler/nodes/utils"
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
	err := u.GetProperty(n.Config.Property, &property)
	if err != nil {
		fmt.Println("IEEE1935_Cluster: Get property error,", err)
	}

	client := &http.Client{}
	var res *http.Response
	var output OutputFormat
	is_error := false

	switch property.Request {
	case "List_Cluster":
		res, err = client.Get("http://localhost:8000/api/clusters")
	case "Get_Cluster":
		res, err = client.Get(fmt.Sprintf("http://localhost:8000/api/clusters/%s", property.Cluster_Name))
	case "Create_Cluster":
		// Build payload using encoding/json and bytes
		payload := map[string]string{
			"Cluster_Name": property.Cluster_Name,
			"Node_Name":    property.Node_Name,
			"Machine_Name": property.Machine_Name,
			"OvaPath":      property.OvaPath,
		}
		b, errMarshal := json.Marshal(payload)
		if errMarshal != nil {
			output = OutputFormat{Status_code: 500, Response: fmt.Sprintf("JSON marshal error: %v", errMarshal)}
			is_error = true
			break
		}
		res, err = client.Post("http://localhost:8000/api/clusters", "application/json", bytes.NewBuffer(b))
	case "Get_Cluster_join_cmd":
		res, err = client.Get(fmt.Sprintf("http://localhost:8000/api/clusters/%s/join_cmd", property.Cluster_Name))
	case "Delete_Cluster":
		req, _ := http.NewRequest("DELETE", fmt.Sprintf("http://localhost:8000/api/clusters/%s", property.Cluster_Name), nil)
		res, err = client.Do(req)
	default:
		output = OutputFormat{Status_code: 400, Response: "Unsupported request"}
		is_error = true
	}

	// ...existing code...
	if res != nil {
		defer res.Body.Close()
	}
	if err != nil {
		output = OutputFormat{Status_code: 500, Response: fmt.Sprintf("Error: %v", err)}
		is_error = true
	}
	if !is_error && res != nil {
		body, _ := io.ReadAll(res.Body)
		output = OutputFormat{Status_code: res.StatusCode, Response: string(body)}
	}
	fmt.Println("IEEE1935_Cluster output:", output)
	encoded_output, err := u.EncodeOutput(output, Description)
	if err != nil {
		fmt.Println("IEEE1935_Cluster: Encode output error,", err)
	}
	return i.NodeExecResult{Output: encoded_output}
}
