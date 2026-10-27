package If

import (
	i "1935/efo/Rule_Framework/interfaces"
	u "1935/efo/Rule_Framework/nodes/utils"
	"fmt"

	lcm "1935/efo/LCM_Enabler/interfaces"
)

// Combinator values joining a condition list into one verdict.
const (
	CombinatorAnd = "AND"
	CombinatorOr  = "OR"
)

// Condition is one comparison against a field of the incoming payload.
type Condition struct {
	// Field is the payload key to read. The node reads the payload as a map
	// rather than a fixed struct, so any upstream key can be compared without
	// renaming it on the edge first. A qualified name such as "webhook-0.Value"
	// names which upstream node's value is meant, for when a later one emitted
	// the same field and shadowed it.
	Field    string  `yaml:"field"`
	Operator string  `yaml:"operator"`
	Value    float64 `yaml:"value"`
}

type Property struct {
	Conditions []Condition `yaml:"conditions"`
	// Combinator joins every condition. It applies to the whole list, so
	// "a AND b OR c" is not expressible in one node -- chain two instead.
	Combinator string `yaml:"combinator"`
}

var Description = i.NodeDescription{
	NodeType:      "If",
	NumInputPort:  1,
	NumOutputPort: 2,
}

type If struct {
	i.FunctionNode
}

func (n *If) Execute(input []any, info i.ExecInfo) i.NodeExecResult {
	var property Property
	u.DecodeProperty(Description.NodeType, n.Config, input, &property)

	port := []int{1}
	if evaluate(payloadOf(input), u.NamespacesAt(info, 0), property) {
		port = []int{0}
	}

	// If routes; it does not produce. The verdict needs no field of its own
	// because the port carries it already.
	return i.NodeExecResult{
		Output: u.EmptyOutput(Description.NumOutputPort),
		Ports:  port,
	}
}

// payloadOf reads input port 0 as a plain map. A node upstream that produced
// nothing, or a transform that failed, leaves nil here; an empty map keeps every
// lookup below a miss rather than a panic.
func payloadOf(input []any) lcm.DataPacketContent {
	if len(input) == 0 {
		return lcm.DataPacketContent{}
	}
	content, ok := input[0].(lcm.DataPacketContent)
	if ok && content != nil {
		return content
	}
	raw, ok := input[0].(map[string]any)
	if ok && raw != nil {
		return raw
	}
	return lcm.DataPacketContent{}
}

// evaluate applies every condition and joins them with the combinator. An empty
// condition list is a misconfiguration rather than a vacuous truth: reporting
// true would silently run the true branch of a node nobody finished setting up,
// so it evaluates false and says why.
func evaluate(
	payload lcm.DataPacketContent,
	namespaced lcm.DataPacketContent,
	property Property,
) bool {
	if len(property.Conditions) == 0 {
		fmt.Println("If: no conditions configured, routing to the false port")
		return false
	}

	or := property.Combinator == CombinatorOr
	for _, condition := range property.Conditions {
		value, found := u.NumberAt(payload, namespaced, condition.Field)
		// A missing field cannot satisfy any operator, not even "!=": there is
		// no value to be unequal to. Treat it as a failed condition.
		hit := found && u.OperatorExec(condition.Operator, value, condition.Value)
		if !found {
			fmt.Println("If: field", condition.Field, "is not a number in the input payload")
		}
		if or && hit {
			return true
		}
		if !or && !hit {
			return false
		}
	}
	return !or
}
