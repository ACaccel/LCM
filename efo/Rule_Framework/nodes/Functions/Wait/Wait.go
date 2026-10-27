/*
Package Wait pauses a branch before continuing.

Orchestration needs it: creating a cluster or instantiating a service returns
long before the thing is actually up, so the next node has to be held back.
*/
package Wait

import (
	config "1935/efo/Rule_Framework/config"
	i "1935/efo/Rule_Framework/interfaces"
	u "1935/efo/Rule_Framework/nodes/utils"
	"fmt"
	"time"
)

type Property struct {
	Seconds float64 `yaml:"seconds"`
}

var Description = i.NodeDescription{
	NodeType:      "Wait",
	NumInputPort:  1,
	NumOutputPort: 1,
}

type Wait struct {
	i.FunctionNode
}

func (n *Wait) Execute(input []any, info i.ExecInfo) i.NodeExecResult {
	var property Property
	u.DecodeProperty(Description.NodeType, n.Config, input, &property)

	// The cap bounds how long one node can hold up a run; a misconfigured value
	// would otherwise pin a walker for the life of the process.
	seconds := property.Seconds
	if max := config.MaxWaitSeconds(); seconds > max {
		fmt.Println("Wait: seconds", seconds, "exceeds the cap, waiting", max, "instead")
		seconds = max
	}
	if seconds > 0 {
		// Interruptible, because the engine retires a run by cancelling it and
		// then waiting for its walkers. A plain Sleep here would make both the
		// next trigger event and undeploy wait out the full delay -- and this
		// node exists precisely to be told to wait a long time.
		timer := time.NewTimer(time.Duration(seconds * float64(time.Second)))
		defer timer.Stop()
		select {
		case <-timer.C:
		case <-info.RunDone:
			return i.NodeExecResult{Output: u.EmptyOutput(Description.NumOutputPort), Ports: []int{0}}
		}
	}

	// The delay is the node's only effect. What arrived travels on as the
	// namespaces it arrived with, so re-emitting it would only file a duplicate
	// under this node's name.
	return i.NodeExecResult{
		Output: u.EmptyOutput(Description.NumOutputPort),
		Ports:  []int{0},
	}
}
