package model

import (
	"bytes"
	"errors"
	"fmt"
	"io"
	"strings"

	"go.yaml.in/yaml/v3"
)

// Kinds that only exist in lcm/v1 documents.
const (
	kindServiceV1 Kind = "Service"
	kindMachineV1 Kind = "Machine"
)

// v1Service is the lcm/v1 Service document.
type v1Service struct {
	Header `yaml:",inline"`
	Spec   struct {
		Host      string             `yaml:"host,omitempty"`
		Runtime   Runtime            `yaml:"runtime"`
		Source    *Source            `yaml:"source,omitempty"`
		Backup    *Command           `yaml:"backup,omitempty"`
		Jobs      map[string]Command `yaml:"jobs,omitempty"`
		DependsOn []string           `yaml:"dependsOn,omitempty"`
		Checks    []Check            `yaml:"checks,omitempty"`
	} `yaml:"spec"`
}

// v1Machine is the lcm/v1 Machine document.
type v1Machine struct {
	Header `yaml:",inline"`
	Spec   struct {
		Host       string        `yaml:"host,omitempty"`
		Monitoring MachineConfig `yaml:"monitoring,omitempty"`
	} `yaml:"spec"`
}

// ConvertV1 converts lcm/v1 Service, Machine, Policy and Module documents to
// lcm/v2. data holds one or more YAML documents or one JSON document;
// documents already at lcm/v2 are decoded unchanged. The result is decoded
// and validated like any v2 document. Warnings name expressions that still
// refer to the removed service variable after the automatic rewrite and
// need a manual change. An error lists the problems of every invalid
// document, each prefixed with its position; the documents that did convert
// are still returned.
func ConvertV1(data []byte) ([]Document, []string, error) {
	parts, err := splitDocuments(data)
	if err != nil {
		return nil, nil, err
	}
	var (
		docs  []Document
		warns []string
		errs  []error
	)
	for i, part := range parts {
		doc, w, err := convertOne(part, "")
		if err != nil {
			errs = append(errs, fmt.Errorf("document %d: %w", i+1, err))
			continue
		}
		docs = append(docs, doc)
		warns = append(warns, w...)
	}
	return docs, warns, errors.Join(errs...)
}

// ConvertServiceV1JSON converts one lcm/v1 Service document to the
// canonical JSON (Encode output) of the equivalent Resource.
func ConvertServiceV1JSON(data []byte) ([]byte, []string, error) {
	return convertJSON(data, kindServiceV1)
}

// ConvertMachineV1JSON converts one lcm/v1 Machine document to the
// canonical JSON of the equivalent Resource.
func ConvertMachineV1JSON(data []byte) ([]byte, []string, error) {
	return convertJSON(data, kindMachineV1)
}

// ConvertPolicyV1JSON converts one lcm/v1 Policy document to canonical
// lcm/v2 JSON; see ConvertV1 for the warnings.
func ConvertPolicyV1JSON(data []byte) ([]byte, []string, error) {
	return convertJSON(data, KindPolicy)
}

// ConvertModuleV1JSON converts one lcm/v1 Module document to canonical
// lcm/v2 JSON; see ConvertV1 for the warnings.
func ConvertModuleV1JSON(data []byte) ([]byte, []string, error) {
	return convertJSON(data, KindModule)
}

func convertJSON(data []byte, want Kind) ([]byte, []string, error) {
	doc, warns, err := convertOne(data, want)
	if err != nil {
		return nil, warns, err
	}
	b, err := Encode(doc)
	return b, warns, err
}

func splitDocuments(data []byte) ([][]byte, error) {
	dec := yaml.NewDecoder(bytes.NewReader(data))
	var out [][]byte
	for {
		var n yaml.Node
		err := dec.Decode(&n)
		if errors.Is(err, io.EOF) {
			break
		}
		if err != nil {
			return nil, yamlIssues(err)
		}
		if len(n.Content) == 0 {
			continue
		}
		b, err := yaml.Marshal(&n)
		if err != nil {
			return nil, fmt.Errorf("model: split documents: %w", err)
		}
		out = append(out, b)
	}
	if len(out) == 0 {
		return nil, Issues{{Message: "empty document"}}
	}
	return out, nil
}

// convertOne converts a single document; want, when set, is the v1 kind the
// caller requires.
func convertOne(data []byte, want Kind) (Document, []string, error) {
	if len(data) > MaxDocumentSize {
		return nil, nil, Issues{{Message: fmt.Sprintf("document exceeds %d bytes", MaxDocumentSize)}}
	}
	var head struct {
		APIVersion string `yaml:"apiVersion"`
		Kind       Kind   `yaml:"kind"`
	}
	if err := decodeOne(data, &head, false); err != nil {
		return nil, nil, err
	}
	if head.APIVersion == APIVersion && want == "" {
		doc, err := Decode(data)
		return doc, nil, err
	}
	if head.APIVersion != APIVersionV1 {
		return nil, nil, Issues{{Path: "apiVersion", Message: fmt.Sprintf("must be %q or %q", APIVersionV1, APIVersion)}}
	}
	if want != "" && head.Kind != want {
		return nil, nil, Issues{{Path: "kind", Message: fmt.Sprintf("must be %q", want)}}
	}
	var (
		next  Document
		warns []string
		err   error
	)
	switch head.Kind {
	case kindServiceV1:
		next, err = convertService(data)
	case kindMachineV1:
		next, err = convertMachine(data)
	case KindPolicy:
		var p Policy
		if err = decodeOne(data, &p, true); err == nil {
			p.APIVersion = APIVersion
			warns = rewriteNodes(p.Spec.Nodes, string(KindPolicy)+" "+p.Metadata.Name)
			next = &p
		}
	case KindModule:
		var m Module
		if err = decodeOne(data, &m, true); err == nil {
			m.APIVersion = APIVersion
			warns = rewriteNodes(m.Spec.Nodes, string(KindModule)+" "+m.Metadata.Name)
			next = &m
		}
	default:
		err = Issues{{Path: "kind", Message: fmt.Sprintf("must be %q, %q, %q or %q", kindServiceV1, kindMachineV1, KindPolicy, KindModule)}}
	}
	if err != nil {
		return nil, warns, err
	}
	// The encoded v2 form goes through the normal decoder so that the
	// result is normalized and validated exactly like a document written
	// by hand.
	b, err := Encode(next)
	if err != nil {
		return nil, warns, err
	}
	doc, err := Decode(b)
	return doc, warns, err
}

func convertService(data []byte) (*Resource, error) {
	var s v1Service
	if err := decodeOne(data, &s, true); err != nil {
		return nil, err
	}
	if s.Spec.Host != "" && s.Spec.Host != AdapterLocal {
		return nil, Issues{{Path: "spec.host", Message: fmt.Sprintf("only %q is supported", AdapterLocal)}}
	}
	rt := s.Spec.Runtime
	cfg := &ServiceConfig{
		Unit: rt.Unit, Scope: rt.Scope,
		Container: rt.Container,
		Project:   rt.Project, File: rt.File,
		Start: rt.Start, Stop: rt.Stop, Restart: rt.Restart, Status: rt.Status,
		Source: s.Spec.Source, Backup: s.Spec.Backup, Jobs: s.Spec.Jobs,
	}
	var rels []Relation
	for _, d := range s.Spec.DependsOn {
		rels = append(rels, Relation{Type: RelationDependsOn, Target: d})
	}
	return &Resource{
		Header: v2Header(s.Header),
		Spec: ResourceSpec{
			Type: ResourceService, Adapter: string(rt.Type),
			Config: cfg, Checks: s.Spec.Checks, Relations: rels,
		},
	}, nil
}

func convertMachine(data []byte) (*Resource, error) {
	var m v1Machine
	if err := decodeOne(data, &m, true); err != nil {
		return nil, err
	}
	if m.Spec.Host != "" && m.Spec.Host != AdapterLocal {
		return nil, Issues{{Path: "spec.host", Message: "only local is supported"}}
	}
	cfg := m.Spec.Monitoring
	return &Resource{
		Header: v2Header(m.Header),
		Spec:   ResourceSpec{Type: ResourceMachine, Adapter: AdapterLocal, Config: &cfg},
	}, nil
}

func v2Header(h Header) Header {
	return Header{APIVersion: APIVersion, Kind: KindResource, Metadata: h.Metadata}
}

// rewriteNodes applies the v2 renames to the nodes of a policy or module in
// place and returns warnings for references it could not rewrite. owner
// names the document in warnings.
func rewriteNodes(nodes []Node, owner string) []string {
	var warns []string
	// Lifecycle nodes renamed the service key of their output (ActionResult
	// and Plan) to resource, so references to it from other nodes of the
	// same document (nodes.<id>.service) follow.
	lifecycle := map[string]bool{}
	for _, n := range nodes {
		if strings.HasPrefix(n.Type, "lifecycle.") {
			lifecycle[n.ID] = true
		}
	}
	var acknowledged []string
	for i := range nodes {
		n := &nodes[i]
		base := fmt.Sprintf("spec.nodes[%d].props", i)
		if guardedLifecycleNodes[n.Type] {
			if n.Props == nil {
				n.Props = map[string]any{}
			}
			if _, set := n.Props["acknowledgeImpact"]; !set {
				n.Props["acknowledgeImpact"] = true
				acknowledged = append(acknowledged, n.ID)
			}
		}
		if n.Props == nil {
			continue
		}
		switch {
		case strings.HasPrefix(n.Type, "lifecycle."):
			warns = renameProp(n.Props, "service", "resource", base, owner, warns)
		case n.Type == "trigger.signal":
			warns = renameProp(n.Props, "services", "resources", base, owner, warns)
		case n.Type == "lock" || n.Type == "unlock":
			warns = renameLock(n.Props, base, owner, warns)
		}
		r := rewriter{owner: owner, nodeType: n.Type, lifecycle: lifecycle}
		for _, k := range sortedKeys(n.Props) {
			n.Props[k] = r.value(n.Props[k], base+"."+k, k)
		}
		warns = append(warns, r.warns...)
	}
	if len(acknowledged) > 0 {
		warns = append(warns, fmt.Sprintf("%s: added acknowledgeImpact: true to lifecycle nodes %s to keep their v1 behavior, which had no impact guard; "+
			"remove it where an action on a resource with dependents should stop the node", owner, strings.Join(acknowledged, ", ")))
	}
	return warns
}

// guardedLifecycleNodes are the lifecycle node types whose action the
// resource impact guard applies to (the disruptive service actions of
// catalog). A v1 dependsOn becomes a relation, so without an added
// acknowledgeImpact these nodes would start to fail permanently on
// services that other services depend on.
var guardedLifecycleNodes = map[string]bool{
	"lifecycle.stop": true, "lifecycle.restart": true, "lifecycle.deploy": true,
	"lifecycle.rollback": true, "lifecycle.run-job": true,
}

// Lock name prefixes: lifecycle actions hold "service:<name>" in v1 and
// "resource:<name>" in v2; the stored lock rows were renamed alike.
const (
	lockPrefixV1 = "service:"
	lockPrefixV2 = "resource:"
)

// renameLock moves the name of a lock or unlock node that shares the action
// lock of a service to the v2 prefix. A literal prefix is renamed, also in
// front of a template; a name computed by an expression cannot be, so it is
// reported when it mentions the old prefix.
func renameLock(props map[string]any, base, owner string, warns []string) []string {
	name, ok := props["name"].(string)
	switch {
	case !ok:
		return warns
	case strings.HasPrefix(name, lockPrefixV1):
		props["name"] = lockPrefixV2 + strings.TrimPrefix(name, lockPrefixV1)
	case strings.Contains(name, "{{") && strings.Contains(name, lockPrefixV1):
		warns = append(warns, fmt.Sprintf("%s: %s.name: lock name may build the old action lock name %q; lifecycle actions now hold %q: %s",
			owner, base, lockPrefixV1+"<name>", lockPrefixV2+"<name>", name))
	}
	return warns
}

func renameProp(props map[string]any, from, to, base, owner string, warns []string) []string {
	v, ok := props[from]
	if !ok {
		return warns
	}
	if _, clash := props[to]; clash {
		return append(warns, fmt.Sprintf("%s: %s: both %q and %q are set; remove %q", owner, base, from, to, from))
	}
	props[to] = v
	delete(props, from)
	return warns
}

// rewriter renames the service expression variable inside the string props
// of one node. Strings are templates whose {{ }} segments are expressions,
// except the fields that hold a bare expression.
type rewriter struct {
	owner    string
	nodeType string
	// lifecycle holds the ids of the document's lifecycle nodes.
	lifecycle map[string]bool
	warns     []string
}

// value rewrites v; rel is its path relative to the node's props, used to
// recognize the bare expression fields.
func (r *rewriter) value(v any, path, rel string) any {
	switch x := v.(type) {
	case string:
		out, left := r.text(x, rel)
		if left {
			r.warns = append(r.warns, fmt.Sprintf("%s: %s: expression still refers to %q and needs a manual change: %s", r.owner, path, "service", out))
		}
		return out
	case map[string]any:
		for _, k := range sortedKeys(x) {
			x[k] = r.value(x[k], path+"."+k, rel+"."+k)
		}
		return x
	case []any:
		for i := range x {
			x[i] = r.value(x[i], fmt.Sprintf("%s[%d]", path, i), fmt.Sprintf("%s[]", rel))
		}
		return x
	}
	return v
}

func (r *rewriter) text(s, rel string) (string, bool) {
	// The only bare expression props of the built-in nodes: flow.if
	// condition and flow.switch case conditions.
	if (r.nodeType == "flow.if" && rel == "condition") || (r.nodeType == "flow.switch" && rel == "cases[].when") {
		return rewriteExpr(s, r.lifecycle)
	}
	return rewriteTemplate(s, r.lifecycle)
}

// rewriteTemplate rewrites every {{ }} segment of s. A malformed template is
// returned unchanged: the policy compiler reports it with a better message.
func rewriteTemplate(s string, lifecycle map[string]bool) (string, bool) {
	if !strings.Contains(s, "{{") {
		return s, false
	}
	var b strings.Builder
	left := false
	rest := s
	for {
		start := strings.Index(rest, "{{")
		if start < 0 {
			b.WriteString(rest)
			break
		}
		body := rest[start+2:]
		end, ok := templateEnd(body)
		if !ok {
			return s, false
		}
		expr, l := rewriteExpr(body[:end], lifecycle)
		left = left || l
		b.WriteString(rest[:start+2])
		b.WriteString(expr)
		b.WriteString("}}")
		rest = body[end+2:]
	}
	return b.String(), left
}

// templateEnd returns the offset of the "}}" closing a template segment,
// skipping string literals and balanced braces like the expression engine.
func templateEnd(s string) (int, bool) {
	depth := 0
	for i := 0; i < len(s); i++ {
		switch c := s[i]; c {
		case '"', '\'', '`':
			j := literalEnd(s, i)
			if j < 0 {
				return 0, false
			}
			i = j
		case '{':
			depth++
		case '}':
			if depth == 0 {
				return i, i+1 < len(s) && s[i+1] == '}'
			}
			depth--
		}
	}
	return 0, false
}

// literalEnd returns the index of the quote closing the string literal that
// starts at s[i], or -1.
func literalEnd(s string, i int) int {
	q := s[i]
	for j := i + 1; j < len(s); j++ {
		if s[j] == '\\' && q != '`' {
			j++
			continue
		}
		if s[j] == q {
			return j
		}
	}
	return -1
}

// rewriteExpr renames the service variable in one expression: service[...]
// and service.x become resource[...] and resource.x, and trigger.service
// (the signal field, also as trigger?.service or trigger["service"])
// becomes trigger.resource. Member accesses such as params.service and
// string literals are left alone. The boolean reports what the rewrite
// cannot handle: a remaining use of service as a variable, or a service
// member of anything but params, which may be a field renamed to resource.
//
// nodes.<id>.service becomes nodes.<id>.resource when <id> is in
// lifecycle, the ids of the document's lifecycle nodes.
func rewriteExpr(s string, lifecycle map[string]bool) (string, bool) {
	var b strings.Builder
	left := false
	for i := 0; i < len(s); {
		c := s[i]
		switch {
		case c == '"' || c == '\'' || c == '`':
			j := literalEnd(s, i)
			if j < 0 {
				b.WriteString(s[i:])
				return b.String(), left
			}
			if s[i+1:j] == "service" && strings.HasSuffix(s[:i], "[") && strings.HasPrefix(s[j+1:], "]") && !onParams(s[:i-1]) {
				left = true
			}
			b.WriteString(s[i : j+1])
			i = j + 1
		case isIdentStart(c) || isDigit(c):
			j := i
			for j < len(s) && (isIdentStart(s[j]) || isDigit(s[j])) {
				j++
			}
			word, next := s[i:j], s[j:]
			member := i > 0 && s[i-1] == '.'
			switch {
			case member && word == "service":
				b.WriteString(word)
				left = left || !onParams(strings.TrimSuffix(s[:i-1], "?"))
			case member || isDigit(c):
				b.WriteString(word)
			case word == "service" && (strings.HasPrefix(next, "[") || strings.HasPrefix(next, ".") || strings.HasPrefix(next, "?.")):
				b.WriteString("resource")
			case word == "service":
				b.WriteString(word)
				left = true
			case word == "trigger":
				b.WriteString(word)
				if key, n := accessor(next); key == "service" {
					b.WriteString(strings.Replace(next[:n], "service", "resource", 1))
					j += n
				}
			case word == "nodes":
				out, n := rewriteNodeRef(next, lifecycle)
				b.WriteString(word)
				b.WriteString(out)
				j += n
			default:
				b.WriteString(word)
			}
			i = j
		default:
			b.WriteByte(c)
			i++
		}
	}
	return b.String(), left
}

// rewriteNodeRef reads the accessors that follow the nodes variable and
// renames the service key of a lifecycle node's output: in
// `.restart.service` (or with ?. or ["..."] accessors) the second accessor
// becomes resource. It returns the replacement for the n bytes of s it
// consumed; n is 0 when s does not have that shape.
func rewriteNodeRef(s string, lifecycle map[string]bool) (string, int) {
	id, at := accessor(s)
	if at == 0 || !lifecycle[id] {
		return "", 0
	}
	key, keyLen := accessor(s[at:])
	if keyLen == 0 || key != "service" {
		return "", 0
	}
	raw := s[at : at+keyLen]
	return s[:at] + strings.Replace(raw, "service", "resource", 1), at + keyLen
}

// onParams reports whether the expression text before a member access ends
// with the params variable itself, whose service member is a template
// parameter and was not renamed.
func onParams(before string) bool {
	rest, ok := strings.CutSuffix(before, "params")
	if !ok {
		return false
	}
	return rest == "" || (!isIdentStart(rest[len(rest)-1]) && !isDigit(rest[len(rest)-1]) && rest[len(rest)-1] != '.')
}

// accessor reads one member access at the start of s: .name, ?.name,
// ["name"], ['name'] or the optional ?.["name"]. It returns the member name
// and the length of the access, or 0 when s does not start with one. A
// dotted name must end at an identifier boundary, so .serviceX is the
// member serviceX.
func accessor(s string) (string, int) {
	switch {
	case strings.HasPrefix(s, "?.["):
		name, n := accessor(s[2:])
		if n == 0 {
			return "", 0
		}
		return name, n + 2
	case strings.HasPrefix(s, "?."):
		name, n := ident(s[2:])
		if n == 0 {
			return "", 0
		}
		return name, n + 2
	case strings.HasPrefix(s, "."):
		name, n := ident(s[1:])
		if n == 0 {
			return "", 0
		}
		return name, n + 1
	case len(s) > 2 && s[0] == '[' && (s[1] == '"' || s[1] == '\''):
		end := strings.IndexByte(s[2:], s[1])
		if end < 0 || len(s) < end+4 || s[end+3] != ']' {
			return "", 0
		}
		return s[2 : end+2], end + 4
	}
	return "", 0
}

// ident returns the identifier at the start of s and its length.
func ident(s string) (string, int) {
	if s == "" || !isIdentStart(s[0]) {
		return "", 0
	}
	j := 1
	for j < len(s) && (isIdentStart(s[j]) || isDigit(s[j])) {
		j++
	}
	return s[:j], j
}

func isIdentStart(c byte) bool {
	return c == '_' || (c >= 'a' && c <= 'z') || (c >= 'A' && c <= 'Z')
}

func isDigit(c byte) bool { return c >= '0' && c <= '9' }
