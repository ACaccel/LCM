package model

import (
	"fmt"
	"regexp"
	"sort"
)

// Limits on document contents.
const (
	MaxNodes       = 2000
	MaxRetries     = 100
	maxNameLen     = 63
	maxLabelKeyLen = 63
	maxLabelValLen = 255

	// MaxAnnotationsSize bounds the summed length of all annotation keys and
	// values of one document.
	MaxAnnotationsSize = 64 << 10
	maxAnnotationKey   = 253
)

var (
	nameRe     = regexp.MustCompile(`^[a-z]([a-z0-9-]*[a-z0-9])?$`)
	nodeIDRe   = regexp.MustCompile(`^[a-z][a-z0-9_]*$`)
	typeRe     = regexp.MustCompile(`^[a-z][a-z0-9_.-]*$`)
	portRe     = regexp.MustCompile(`^[a-z][a-z0-9_-]*$`)
	paramRe    = regexp.MustCompile(`^[A-Za-z_][A-Za-z0-9_]*$`)
	labelKeyRe = regexp.MustCompile(`^[A-Za-z0-9]([A-Za-z0-9._/-]*[A-Za-z0-9])?$`)
)

// ValidatePolicy checks the structure of a decoded policy. It does not know
// node types beyond the module markers; type-aware checks happen when the
// engine compiles the policy.
func ValidatePolicy(p *Policy) error {
	var is Issues
	validateHeader(&p.Header, KindPolicy, &is)
	s := &p.Spec
	switch s.Concurrency.Mode {
	case "", ConcurrencyQueue, ConcurrencyDrop, ConcurrencyReplace, ConcurrencyParallel:
	default:
		is.addf("spec.concurrency.mode", "must be one of queue, drop, replace, parallel")
	}
	switch {
	case s.Concurrency.Limit < 0:
		is.addf("spec.concurrency.limit", "must not be negative")
	case s.Concurrency.Limit > 0 && s.Concurrency.Mode != ConcurrencyParallel:
		is.addf("spec.concurrency.limit", "applies only to parallel mode")
	case s.Concurrency.Limit == 0 && s.Concurrency.Mode == ConcurrencyParallel:
		is.addf("spec.concurrency.limit", "is required for parallel mode")
	}
	if s.Timeout < 0 {
		is.addf("spec.timeout", "must not be negative")
	}
	switch s.OnInterrupt {
	case "", OnInterruptResume, OnInterruptAbort:
	default:
		is.addf("spec.onInterrupt", "must be resume or abort")
	}
	validateNodes(s.Nodes, &is)
	for i, n := range s.Nodes {
		if n.Type == TypeModuleInput || n.Type == TypeModuleOutput {
			is.addf(fmt.Sprintf("spec.nodes[%d].type", i), "%s is only allowed in modules", n.Type)
		}
	}
	return is.Err()
}

// ValidateModule checks the structure of a decoded module.
func ValidateModule(m *Module) error {
	var is Issues
	validateHeader(&m.Header, KindModule, &is)
	seen := map[string]int{}
	for i, p := range m.Spec.Params {
		path := fmt.Sprintf("spec.params[%d].name", i)
		switch {
		case !paramRe.MatchString(p.Name):
			is.addf(path, "must be an identifier (letters, digits, underscore)")
		default:
			if j, dup := seen[p.Name]; dup {
				is.addf(path, "duplicate parameter %q (first at spec.params[%d])", p.Name, j)
			} else {
				seen[p.Name] = i
			}
		}
	}
	validateNodes(m.Spec.Nodes, &is)
	inputs, outputs := 0, 0
	for _, n := range m.Spec.Nodes {
		switch n.Type {
		case TypeModuleInput:
			inputs++
		case TypeModuleOutput:
			outputs++
		}
	}
	if inputs != 1 {
		is.addf("spec.nodes", "a module needs exactly one %s node, found %d", TypeModuleInput, inputs)
	}
	if outputs < 1 {
		is.addf("spec.nodes", "a module needs at least one %s node", TypeModuleOutput)
	}
	return is.Err()
}

func validateHeader(h *Header, kind Kind, is *Issues) {
	if h.APIVersion != APIVersion {
		is.addf("apiVersion", "must be %q", APIVersion)
	}
	if h.Kind != kind {
		is.addf("kind", "must be %q", kind)
	}
	switch {
	case h.Metadata.Name == "":
		is.addf("metadata.name", "is required")
	case len(h.Metadata.Name) > maxNameLen || !nameRe.MatchString(h.Metadata.Name):
		is.addf("metadata.name", "must be 1-%d lowercase letters, digits or '-', starting with a letter", maxNameLen)
	}
	for _, k := range sortedKeys(h.Metadata.Labels) {
		path := "metadata.labels." + k
		if len(k) > maxLabelKeyLen || !labelKeyRe.MatchString(k) {
			is.addf(path, "invalid label key")
		}
		if len(h.Metadata.Labels[k]) > maxLabelValLen {
			is.addf(path, "label value exceeds %d characters", maxLabelValLen)
		}
	}
	size := 0
	for _, k := range sortedKeys(h.Metadata.Annotations) {
		if len(k) > maxAnnotationKey || !labelKeyRe.MatchString(k) {
			is.addf("metadata.annotations."+k, "invalid annotation key")
		}
		size += len(k) + len(h.Metadata.Annotations[k])
	}
	if size > MaxAnnotationsSize {
		is.addf("metadata.annotations", "total size of keys and values exceeds %d bytes", MaxAnnotationsSize)
	}
}

func validateNodes(nodes []Node, is *Issues) {
	if len(nodes) == 0 {
		is.addf("spec.nodes", "at least one node is required")
		return
	}
	if len(nodes) > MaxNodes {
		is.addf("spec.nodes", "at most %d nodes are allowed", MaxNodes)
		return
	}
	ids := map[string]int{}
	for i, n := range nodes {
		path := fmt.Sprintf("spec.nodes[%d].id", i)
		switch {
		case n.ID == "":
			is.addf(path, "is required")
		case !nodeIDRe.MatchString(n.ID):
			is.addf(path, "must match %s", nodeIDRe)
		default:
			if j, dup := ids[n.ID]; dup {
				is.addf(path, "duplicate id %q (first at spec.nodes[%d])", n.ID, j)
			} else {
				ids[n.ID] = i
			}
		}
	}
	for i, n := range nodes {
		base := fmt.Sprintf("spec.nodes[%d]", i)
		switch {
		case n.Type == "":
			is.addf(base+".type", "is required")
		case !typeRe.MatchString(n.Type):
			is.addf(base+".type", "invalid node type %q", n.Type)
		}
		for _, port := range sortedKeys(n.Next) {
			ppath := base + ".next." + port
			if !portRe.MatchString(port) {
				is.addf(ppath, "invalid port name %q", port)
			}
			targets := map[string]bool{}
			for j, t := range n.Next[port] {
				tpath := fmt.Sprintf("%s[%d]", ppath, j)
				switch _, ok := ids[t]; {
				case !ok:
					is.addf(tpath, "unknown node %q", t)
				case t == n.ID:
					is.addf(tpath, "a node cannot connect to itself")
				case targets[t]:
					is.addf(tpath, "duplicate edge to %q", t)
				}
				targets[t] = true
			}
		}
		if n.Timeout < 0 {
			is.addf(base+".timeout", "must not be negative")
		}
		if r := n.Retry; r != nil {
			if r.Max < 0 || r.Max > MaxRetries {
				is.addf(base+".retry.max", "must be between 0 and %d", MaxRetries)
			}
			if r.Backoff < 0 {
				is.addf(base+".retry.backoff", "must not be negative")
			}
			if r.MaxBackoff < 0 {
				is.addf(base+".retry.maxBackoff", "must not be negative")
			} else if r.MaxBackoff > 0 && r.MaxBackoff < r.Backoff {
				is.addf(base+".retry.maxBackoff", "must not be less than backoff")
			}
		}
		switch n.JoinMode {
		case "", JoinAll, JoinAny:
		default:
			is.addf(base+".joinMode", "must be all or any")
		}
	}
}

func sortedKeys[V any](m map[string]V) []string {
	keys := make([]string, 0, len(m))
	for k := range m {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	return keys
}
