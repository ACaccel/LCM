package webhook

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
	status := true
	for status {
		select {
			case policyStatus := <-info.StatusCh:
				status = policyStatus
			case content := <-info.WebhookCh:
				output, err := u.EncodeMapNodeOutput(content)
				if err != nil {
					fmt.Println("Webhook: Execute function", n.Config.NodeName, "Error:", err)
				}
				fmt.Println("Webhook: Execute function", n.Config.NodeName)
				info.TriggerCh <- output
		}
	}

	return i.NodeExecResult{}
}