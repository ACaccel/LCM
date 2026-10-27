package state

import (
	"context"

	"github.com/ACaccel/lcm/internal/engine"
)

// VarName is the expression variable that exposes the policy's state.
const VarName = "state"

// Var returns the engine variable "state": a map of every key of the running
// policy to its value. It is read when a node whose props mention state is
// about to run, so expressions always see the latest committed values.
func (s *Store) Var() engine.Var {
	return engine.Var{
		Name: VarName,
		Value: func(ctx context.Context, info engine.RunInfo) (any, error) {
			return s.All(ctx, info.Policy)
		},
	}
}
