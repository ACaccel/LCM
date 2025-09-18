package IEEE1935_Manifest

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
	err := u.GetProperty(n.Config.Property, &property)
	if err != nil {
		fmt.Println("IEEE1935_Manifest: Get property error,", err)
	}

	client := &http.Client{}
	var res *http.Response
	var output OutputFormat
	var is_error bool
	manifest_path := info.File_Path + "/" + property.Manifest_File

	switch property.Request {
	case "List_Manifest":
		res, err = client.Get("http://localhost:8000/api/manifests")
	case "Get_Manifest":
		res, err = client.Get(fmt.Sprintf("http://localhost:8000/api/manifests/%s", property.Manifest_File))
	case "Onboarded_Manifest":
		var b bytes.Buffer
		w := multipart.NewWriter(&b)
		
		// Open the file
		file, err := os.Open(manifest_path)
		if err != nil {
			output = OutputFormat{
				Status_code: 500,
				Response:    fmt.Sprintf("Error opening manifest file: %v", err),
			}
			is_error = true
			break
		}
		defer file.Close()
		
		// Create form file field
		fw, err := w.CreateFormFile("file", property.Manifest_File)
		if err != nil {
			output = OutputFormat{
				Status_code: 500,
				Response:    fmt.Sprintf("Error creating form file: %v", err),
			}
			is_error = true
			break
		}
		
		// Copy file content to form field
		if _, err = io.Copy(fw, file); err != nil {
			output = OutputFormat{
				Status_code: 500,
				Response:    fmt.Sprintf("Error copying file content: %v", err),
			}
			is_error = true
			break
		}
		
		w.Close()
		req, err := http.NewRequest("POST", "http://localhost:8000/api/manifests", &b)
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
	case "Update_Manifest":
		var b bytes.Buffer
		w := multipart.NewWriter(&b)
		
		// Open the file
		file, err := os.Open(manifest_path)
		if err != nil {
			output = OutputFormat{
				Status_code: 500,
				Response:    fmt.Sprintf("Error opening manifest file: %v", err),
			}
			is_error = true
			break
		}
		defer file.Close()
		
		// Create form file field
		fw, err := w.CreateFormFile("file", property.Manifest_File)
		if err != nil {
			output = OutputFormat{
				Status_code: 500,
				Response:    fmt.Sprintf("Error creating form file: %v", err),
			}
			is_error = true
			break
		}
		
		// Copy file content to form field
		if _, err = io.Copy(fw, file); err != nil {
			output = OutputFormat{
				Status_code: 500,
				Response:    fmt.Sprintf("Error copying file content: %v", err),
			}
			is_error = true
			break
		}
		
		w.Close()
		req, err := http.NewRequest("PUT", fmt.Sprintf("http://localhost:8000/api/manifests/%s", property.Manifest_File), &b)
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
	case "Remove_Manifest":
		req, _ := http.NewRequest("DELETE", fmt.Sprintf("http://localhost:8000/api/manifests/%s", property.Manifest_File), nil)
		res, err = client.Do(req)
	default:
		output = OutputFormat{
			Status_code: 400,
			Response:    "Unsupported request",
		}
		is_error = true
	}

	// ...existing code...
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
	fmt.Println("IEEE1935_Manifest output:", output)
	encoded_output, err := u.EncodeOutput(output, Description)
	if err != nil {
		fmt.Println("IEEE1935_Manifest: Encode output error,", err)
	}
	return i.NodeExecResult{
		Output: encoded_output,
	}
}
