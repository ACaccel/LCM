package engine

import (
	"fmt"

	"github.com/ACaccel/lcm/internal/expr"
	"github.com/ACaccel/lcm/internal/model"
	"github.com/ACaccel/lcm/internal/node"
)

// evaluator is a compiled template or bare expression.
type evaluator interface {
	Eval(expr.Env) (any, error)
	Refs() []string
	Vars() []string
}

type compiledLeaf struct {
	path []any
	ev   evaluator
}

// compiledProps are a node's props with every dynamic value compiled. When
// there are no dynamic values the props are decoded once at compile time.
type compiledProps struct {
	def    *node.Definition
	raw    map[string]any
	leaves []compiledLeaf
	static any
	// partial holds the props decoded from static values only; it is used
	// to compute ports of nodes whose ports depend on props.
	partial any
	refs    []string
	vars    []string
}

// compileProps validates and compiles raw props. Issue paths are relative
// to props.
func compileProps(def *node.Definition, raw map[string]any, extraVars []string) (*compiledProps, model.Issues) {
	if raw == nil {
		raw = map[string]any{}
	}
	cp := &compiledProps{def: def, raw: raw}
	var is model.Issues
	refs, vars := map[string]bool{}, map[string]bool{}
	for _, l := range def.Leaves(raw) {
		var (
			ev  evaluator
			err error
		)
		if l.Bare {
			ev, err = expr.Compile(l.Source, extraVars)
		} else {
			ev, err = expr.ParseTemplate(l.Source, extraVars)
		}
		if err != nil {
			is = append(is, model.Issue{Path: node.PathString(l.Path), Message: err.Error()})
			continue
		}
		for _, r := range ev.Refs() {
			refs[r] = true
		}
		for _, v := range ev.Vars() {
			vars[v] = true
		}
		cp.leaves = append(cp.leaves, compiledLeaf{path: l.Path, ev: ev})
	}
	if len(is) > 0 {
		return nil, is
	}
	cp.refs, cp.vars = sortedKeys(refs), sortedKeys(vars)
	if len(cp.leaves) == 0 {
		p, err := def.Decode(raw)
		if err != nil {
			return nil, asIssues(err)
		}
		cp.static, cp.partial = p, p
		return cp, nil
	}
	p, err := def.CheckStatic(raw)
	if err != nil {
		return nil, asIssues(err)
	}
	cp.partial = p
	return cp, nil
}

// resolve evaluates the dynamic values and decodes the props. It returns the
// decoded props and the resolved, normalized props for the trace.
func (cp *compiledProps) resolve(env expr.Env) (props any, resolved any, err error) {
	if len(cp.leaves) == 0 {
		return cp.static, cp.raw, nil
	}
	out, _ := deepCopy(cp.raw).(map[string]any)
	for _, l := range cp.leaves {
		v, err := l.ev.Eval(env)
		if err != nil {
			return nil, nil, fmt.Errorf("props.%s: %w", node.PathString(l.path), err)
		}
		setPath(out, l.path, v)
	}
	norm, err := model.Normalize(out)
	if err != nil {
		return nil, nil, fmt.Errorf("props: %w", err)
	}
	m, _ := norm.(map[string]any)
	p, err := cp.def.Decode(m)
	if err != nil {
		return nil, m, prefixIssues("props", err)
	}
	return p, m, nil
}

func deepCopy(v any) any {
	switch x := v.(type) {
	case map[string]any:
		m := make(map[string]any, len(x))
		for k, e := range x {
			m[k] = deepCopy(e)
		}
		return m
	case []any:
		s := make([]any, len(x))
		for i, e := range x {
			s[i] = deepCopy(e)
		}
		return s
	}
	return v
}

// setPath stores v at path inside root, whose containers along the path
// were created by deepCopy and are therefore safe to modify.
func setPath(root map[string]any, path []any, v any) {
	var cur any = root
	for i, e := range path {
		last := i == len(path)-1
		switch c := cur.(type) {
		case map[string]any:
			k, _ := e.(string)
			if last {
				c[k] = v
				return
			}
			cur = c[k]
		case []any:
			j, _ := e.(int)
			if last {
				c[j] = v
				return
			}
			cur = c[j]
		default:
			return
		}
	}
}

func asIssues(err error) model.Issues {
	if is, ok := err.(model.Issues); ok { //nolint:errorlint // node.Definition returns model.Issues unwrapped
		return is
	}
	return model.Issues{{Message: err.Error()}}
}

func prefixIssues(prefix string, err error) error {
	is := asIssues(err)
	out := make(model.Issues, len(is))
	for i, x := range is {
		out[i] = model.Issue{Path: joinIssuePath(prefix, x.Path), Message: x.Message}
	}
	return out
}

func joinIssuePath(base, rel string) string {
	switch {
	case rel == "":
		return base
	case base == "":
		return rel
	case rel[0] == '[':
		return base + rel
	}
	return base + "." + rel
}
