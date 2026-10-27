package configsync

import (
	"bytes"
	"fmt"

	"go.yaml.in/yaml/v3"
)

// canonicalYAML renders the canonical JSON form of a document as block
// style YAML. Field order is the one of the model structs and map keys are
// sorted, so equal documents always produce the same text and a diff of two
// versions shows only real changes.
//
// The JSON text is parsed into a YAML node tree rather than decoded into
// generic values because that keeps the key order; the quoting and flow
// styles that came from JSON are then cleared, and the encoder re-adds
// quotes only where a plain scalar would change meaning (for example the
// string "true").
func canonicalYAML(canon []byte) ([]byte, error) {
	var n yaml.Node
	if err := yaml.Unmarshal(canon, &n); err != nil {
		return nil, fmt.Errorf("configsync: render yaml: %w", err)
	}
	plainStyle(&n)
	var buf bytes.Buffer
	enc := yaml.NewEncoder(&buf)
	enc.SetIndent(2)
	if err := enc.Encode(&n); err != nil {
		return nil, fmt.Errorf("configsync: render yaml: %w", err)
	}
	if err := enc.Close(); err != nil {
		return nil, fmt.Errorf("configsync: render yaml: %w", err)
	}
	return buf.Bytes(), nil
}

func plainStyle(n *yaml.Node) {
	n.Style = 0
	for _, c := range n.Content {
		plainStyle(c)
	}
}
