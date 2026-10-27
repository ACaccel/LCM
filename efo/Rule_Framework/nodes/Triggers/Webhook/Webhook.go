package webhook

import (
	i "1935/efo/Rule_Framework/interfaces"
	u "1935/efo/Rule_Framework/nodes/utils"
	"fmt"
)

type Property struct {
	// EventType is the topic this webhook trigger subscribes to; an incoming
	// webhook event with a matching EventType is dispatched to this trigger.
	EventType string `json:"event_type" yaml:"event_type"`
}

var Description = i.TriggerDescription{
	TriggerType: "Webhook",
}

type Webhook struct {
	i.TriggerNode
}

func (n *Webhook) Execute(input []any, info i.ExecInfo) i.NodeExecResult {
	for {
		select {
		case <-info.StopCh:
			return i.NodeExecResult{}
		case content := <-info.WebhookCh:
			fmt.Println("Webhook: Execute node", n.Config.NodeName)
			output, err := u.EncodeMapNodeOutput(content)
			if err != nil {
				fmt.Println("Webhook: Encode output error,", err)
			}
			// The handover is selectable so that undeploying while an event is
			// in flight releases this goroutine instead of leaking it: once the
			// policy has stopped, nothing is draining TriggerCh any more.
			select {
			case info.TriggerCh <- i.TriggerEvent{Node: n.Config.NodeName, Content: output}:
			case <-info.StopCh:
				return i.NodeExecResult{}
			}
		}
	}
}
