package Functions

import (
	"fmt"
	i "interfaces"
)

var description = i.TriggerDescription{
	TriggerType: "Webhook",
}

type Webhook struct {
	i.TriggerNode
}

func (n *Webhook) Init(config i.NodeConfig) (*Webhook) {
	n.Description = description
	n.Config = config

	return n
}

func (n *Webhook) Execute(input []any) (*any, []int) {
	output := input[0]
	fmt.Println("Webhook: Execute function", n.Config.NodeName)
	return &output, []int{0}
}