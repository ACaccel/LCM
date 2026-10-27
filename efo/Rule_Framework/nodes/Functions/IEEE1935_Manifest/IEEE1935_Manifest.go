package IEEE1935_Manifest

import (
	"bytes"
	"fmt"
	"io"
	"mime/multipart"
	"net/http"
	"os"

	i "1935/efo/Rule_Framework/interfaces"
	h "1935/efo/Rule_Framework/nodes/Functions/httpnode"
	u "1935/efo/Rule_Framework/nodes/utils"
)

type Property struct {
	Request       string `yaml:"request"`
	Manifest_File string `yaml:"manifest_file"`
}

type OutputFormat struct {
	Status_code int
	Response    string
}

var Description = i.NodeDescription{
	NodeType:      "IEEE1935_Manifest",
	NumInputPort:  1,
	NumOutputPort: 1,
}

type IEEE1935_Manifest struct {
	i.FunctionNode
}

func (n *IEEE1935_Manifest) Execute(input []any, info i.ExecInfo) i.NodeExecResult {
	var property Property
	u.DecodeProperty(Description.NodeType, n.Config, input, &property)

	var result h.Result
	manifest_path := info.File_Path + "/" + property.Manifest_File

	switch property.Request {
	case "List_Manifest":
		result = h.Get(fmt.Sprintf("%s/api/manifests", info.ControlPlaneBaseURL))
	case "Get_Manifest":
		result = h.Get(fmt.Sprintf("%s/api/manifests/%s", info.ControlPlaneBaseURL, property.Manifest_File))
	case "Onboarded_Manifest":
		var b bytes.Buffer
		w := multipart.NewWriter(&b)

		file, err := os.Open(manifest_path)
		if err != nil {
			result = h.Result{StatusCode: 500, Body: fmt.Sprintf("Error opening manifest file: %v", err)}
			break
		}
		defer file.Close()

		fw, err := w.CreateFormFile("file", property.Manifest_File)
		if err != nil {
			result = h.Result{StatusCode: 500, Body: fmt.Sprintf("Error creating form file: %v", err)}
			break
		}

		if _, err = io.Copy(fw, file); err != nil {
			result = h.Result{StatusCode: 500, Body: fmt.Sprintf("Error copying file content: %v", err)}
			break
		}

		w.Close()
		req, err := http.NewRequest("POST", fmt.Sprintf("%s/api/manifests", info.ControlPlaneBaseURL), &b)
		if err != nil {
			result = h.Result{StatusCode: 500, Body: fmt.Sprintf("Error creating request: %v", err)}
			break
		}
		req.Header.Set("Content-Type", w.FormDataContentType())
		result = h.DoRequest(req)
	case "Update_Manifest":
		var b bytes.Buffer
		w := multipart.NewWriter(&b)

		file, err := os.Open(manifest_path)
		if err != nil {
			result = h.Result{StatusCode: 500, Body: fmt.Sprintf("Error opening manifest file: %v", err)}
			break
		}
		defer file.Close()

		fw, err := w.CreateFormFile("file", property.Manifest_File)
		if err != nil {
			result = h.Result{StatusCode: 500, Body: fmt.Sprintf("Error creating form file: %v", err)}
			break
		}

		if _, err = io.Copy(fw, file); err != nil {
			result = h.Result{StatusCode: 500, Body: fmt.Sprintf("Error copying file content: %v", err)}
			break
		}

		w.Close()
		req, err := http.NewRequest("PUT", fmt.Sprintf("%s/api/manifests/%s", info.ControlPlaneBaseURL, property.Manifest_File), &b)
		if err != nil {
			result = h.Result{StatusCode: 500, Body: fmt.Sprintf("Error creating request: %v", err)}
			break
		}
		req.Header.Set("Content-Type", w.FormDataContentType())
		result = h.DoRequest(req)
	case "Remove_Manifest":
		result = h.Delete(fmt.Sprintf("%s/api/manifests/%s", info.ControlPlaneBaseURL, property.Manifest_File))
	default:
		result = h.UnsupportedRequest()
	}

	output := OutputFormat{Status_code: result.StatusCode, Response: result.Body}
	fmt.Println("IEEE1935_Manifest output:", output)
	encoded_output, err := u.EncodeOutput(output, Description)
	if err != nil {
		fmt.Println("IEEE1935_Manifest: Encode output error,", err)
	}
	return i.NodeExecResult{
		Output: encoded_output,
	}
}
