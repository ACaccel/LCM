package node

import (
	"encoding/json"
	"fmt"
	"reflect"
	"slices"
	"strings"
	"time"
)

// Schema describes a node type for the frontend and for validation.
type Schema struct {
	Type        string `json:"type"`
	Category    string `json:"category"`
	Description string `json:"description,omitempty"`
	Effect      Effect `json:"effect"`
	// Ports are the declared output ports; "error" is implicit.
	Ports []string `json:"ports"`
	// DynamicPorts is true when the ports depend on the props (for
	// example flow.switch cases); Ports then lists only the fixed ones.
	DynamicPorts bool   `json:"dynamicPorts,omitempty"`
	Props        []Prop `json:"props"`
	// AdditionalProps is true when the props are a free-form object.
	AdditionalProps bool `json:"additionalProps,omitempty"`
}

// Prop types.
const (
	TypeString   = "string"
	TypeInteger  = "integer"
	TypeNumber   = "number"
	TypeBoolean  = "boolean"
	TypeDuration = "duration"
	TypeArray    = "array"
	TypeObject   = "object"
	TypeAny      = "any"
)

// Prop describes one property, reflected from a field of the props type.
//
// Field tags: `json:"name"` (required on every exported field),
// `lcm:"required"`, `lcm:"expr"` (the value is a bare expression whose
// result is decoded into the field), `enum:"a,b"` (string fields only),
// `default:"x"` (a string for string and duration fields, JSON otherwise)
// and `desc:"..."`.
type Prop struct {
	Name        string   `json:"name"`
	Type        string   `json:"type"`
	Required    bool     `json:"required,omitempty"`
	Expr        bool     `json:"expr,omitempty"`
	Enum        []string `json:"enum,omitempty"`
	Default     any      `json:"default,omitempty"`
	Description string   `json:"description,omitempty"`
	// Items describes array elements and map values.
	Items *Prop `json:"items,omitempty"`
	// Props describes the fields of a nested object; nil for free-form
	// objects.
	Props []Prop `json:"props,omitempty"`

	hasDefault bool
}

// HasDefault reports whether the prop declares a default.
func (p Prop) HasDefault() bool { return p.hasDefault }

var (
	durationType = reflect.TypeFor[Duration]()
	stdDuration  = reflect.TypeFor[time.Duration]()
)

// reflectProps returns the props of struct type t, or additional=true for
// map types.
func reflectProps(t reflect.Type) (props []Prop, additional bool, err error) {
	switch t.Kind() {
	case reflect.Struct:
		props, err := structProps(t, map[reflect.Type]bool{})
		return props, false, err
	case reflect.Map:
		if t.Key().Kind() != reflect.String {
			return nil, false, fmt.Errorf("props map %s must have string keys", t)
		}
		return nil, true, nil
	}
	return nil, false, fmt.Errorf("props type %s must be a struct or map[string]T", t)
}

func structProps(t reflect.Type, seen map[reflect.Type]bool) ([]Prop, error) {
	if seen[t] {
		return nil, fmt.Errorf("recursive props type %s", t)
	}
	seen[t] = true
	defer delete(seen, t)
	props := []Prop{}
	names := map[string]bool{}
	for f := range t.Fields() {
		if !f.IsExported() {
			continue
		}
		if f.Anonymous {
			return nil, fmt.Errorf("%s.%s: embedded fields are not supported", t, f.Name)
		}
		tag, ok := f.Tag.Lookup("json")
		name, _, _ := strings.Cut(tag, ",")
		if name == "-" {
			continue
		}
		if !ok || name == "" {
			return nil, fmt.Errorf("%s.%s: missing json tag", t, f.Name)
		}
		if names[name] {
			return nil, fmt.Errorf("%s: duplicate prop %q", t, name)
		}
		names[name] = true
		p, err := fieldProp(f.Type, seen)
		if err != nil {
			return nil, fmt.Errorf("%s.%s: %w", t, f.Name, err)
		}
		p.Name = name
		p.Description = f.Tag.Get("desc")
		for opt := range strings.SplitSeq(f.Tag.Get("lcm"), ",") {
			switch strings.TrimSpace(opt) {
			case "":
			case "required":
				p.Required = true
			case "expr":
				p.Expr = true
			default:
				return nil, fmt.Errorf("%s.%s: unknown lcm tag option %q", t, f.Name, opt)
			}
		}
		if enum, ok := f.Tag.Lookup("enum"); ok {
			if p.Type != TypeString {
				return nil, fmt.Errorf("%s.%s: enum requires a string field", t, f.Name)
			}
			p.Enum = strings.Split(enum, ",")
		}
		if def, ok := f.Tag.Lookup("default"); ok {
			if p.Required {
				return nil, fmt.Errorf("%s.%s: a required prop cannot have a default", t, f.Name)
			}
			v, err := parseDefault(f.Type, def)
			if err != nil {
				return nil, fmt.Errorf("%s.%s: default: %w", t, f.Name, err)
			}
			if p.Enum != nil && !slices.Contains(p.Enum, def) {
				return nil, fmt.Errorf("%s.%s: default %q is not in enum", t, f.Name, def)
			}
			p.Default, p.hasDefault = v, true
		}
		props = append(props, p)
	}
	return props, nil
}

func fieldProp(t reflect.Type, seen map[reflect.Type]bool) (Prop, error) {
	if t == durationType || t == stdDuration {
		if t == stdDuration {
			return Prop{}, fmt.Errorf("use node.Duration instead of time.Duration so the value is written as \"30s\"")
		}
		return Prop{Type: TypeDuration}, nil
	}
	switch t.Kind() {
	case reflect.Pointer:
		return fieldProp(t.Elem(), seen)
	case reflect.String:
		return Prop{Type: TypeString}, nil
	case reflect.Bool:
		return Prop{Type: TypeBoolean}, nil
	case reflect.Int, reflect.Int8, reflect.Int16, reflect.Int32, reflect.Int64,
		reflect.Uint, reflect.Uint8, reflect.Uint16, reflect.Uint32, reflect.Uint64:
		return Prop{Type: TypeInteger}, nil
	case reflect.Float32, reflect.Float64:
		return Prop{Type: TypeNumber}, nil
	case reflect.Interface:
		return Prop{Type: TypeAny}, nil
	case reflect.Slice, reflect.Array:
		item, err := fieldProp(t.Elem(), seen)
		if err != nil {
			return Prop{}, err
		}
		return Prop{Type: TypeArray, Items: &item}, nil
	case reflect.Map:
		if t.Key().Kind() != reflect.String {
			return Prop{}, fmt.Errorf("map %s must have string keys", t)
		}
		item, err := fieldProp(t.Elem(), seen)
		if err != nil {
			return Prop{}, err
		}
		return Prop{Type: TypeObject, Items: &item}, nil
	case reflect.Struct:
		props, err := structProps(t, seen)
		if err != nil {
			return Prop{}, err
		}
		return Prop{Type: TypeObject, Props: props}, nil
	}
	return Prop{}, fmt.Errorf("unsupported field type %s", t)
}

func parseDefault(t reflect.Type, def string) (any, error) {
	for t.Kind() == reflect.Pointer {
		t = t.Elem()
	}
	if t == durationType {
		d, err := time.ParseDuration(def)
		if err != nil {
			return nil, err
		}
		return Duration(d).String(), nil
	}
	if t.Kind() == reflect.String {
		return def, nil
	}
	ptr := reflect.New(t)
	if err := json.Unmarshal([]byte(def), ptr.Interface()); err != nil {
		return nil, err
	}
	var v any
	if err := json.Unmarshal([]byte(def), &v); err != nil {
		return nil, err
	}
	return v, nil
}

// Duration is a props field holding a Go duration string such as "30s".
type Duration time.Duration

// D returns the value as a time.Duration.
func (d Duration) D() time.Duration { return time.Duration(d) }

// String formats the duration in Go syntax.
func (d Duration) String() string { return time.Duration(d).String() }

// MarshalJSON writes the duration as a string.
func (d Duration) MarshalJSON() ([]byte, error) { return json.Marshal(d.String()) }

// UnmarshalJSON parses a Go duration string.
func (d *Duration) UnmarshalJSON(b []byte) error {
	var s string
	if err := json.Unmarshal(b, &s); err != nil {
		return fmt.Errorf("duration must be a string such as \"30s\"")
	}
	v, err := time.ParseDuration(s)
	if err != nil {
		return fmt.Errorf("invalid duration %q", s)
	}
	*d = Duration(v)
	return nil
}
