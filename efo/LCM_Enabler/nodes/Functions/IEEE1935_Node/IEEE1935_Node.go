package IEEE1935_Node

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
	err := u.GetProperty(n.Config.Property, &property)
	if err != nil {
		fmt.Println("IEEE1935_Node: Get property error,", err)
	}

	client := &http.Client{}
	var res *http.Response
	var output OutputFormat
	is_error := false

	switch property.Request {
	case "List_Node":
		res, err = client.Get(fmt.Sprintf("http://localhost:8000/api/clusters/%s/nodes", property.Cluster_Name))
	case "Get_Node":
		res, err = client.Get(fmt.Sprintf("http://localhost:8000/api/clusters/%s/nodes/%s", property.Cluster_Name, property.Node_Name))
	case "Join_Node":
		payload := map[string]string{
			"Machine_Name": property.Machine_Name,
			"Node_Name":    property.Node_Name,
		}
		b, errMarshal := json.Marshal(payload)
		if errMarshal != nil {
			output = OutputFormat{Status_code: 500, Response: fmt.Sprintf("JSON marshal error: %v", errMarshal)}
			is_error = true
			break
		}
		req, _ := http.NewRequest("POST", fmt.Sprintf("http://localhost:8000/api/clusters/%s/nodes", property.Cluster_Name), bytes.NewBuffer(b))
		req.Header.Set("Content-Type", "application/json")
		res, err = client.Do(req)
	case "Delete_Node":
		req, _ := http.NewRequest("DELETE", fmt.Sprintf("http://localhost:8000/api/clusters/%s/nodes/%s", property.Cluster_Name, property.Node_Name), nil)
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
	fmt.Println("IEEE1935_Node output:", output)
	encoded_output, err := u.EncodeOutput(output, Description)
	if err != nil {
		fmt.Println("IEEE1935_Node: Encode output error,", err)
	}
	return i.NodeExecResult{Output: encoded_output}
}
