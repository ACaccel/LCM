package Webhook

import (
	"fmt"
	i "interfaces"
)

var Description = i.TriggerDescription{
	TriggerType: "Webhook",
}

type Webhook struct {
	i.TriggerNode
}

func (n *Webhook) Execute(input []any) (*any, []int) {
	output := input[0]
	fmt.Println("Webhook: Execute function", n.Config.NodeName)
	return &output, []int{0}
}