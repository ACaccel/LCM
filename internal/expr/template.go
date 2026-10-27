package expr

import (
	"encoding/json"
	"errors"
	"fmt"
	"strconv"
	"strings"
	"time"
)

// Template is a string prop containing one or more {{ expr }} segments.
type Template struct {
	src  string
	segs []segment
	refs []string
	vars []string
}

type segment struct {
	lit  string
	prog *Program
}

// HasTemplate reports whether s contains a {{ marker and therefore must be
// parsed as a template.
func HasTemplate(s string) bool { return strings.Contains(s, "{{") }

// ParseTemplate compiles every {{ ... }} segment of s. When s is exactly one
// segment the result keeps the expression's native type; otherwise segments
// are stringified and concatenated with the literal text.
func ParseTemplate(s string, extraVars []string) (*Template, error) {
	t := &Template{src: s}
	refs, vars := map[string]bool{}, map[string]bool{}
	rest := s
	for {
		start := strings.Index(rest, "{{")
		if start < 0 {
			if rest != "" {
				t.segs = append(t.segs, segment{lit: rest})
			}
			break
		}
		if start > 0 {
			t.segs = append(t.segs, segment{lit: rest[:start]})
		}
		body := rest[start+2:]
		end, err := closing(body)
		if err != nil {
			return nil, err
		}
		prog, err := Compile(body[:end], extraVars)
		if err != nil {
			return nil, fmt.Errorf("{{ %s }}: %w", strings.TrimSpace(body[:end]), err)
		}
		for _, r := range prog.refs {
			refs[r] = true
		}
		for _, v := range prog.vars {
			vars[v] = true
		}
		t.segs = append(t.segs, segment{prog: prog})
		rest = body[end+2:]
	}
	t.refs, t.vars = sortedSet(refs), sortedSet(vars)
	return t, nil
}

// closing returns the offset of the "}}" that ends the expression starting
// at s. It skips string literals and balanced braces so that map literals
// and strings containing "}}" do not end the segment early.
func closing(s string) (int, error) {
	depth := 0
	for i := 0; i < len(s); i++ {
		switch c := s[i]; c {
		case '"', '\'', '`':
			j := i + 1
			for ; j < len(s); j++ {
				if s[j] == '\\' && c != '`' {
					j++
					continue
				}
				if s[j] == c {
					break
				}
			}
			if j >= len(s) {
				return 0, errors.New("unterminated string literal in {{ ... }}")
			}
			i = j
		case '{':
			depth++
		case '}':
			if depth == 0 {
				if i+1 < len(s) && s[i+1] == '}' {
					return i, nil
				}
				return 0, errors.New("unbalanced '}' in {{ ... }}")
			}
			depth--
		}
	}
	return 0, errors.New("unterminated {{ ... }}")
}

// Source returns the template text.
func (t *Template) Source() string { return t.src }

// Refs returns the node ids referenced by any segment.
func (t *Template) Refs() []string { return t.refs }

// Vars returns the top-level variables used by any segment.
func (t *Template) Vars() []string { return t.vars }

// Native reports whether the template is a single expression whose result
// keeps its type.
func (t *Template) Native() bool { return len(t.segs) == 1 && t.segs[0].prog != nil }

// Eval evaluates the template against env.
func (t *Template) Eval(env Env) (any, error) {
	if t.Native() {
		return t.segs[0].prog.Eval(env)
	}
	var b strings.Builder
	for _, s := range t.segs {
		if s.prog == nil {
			b.WriteString(s.lit)
			continue
		}
		v, err := s.prog.Eval(env)
		if err != nil {
			return nil, err
		}
		str, err := Stringify(v)
		if err != nil {
			return nil, fmt.Errorf("evaluate %q: %w", s.prog.src, err)
		}
		b.WriteString(str)
	}
	return b.String(), nil
}

// Stringify formats a value for interpolation: nil is empty, scalars use
// their natural text form, and composite values are JSON.
func Stringify(v any) (string, error) {
	switch x := v.(type) {
	case nil:
		return "", nil
	case string:
		return x, nil
	case bool:
		return strconv.FormatBool(x), nil
	case int:
		return strconv.Itoa(x), nil
	case int64:
		return strconv.FormatInt(x, 10), nil
	case float64:
		return strconv.FormatFloat(x, 'f', -1, 64), nil
	case time.Time:
		return x.Format(time.RFC3339Nano), nil
	case time.Duration:
		return x.String(), nil
	case fmt.Stringer:
		return x.String(), nil
	}
	b, err := json.Marshal(v)
	if err != nil {
		return "", fmt.Errorf("cannot interpolate %T: %w", v, err)
	}
	return string(b), nil
}
