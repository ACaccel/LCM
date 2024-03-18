package interfaces

/*
 * BaseNode
 */

type IPolicyNode interface {
	Execute(input []any, info ExecInfo) NodeExecResult
}

type ExecInfo struct {
	ActiveInPort  	int
	UseShared 	 	bool
	SharedContent 	any
}

type NodeExecResult struct {
	Output any
	Ports  []int
}

type FunctionNode struct {
	Description NodeDescription
	Config 		NodeConfig
}

type TriggerNode struct {
	Description TriggerDescription   
	Config 		NodeConfig     
}

type NodeConfig struct {
	NodeName    string	`json:"nodeName" yaml:"nodeName"`
	NodeType	string	`json:"nodeType" yaml:"nodeType"`
	Property 	any		`json:"property" yaml:"property"`
}

type NodeDescription struct {
	NodeType	  string
	NumInputPort  int
	NumOutputPort int
}

type TriggerDescription struct {
	TriggerType   string
}

/*
 * Module
 */

 type ModuleConfig struct {
	ModuleName 	string `json:"moduleName" yaml:"moduleName"`
	NumInPort 	int `json:"numInPort" yaml:"numInPort"`
	NumOutPort 	int `json:"numOutPort" yaml:"numOutPort"`
	InPort      []ModulePortConfig `json:"inPort" yaml:"inPort"`
	OutPort     []ModulePortConfig `json:"outPort" yaml:"outPort"`
	ModuleNode	[]NodeConfig `json:"moduleNode" yaml:"moduleNode"`
	ModulePath	[]ConnectionUnit `json:"modulePath" yaml:"modulePath"`
}

type ModulePortConfig struct {
    ModulePort 	int `json:"modulePort" yaml:"modulePort"`
	NodeName    string `json:"nodeName" yaml:"nodeName"`
	NodePort	int `json:"nodePort" yaml:"nodePort"`
}

/*
 * Others
 */

type ConfigFormat struct {
	Module []ModuleConfig `json:"module" yaml:"module"`
	Policy []PolicyConfig `json:"policy" yaml:"policy"`
}

type PolicyConfig struct {
	PolicyName    string 				`json:"policyName" yaml:"policyName"`	
	PolicyType    string 				`json:"policyType" yaml:"policyType"`
	PolicyTrigger []NodeConfig 			`json:"policyTrigger" yaml:"policyTrigger"`
	PolicyNode    []NodeConfig 			`json:"policyNode" yaml:"policyNode"`
	PolicyPath    []ConnectionUnit 		`json:"policyPath" yaml:"policyPath"`
}

type ConnectionUnit struct {
	NodePair 	[2]string 			`json:"nodePair" yaml:"nodePair"`
	PortPair 	[2]int 				`json:"portPair" yaml:"portPair"`
	TransFormat map[string]string 	`json:"transFormat" yaml:"transFormat"`
}

type NumPort struct {
	NumInput  int
	NumOutput int
}

type WebhookEvent struct {
	EventType string
	Content   any
}