package IEEE1935_AppStore

import (
	"fmt"

	i "1935/efo/Rule_Framework/interfaces"
	h "1935/efo/Rule_Framework/nodes/Functions/httpnode"
	u "1935/efo/Rule_Framework/nodes/utils"
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
	u.DecodeProperty(Description.NodeType, n.Config, input, &property)

	var result h.Result
	switch property.Request {
	case "Get_AppStore":
		result = h.Get(fmt.Sprintf("%s/api/appstore", info.ControlPlaneBaseURL))
	case "Create_AppStore":
		result = h.PostJSON(fmt.Sprintf("%s/api/appstore", info.ControlPlaneBaseURL), map[string]string{
			"On_Which_Machine": property.On_Which_Machine,
			"Network_IP":       property.Network_IP,
			"Network_Port":     property.Network_Port,
			"Login_Name":       property.Login_Name,
			"Login_Passwd":     property.Login_Passwd,
		})
	case "Remove_AppStore":
		result = h.Delete(fmt.Sprintf("%s/api/appstore", info.ControlPlaneBaseURL))
	default:
		result = h.UnsupportedRequest()
	}

	output := OutputFormat{Status_code: result.StatusCode, Response: result.Body}
	fmt.Println("IEEE1935_AppStore output:", output)
	encoded_output, err := u.EncodeOutput(output, Description)
	if err != nil {
		fmt.Println("IEEE1935_AppStore: Encode output error,", err)
	}
	return i.NodeExecResult{Output: encoded_output}
}
