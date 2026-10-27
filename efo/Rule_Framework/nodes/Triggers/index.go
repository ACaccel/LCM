package Triggers

import (
	"errors"

	i "1935/efo/Rule_Framework/interfaces"

	// import your new written triggers here
	timer "1935/efo/Rule_Framework/nodes/Triggers/Timer"
	webhook "1935/efo/Rule_Framework/nodes/Triggers/Webhook"
)

type triggerFunc func(info i.TriggerNode) i.IPolicyNode

// validator is implemented by a trigger whose configuration can be rejected
// before the policy runs. A trigger fires from its own goroutine, so a property
// it cannot work with has no caller to report to once started: the policy would
// deploy, report DEPLOYED, and simply never fire. Checking here fails the deploy
// instead.
type validator interface {
	Validate() error
}

var Description = make(map[string]i.TriggerDescription)
var Trigger = make(map[string]triggerFunc)

func init() {
	// add your new written triggers' descriptions here
	Description = map[string]i.TriggerDescription{
		"Webhook": webhook.Description,
		"Timer":   timer.Description,
	}

	// add your new written triggers here
	Trigger = map[string]triggerFunc{
		"Webhook": func(info i.TriggerNode) i.IPolicyNode { return &webhook.Webhook{TriggerNode: info} },
		"Timer":   func(info i.TriggerNode) i.IPolicyNode { return &timer.Timer{TriggerNode: info} },
	}
}

func InitNode(info i.TriggerNode) (*i.IPolicyNode, error) {
	var node i.IPolicyNode

	if t, ok := Trigger[info.Description.TriggerType]; ok {
		node = t(info)
	} else {
		return nil, errors.New("LCM: Trigger Node " + info.Description.TriggerType + " not found")
	}

	if v, ok := node.(validator); ok {
		if err := v.Validate(); err != nil {
			return nil, err
		}
	}

	return &node, nil
}
