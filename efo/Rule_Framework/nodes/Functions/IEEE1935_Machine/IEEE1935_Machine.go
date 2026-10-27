package IEEE1935_Machine

import (
	"fmt"

	i "1935/efo/Rule_Framework/interfaces"
	h "1935/efo/Rule_Framework/nodes/Functions/httpnode"
	u "1935/efo/Rule_Framework/nodes/utils"
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
	u.DecodeProperty(Description.NodeType, n.Config, input, &property)

	var result h.Result
	switch property.Request {
	case "List_Machine":
		result = h.Get(fmt.Sprintf("%s/api/machines", info.ControlPlaneBaseURL))
	case "Get_Machine":
		result = h.Get(fmt.Sprintf("%s/api/machines/%s", info.ControlPlaneBaseURL, property.Machine_Name))
	case "Create_VM":
		result = h.PostJSON(fmt.Sprintf("%s/api/vms", info.ControlPlaneBaseURL), map[string]interface{}{
			"Name":             property.Name,
			"CPU":              property.CPU,
			"Memory":           property.Memory,
			"On_Which_Machine": property.On_Which_Machine,
			"OvaPath":          property.OvaPath,
		})
	case "Join_Machine":
		result = h.PostJSON(fmt.Sprintf("%s/api/machines", info.ControlPlaneBaseURL), map[string]string{
			"Machine_Name": property.Machine_Name,
			"Login_Name":   property.Login_Name,
			"Login_Passwd": property.Login_Passwd,
			"Network_IP":   property.Network_IP,
			"Network_Port": property.Network_Port,
			"Area_Name":    property.Area_Name,
		})
	case "Delete_Machine":
		result = h.Delete(fmt.Sprintf("%s/api/machines/%s", info.ControlPlaneBaseURL, property.Machine_Name))
	default:
		result = h.UnsupportedRequest()
	}

	output := OutputFormat{Status_code: result.StatusCode, Response: result.Body}
	fmt.Println("IEEE1935_Machine output:", output)
	encoded_output, err := u.EncodeOutput(output, Description)
	if err != nil {
		fmt.Println("IEEE1935_Machine: Encode output error,", err)
	}
	return i.NodeExecResult{Output: encoded_output}
}
