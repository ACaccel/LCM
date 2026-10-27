package Functions

import (
	"errors"
	
	i "1935/efo/Rule_Framework/interfaces"

	// import your new written functions here
	"1935/efo/Rule_Framework/nodes/Functions/CALM_PlacementApply"
	"1935/efo/Rule_Framework/nodes/Functions/CALM_PolicyMode"
	"1935/efo/Rule_Framework/nodes/Functions/CALM_PolicySelector"
	"1935/efo/Rule_Framework/nodes/Functions/CALM_PostVerify"
	"1935/efo/Rule_Framework/nodes/Functions/CALM_Telemetry"
	"1935/efo/Rule_Framework/nodes/Functions/HTTPRequest"
	"1935/efo/Rule_Framework/nodes/Functions/If"
	"1935/efo/Rule_Framework/nodes/Functions/Compare"
	"1935/efo/Rule_Framework/nodes/Functions/Merge"
	"1935/efo/Rule_Framework/nodes/Functions/Wait"
	"1935/efo/Rule_Framework/nodes/Functions/IEEE1935_AppStore"
	"1935/efo/Rule_Framework/nodes/Functions/IEEE1935_Application"
	"1935/efo/Rule_Framework/nodes/Functions/IEEE1935_Cluster"
	"1935/efo/Rule_Framework/nodes/Functions/IEEE1935_Deployment"
	"1935/efo/Rule_Framework/nodes/Functions/IEEE1935_Image"
	"1935/efo/Rule_Framework/nodes/Functions/IEEE1935_Jaeger"
	"1935/efo/Rule_Framework/nodes/Functions/IEEE1935_Machine"
	"1935/efo/Rule_Framework/nodes/Functions/IEEE1935_Manifest"
	"1935/efo/Rule_Framework/nodes/Functions/IEEE1935_Node"
	"1935/efo/Rule_Framework/nodes/Functions/IEEE1935_Resourceoptimizer"
	"1935/efo/Rule_Framework/nodes/Functions/IEEE1935_Service"
	"1935/efo/Rule_Framework/nodes/Functions/MachinePropertySource"
	"1935/efo/Rule_Framework/nodes/Functions/Request_tracer"
)

type nodeFunc func(info i.FunctionNode) i.IPolicyNode

var Description = make(map[string]i.NodeDescription)
var Function = make(map[string]nodeFunc)

func init() {
	// add your new written functions' descriptions here
	Description = map[string]i.NodeDescription{
		"If":           		If.Description,
		"Compare":      		Compare.Description,
		"Merge":				Merge.Description,
		"Wait":					Wait.Description,
		"HTTPRequest":			HTTPRequest.Description,
		"IEEE1935_AppStore":	IEEE1935_AppStore.Description,
		"IEEE1935_Application":	IEEE1935_Application.Description,
		"IEEE1935_Cluster":		IEEE1935_Cluster.Description,
		"IEEE1935_Deployment":	IEEE1935_Deployment.Description,
		"IEEE1935_Image":		IEEE1935_Image.Description,
		"IEEE1935_Jaeger":		IEEE1935_Jaeger.Description,
		"IEEE1935_Machine":		IEEE1935_Machine.Description,
		"IEEE1935_Manifest":	IEEE1935_Manifest.Description,
		"IEEE1935_Node":		IEEE1935_Node.Description,
		"IEEE1935_Resourceoptimizer":	IEEE1935_Resourceoptimizer.Description,
		"IEEE1935_Service":		IEEE1935_Service.Description,
		"MachinePropertySource":	MachinePropertySource.Description,
		"Request_tracer":		Request_tracer.Description,
		"CALM_Telemetry":		CALM_Telemetry.Description,
		"CALM_PolicySelector":	CALM_PolicySelector.Description,
		"CALM_PolicyMode":		CALM_PolicyMode.Description,
		"CALM_PlacementApply":	CALM_PlacementApply.Description,
		"CALM_PostVerify":		CALM_PostVerify.Description,
	}

	// add your new written functions here
	Function = map[string]nodeFunc{
		"If":           		func(info i.FunctionNode) i.IPolicyNode { return &If.If{FunctionNode: info} },
		"Compare":      		func(info i.FunctionNode) i.IPolicyNode { return &Compare.Compare{FunctionNode: info} },
		"Merge":				func(info i.FunctionNode) i.IPolicyNode { return &Merge.Merge{FunctionNode: info} },
		"Wait":					func(info i.FunctionNode) i.IPolicyNode { return &Wait.Wait{FunctionNode: info} },
		"HTTPRequest":			func(info i.FunctionNode) i.IPolicyNode { return &HTTPRequest.HTTPRequest{FunctionNode: info} },
		"IEEE1935_AppStore":	func(info i.FunctionNode) i.IPolicyNode { return &IEEE1935_AppStore.IEEE1935_AppStore{FunctionNode: info} },
		"IEEE1935_Application":	func(info i.FunctionNode) i.IPolicyNode { return &IEEE1935_Application.IEEE1935_Application{FunctionNode: info} },
		"IEEE1935_Cluster":		func(info i.FunctionNode) i.IPolicyNode { return &IEEE1935_Cluster.IEEE1935_Cluster{FunctionNode: info} },
		"IEEE1935_Deployment":	func(info i.FunctionNode) i.IPolicyNode { return &IEEE1935_Deployment.IEEE1935_Deployment{FunctionNode: info} },
		"IEEE1935_Image":		func(info i.FunctionNode) i.IPolicyNode { return &IEEE1935_Image.IEEE1935_Image{FunctionNode: info} },
		"IEEE1935_Jaeger":		func(info i.FunctionNode) i.IPolicyNode { return &IEEE1935_Jaeger.IEEE1935_Jaeger{FunctionNode: info} },
		"IEEE1935_Machine":		func(info i.FunctionNode) i.IPolicyNode { return &IEEE1935_Machine.IEEE1935_Machine{FunctionNode: info} },
		"IEEE1935_Manifest":	func(info i.FunctionNode) i.IPolicyNode { return &IEEE1935_Manifest.IEEE1935_Manifest{FunctionNode: info} },
		"IEEE1935_Node":		func(info i.FunctionNode) i.IPolicyNode { return &IEEE1935_Node.IEEE1935_Node{FunctionNode: info} },
		"IEEE1935_Resourceoptimizer":	func(info i.FunctionNode) i.IPolicyNode { return &IEEE1935_Resourceoptimizer.IEEE1935_Resourceoptimizer{FunctionNode: info} },
		"IEEE1935_Service":		func(info i.FunctionNode) i.IPolicyNode { return &IEEE1935_Service.IEEE1935_Service{FunctionNode: info} },
		"MachinePropertySource":	func(info i.FunctionNode) i.IPolicyNode { return &MachinePropertySource.MachinePropertySource{FunctionNode: info} },
		"Request_tracer":		func(info i.FunctionNode) i.IPolicyNode { return &Request_tracer.Request_tracer{FunctionNode: info} },
		"CALM_Telemetry":		func(info i.FunctionNode) i.IPolicyNode { return &CALM_Telemetry.CALM_Telemetry{FunctionNode: info} },
		"CALM_PolicySelector":	func(info i.FunctionNode) i.IPolicyNode { return &CALM_PolicySelector.CALM_PolicySelector{FunctionNode: info} },
		"CALM_PolicyMode":		func(info i.FunctionNode) i.IPolicyNode { return &CALM_PolicyMode.CALM_PolicyMode{FunctionNode: info} },
		"CALM_PlacementApply":	func(info i.FunctionNode) i.IPolicyNode { return &CALM_PlacementApply.CALM_PlacementApply{FunctionNode: info} },
		"CALM_PostVerify":		func(info i.FunctionNode) i.IPolicyNode { return &CALM_PostVerify.CALM_PostVerify{FunctionNode: info} },
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
