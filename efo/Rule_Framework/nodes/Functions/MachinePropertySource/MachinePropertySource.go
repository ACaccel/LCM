/*
Package MachinePropertySource emits its own property values as a DataPacket.

It exists to exercise transFormat and fill_property_from_input end to end: a
downstream IEEE1935_Machine can be left with empty properties and have them
filled from this node's output.
*/
package MachinePropertySource

import (
	lcm "1935/efo/LCM_Enabler/interfaces"
	i "1935/efo/Rule_Framework/interfaces"
	u "1935/efo/Rule_Framework/nodes/utils"
)

// Property mirrors the Join_Machine-related properties of IEEE1935_Machine,
// with identical yaml tag keys, so the emitted packet looks exactly like what
// that node expects to be filled with.
type Property struct {
	Request      string `yaml:"request"`
	Machine_Name string `yaml:"machine_Name"`
	Login_Name   string `yaml:"login_name"`
	Login_Passwd string `yaml:"login_passwd"`
	Network_IP   string `yaml:"network_ip"`
	Network_Port string `yaml:"network_port"`
	Area_Name    string `yaml:"area_name"`
}

var Description = i.NodeDescription{
	NodeType:      "MachinePropertySource",
	NumInputPort:  1,
	NumOutputPort: 1,
}

type MachinePropertySource struct {
	i.FunctionNode
}

func (n *MachinePropertySource) Execute(input []any, info i.ExecInfo) i.NodeExecResult {
	var property Property
	u.DecodeProperty(Description.NodeType, n.Config, input, &property)

	// Keys must match the yaml tags of IEEE1935_Machine.Property, so a
	// downstream transFormat can address them as src: request and so on.
	out := lcm.DataPacketContent{
		"request":      property.Request,
		"machine_Name": property.Machine_Name,
		"login_name":   property.Login_Name,
		"login_passwd": property.Login_Passwd,
		"network_ip":   property.Network_IP,
		"network_port": property.Network_Port,
		"area_name":    property.Area_Name,
	}

	return i.NodeExecResult{
		Output: lcm.NodeOutput{out},
	}
}
