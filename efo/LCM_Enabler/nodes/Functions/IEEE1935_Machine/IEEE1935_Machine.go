package IEEE1935_Machine

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
	Machine_Name string `yaml:"machine_Name"`
	// For Create_VM
	Name             string `yaml:"name"`
	CPU              int    `yaml:"cpu"`
	Memory           int    `yaml:"memory"`
	On_Which_Machine string `yaml:"on_which_machine"`
	OvaPath          string `yaml:"ovaPath"`
	// For Join_Machine
	Login_Name   string `yaml:"login_name"`
	Login_Passwd string `yaml:"login_passwd"`
	Network_IP   string `yaml:"network_ip"`
	Network_Port string `yaml:"network_port"`
	Area_Name    string `yaml:"area_name"`
}

type OutputFormat struct {
	Status_code int
	Response    string
}

var Description = i.NodeDescription{
	NodeType:      "IEEE1935_Machine",
	NumInputPort:  1,
	NumOutputPort: 1,
}

type IEEE1935_Machine struct {
	i.FunctionNode
}

func (n *IEEE1935_Machine) Execute(input []any, info i.ExecInfo) i.NodeExecResult {
	var property Property
	err := u.GetProperty(n.Config.Property, &property)
	if err != nil {
		fmt.Println("IEEE1935_Machine: Get property error,", err)
	}

	client := &http.Client{}
	var res *http.Response
	var output OutputFormat
	is_error := false

	switch property.Request {
	case "List_Machine":
		res, err = client.Get("http://localhost:8000/api/machines")
	case "Get_Machine":
		res, err = client.Get(fmt.Sprintf("http://localhost:8000/api/machines/%s", property.Machine_Name))
	case "Create_VM":
		// Build payload using encoding/json and bytes
		payload := map[string]interface{}{
			"Name":             property.Name,
			"CPU":              property.CPU,
			"Memory":           property.Memory,
			"On_Which_Machine": property.On_Which_Machine,
			"OvaPath":          property.OvaPath,
		}
		b, errMarshal := json.Marshal(payload)
		if errMarshal != nil {
			output = OutputFormat{Status_code: 500, Response: fmt.Sprintf("JSON marshal error: %v", errMarshal)}
			is_error = true
			break
		}
		res, err = client.Post("http://localhost:8000/api/vms", "application/json", bytes.NewBuffer(b))
	case "Join_Machine":
		// Build payload using encoding/json and bytes
		payload := map[string]string{
			"Machine_Name": property.Machine_Name,
			"Login_Name":   property.Login_Name,
			"Login_Passwd": property.Login_Passwd,
			"Network_IP":   property.Network_IP,
			"Network_Port": property.Network_Port,
			"Area_Name":    property.Area_Name,
		}
		b, errMarshal := json.Marshal(payload)
		if errMarshal != nil {
			output = OutputFormat{Status_code: 500, Response: fmt.Sprintf("JSON marshal error: %v", errMarshal)}
			is_error = true
			break
		}
		req, _ := http.NewRequest("POST", "http://localhost:8000/api/machines", bytes.NewBuffer(b))
		req.Header.Set("Content-Type", "application/json")
		res, err = client.Do(req)
	case "Delete_Machine":
		req, _ := http.NewRequest("DELETE", fmt.Sprintf("http://localhost:8000/api/machines/%s", property.Machine_Name), nil)
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
	fmt.Println("IEEE1935_Machine output:", output)
	encoded_output, err := u.EncodeOutput(output, Description)
	if err != nil {
		fmt.Println("IEEE1935_Machine: Encode output error,", err)
	}
	return i.NodeExecResult{Output: encoded_output}
}
