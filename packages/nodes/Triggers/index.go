package Triggers

import (
	i "lcm/packages/interfaces"
	"lcm/packages/nodes/Triggers/Webhook"
	"lcm/packages/nodes/Triggers/Interval"
	"lcm/packages/nodes/Triggers/Monitor"
)

var Description = make(map[string]i.TriggerDescription)

func init() {
	Description["Webhook"] = webhook.Description
	Description["Interval"] = interval.Description
	Description["MonitorEvent"] = monitor.Description
}

func InitNode(info i.TriggerNode) (*i.IPolicyNode) {
	var node i.IPolicyNode

	switch info.Description.TriggerType {
	case "Webhook":
		node = &webhook.Webhook{TriggerNode: info}
	case "Interval":
		node = &interval.Interval{TriggerNode: info}
	case "MonitorEvent":
		node = &monitor.Monitor{TriggerNode: info}
	default:
		node = nil
	}

	return &node
}