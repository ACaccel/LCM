/*
Package Compare routes on a comparison between two inputs.

Where If compares a field against a constant, Compare compares a field of input
port 0 against a field of input port 1, so it is the node for questions that
span two chains: is the measured value still under the configured limit, did the
reported count reach the requested one.

Conditions are a list joined by one combinator, the same shape If uses. A list
rather than a single comparison is what makes a two-sided range expressible at
all: the engine allows one connection per output port, so the same pair of
upstreams cannot be fed to a second Compare.
*/
package Compare

import (
	lcm "1935/efo/LCM_Enabler/interfaces"
	i "1935/efo/Rule_Framework/interfaces"
	u "1935/efo/Rule_Framework/nodes/utils"
	"fmt"
)

// Combinator values joining a condition list into one verdict.
const (
	CombinatorAnd = "AND"
	CombinatorOr  = "OR"
)

// Condition compares one field of the left input against one of the right.
type Condition struct {
	// Left names a field of input port 0, Right a field of input port 1. Both
	// read the payload as a map rather than a fixed struct, so any upstream key
	// can be compared without renaming it on the edge first. A qualified name
	// such as "webhook-0.Value" names which upstream node's value is meant, for
	// when a later one emitted the same field and shadowed it.
	Left     string `yaml:"left"`
	Operator string `yaml:"operator"`
	Right    string `yaml:"right"`
}

type Property struct {
	Conditions []Condition `yaml:"conditions"`
	// Combinator joins every condition. It applies to the whole list, so
	// "a AND b OR c" is not expressible in one node -- chain two instead.
	Combinator string `yaml:"combinator"`
}

var Description = i.NodeDescription{
	NodeType:      "Compare",
	NumInputPort:  2,
	NumOutputPort: 2,
}

type Compare struct {
	i.FunctionNode
}

func (n *Compare) Execute(input []any, info i.ExecInfo) i.NodeExecResult {
	var property Property
	u.DecodeProperty(Description.NodeType, n.Config, input, &property)

	left := side{flat: sideOf(input, 0), namespaced: u.NamespacesAt(info, 0)}
	right := side{flat: sideOf(input, 1), namespaced: u.NamespacesAt(info, 1)}

	port := []int{1}
	if evaluate(left, right, property) {
		port = []int{0}
	}

	// Compare routes; it does not produce. Both inputs already travel on as the
	// namespaces they arrived with, and the port carries the verdict.
	return i.NodeExecResult{
		Output: u.EmptyOutput(Description.NumOutputPort),
		Ports:  port,
	}
}

// side is one input port in both the forms a condition can name: the flat view
// for a bare field, and the namespaces for a qualified one.
type side struct {
	flat       lcm.DataPacketContent
	namespaced lcm.DataPacketContent
}

// sideOf reads one input port as a plain map. A node upstream that produced
// nothing, or a transform that failed, leaves nil here; an empty map keeps every
// lookup below a miss rather than a panic.
func sideOf(input []any, port int) lcm.DataPacketContent {
	if port >= len(input) {
		return lcm.DataPacketContent{}
	}
	if content := u.ContentOf(input[port]); content != nil {
		return content
	}
	return lcm.DataPacketContent{}
}

// evaluate applies every condition and joins them with the combinator. An empty
// condition list is a misconfiguration rather than a vacuous truth: reporting
// true would silently run the true branch of a node nobody finished setting up,
// so it evaluates false and says why.
func evaluate(left side, right side, property Property) bool {
	if len(property.Conditions) == 0 {
		fmt.Println("Compare: no conditions configured, routing to the false port")
		return false
	}

	or := property.Combinator == CombinatorOr
	for _, condition := range property.Conditions {
		leftValue, leftFound := u.NumberAt(left.flat, left.namespaced, condition.Left)
		rightValue, rightFound := u.NumberAt(right.flat, right.namespaced, condition.Right)
		// A missing field cannot satisfy any operator, not even "!=": there is
		// no value to be unequal to. Treat it as a failed condition, the same
		// rule If applies, so both nodes behave one way and not two.
		found := leftFound && rightFound
		if !leftFound {
			fmt.Println("Compare: field", condition.Left, "is not a number on input port 0")
		}
		if !rightFound {
			fmt.Println("Compare: field", condition.Right, "is not a number on input port 1")
		}
		hit := found && u.OperatorExec(condition.Operator, leftValue, rightValue)
		if or && hit {
			return true
		}
		if !or && !hit {
			return false
		}
	}
	return !or
}
