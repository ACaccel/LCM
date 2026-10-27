// Package expr wraps github.com/expr-lang/expr for policy expressions: bare
// expressions (flow.if conditions) and {{ ... }} templates inside string
// props. Expressions are compiled once, their references to upstream nodes
// and top-level variables are extracted statically, and evaluation is pure
// and bounded.
package expr

import (
	"errors"
	"fmt"
	"slices"
	"strings"

	"github.com/expr-lang/expr"
	"github.com/expr-lang/expr/ast"
	"github.com/expr-lang/expr/file"
	"github.com/expr-lang/expr/parser"
	"github.com/expr-lang/expr/vm"
)

// Built-in top-level variables.
const (
	VarTrigger = "trigger"
	VarNodes   = "nodes"
	VarParams  = "params"
	VarRun     = "run"
)

// BuiltinVars lists the variables every expression may use.
var BuiltinVars = []string{VarTrigger, VarNodes, VarParams, VarRun}

const (
	// maxNodes bounds expression size at compile time.
	maxNodes = 1000
	// memoryBudget bounds allocations during one evaluation (units as
	// defined by the expr VM: roughly elements allocated).
	memoryBudget = 1_000_000
)

// disabledBuiltins are functions whose result depends on something other
// than the environment. Evaluation must be deterministic so that dry runs,
// recovery and replay see the same results.
var disabledBuiltins = []string{"now"}

// Program is a compiled bare expression.
type Program struct {
	src  string
	prog *vm.Program
	refs []string
	vars []string
}

// Source returns the expression text.
func (p *Program) Source() string { return p.src }

// Refs returns the node ids referenced as nodes.<id>, sorted and unique.
func (p *Program) Refs() []string { return p.refs }

// Vars returns the top-level variables used, sorted and unique.
func (p *Program) Vars() []string { return p.vars }

// Compile compiles a bare expression. extraVars are the top-level
// variables available besides BuiltinVars.
func Compile(src string, extraVars []string) (*Program, error) {
	if strings.TrimSpace(src) == "" {
		return nil, errors.New("empty expression")
	}
	known := knownVars(extraVars)
	tree, err := parser.Parse(src)
	if err != nil {
		return nil, compileError(err)
	}
	refs, vars, err := inspect(tree, known)
	if err != nil {
		return nil, err
	}
	// Variables are dynamically typed (event payloads and node outputs have
	// no static schema), so the checker runs without an environment and
	// inspect rejects unknown names instead.
	opts := []expr.Option{expr.AllowUndefinedVariables(), expr.MaxNodes(maxNodes)}
	for _, name := range disabledBuiltins {
		opts = append(opts, expr.DisableBuiltin(name))
	}
	prog, err := expr.Compile(src, opts...)
	if err != nil {
		return nil, compileError(err)
	}
	return &Program{src: src, prog: prog, refs: refs, vars: vars}, nil
}

// Eval evaluates the program against env.
func (p *Program) Eval(env Env) (any, error) {
	machine := vm.VM{MemoryBudget: memoryBudget}
	out, err := machine.Run(p.prog, map[string]any(env))
	if err != nil {
		return nil, fmt.Errorf("evaluate %q: %w", p.src, runtimeError(err))
	}
	return out, nil
}

func knownVars(extra []string) map[string]bool {
	known := make(map[string]bool, len(BuiltinVars)+len(extra))
	for _, v := range BuiltinVars {
		known[v] = true
	}
	for _, v := range extra {
		known[v] = true
	}
	return known
}

type inspector struct {
	known    map[string]bool
	refs     map[string]bool
	vars     map[string]bool
	allowed  map[*ast.IdentifierNode]bool
	idents   []*ast.IdentifierNode
	locals   map[string]bool
	shadowed []string
	disabled []string
}

func (v *inspector) Visit(n *ast.Node) {
	switch x := (*n).(type) {
	case *ast.IdentifierNode:
		v.idents = append(v.idents, x)
	case *ast.MemberNode:
		base, ok := x.Node.(*ast.IdentifierNode)
		if !ok || base.Value != VarNodes {
			return
		}
		if prop, ok := x.Property.(*ast.StringNode); ok {
			v.allowed[base] = true
			v.refs[prop.Value] = true
		}
	case *ast.BuiltinNode:
		if slices.Contains(disabledBuiltins, x.Name) {
			v.disabled = append(v.disabled, x.Name)
		}
	case *ast.VariableDeclaratorNode:
		if v.known[x.Name] {
			v.shadowed = append(v.shadowed, x.Name)
		}
		v.locals[x.Name] = true
	}
}

// inspect enforces the reference rules and extracts references. nodes may
// only be indexed by a literal id so that every dependency is known at
// compile time; $env would bypass that, so it is rejected.
func inspect(tree *parser.Tree, known map[string]bool) (refs, vars []string, err error) {
	v := &inspector{
		known:   known,
		refs:    map[string]bool{},
		vars:    map[string]bool{},
		allowed: map[*ast.IdentifierNode]bool{},
		locals:  map[string]bool{},
	}
	ast.Walk(&tree.Node, v)
	if len(v.disabled) > 0 {
		return nil, nil, fmt.Errorf("function %s is not allowed: expressions must be deterministic", v.disabled[0])
	}
	if len(v.shadowed) > 0 {
		return nil, nil, fmt.Errorf("let %s shadows a built-in variable", v.shadowed[0])
	}
	for _, id := range v.idents {
		switch {
		case id.Value == "$env":
			return nil, nil, errors.New("$env is not allowed; use trigger, nodes, params or run")
		case id.Value == VarNodes && !v.allowed[id]:
			return nil, nil, errors.New("nodes must be followed by a literal node id, as in nodes.check")
		case known[id.Value]:
			v.vars[id.Value] = true
		case !v.locals[id.Value]:
			return nil, nil, fmt.Errorf("unknown name %s", id.Value)
		}
	}
	return sortedSet(v.refs), sortedSet(v.vars), nil
}

func sortedSet(m map[string]bool) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	slices.Sort(out)
	return out
}

// compileError strips the multi-line source snippet that expr appends so
// that messages fit on one line in validation reports.
func compileError(err error) error {
	var fe *file.Error
	if errors.As(err, &fe) {
		return fmt.Errorf("%s (column %d)", fe.Message, fe.Column+1)
	}
	return err
}

func runtimeError(err error) error {
	var fe *file.Error
	if errors.As(err, &fe) {
		return errors.New(fe.Message)
	}
	return err
}
