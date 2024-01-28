package interfaces

/*
 * BaseNode
 */

type IPolicyNode interface {
	Execute(input []any) (*any, []int)
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
	NodeName    string
	NodeType	string
	Property 	any
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
 * Others
 */

type PolicyConfig struct {
	PolicyName    string 				`json:"policyName"`
	PolicyType    string 				`json:"policyType"`
	PolicyTrigger []NodeConfig 			`json:"policyTrigger"`
	PolicyNode    []NodeConfig 			`json:"policyNode"`
	PolicyPath    []ConnectionUnit 		`json:"policyPath"`
}

type ConnectionUnit struct {
	NodePair [2]string 	`json:"nodePair"`
	PortPair [2]int 	`json:"portPair"`
}

type NumPort struct {
	NumInput  int
	NumOutput int
}

type WebhookEvent struct {
	EventType string
	Content   any
}