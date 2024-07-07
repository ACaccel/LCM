package Triggers

import (
	i "lcm/packages/interfaces"
	"lcm/packages/nodes/Triggers/Webhook"
	"lcm/packages/nodes/Triggers/Interval"
)

var Description = make(map[string]i.TriggerDescription)

func init() {
	Description["Webhook"] = Webhook.Description
	Description["Interval"] = Interval.Description
}

func InitNode(info i.TriggerNode) (*i.IPolicyNode) {
	var node i.IPolicyNode

	switch info.Description.TriggerType {
	case "Webhook":
		node = &Webhook.Webhook{TriggerNode: info}
	case "Interval":
		node = &Interval.Interval{TriggerNode: info}
	default:
		node = nil
	}

	return &node
}