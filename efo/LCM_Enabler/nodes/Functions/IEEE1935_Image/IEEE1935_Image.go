package IEEE1935_Image

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
	Manifest_Name    string `yaml:"manifest_name"`
	Image_Name   string `yaml:"image_name"`
}

type OutputFormat struct {
	Status_code int
	Response    string
}

var Description = i.NodeDescription{
	NodeType:      "IEEE1935_Image",
	NumInputPort:  1,
	NumOutputPort: 1,
}

type IEEE1935_Image struct {
	i.FunctionNode
}

func (n *IEEE1935_Image) Execute(input []any, info i.ExecInfo) i.NodeExecResult {
	var property Property
	err := u.GetProperty(n.Config.Property, &property)
	if err != nil {
		fmt.Println("IEEE1935_Image: Get property error,", err)
	}

	client := &http.Client{}
	var res *http.Response
	var output OutputFormat
	var is_error bool = false

	switch property.Request {
	case "List_Image":
		res, err = client.Get("http://localhost:8000/api/images")
	case "List_Image_InCluster":
		res, err = client.Get(fmt.Sprintf("http://localhost:8000/api/clusters/%s/images", property.Cluster_Name))
	case "Build_Image":
		// Build payload using encoding/json and bytes
		payload := map[string]string{
			"File_Name": property.Manifest_Name,
		}
		b, errMarshal := json.Marshal(payload)
		if errMarshal != nil {
			output = OutputFormat{Status_code: 500, Response: fmt.Sprintf("JSON marshal error: %v", errMarshal)}
			is_error = true
			break
		}
		res, err = client.Post("http://localhost:8000/api/images", "application/json", bytes.NewBuffer(b))
	case "Remove_Image":
		req, _ := http.NewRequest("DELETE", fmt.Sprintf("http://localhost:8000/api/images/%s", property.Image_Name), nil)
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
	fmt.Println("IEEE1935_Image output:", output)
	encoded_output, err := u.EncodeOutput(output, Description)
	if err != nil {
		fmt.Println("IEEE1935_Image: Encode output error,", err)
	}
	return i.NodeExecResult{
		Output: encoded_output,
	}
}
