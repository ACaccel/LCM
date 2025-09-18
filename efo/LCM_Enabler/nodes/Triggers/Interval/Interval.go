package interval

import (
	i "1935/efo/LCM_Enabler/interfaces"
	u "1935/efo/LCM_Enabler/nodes/utils"
	"fmt"
	"time"
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

	ticker := time.NewTicker(time.Duration(time.Duration(property.Interval) * time.Second))
	go func() {
		status := true
		for status {
			select {
			case <-ticker.C:
				fmt.Println("Interval: Execute node", n.Config.NodeName)
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
