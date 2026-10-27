/*
Package Merge brings two branches back together.

The mode decides when it runs, and that is the whole of the node:

	all (default)  wait for both inputs, then combine them
	any            continue as soon as either input arrives

"any" is what unblocks a plain If. Only one of its branches ever runs, so
anything downstream that waits for both sides waits forever.

What it does NOT do is combine data. Each branch already travels on as its own
namespace, and the engine unions the namespaces of every input port, so both
branches reach the next node whether or not this node writes anything. Merging
control is the whole job; merging data happens either way.

Two branches carrying the same field name therefore lose nothing: both are on
the wire, the flat view the next node reads keeps the last one folded, and a
qualified reference such as "webhook-0.Value" still reaches the other.
*/
package Merge

import (
	i "1935/efo/Rule_Framework/interfaces"
	u "1935/efo/Rule_Framework/nodes/utils"
	"fmt"
)

// Modes deciding when the node runs.
const (
	ModeAll = "all"
	ModeAny = "any"
)

type Property struct {
	// Mode is all or any; anything else, including empty, means all.
	Mode string `yaml:"mode"`
}

var Description = i.NodeDescription{
	NodeType:      "Merge",
	NumInputPort:  2,
	NumOutputPort: 1,
	Join:          i.JoinAll,
}

type Merge struct {
	i.FunctionNode
}

// JoinMode lets one node type cover both behaviors. NodeDescription.Join is
// fixed per type, so without this the two modes would have to be two types.
func (n *Merge) JoinMode() i.JoinMode {
	if n.property().Mode == ModeAny {
		return i.JoinAny
	}
	return i.JoinAll
}

// Execute does nothing but let the walker through. Both modes reach here with
// the join already decided, and the union of the input namespaces is the
// engine's work, so there is no payload of this node's own to write.
func (n *Merge) Execute(input []any, info i.ExecInfo) i.NodeExecResult {
	return i.NodeExecResult{
		Output: u.EmptyOutput(Description.NumOutputPort),
		Ports:  []int{0},
	}
}

func (n *Merge) property() Property {
	var property Property
	if err := u.GetProperty(n.Config.Property, &property); err != nil {
		fmt.Println("Merge: Get property error,", err)
	}
	return property
}
