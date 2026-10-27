package policy

import (
	i "1935/efo/LCM_Enabler/interfaces"
	"sort"
)

/*
Namespaced payloads.

What travels on a connection is not one flat map but one namespace per node that
has run: {"webhook-0": {...}, "http-2": {...}}. Two nodes can both emit a field
called Value without either losing it, and a node four hops downstream can still
reach what the trigger produced -- neither of which was possible when every node
replaced the payload with its own fields.

Nodes never see this form. They are handed the flattened view, so a node still
decodes a plain struct and a condition still names a bare field.
*/

// asContent narrows a value to a content map.
//
// The two cases are not interchangeable to a type assertion even though they
// share an underlying type: a namespace holds i.DataPacketContent while a
// decoded YAML or JSON payload holds map[string]any, and asserting the wrong one
// silently reports "not a map".
func asContent(value any) (map[string]any, bool) {
	switch typed := value.(type) {
	case i.DataPacketContent:
		return typed, true
	case map[string]any:
		return typed, true
	default:
		return nil, false
	}
}

// selectNamespaces keeps the namespaces an edge forwards.
//
// An empty carry list forwards everything, matching how an empty transFormat
// means pass-through.
func selectNamespaces(namespaced i.DataPacketContent, carry []string) i.DataPacketContent {
	selected := i.DataPacketContent{}
	if len(carry) == 0 {
		for name, content := range namespaced {
			selected[name] = content
		}
		return selected
	}
	for _, name := range carry {
		if content, ok := namespaced[name]; ok {
			selected[name] = content
		}
	}
	return selected
}

// flattenOrder decides which namespace wins when two of them share a field name.
//
// An explicit carry list is the author's own precedence, so it is followed as
// written and the last entry wins. Without one the order has to be derived, and
// deriving it from map iteration would make the same policy produce different
// results on different runs. Sorting is what makes it deterministic; putting the
// immediately upstream node last is what makes it match the intuition the flat
// model established, where a node read what the node before it produced.
func flattenOrder(namespaced i.DataPacketContent, carry []string, producer string) []string {
	if len(carry) > 0 {
		return carry
	}
	order := make([]string, 0, len(namespaced))
	for name := range namespaced {
		if name != producer {
			order = append(order, name)
		}
	}
	sort.Strings(order)
	if _, ok := namespaced[producer]; ok {
		order = append(order, producer)
	}
	return order
}

// flattenNamespaces folds namespaces into the single flat map a node reads.
func flattenNamespaces(namespaced i.DataPacketContent, order []string) i.DataPacketContent {
	flat := i.DataPacketContent{}
	for _, name := range order {
		content, ok := asContent(namespaced[name])
		if !ok {
			continue
		}
		for key, value := range content {
			flat[key] = value
		}
	}
	return flat
}

// accumulate returns the namespaces leaving a node: everything it forwards, plus
// its own output filed under its own name.
//
// The result is a fresh map every time. EncodeOutput hands the same map to every
// output port, so sharing one here would alias a single payload across every
// branch and into the trace.
func accumulate(carried i.DataPacketContent, nodeName string, own i.DataPacketContent) i.DataPacketContent {
	namespaced := i.DataPacketContent{}
	for name, content := range carried {
		namespaced[name] = content
	}
	namespaced[nodeName] = own
	return namespaced
}
