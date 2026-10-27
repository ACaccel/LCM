package IEEE1935_Application

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
	Request         string   `yaml:"request"`
	App_Name        string   `yaml:"app_name"`
	Yaml_File       string   `yaml:"yaml_file"`
	Manifest_Number string   `yaml:"manifest_number"`
	Manifest        []string `yaml:"manifest"`
}

type OutputFormat struct {
	Status_code int
	Response    string
}

var Description = i.NodeDescription{
	NodeType:      "IEEE1935_Application",
	NumInputPort:  1,
	NumOutputPort: 1,
}

type IEEE1935_Application struct {
	i.FunctionNode
}

func (n *IEEE1935_Application) Execute(input []any, info i.ExecInfo) i.NodeExecResult {
	var property Property
	u.DecodeProperty(Description.NodeType, n.Config, input, &property)

	var result h.Result
	yaml_path := info.File_Path + "/" + property.Yaml_File

	switch property.Request {
	case "List_App":
		result = h.Get(fmt.Sprintf("%s/api/apps", info.ControlPlaneBaseURL))
	case "Get_App":
		result = h.Get(fmt.Sprintf("%s/api/apps/%s", info.ControlPlaneBaseURL, property.App_Name))
	case "Design_App":
		var b bytes.Buffer
		w := multipart.NewWriter(&b)

		file, err := os.Open(yaml_path)
		if err != nil {
			result = h.Result{StatusCode: 500, Body: fmt.Sprintf("Error opening YAML file: %v", err)}
			break
		}
		defer file.Close()

		fw, err := w.CreateFormFile("file", property.Yaml_File)
		if err != nil {
			result = h.Result{StatusCode: 500, Body: fmt.Sprintf("Error creating form file: %v", err)}
			break
		}
		if _, err = io.Copy(fw, file); err != nil {
			result = h.Result{StatusCode: 500, Body: fmt.Sprintf("Error copying file content: %v", err)}
			break
		}

		w.WriteField("app_Name", property.App_Name)
		w.WriteField("manifest_Number", property.Manifest_Number)
		for i, m := range property.Manifest {
			w.WriteField(fmt.Sprintf("manifest%d", i+1), m)
		}
		w.Close()
		req, err := http.NewRequest("POST", fmt.Sprintf("%s/api/apps", info.ControlPlaneBaseURL), &b)
		if err != nil {
			result = h.Result{StatusCode: 500, Body: fmt.Sprintf("Error creating request: %v", err)}
			break
		}
		req.Header.Set("Content-Type", w.FormDataContentType())
		result = h.DoRequest(req)
	case "Update_App":
		var b bytes.Buffer
		w := multipart.NewWriter(&b)

		file, err := os.Open(yaml_path)
		if err != nil {
			result = h.Result{StatusCode: 500, Body: fmt.Sprintf("Error opening YAML file: %v", err)}
			break
		}
		defer file.Close()

		fw, err := w.CreateFormFile("file", property.Yaml_File)
		if err != nil {
			result = h.Result{StatusCode: 500, Body: fmt.Sprintf("Error creating form file: %v", err)}
			break
		}
		if _, err = io.Copy(fw, file); err != nil {
			result = h.Result{StatusCode: 500, Body: fmt.Sprintf("Error copying file content: %v", err)}
			break
		}

		w.WriteField("manifest_Number", property.Manifest_Number)
		for i, m := range property.Manifest {
			w.WriteField(fmt.Sprintf("manifest%d", i+1), m)
		}
		w.Close()
		req, err := http.NewRequest("PUT", fmt.Sprintf("%s/api/apps/%s", info.ControlPlaneBaseURL, property.App_Name), &b)
		if err != nil {
			result = h.Result{StatusCode: 500, Body: fmt.Sprintf("Error creating request: %v", err)}
			break
		}
		req.Header.Set("Content-Type", w.FormDataContentType())
		result = h.DoRequest(req)
	case "Remove_App":
		result = h.Delete(fmt.Sprintf("%s/api/apps/%s", info.ControlPlaneBaseURL, property.App_Name))
	default:
		result = h.UnsupportedRequest()
	}

	output := OutputFormat{Status_code: result.StatusCode, Response: result.Body}
	fmt.Println("IEEE1935_Application output:", output)
	encoded_output, err := u.EncodeOutput(output, Description)
	if err != nil {
		fmt.Println("IEEE1935_Application: Encode output error,", err)
	}
	return i.NodeExecResult{
		Output: encoded_output,
	}
}
