package node

import (
	"bytes"
	"encoding/json"
	"fmt"
	"reflect"
	"slices"
	"strconv"

	"github.com/ACaccel/lcm/internal/expr"
	"github.com/ACaccel/lcm/internal/model"
)

// Leaf is a prop value that must be evaluated at run time: either a string
// containing {{ ... }} or the source of a field tagged lcm:"expr".
type Leaf struct {
	// Path locates the value: string elements are object keys, int
	// elements are array indexes.
	Path []any
	// Source is the template or expression text.
	Source string
	// Bare is true for lcm:"expr" fields, whose Source has no braces.
	Bare bool
}

// PathString formats a path relative to props, such as cases[1].when.
func PathString(path []any) string {
	var b bytes.Buffer
	for _, e := range path {
		switch x := e.(type) {
		case int:
			b.WriteString("[" + strconv.Itoa(x) + "]")
		case string:
			if b.Len() > 0 {
				b.WriteByte('.')
			}
			b.WriteString(x)
		}
	}
	return b.String()
}

func joinPath(base string, e any) string {
	switch x := e.(type) {
	case int:
		return base + "[" + strconv.Itoa(x) + "]"
	default:
		if base == "" {
			return fmt.Sprint(x)
		}
		return base + "." + fmt.Sprint(x)
	}
}

// propIndex finds a prop by name.
func propIndex(props []Prop, name string) *Prop {
	for i := range props {
		if props[i].Name == name {
			return &props[i]
		}
	}
	return nil
}

// dynamicLeaves lists the values of raw that need evaluation.
func (d *Definition) dynamicLeaves(raw map[string]any) []Leaf {
	var out []Leaf
	if d.additional {
		walkAny(raw, nil, &out)
		return out
	}
	walkObject(d.props, raw, nil, &out)
	return out
}

func walkObject(props []Prop, raw map[string]any, path []any, out *[]Leaf) {
	for _, k := range sortedKeys(raw) {
		p := propIndex(props, k)
		if p == nil {
			continue
		}
		walkValue(p, raw[k], appendPath(path, k), out)
	}
}

func walkValue(p *Prop, v any, path []any, out *[]Leaf) {
	if p.Expr {
		if s, ok := v.(string); ok {
			*out = append(*out, Leaf{Path: path, Source: s, Bare: true})
		}
		return
	}
	switch x := v.(type) {
	case string:
		if expr.HasTemplate(x) {
			*out = append(*out, Leaf{Path: path, Source: x})
		}
	case map[string]any:
		switch {
		case p.Props != nil:
			walkObject(p.Props, x, path, out)
		case p.Items != nil:
			for _, k := range sortedKeys(x) {
				walkValue(p.Items, x[k], appendPath(path, k), out)
			}
		default:
			walkAny(x, path, out)
		}
	case []any:
		item := p.Items
		if item == nil {
			item = &Prop{Type: TypeAny}
		}
		for i, e := range x {
			walkValue(item, e, appendPath(path, i), out)
		}
	}
}

func walkAny(v any, path []any, out *[]Leaf) {
	switch x := v.(type) {
	case string:
		if expr.HasTemplate(x) {
			*out = append(*out, Leaf{Path: path, Source: x})
		}
	case map[string]any:
		for _, k := range sortedKeys(x) {
			walkAny(x[k], appendPath(path, k), out)
		}
	case []any:
		for i, e := range x {
			walkAny(e, appendPath(path, i), out)
		}
	}
}

func appendPath(path []any, e any) []any {
	return append(slices.Clip(path), e)
}

// checkShape reports unknown fields, missing required fields, enum
// violations and malformed expression fields. When resolved is false,
// dynamic values are not inspected further.
func checkShape(props []Prop, raw map[string]any, path string, resolved bool, is *model.Issues) {
	for _, k := range sortedKeys(raw) {
		if propIndex(props, k) == nil {
			*is = append(*is, model.Issue{Path: joinPath(path, k), Message: "unknown field"})
		}
	}
	for i := range props {
		p := &props[i]
		v, ok := raw[p.Name]
		if !ok {
			if p.Required {
				*is = append(*is, model.Issue{Path: joinPath(path, p.Name), Message: "is required"})
			}
			continue
		}
		checkValue(p, v, joinPath(path, p.Name), resolved, is)
	}
}

func checkValue(p *Prop, v any, path string, resolved bool, is *model.Issues) {
	if p.Expr && !resolved {
		if s, ok := v.(string); !ok || s == "" {
			*is = append(*is, model.Issue{Path: path, Message: "must be an expression string"})
		}
		return
	}
	if s, ok := v.(string); ok && !resolved && expr.HasTemplate(s) {
		return
	}
	switch x := v.(type) {
	case string:
		if p.Enum != nil && !slices.Contains(p.Enum, x) {
			*is = append(*is, model.Issue{Path: path, Message: fmt.Sprintf("must be one of %v", p.Enum)})
		}
	case map[string]any:
		switch {
		case p.Props != nil:
			checkShape(p.Props, x, path, resolved, is)
		case p.Items != nil:
			for _, k := range sortedKeys(x) {
				checkValue(p.Items, x[k], joinPath(path, k), resolved, is)
			}
		}
	case []any:
		if p.Items != nil {
			for i, e := range x {
				checkValue(p.Items, e, joinPath(path, i), resolved, is)
			}
		}
	}
}

// strip returns a copy of raw without dynamic values, so that the static
// remainder can be type-checked by decoding it. Array elements that are
// dynamic become nil to keep indexes stable.
func strip(props []Prop, raw map[string]any) map[string]any {
	out := make(map[string]any, len(raw))
	for k, v := range raw {
		p := propIndex(props, k)
		if p == nil {
			continue
		}
		if sv, keep := stripValue(p, v); keep {
			out[k] = sv
		}
	}
	return out
}

func stripValue(p *Prop, v any) (any, bool) {
	if p.Expr {
		return nil, false
	}
	switch x := v.(type) {
	case string:
		if expr.HasTemplate(x) {
			return nil, false
		}
	case map[string]any:
		switch {
		case p.Props != nil:
			return strip(p.Props, x), true
		case p.Items != nil:
			out := make(map[string]any, len(x))
			for k, e := range x {
				if sv, keep := stripValue(p.Items, e); keep {
					out[k] = sv
				}
			}
			return out, true
		default:
			return stripAny(x), true
		}
	case []any:
		item := p.Items
		if item == nil {
			item = &Prop{Type: TypeAny}
		}
		out := make([]any, len(x))
		for i, e := range x {
			out[i], _ = stripValue(item, e)
		}
		return out, true
	}
	if p.Type == TypeAny {
		return stripAny(v), true
	}
	return v, true
}

func stripAny(v any) any {
	switch x := v.(type) {
	case string:
		if expr.HasTemplate(x) {
			return nil
		}
	case map[string]any:
		out := make(map[string]any, len(x))
		for k, e := range x {
			out[k] = stripAny(e)
		}
		return out
	case []any:
		out := make([]any, len(x))
		for i, e := range x {
			out[i] = stripAny(e)
		}
		return out
	}
	return v
}

// withDefaults returns a copy of raw with declared defaults filled in for
// absent fields, including inside nested objects and arrays of objects.
// raw itself is never modified because plans share it between runs.
func withDefaults(props []Prop, raw map[string]any) map[string]any {
	out := make(map[string]any, len(raw)+len(props))
	for k, v := range raw {
		out[k] = v
	}
	for i := range props {
		p := &props[i]
		v, ok := out[p.Name]
		if !ok {
			if p.hasDefault {
				out[p.Name] = p.Default
			}
			continue
		}
		out[p.Name] = defaultsInValue(p, v)
	}
	return out
}

func defaultsInValue(p *Prop, v any) any {
	switch x := v.(type) {
	case map[string]any:
		if p.Props != nil {
			return withDefaults(p.Props, x)
		}
	case []any:
		if p.Items != nil && p.Items.Props != nil {
			out := make([]any, len(x))
			for i, e := range x {
				out[i] = defaultsInValue(p.Items, e)
			}
			return out
		}
	}
	return v
}

// decodeInto JSON-decodes v into a new value of the props type.
func decodeInto[P any](v map[string]any) (P, error) {
	var p P
	b, err := json.Marshal(v)
	if err != nil {
		return p, fmt.Errorf("encode props: %w", err)
	}
	dec := json.NewDecoder(bytes.NewReader(b))
	dec.DisallowUnknownFields()
	dec.UseNumber()
	if err := dec.Decode(&p); err != nil {
		return p, fmt.Errorf("decode props: %w", err)
	}
	fixNumbers(reflect.ValueOf(&p).Elem())
	return p, nil
}

// fixNumbers converts json.Number values left in interface-typed parts of
// v into int64 or float64, matching model.Normalize, so that free-form
// props have the same types whether they came from a document or from an
// expression.
func fixNumbers(v reflect.Value) {
	switch v.Kind() {
	case reflect.Interface:
		if !v.IsNil() && v.CanSet() {
			fixed := model.FixNumbers(v.Elem().Interface())
			if fixed != nil {
				v.Set(reflect.ValueOf(fixed))
			}
		}
	case reflect.Pointer:
		if !v.IsNil() {
			fixNumbers(v.Elem())
		}
	case reflect.Struct:
		for i := range v.NumField() {
			if v.Type().Field(i).IsExported() {
				fixNumbers(v.Field(i))
			}
		}
	case reflect.Slice, reflect.Array:
		for i := range v.Len() {
			fixNumbers(v.Index(i))
		}
	case reflect.Map:
		iter := v.MapRange()
		for iter.Next() {
			e := reflect.New(v.Type().Elem()).Elem()
			e.Set(iter.Value())
			fixNumbers(e)
			v.SetMapIndex(iter.Key(), e)
		}
	}
}

func sortedKeys[V any](m map[string]V) []string {
	keys := make([]string, 0, len(m))
	for k := range m {
		keys = append(keys, k)
	}
	slices.Sort(keys)
	return keys
}
