package Triggers

import (
	lcm "1935/efo/LCM_Enabler/interfaces"
	"1935/efo/Rule_Framework/nodes/schema"

	timer "1935/efo/Rule_Framework/nodes/Triggers/Timer"
	webhook "1935/efo/Rule_Framework/nodes/Triggers/Webhook"
)

/*
Design-time schema for trigger nodes.

Triggers declare no output format type -- a webhook emits whatever the caller
posted -- so the output shapes here are hand-written and describe that honestly
rather than pretending a static shape exists.
*/

var Schema = map[string]lcm.NodeSchema{
	"Webhook": {
		Type:          "Webhook",
		Kind:          "trigger",
		NumInputPort:  0,
		NumOutputPort: 1,
		Properties: schema.Apply(schema.PropertyFields(webhook.Property{}), schema.Meta{
			Required: []string{"event_type"},
			Docs: map[string]string{
				"event_type": "topic this trigger subscribes to; an incoming event with a matching EventType is dispatched here",
			},
		}),
		Input: nil,
		// The payload is whatever the caller posts, so the field set is known
		// only after a run. The GUI offers free text and live samples instead.
		Output: &lcm.PortSchema{Fields: []lcm.FieldSchema{}, Dynamic: true},
	},
	"Timer": {
		Type:          "Timer",
		Kind:          "trigger",
		NumInputPort:  0,
		NumOutputPort: 1,
		Properties: schema.Apply(schema.PropertyFields(timer.Property{}), schema.Meta{
			Required: []string{"interval_seconds"},
			Docs: map[string]string{
				"interval_seconds": "period between ticks, in seconds; at least 1, and a tick that arrives while the previous run is still going is dropped rather than queued",
				"max_ticks":        "stop after this many ticks have fired; 0 runs until the policy is undeployed",
			},
		}),
		Input: nil,
		// Unlike a webhook, a tick's payload is produced here, so the field set
		// is fixed and the GUI can offer it as a closed list.
		Output: &lcm.PortSchema{
			Fields: []lcm.FieldSchema{
				{Name: "tick", Type: "integer", Doc: "1 for the first tick, incremented for every tick after"},
				{Name: "interval_seconds", Type: "number", Doc: "the configured period, echoed so a downstream node can work in slot units"},
			},
			Dynamic: false,
		},
	},
}
