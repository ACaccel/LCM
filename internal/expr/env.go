package expr

// Env is the variable environment of one evaluation. Values must be treated
// as immutable: they are shared between evaluations and runs.
type Env map[string]any

// RunVars describes the current run and node attempt, exposed as run.
type RunVars struct {
	ID      string
	Policy  string
	Attempt int
}

// Base holds the built-in variables.
type Base struct {
	Trigger any
	// Nodes maps node ids (as written in the document scope) to outputs.
	Nodes  map[string]any
	Params any
	Run    RunVars
}

// NewEnv builds an environment from the built-in variables plus extra
// top-level variables supplied by later subsystems (for example service or
// state). Extra entries never replace built-ins.
func NewEnv(b Base, extra map[string]any) Env {
	env := make(Env, len(BuiltinVars)+len(extra))
	for k, v := range extra {
		env[k] = v
	}
	nodes := b.Nodes
	if nodes == nil {
		nodes = map[string]any{}
	}
	env[VarTrigger] = b.Trigger
	env[VarNodes] = nodes
	env[VarParams] = b.Params
	env[VarRun] = map[string]any{"id": b.Run.ID, "policy": b.Run.Policy, "attempt": b.Run.Attempt}
	return env
}
