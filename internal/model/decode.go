package model

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"strings"

	"go.yaml.in/yaml/v3"
)

// MaxDocumentSize bounds the accepted document size.
const MaxDocumentSize = 4 << 20

// Decode parses a Policy or Module from YAML or JSON (JSON is valid YAML).
// Unknown fields are rejected, values are normalized (see Normalize) and the
// document is structurally validated. Errors are Issues.
func Decode(data []byte) (Document, error) {
	if len(data) > MaxDocumentSize {
		return nil, Issues{{Message: fmt.Sprintf("document exceeds %d bytes", MaxDocumentSize)}}
	}
	var head struct {
		APIVersion string `yaml:"apiVersion"`
		Kind       Kind   `yaml:"kind"`
	}
	if err := decodeOne(data, &head, false); err != nil {
		return nil, err
	}
	if head.APIVersion != APIVersion {
		return nil, Issues{{Path: "apiVersion", Message: fmt.Sprintf("must be %q", APIVersion)}}
	}
	switch head.Kind {
	case KindPolicy:
		var p Policy
		if err := decodeOne(data, &p, true); err != nil {
			return nil, err
		}
		if err := p.normalize(); err != nil {
			return nil, err
		}
		if err := ValidatePolicy(&p); err != nil {
			return nil, err
		}
		return &p, nil
	case KindModule:
		var m Module
		if err := decodeOne(data, &m, true); err != nil {
			return nil, err
		}
		if err := m.normalize(); err != nil {
			return nil, err
		}
		if err := ValidateModule(&m); err != nil {
			return nil, err
		}
		return &m, nil
	}
	return nil, Issues{{Path: "kind", Message: fmt.Sprintf("must be %q or %q", KindPolicy, KindModule)}}
}

// DecodePolicy decodes data and requires kind Policy.
func DecodePolicy(data []byte) (*Policy, error) {
	doc, err := Decode(data)
	if err != nil {
		return nil, err
	}
	p, ok := doc.(*Policy)
	if !ok {
		return nil, Issues{{Path: "kind", Message: fmt.Sprintf("must be %q", KindPolicy)}}
	}
	return p, nil
}

// DecodeModule decodes data and requires kind Module.
func DecodeModule(data []byte) (*Module, error) {
	doc, err := Decode(data)
	if err != nil {
		return nil, err
	}
	m, ok := doc.(*Module)
	if !ok {
		return nil, Issues{{Path: "kind", Message: fmt.Sprintf("must be %q", KindModule)}}
	}
	return m, nil
}

// Encode returns the canonical JSON form of a document, which Decode reads
// back to an equal document.
func Encode(doc Document) ([]byte, error) {
	b, err := json.Marshal(doc)
	if err != nil {
		return nil, fmt.Errorf("model: encode: %w", err)
	}
	return b, nil
}

func decodeOne(data []byte, v any, strict bool) error {
	dec := yaml.NewDecoder(bytes.NewReader(data))
	dec.KnownFields(strict)
	if err := dec.Decode(v); err != nil {
		if errors.Is(err, io.EOF) {
			return Issues{{Message: "empty document"}}
		}
		return yamlIssues(err)
	}
	var extra yaml.Node
	if err := dec.Decode(&extra); !errors.Is(err, io.EOF) {
		return Issues{{Message: "expected a single document"}}
	}
	return nil
}

func yamlIssues(err error) Issues {
	var te *yaml.TypeError
	if errors.As(err, &te) {
		is := make(Issues, 0, len(te.Errors))
		for _, e := range te.Errors {
			is = append(is, Issue{Message: e})
		}
		return is
	}
	return Issues{{Message: strings.TrimPrefix(err.Error(), "yaml: ")}}
}

func (p *Policy) normalize() error {
	var is Issues
	if p.Spec.Params != nil {
		v, err := Normalize(p.Spec.Params)
		if err != nil {
			is.addf("spec.params", "%v", err)
		} else {
			p.Spec.Params, _ = v.(map[string]any)
		}
	}
	normalizeNodes(p.Spec.Nodes, &is)
	return is.Err()
}

func (m *Module) normalize() error {
	var is Issues
	for i := range m.Spec.Params {
		if !m.Spec.Params[i].HasDefault {
			continue
		}
		v, err := Normalize(m.Spec.Params[i].Default)
		if err != nil {
			is.addf(fmt.Sprintf("spec.params[%d].default", i), "%v", err)
			continue
		}
		m.Spec.Params[i].Default = v
	}
	normalizeNodes(m.Spec.Nodes, &is)
	return is.Err()
}

func normalizeNodes(nodes []Node, is *Issues) {
	for i := range nodes {
		if nodes[i].Props == nil {
			continue
		}
		v, err := Normalize(nodes[i].Props)
		if err != nil {
			is.addf(fmt.Sprintf("spec.nodes[%d].props", i), "%v", err)
			continue
		}
		nodes[i].Props, _ = v.(map[string]any)
	}
}
