package IEEE1935_Application

import (
	"bytes"
	"fmt"
	"io"
	"mime/multipart"
	"net/http"
	"os"

	i "1935/efo/LCM_Enabler/interfaces"
	u "1935/efo/LCM_Enabler/nodes/utils"
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
	err := u.GetProperty(n.Config.Property, &property)
	if err != nil {
		fmt.Println("IEEE1935_Application: Get property error,", err)
	}

	client := &http.Client{}
	var res *http.Response
	var output OutputFormat
	var is_error bool = false
	yaml_path := info.File_Path + "/" + property.Yaml_File

	switch property.Request {
	case "List_App":
		res, err = client.Get("http://localhost:8000/api/apps")
	case "Get_App":
		res, err = client.Get(fmt.Sprintf("http://localhost:8000/api/apps/%s", property.App_Name))
	case "Design_App":
		var b bytes.Buffer
		w := multipart.NewWriter(&b)

		// Open YAML file
		file, err := os.Open(yaml_path)
		if err != nil {
			output = OutputFormat{
				Status_code: 500,
				Response:    fmt.Sprintf("Error opening YAML file: %v", err),
			}
			is_error = true
			break
		}
		defer file.Close()

		// Create form file field and copy file content
		fw, err := w.CreateFormFile("file", property.Yaml_File)
		if err != nil {
			output = OutputFormat{
				Status_code: 500,
				Response:    fmt.Sprintf("Error creating form file: %v", err),
			}
			is_error = true
			break
		}
		if _, err = io.Copy(fw, file); err != nil {
			output = OutputFormat{
				Status_code: 500,
				Response:    fmt.Sprintf("Error copying file content: %v", err),
			}
			is_error = true
			break
		}

		// Add other fields
		w.WriteField("app_Name", property.App_Name)
		w.WriteField("manifest_Number", property.Manifest_Number)
		for i, m := range property.Manifest {
			w.WriteField(fmt.Sprintf("manifest%d", i+1), m)
		}
		w.Close()
		req, err := http.NewRequest("POST", "http://localhost:8000/api/apps", &b)
		if err != nil {
			output = OutputFormat{
				Status_code: 500,
				Response:    fmt.Sprintf("Error creating request: %v", err),
			}
			is_error = true
			break
		}
		req.Header.Set("Content-Type", w.FormDataContentType())
		res, err = client.Do(req)
	case "Update_App":
		var b bytes.Buffer
		w := multipart.NewWriter(&b)

		// Open YAML file
		file, err := os.Open(yaml_path)
		if err != nil {
			output = OutputFormat{
				Status_code: 500,
				Response:    fmt.Sprintf("Error opening YAML file: %v", err),
			}
			is_error = true
			break
		}
		defer file.Close()

		// Create form file field and copy file content
		fw, err := w.CreateFormFile("file", property.Yaml_File)
		if err != nil {
			output = OutputFormat{
				Status_code: 500,
				Response:    fmt.Sprintf("Error creating form file: %v", err),
			}
			is_error = true
			break
		}
		if _, err = io.Copy(fw, file); err != nil {
			output = OutputFormat{
				Status_code: 500,
				Response:    fmt.Sprintf("Error copying file content: %v", err),
			}
			is_error = true
			break
		}

		// Add other fields
		w.WriteField("manifest_Number", property.Manifest_Number)
		for i, m := range property.Manifest {
			w.WriteField(fmt.Sprintf("manifest%d", i+1), m)
		}
		w.Close()
		req, err := http.NewRequest("PUT", fmt.Sprintf("http://localhost:8000/api/apps/%s", property.App_Name), &b)
		if err != nil {
			output = OutputFormat{
				Status_code: 500,
				Response:    fmt.Sprintf("Error creating request: %v", err),
			}
			is_error = true
			break
		}
		req.Header.Set("Content-Type", w.FormDataContentType())
		res, err = client.Do(req)
	case "Remove_App":
		req, _ := http.NewRequest("DELETE", fmt.Sprintf("http://localhost:8000/api/apps/%s", property.App_Name), nil)
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
	fmt.Println("IEEE1935_Application output:", output)
	encoded_output, err := u.EncodeOutput(output, Description)
	if err != nil {
		fmt.Println("IEEE1935_Application: Encode output error,", err)
	}
	return i.NodeExecResult{
		Output: encoded_output,
	}
}
