package Triggers

import (
	i "lcm/packages/interfaces"

	// import your new written triggers here
	"lcm/packages/nodes/Triggers/Webhook"
	"lcm/packages/nodes/Triggers/Interval"
	"lcm/packages/nodes/Triggers/Monitor"
)

type triggerFunc func(info i.TriggerNode) i.IPolicyNode

var Description = make(map[string]i.TriggerDescription)
var Trigger = make(map[string]triggerFunc)

func init() {
	// add your new written triggers' descriptions here
	Description = map[string]i.TriggerDescription{
		"Webhook": webhook.Description,
		"Interval": interval.Description,
		"MonitorEvent": monitor.Description,
	}

	// add your new written triggers here
	Trigger = map[string]triggerFunc{
		"Webhook": func(info i.TriggerNode) i.IPolicyNode { return &webhook.Webhook{TriggerNode: info} },
		"Interval": func(info i.TriggerNode) i.IPolicyNode { return &interval.Interval{TriggerNode: info} },
		"MonitorEvent": func(info i.TriggerNode) i.IPolicyNode { return &monitor.Monitor{TriggerNode: info} },
	}
}

func InitNode(info i.TriggerNode) (*i.IPolicyNode) {
	var node i.IPolicyNode

	if t, ok := Trigger[info.Description.TriggerType]; ok {
		node = t(info)
	} else {
		node = nil
	}

	return &node
}