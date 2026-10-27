/*
Package timer provides the Timer trigger: a periodic clock owned by the policy.

A policy that has to act on a schedule -- a control loop evaluating one slot per
period -- would otherwise need something outside the EFO posting webhooks at the
right rate. Timer keeps the clock inside the policy design, where it is visible,
deployable and undeployable with everything else.
*/
package timer

import (
	"errors"
	"fmt"
	"strconv"
	"time"

	lcm "1935/efo/LCM_Enabler/interfaces"
	i "1935/efo/Rule_Framework/interfaces"
	u "1935/efo/Rule_Framework/nodes/utils"
)

// MinIntervalSeconds is the shortest period a Timer may be deployed with.
//
// The engine serializes runs, so a period below what a run takes produces
// nothing but dropped ticks. One second is a floor low enough for any control
// loop the LCM expresses and high enough that a mistyped property cannot turn
// a policy into a busy loop. There is deliberately no upper bound.
const MinIntervalSeconds = 1.0

type Property struct {
	// IntervalSeconds is the period between ticks. It is required, and must be
	// at least MinIntervalSeconds; a policy configured below that is refused at
	// deploy time rather than started and left misbehaving.
	IntervalSeconds float64 `json:"interval_seconds" yaml:"interval_seconds"`
	// MaxTicks stops the timer after that many ticks have fired; 0 runs until
	// the policy is undeployed. Dropped ticks count, because the count is of
	// slots elapsed, not of runs started.
	MaxTicks int `json:"max_ticks" yaml:"max_ticks"`
}

var Description = i.TriggerDescription{
	TriggerType: "Timer",
}

type Timer struct {
	i.TriggerNode
}

// Validate reports a configuration the timer could not run with.
//
// It is called while the policy is being built, so a bad interval fails the
// deploy with a message naming the node. Reporting it from Execute instead
// would leave the policy DEPLOYED with a trigger that never fires and nothing
// but a log line to explain it.
func (n *Timer) Validate() error {
	property, err := n.property()
	if err != nil {
		return errors.New("LCM: trigger " + n.Config.NodeName +
			" has unreadable properties: " + err.Error())
	}
	if property.IntervalSeconds < MinIntervalSeconds {
		return errors.New("LCM: trigger " + n.Config.NodeName + " has interval_seconds " +
			strconv.FormatFloat(property.IntervalSeconds, 'g', -1, 64) +
			", below the minimum of " +
			strconv.FormatFloat(MinIntervalSeconds, 'g', -1, 64) + " second")
	}
	if property.MaxTicks < 0 {
		return errors.New("LCM: trigger " + n.Config.NodeName +
			" has a negative max_ticks; use 0 for unlimited")
	}
	return nil
}

func (n *Timer) property() (Property, error) {
	var property Property
	err := u.GetProperty(n.Config.Property, &property)
	return property, err
}

func (n *Timer) Execute(input []any, info i.ExecInfo) i.NodeExecResult {
	property, err := n.property()
	if err != nil {
		fmt.Println("Timer: Get property error,", err)
		return i.NodeExecResult{}
	}
	// Validate has already refused anything below the minimum at deploy time.
	// This second guard covers a Timer constructed directly, where a
	// non-positive period would panic inside time.NewTicker.
	if property.IntervalSeconds <= 0 {
		fmt.Println("Timer: node", n.Config.NodeName,
			"has a non-positive interval_seconds; not starting")
		return i.NodeExecResult{}
	}

	ticker := time.NewTicker(time.Duration(property.IntervalSeconds * float64(time.Second)))
	defer ticker.Stop()

	tick := 0
	for {
		select {
		case <-info.StopCh:
			return i.NodeExecResult{}
		case <-ticker.C:
			tick++
			output, encErr := u.EncodeMapNodeOutput(map[string]any{
				"tick":             tick,
				"interval_seconds": property.IntervalSeconds,
			})
			if encErr != nil {
				fmt.Println("Timer: Encode output error,", encErr)
			} else if !n.dispatch(info, output, tick) {
				return i.NodeExecResult{}
			}
			if property.MaxTicks > 0 && tick >= property.MaxTicks {
				fmt.Println("Timer: node", n.Config.NodeName,
					"reached max_ticks", property.MaxTicks, "and stopped")
				return i.NodeExecResult{}
			}
		}
	}
}

// dispatch hands one tick to the policy loop without ever waiting for it, and
// reports whether the timer should keep running.
//
// The engine serializes runs and takes trigger events with a plain receive, so
// a tick arriving while the previous run is still draining has nobody to take
// it. Waiting would build a backlog and then fire it all at once the moment the
// run ends, which for a control loop is worse than missing a slot: the schedule
// may drift, but it must never accumulate. The tick is dropped and reported
// instead. The stop case is what releases this goroutine on undeploy.
func (n *Timer) dispatch(info i.ExecInfo, output lcm.NodeOutput, tick int) bool {
	select {
	case info.TriggerCh <- i.TriggerEvent{Node: n.Config.NodeName, Content: output}:
	case <-info.StopCh:
		return false
	default:
		fmt.Println("Timer: node", n.Config.NodeName, "dropped tick", tick,
			"- the previous run has not finished")
	}
	return true
}
