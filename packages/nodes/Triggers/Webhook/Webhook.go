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
				fmt.Println("Webhook: Execute node", n.Config.NodeName)
				output, err := u.EncodeMapNodeOutput(content)
				if err != nil {
					fmt.Println("Webhook: Encode output error,", err)
				}
				info.TriggerCh <- output
		}
	}

	return i.NodeExecResult{}
}