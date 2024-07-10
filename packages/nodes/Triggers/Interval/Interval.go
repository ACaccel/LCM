package Interval

import (
	"fmt"
	"time"
	i "lcm/packages/interfaces"
	u "lcm/packages/nodes/utils"
)

type Property struct {
	Interval int
}

var Description = i.TriggerDescription{
	TriggerType: "Interval",
}

type Interval struct {
	i.TriggerNode
}

func (n *Interval) Execute(input []any, info i.ExecInfo) i.NodeExecResult {
	var property Property
	u.GetProperty(n.Config.Property, &property)
	output := i.NodeOutput{nil}

	ticker := time.NewTicker(time.Duration(property.Interval * int(time.Second)))
	go func() {
		status := true
		for status {
			select {
				case <-ticker.C:
					fmt.Println("Interval: Execute function", n.Config.NodeName)
					info.TriggerCh <- output
				case policyStatus := <-info.StatusCh:
					status = policyStatus
					if !status {
						ticker.Stop()
					}
			}
		}
	}()

	return i.NodeExecResult{}
}
