package IEEE1935_Image

import (
	"fmt"

	i "1935/efo/Rule_Framework/interfaces"
	h "1935/efo/Rule_Framework/nodes/Functions/httpnode"
	u "1935/efo/Rule_Framework/nodes/utils"
)

type Property struct {
	Request       string `yaml:"request"`
	Cluster_Name  string `yaml:"cluster_Name"`
	Manifest_Name string `yaml:"manifest_name"`
	Image_Name    string `yaml:"image_name"`
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
	u.DecodeProperty(Description.NodeType, n.Config, input, &property)

	var result h.Result
	switch property.Request {
	case "List_Image":
		result = h.Get(fmt.Sprintf("%s/api/images", info.ControlPlaneBaseURL))
	case "List_Image_InCluster":
		result = h.Get(fmt.Sprintf("%s/api/clusters/%s/images", info.ControlPlaneBaseURL, property.Cluster_Name))
	case "Build_Image":
		result = h.PostJSON(fmt.Sprintf("%s/api/images", info.ControlPlaneBaseURL), map[string]string{
			"File_Name": property.Manifest_Name,
		})
	case "Remove_Image":
		result = h.Delete(fmt.Sprintf("%s/api/images/%s", info.ControlPlaneBaseURL, property.Image_Name))
	default:
		result = h.UnsupportedRequest()
	}

	output := OutputFormat{Status_code: result.StatusCode, Response: result.Body}
	fmt.Println("IEEE1935_Image output:", output)
	encoded_output, err := u.EncodeOutput(output, Description)
	if err != nil {
		fmt.Println("IEEE1935_Image: Encode output error,", err)
	}
	return i.NodeExecResult{
		Output: encoded_output,
	}
}
