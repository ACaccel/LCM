package Webhook

import (
	"fmt"
	i "lcm/packages/interfaces"
	u "lcm/packages/nodes/utils"
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
	content := <-info.WebhookChannel
	output, err := u.GetRawInputContent(content)
	if err != nil {
		fmt.Println("Webhook: Execute function", n.Config.NodeName, "Error:", err)
	}

	fmt.Println("Webhook: Execute function", n.Config.NodeName)
	return i.NodeExecResult{
		Output: output,
		Ports: []int{0},
	}
}