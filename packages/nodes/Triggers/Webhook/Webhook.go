package Webhook

import (
	"fmt"
	i "interfaces"
	u "nodes/utils"
)

type Property struct {
	
}

var Description = i.TriggerDescription{
	TriggerType: "Webhook",
}

type Webhook struct {
	i.TriggerNode
}

func (n *Webhook) Execute(input []any, info i.ExecInfo) i.NodeExecResult {
	output, err := u.GetRawInputContent(input[0].(map[string]any))
	if err != nil {
		fmt.Println("Webhook: Execute function", n.Config.NodeName, "Error:", err)
	}

	fmt.Println("Webhook: Execute function", n.Config.NodeName)
	return i.NodeExecResult{
		Output: output,
		Ports: []int{0},
	}
}