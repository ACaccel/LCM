package Triggers

import (
	"errors"

	i "1935/efo/LCM_Enabler/interfaces"

	// import your new written triggers here
	interval "1935/efo/LCM_Enabler/nodes/Triggers/Interval"
	webhook "1935/efo/LCM_Enabler/nodes/Triggers/Webhook"
)

type triggerFunc func(info i.TriggerNode) i.IPolicyNode

var Description = make(map[string]i.TriggerDescription)
var Trigger = make(map[string]triggerFunc)

func init() {
	// add your new written triggers' descriptions here
	Description = map[string]i.TriggerDescription{
		"Webhook":      webhook.Description,
		"Interval":     interval.Description,
	}

	// add your new written triggers here
	Trigger = map[string]triggerFunc{
		"Webhook":      func(info i.TriggerNode) i.IPolicyNode { return &webhook.Webhook{TriggerNode: info} },
		"Interval":     func(info i.TriggerNode) i.IPolicyNode { return &interval.Interval{TriggerNode: info} },
	}
}

func InitNode(info i.TriggerNode) (*i.IPolicyNode, error) {
	var node i.IPolicyNode

	if t, ok := Trigger[info.Description.TriggerType]; ok {
		node = t(info)
	} else {
		return nil, errors.New("LCM: Trigger Node " + info.Description.TriggerType + " not found")
	}

	return &node, nil
}
