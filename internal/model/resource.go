package model

import (
	"bytes"
	"fmt"
	"slices"
	"strings"

	"go.yaml.in/yaml/v3"
)

// Relation types between resources.
const (
	// RelationDependsOn is an availability dependency on the target.
	RelationDependsOn = "dependsOn"
	// RelationRunsOn means the target hosts this resource.
	RelationRunsOn = "runsOn"
	// RelationControlPath means LCM reaches this resource through the target.
	RelationControlPath = "controlPath"
)

// MaxRelations bounds the relations of one resource.
const MaxRelations = 100

// Resource describes one managed object. Spec.Type selects the config struct
// and the actions LCM offers; checks and relations are shared by all types.
type Resource struct {
	Header `yaml:",inline"`
	Spec   ResourceSpec `yaml:"spec" json:"spec"`
}

// DocKind implements Document.
func (r *Resource) DocKind() Kind { return KindResource }

// DocName implements Document.
func (r *Resource) DocName() string { return r.Metadata.Name }

// ResourceSpec is the body of a Resource.
type ResourceSpec struct {
	Type    string `yaml:"type" json:"type"`
	Adapter string `yaml:"adapter" json:"adapter"`
	// Connection names a host-configured connection; only adapters that
	// need one accept it. A document can name a connection but never widen
	// what the connection allows.
	Connection string         `yaml:"connection,omitempty" json:"connection,omitempty"`
	Config     ResourceConfig `yaml:"config" json:"config"`
	Checks     []Check        `yaml:"checks,omitempty" json:"checks,omitempty"`
	Relations  []Relation     `yaml:"relations,omitempty" json:"relations,omitempty"`
}

// Relation declares how a resource relates to another one, named by
// Target. The target may not exist yet.
type Relation struct {
	Type   string `yaml:"type" json:"type"`
	Target string `yaml:"target" json:"target"`
}

// Service returns the config of a service resource.
func (r *Resource) Service() (*ServiceConfig, bool) {
	c, ok := r.Spec.Config.(*ServiceConfig)
	return c, ok && r.Spec.Type == ResourceService
}

// Machine returns the config of a machine resource.
func (r *Resource) Machine() (*MachineConfig, bool) {
	c, ok := r.Spec.Config.(*MachineConfig)
	return c, ok && r.Spec.Type == ResourceMachine
}

// Router returns the config of a router resource.
func (r *Resource) Router() (*RouterConfig, bool) {
	c, ok := r.Spec.Config.(*RouterConfig)
	return c, ok && r.Spec.Type == ResourceRouter
}

// UnmarshalJSON decodes a spec strictly, choosing the config struct by type.
// Values are normalized but not validated; use DecodeResource for documents.
func (s *ResourceSpec) UnmarshalJSON(b []byte) error {
	return s.decode(b)
}

// UnmarshalYAML decodes a spec strictly, like UnmarshalJSON.
func (s *ResourceSpec) UnmarshalYAML(value *yaml.Node) error {
	// Node.Decode would not reject unknown fields, so the node is decoded
	// again from text by the strict decoder.
	b, err := yaml.Marshal(value)
	if err != nil {
		return err
	}
	return s.decode(b)
}

func (s *ResourceSpec) decode(b []byte) error {
	var head struct {
		Type string `yaml:"type"`
	}
	if err := decodeOne(b, &head, false); err != nil {
		return err
	}
	spec, err := codecOf(head.Type).decodeSpec(b)
	if err != nil {
		return err
	}
	var is Issues
	spec.normalize(&is)
	*s = spec
	return is.Err()
}

func codecOf(typ string) configCodec {
	if t, ok := lookupType(typ); ok {
		return t.codec
	}
	return codec[unknownConfig, *unknownConfig]{}
}

// DecodeResource decodes data and requires kind Resource.
func DecodeResource(data []byte) (*Resource, error) {
	doc, err := Decode(data)
	if err != nil {
		return nil, err
	}
	r, ok := doc.(*Resource)
	if !ok {
		return nil, Issues{{Path: "kind", Message: fmt.Sprintf("must be %q", KindResource)}}
	}
	return r, nil
}

func decodeResource(data []byte) (*Resource, error) {
	var head struct {
		Spec struct {
			Type string `yaml:"type"`
		} `yaml:"spec"`
	}
	if err := decodeOne(data, &head, false); err != nil {
		return nil, err
	}
	r, err := codecOf(head.Spec.Type).decodeDocument(data)
	if err != nil {
		return nil, err
	}
	var is Issues
	r.Spec.normalize(&is)
	if err := is.Err(); err != nil {
		return nil, err
	}
	if err := ValidateResource(r); err != nil {
		return nil, err
	}
	return r, nil
}

func (s *ResourceSpec) normalize(is *Issues) {
	for i := range s.Checks {
		if s.Checks[i].Props == nil {
			continue
		}
		v, err := Normalize(s.Checks[i].Props)
		if err != nil {
			is.addf(fmt.Sprintf("spec.checks[%d].props", i), "%v", err)
			continue
		}
		s.Checks[i].Props, _ = v.(map[string]any)
	}
	if s.Config != nil {
		s.Config.normalize(s.Adapter)
	}
}

// ValidateResource checks the structure of a decoded resource, including
// its typed config. Relation targets are not resolved here: whether they
// exist and whether they form a cycle depends on the other resources.
func ValidateResource(r *Resource) error {
	var is Issues
	validateHeader(&r.Header, KindResource, &is)
	sp := &r.Spec
	t, known := lookupType(sp.Type)
	adapterOK := false
	switch {
	case sp.Type == "":
		is.addf("spec.type", "is required")
	case !known:
		is.addf("spec.type", "must be one of %s", strings.Join(typeNames(), ", "))
	case sp.Adapter == "":
		is.addf("spec.adapter", "is required")
	case !slices.Contains(t.Adapters, sp.Adapter):
		is.addf("spec.adapter", "must be one of %s for type %s", strings.Join(t.Adapters, ", "), sp.Type)
	default:
		adapterOK = true
	}
	if known {
		switch {
		case t.NeedsConnection && sp.Connection == "":
			is.addf("spec.connection", "is required for type %s", sp.Type)
		case !t.NeedsConnection && sp.Connection != "":
			is.addf("spec.connection", "is not valid for type %s", sp.Type)
		case sp.Connection != "" && (len(sp.Connection) > maxNameLen || !nameRe.MatchString(sp.Connection)):
			is.addf("spec.connection", "must be a connection name of lowercase letters, digits or '-'")
		}
	}
	// The config of an unknown type or adapter cannot be judged; the issue
	// above already explains what to fix first.
	if adapterOK {
		if sp.Config == nil {
			is.addf("spec.config", "is required")
		} else {
			sp.Config.validate(sp.Adapter, "spec.config", &is)
		}
		if sp.Type == ResourceService && sp.Adapter == string(RuntimeManaged) {
			validateManagedName(r.Metadata.Name, &is)
		}
	}
	validateChecks(sp.Checks, "spec.checks", &is)
	validateRelations(sp.Relations, r.Metadata.Name, &is)
	return is.Err()
}

func validateRelations(rels []Relation, self string, is *Issues) {
	if len(rels) > MaxRelations {
		is.addf("spec.relations", "at most %d relations are allowed", MaxRelations)
	}
	seen := map[Relation]bool{}
	for i, rel := range rels {
		p := fmt.Sprintf("spec.relations[%d]", i)
		switch rel.Type {
		case RelationDependsOn, RelationRunsOn, RelationControlPath:
		case "":
			is.addf(p+".type", "is required")
		default:
			is.addf(p+".type", "must be one of %s, %s, %s", RelationDependsOn, RelationRunsOn, RelationControlPath)
		}
		switch {
		case !nameRe.MatchString(rel.Target) || len(rel.Target) > maxNameLen:
			is.addf(p+".target", "invalid resource name %q", rel.Target)
		case rel.Target == self:
			is.addf(p+".target", "a resource cannot relate to itself")
		case seen[rel]:
			is.addf(p, "duplicate relation %s %s", rel.Type, rel.Target)
		}
		seen[rel] = true
	}
}

// EncodeYAML renders the canonical form of a document as block style YAML.
// Field order is the one of the model structs and map keys are sorted, so
// equal documents always produce the same text.
func EncodeYAML(doc Document) ([]byte, error) {
	canon, err := Encode(doc)
	if err != nil {
		return nil, err
	}
	// Parsing the JSON text into a node tree keeps the key order; clearing
	// the styles that came from JSON lets the encoder quote only where a
	// plain scalar would change meaning.
	var n yaml.Node
	if err := yaml.Unmarshal(canon, &n); err != nil {
		return nil, fmt.Errorf("model: render yaml: %w", err)
	}
	plainStyle(&n)
	var buf bytes.Buffer
	enc := yaml.NewEncoder(&buf)
	enc.SetIndent(2)
	if err := enc.Encode(&n); err != nil {
		return nil, fmt.Errorf("model: render yaml: %w", err)
	}
	if err := enc.Close(); err != nil {
		return nil, fmt.Errorf("model: render yaml: %w", err)
	}
	return buf.Bytes(), nil
}

func plainStyle(n *yaml.Node) {
	n.Style = 0
	for _, c := range n.Content {
		plainStyle(c)
	}
}
