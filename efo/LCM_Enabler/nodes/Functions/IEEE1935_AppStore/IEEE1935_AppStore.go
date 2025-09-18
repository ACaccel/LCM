package IEEE1935_AppStore

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
	Request          string `yaml:"request"`
	On_Which_Machine string `yaml:"on_which_machine"`
	Network_IP       string `yaml:"network_ip"`
	Network_Port     string `yaml:"network_port"`
	Login_Name       string `yaml:"login_name"`
	Login_Passwd     string `yaml:"login_passwd"`
}

type OutputFormat struct {
	Status_code int
	Response    string
}

var Description = i.NodeDescription{
	NodeType:      "IEEE1935_AppStore",
	NumInputPort:  1,
	NumOutputPort: 1,
}

type IEEE1935_AppStore struct {
	i.FunctionNode
}

func (n *IEEE1935_AppStore) Execute(input []any, info i.ExecInfo) i.NodeExecResult {
	var property Property
	err := u.GetProperty(n.Config.Property, &property)
	if err != nil {
		fmt.Println("IEEE1935_AppStore: Get property error,", err)
	}

	client := &http.Client{}
	var res *http.Response
	var output OutputFormat
	is_error := false

	switch property.Request {
	case "Get_AppStore":
		res, err = client.Get("http://localhost:8000/api/appstore")
	case "Create_AppStore":
		payload := map[string]string{
			"On_Which_Machine": property.On_Which_Machine,
			"Network_IP":       property.Network_IP,
			"Network_Port":     property.Network_Port,
			"Login_Name":       property.Login_Name,
			"Login_Passwd":     property.Login_Passwd,
		}
		b, errMarshal := json.Marshal(payload)
		if errMarshal != nil {
			output = OutputFormat{Status_code: 500, Response: fmt.Sprintf("JSON marshal error: %v", errMarshal)}
			is_error = true
			break
		}
		req, _ := http.NewRequest("POST", "http://localhost:8000/api/appstore", bytes.NewBuffer(b))
		req.Header.Set("Content-Type", "application/json")
		res, err = client.Do(req)
	case "Remove_AppStore":
		req, _ := http.NewRequest("DELETE", "http://localhost:8000/api/appstore", nil)
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
	fmt.Println("IEEE1935_AppStore output:", output)
	encoded_output, err := u.EncodeOutput(output, Description)
	if err != nil {
		fmt.Println("IEEE1935_AppStore: Encode output error,", err)
	}
	return i.NodeExecResult{Output: encoded_output}
}
