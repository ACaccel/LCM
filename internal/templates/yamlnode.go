package templates

import (
	"bytes"
	"fmt"
	"regexp"
	"sort"

	"go.yaml.in/yaml/v3"
)

var (
	paramRefRe = regexp.MustCompile(`\bparams\.([A-Za-z_][A-Za-z0-9_]*)`)
	// wholeRefRe matches a scalar that is exactly one parameter reference.
	wholeRefRe = regexp.MustCompile(`^\s*\{\{\s*params\.([A-Za-z_][A-Za-z0-9_]*)\s*\}\}\s*$`)
)

// mapping returns the value node of key in a mapping node, or nil.
func mapping(n *yaml.Node, key string) *yaml.Node {
	if n == nil || n.Kind != yaml.MappingNode {
		return nil
	}
	for i := 0; i+1 < len(n.Content); i += 2 {
		if n.Content[i].Value == key {
			return n.Content[i+1]
		}
	}
	return nil
}

func seq(n *yaml.Node, key string) *yaml.Node {
	if s := mapping(n, key); s != nil && s.Kind == yaml.SequenceNode {
		return s
	}
	return &yaml.Node{Kind: yaml.SequenceNode}
}

func scalarOf(n *yaml.Node) string {
	if n == nil || n.Kind != yaml.ScalarNode {
		return ""
	}
	return n.Value
}

// setNode sets key to value, replacing or appending.
func setNode(n *yaml.Node, key string, value *yaml.Node) {
	for i := 0; i+1 < len(n.Content); i += 2 {
		if n.Content[i].Value == key {
			n.Content[i+1] = value
			return
		}
	}
	n.Content = append(n.Content, &yaml.Node{Kind: yaml.ScalarNode, Value: key}, value)
}

func setScalar(n *yaml.Node, key, value string) {
	setNode(n, key, &yaml.Node{Kind: yaml.ScalarNode, Tag: "!!str", Value: value})
}

// ensureMapping returns the mapping under key, creating it when absent.
func ensureMapping(n *yaml.Node, key string) *yaml.Node {
	if m := mapping(n, key); m != nil && m.Kind == yaml.MappingNode {
		return m
	}
	m := &yaml.Node{Kind: yaml.MappingNode}
	setNode(n, key, m)
	return m
}

func cloneNode(n *yaml.Node) *yaml.Node {
	c := *n
	c.Content = make([]*yaml.Node, len(n.Content))
	for i, ch := range n.Content {
		c.Content[i] = cloneNode(ch)
	}
	return &c
}

func marshal(doc *yaml.Node) ([]byte, error) {
	var buf bytes.Buffer
	enc := yaml.NewEncoder(&buf)
	enc.SetIndent(2)
	if err := enc.Encode(doc); err != nil {
		return nil, fmt.Errorf("encode template: %w", err)
	}
	if err := enc.Close(); err != nil {
		return nil, fmt.Errorf("encode template: %w", err)
	}
	return buf.Bytes(), nil
}

// fileParams reads spec.params of a template document.
func fileParams(doc *yaml.Node) (map[string]any, error) {
	if len(doc.Content) == 0 {
		return nil, fmt.Errorf("empty document")
	}
	params := mapping(mapping(doc.Content[0], "spec"), "params")
	if params == nil {
		return nil, fmt.Errorf("spec.params is missing")
	}
	var out map[string]any
	if err := params.Decode(&out); err != nil {
		return nil, fmt.Errorf("spec.params: %w", err)
	}
	return out, nil
}

// walk calls fn for every scalar value below n.
func walk(n *yaml.Node, fn func(*yaml.Node)) {
	if n == nil {
		return
	}
	if n.Kind == yaml.ScalarNode {
		fn(n)
		return
	}
	for i, c := range n.Content {
		// Mapping keys are never expressions.
		if n.Kind == yaml.MappingNode && i%2 == 0 {
			continue
		}
		walk(c, fn)
	}
}

// collect records the parameters whose names appear as params.<name> below n.
func collect(n *yaml.Node, into map[string]bool) {
	walk(n, func(s *yaml.Node) {
		for _, m := range paramRefRe.FindAllStringSubmatch(s.Value, -1) {
			into[m[1]] = true
		}
	})
}

// referenced reports whether any node of the template document uses the
// parameter.
func referenced(doc *yaml.Node, name string) bool {
	used := map[string]bool{}
	collect(mapping(mapping(doc.Content[0], "spec"), "nodes"), used)
	return used[name]
}

// substitute replaces every scalar of n that consists of a single
// {{ params.x }} reference with the literal value of the parameter.
func substitute(n *yaml.Node, values map[string]any) {
	walk(n, func(s *yaml.Node) {
		m := wholeRefRe.FindStringSubmatch(s.Value)
		if m == nil {
			return
		}
		v, ok := values[m[1]]
		if !ok {
			return
		}
		var repl yaml.Node
		if err := repl.Encode(v); err != nil {
			return
		}
		*s = repl
	})
}

func sortedKeys(m map[string]any) []string {
	keys := make([]string, 0, len(m))
	for k := range m {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	return keys
}
