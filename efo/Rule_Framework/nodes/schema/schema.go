/*
Package schema derives a design-time field schema from the Go types a node
declares, so the GUI never has to hard-code field names.

The types cannot be reached from a constructed node value (a *If.If embeds only
FunctionNode{Description, Config}), so each node registers its InputFormat,
OutputFormat and Property types explicitly and this package reflects over them.
*/
package schema

import (
	"reflect"
	"strings"

	lcm "1935/efo/LCM_Enabler/interfaces"
	abi "1935/efo/Rule_Framework/interfaces"
)

// Tag names matching what the runtime actually decodes with.
const (
	// PropertyTag is what GetProperty sets as its mapstructure TagName.
	PropertyTag = "yaml"
	// PortTag is mapstructure's default; no node sets it, so port field names
	// are the Go field names verbatim.
	PortTag = "mapstructure"
)

// Registration names the types reflection cannot reach from a node value.
//
// In is nil when the node declares no input format at all; it is a zero value
// of the declared type otherwise, even when that type is empty.
type Registration struct {
	Type string
	In   any
	Out  any
	Prop any
	// InPort and OutPort override the reflected port schema for a node whose
	// payload shape is not a fixed struct, such as If reading and forwarding
	// arbitrary keys. When set they win over In and Out.
	InPort  *lcm.PortSchema
	OutPort *lcm.PortSchema
	// Passthrough marks a node that emits nothing of its own; see
	// lcm.NodeSchema.Passthrough.
	Passthrough bool
}

// Meta carries what reflection cannot recover: closed value sets living in
// switch statements, which properties belong to which request, and rendering
// hints.
type Meta struct {
	// Discriminator is the property whose value selects which other properties apply.
	Discriminator string
	// Enums maps a property name to its legal values.
	Enums map[string][]string
	// ShowWhen maps a property name to the discriminator values that use it.
	// A listed property is both shown and required for those values.
	ShowWhen map[string][]string
	Required []string
	Secret   []string
	FilePath []string
	// FieldRef lists properties whose value names a payload field, in input
	// port order: the first entry reads port 0, the second port 1. That is what
	// lets Compare's two sides offer the fields of their own side.
	FieldRef []string
	Docs     map[string]string
	// OutputLabels names each output port, e.g. If's true/false branches.
	OutputLabels []string
	// ItemMeta maps a repeatable property to the metadata for one element's
	// fields, e.g. the operator enum inside If's condition list.
	ItemMeta map[string]Meta
}

// Fields reflects a struct value into the field list a design GUI can render.
//
// tagName must match the mapstructure TagName the runtime uses for this struct,
// because that is what decides the map keys. Nested structs are reported as
// "object" and are not expanded: the engine can only address a top-level key,
// so a dotted name would be unusable.
func Fields(v any, tagName string) []lcm.FieldSchema {
	return fieldsOf(v, tagName, false)
}

// PropertyFields reflects a node's Property struct with the tag GetProperty
// decodes by.
//
// Unlike port fields it expands a slice-of-struct one level into Item, so a GUI
// can render a repeatable row editor for a list property such as If's
// conditions. Expanding is safe here and deliberately not done for ports: port
// field names are what an edge's transFormat addresses, and the engine resolves
// only a top-level key, so a nested port name would be unusable.
func PropertyFields(v any) []lcm.FieldSchema {
	return fieldsOf(v, PropertyTag, true)
}

func fieldsOf(v any, tagName string, expand bool) []lcm.FieldSchema {
	fields := []lcm.FieldSchema{}
	if v == nil {
		return fields
	}
	t := reflect.TypeOf(v)
	for t.Kind() == reflect.Ptr {
		t = t.Elem()
	}
	if t.Kind() != reflect.Struct {
		return fields
	}

	for idx := 0; idx < t.NumField(); idx++ {
		f := t.Field(idx)
		if f.PkgPath != "" {
			continue // unexported: mapstructure cannot reach it either
		}
		name := keyName(f, tagName)
		if name == "" {
			continue // tagged "-"
		}
		field := lcm.FieldSchema{Name: name, Type: kindOf(f.Type)}
		if elem, ok := structElem(f.Type); expand && ok {
			field.Type = "object[]"
			// One level only, so a row editor never has to nest.
			field.Item = fieldsOf(reflect.New(elem).Elem().Interface(), tagName, false)
		}
		fields = append(fields, field)
	}
	return fields
}

// structElem reports the element type when t is a slice or array of structs.
func structElem(t reflect.Type) (reflect.Type, bool) {
	for t.Kind() == reflect.Ptr {
		t = t.Elem()
	}
	if t.Kind() != reflect.Slice && t.Kind() != reflect.Array {
		return nil, false
	}
	elem := t.Elem()
	for elem.Kind() == reflect.Ptr {
		elem = elem.Elem()
	}
	if elem.Kind() != reflect.Struct {
		return nil, false
	}
	return elem, true
}

// keyName reproduces mapstructure's rule: the tag value up to the first comma,
// falling back to the Go field name.
func keyName(f reflect.StructField, tagName string) string {
	tag := f.Tag.Get(tagName)
	if tag == "-" {
		return ""
	}
	if comma := strings.Index(tag, ","); comma >= 0 {
		tag = tag[:comma]
	}
	if tag == "" {
		return f.Name
	}
	return tag
}

func kindOf(t reflect.Type) string {
	for t.Kind() == reflect.Ptr {
		t = t.Elem()
	}
	switch t.Kind() {
	case reflect.String:
		return "string"
	case reflect.Int, reflect.Int8, reflect.Int16, reflect.Int32, reflect.Int64,
		reflect.Uint, reflect.Uint8, reflect.Uint16, reflect.Uint32, reflect.Uint64:
		return "integer"
	case reflect.Float32, reflect.Float64:
		return "number"
	case reflect.Bool:
		return "boolean"
	case reflect.Slice, reflect.Array:
		if kindOf(t.Elem()) == "string" {
			return "string[]"
		}
		return "array"
	case reflect.Struct, reflect.Map:
		return "object"
	default:
		return "unknown"
	}
}

// Build assembles the finished schema for one function node, applying the
// metadata onto the reflected property fields.
func Build(kind string, d abi.NodeDescription, r Registration, m Meta) lcm.NodeSchema {
	s := lcm.NodeSchema{
		Type:          r.Type,
		Kind:          kind,
		NumInputPort:  d.NumInputPort,
		NumOutputPort: d.NumOutputPort,
		OutputLabels:  m.OutputLabels,
		Discriminator: m.Discriminator,
		Properties:    Apply(PropertyFields(r.Prop), m),
		Output:        &lcm.PortSchema{Fields: Fields(r.Out, PortTag)},
		Passthrough:   r.Passthrough,
	}
	// A nil In means the node declares no input format; an empty struct still
	// yields a non-nil port with no fields, and the two are not the same thing.
	if r.In != nil {
		s.Input = &lcm.PortSchema{Fields: Fields(r.In, PortTag)}
	}
	if r.InPort != nil {
		s.Input = r.InPort
	}
	if r.OutPort != nil {
		s.Output = r.OutPort
	}
	return s
}

// Apply overlays metadata onto reflected fields. It is exported so trigger
// schemas, which are hand-written, can reuse it.
func Apply(fields []lcm.FieldSchema, m Meta) []lcm.FieldSchema {
	required := set(m.Required)
	secret := set(m.Secret)
	filePath := set(m.FilePath)
	fieldRefPort := make(map[string]int, len(m.FieldRef))
	for port, name := range m.FieldRef {
		fieldRefPort[name] = port
	}

	for idx := range fields {
		name := fields[idx].Name
		// The discriminator gates every other property, and leaving it unset
		// falls through to an unsupported-request 400 with no other signal.
		if name == m.Discriminator {
			fields[idx].Required = true
		}
		if v, ok := m.Enums[name]; ok {
			fields[idx].Enum = v
		}
		if v, ok := m.ShowWhen[name]; ok {
			fields[idx].ShowWhen = v
			// A property listed under a discriminator value is required for it.
			fields[idx].Required = true
		}
		if _, ok := required[name]; ok {
			fields[idx].Required = true
		}
		if _, ok := secret[name]; ok {
			fields[idx].Secret = true
		}
		if _, ok := filePath[name]; ok {
			fields[idx].FilePath = true
		}
		if port, ok := fieldRefPort[name]; ok {
			fields[idx].FieldRef = true
			fields[idx].FieldRefPort = port
		}
		if v, ok := m.Docs[name]; ok {
			fields[idx].Doc = v
		}
		if v, ok := m.ItemMeta[name]; ok {
			fields[idx].Item = Apply(fields[idx].Item, v)
		}
	}
	return fields
}

func set(names []string) map[string]struct{} {
	out := make(map[string]struct{}, len(names))
	for _, n := range names {
		out[n] = struct{}{}
	}
	return out
}

// MetaKeys returns every field name referenced anywhere in the metadata, so a
// test can assert none of them is a typo.
func MetaKeys(m Meta) []string {
	keys := []string{}
	if m.Discriminator != "" {
		keys = append(keys, m.Discriminator)
	}
	for k := range m.Enums {
		keys = append(keys, k)
	}
	for k := range m.ShowWhen {
		keys = append(keys, k)
	}
	for k := range m.Docs {
		keys = append(keys, k)
	}
	for k := range m.ItemMeta {
		keys = append(keys, k)
	}
	keys = append(keys, m.Required...)
	keys = append(keys, m.Secret...)
	keys = append(keys, m.FilePath...)
	keys = append(keys, m.FieldRef...)
	return keys
}
