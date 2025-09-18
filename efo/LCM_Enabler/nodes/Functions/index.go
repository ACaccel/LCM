package Functions

import (
	"errors"
	
	i "1935/efo/LCM_Enabler/interfaces"

	// import your new written functions here
	"1935/efo/LCM_Enabler/nodes/Functions/If"
	"1935/efo/LCM_Enabler/nodes/Functions/Logic"
	"1935/efo/LCM_Enabler/nodes/Functions/IEEE1935_AppStore"
	"1935/efo/LCM_Enabler/nodes/Functions/IEEE1935_Application"
	"1935/efo/LCM_Enabler/nodes/Functions/IEEE1935_Cluster"
	"1935/efo/LCM_Enabler/nodes/Functions/IEEE1935_Deployment"
	"1935/efo/LCM_Enabler/nodes/Functions/IEEE1935_Image"
	"1935/efo/LCM_Enabler/nodes/Functions/IEEE1935_Machine"
	"1935/efo/LCM_Enabler/nodes/Functions/IEEE1935_Manifest"
	"1935/efo/LCM_Enabler/nodes/Functions/IEEE1935_Node"
	"1935/efo/LCM_Enabler/nodes/Functions/IEEE1935_Service"
)

type nodeFunc func(info i.FunctionNode) i.IPolicyNode

var Description = make(map[string]i.NodeDescription)
var Function = make(map[string]nodeFunc)

func init() {
	// add your new written functions' descriptions here
	Description = map[string]i.NodeDescription{
		"If":           		If.Description,
		"Logic":        		Logic.Description,
		"IEEE1935_AppStore":	IEEE1935_AppStore.Description,
		"IEEE1935_Application":	IEEE1935_Application.Description,
		"IEEE1935_Cluster":		IEEE1935_Cluster.Description,
		"IEEE1935_Deployment":	IEEE1935_Deployment.Description,
		"IEEE1935_Image":		IEEE1935_Image.Description,
		"IEEE1935_Machine":		IEEE1935_Machine.Description,
		"IEEE1935_Manifest":	IEEE1935_Manifest.Description,
		"IEEE1935_Node":		IEEE1935_Node.Description,
		"IEEE1935_Service":		IEEE1935_Service.Description,
	}

	// add your new written functions here
	Function = map[string]nodeFunc{
		"If":           		func(info i.FunctionNode) i.IPolicyNode { return &If.If{FunctionNode: info} },
		"Logic":        		func(info i.FunctionNode) i.IPolicyNode { return &Logic.Logic{FunctionNode: info} },
		"IEEE1935_AppStore":	func(info i.FunctionNode) i.IPolicyNode { return &IEEE1935_AppStore.IEEE1935_AppStore{FunctionNode: info} },
		"IEEE1935_Application":	func(info i.FunctionNode) i.IPolicyNode { return &IEEE1935_Application.IEEE1935_Application{FunctionNode: info} },
		"IEEE1935_Cluster":		func(info i.FunctionNode) i.IPolicyNode { return &IEEE1935_Cluster.IEEE1935_Cluster{FunctionNode: info} },
		"IEEE1935_Deployment":	func(info i.FunctionNode) i.IPolicyNode { return &IEEE1935_Deployment.IEEE1935_Deployment{FunctionNode: info} },
		"IEEE1935_Image":		func(info i.FunctionNode) i.IPolicyNode { return &IEEE1935_Image.IEEE1935_Image{FunctionNode: info} },
		"IEEE1935_Machine":		func(info i.FunctionNode) i.IPolicyNode { return &IEEE1935_Machine.IEEE1935_Machine{FunctionNode: info} },
		"IEEE1935_Manifest":	func(info i.FunctionNode) i.IPolicyNode { return &IEEE1935_Manifest.IEEE1935_Manifest{FunctionNode: info} },
		"IEEE1935_Node":		func(info i.FunctionNode) i.IPolicyNode { return &IEEE1935_Node.IEEE1935_Node{FunctionNode: info} },
		"IEEE1935_Service":		func(info i.FunctionNode) i.IPolicyNode { return &IEEE1935_Service.IEEE1935_Service{FunctionNode: info} },
	}
}

func InitNode(info i.FunctionNode) (*i.IPolicyNode, i.NumPort, error) {
	var node i.IPolicyNode
	var numPort i.NumPort

	if f, ok := Function[info.Description.NodeType]; ok {
		node = f(info)
	} else {
		return nil, numPort, errors.New("LCM: Function Node " + info.Description.NodeType + " not found")
	}

	numPort = i.NumPort{
		NumInput:  info.Description.NumInputPort,
		NumOutput: info.Description.NumOutputPort,
	}

	return &node, numPort, nil
}
