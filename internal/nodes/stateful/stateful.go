// Package stateful provides the nodes that touch durable shared state:
// state.get and state.set (per-policy key-value store) and lock and unlock
// (named locks).
package stateful

import (
	"context"
	"errors"
	"fmt"

	"github.com/ACaccel/lcm/internal/node"
	"github.com/ACaccel/lcm/internal/state"
)

// CategoryState is the category of the nodes in this package.
const CategoryState = "state"

// PortConflict is the port state.set takes when its condition fails.
const PortConflict = "conflict"

// GetProps configures state.get.
type GetProps struct {
	Key     string `json:"key" lcm:"required" desc:"state key"`
	Default any    `json:"default" desc:"value reported when the key does not exist"`
}

// SetProps configures state.set.
type SetProps struct {
	Key           string `json:"key" lcm:"required" desc:"state key"`
	Value         any    `json:"value" desc:"value to store (JSON, at most 64 KiB)"`
	Expect        any    `json:"expect" desc:"compare-and-set: store only if the current value equals this"`
	ExpectVersion *int64 `json:"expectVersion" desc:"compare-and-set: store only if the current version equals this; 0 means the key must not exist"`
}

func validateKey(key string) error {
	if !state.ValidKey(key) {
		return fmt.Errorf("key: invalid key %q (letters, digits and _ . : -, at most 128)", key)
	}
	return nil
}

// Deps are the collaborators of the stateful nodes.
type Deps struct {
	State *state.Store
	Locks LockManager
}

// Register adds the state and lock node types to r. A nil State or Locks
// leaves the corresponding nodes unregistered.
func Register(r *node.Registry, d Deps) {
	if d.State != nil {
		registerState(r, d.State)
	}
	if d.Locks != nil {
		registerLocks(r, d.Locks)
	}
}

func registerState(r *node.Registry, s *state.Store) {
	node.Register(r, node.Spec[GetProps]{
		Type: "state.get", Category: CategoryState, Effect: node.EffectRead,
		Description: "Reads a value from the policy's durable state. Output: found, value, version.",
		Validate:    func(p GetProps) error { return validateKey(p.Key) },
		Run: func(ctx context.Context, rc *node.RunContext, p GetProps) (node.Result, error) {
			e, err := s.Get(ctx, rc.Policy, p.Key)
			if err != nil {
				return node.Result{}, err
			}
			value := e.Value
			if !e.Found() {
				value = p.Default
			}
			return node.Result{Output: map[string]any{"found": e.Found(), "value": value, "version": e.Version}}, nil
		},
	})
	node.Register(r, node.Spec[SetProps]{
		Type: "state.set", Category: CategoryState, Effect: node.EffectWrite,
		Description: "Writes a value to the policy's durable state, optionally only if expect or expectVersion matches; otherwise it takes the conflict port with the current entry. Output: version.",
		Ports:       []string{node.PortOut, PortConflict},
		Validate: func(p SetProps) error {
			if p.ExpectVersion != nil && *p.ExpectVersion < 0 {
				return errors.New("expectVersion: must not be negative")
			}
			return validateKey(p.Key)
		},
		Run: func(ctx context.Context, rc *node.RunContext, p SetProps) (node.Result, error) {
			cond := state.Condition{Version: p.ExpectVersion, HasValue: p.Expect != nil, Value: p.Expect}
			e, swapped, err := s.CompareAndSet(ctx, rc.Policy, p.Key, p.Value, cond)
			switch {
			case errors.Is(err, state.ErrTooLarge), errors.Is(err, state.ErrTooMany), errors.Is(err, state.ErrInvalidKey):
				return node.Result{}, node.Permanent(err)
			case err != nil:
				return node.Result{}, err
			case !swapped:
				return node.Result{Port: PortConflict, Output: map[string]any{
					"found": e.Found(), "value": e.Value, "version": e.Version,
				}}, nil
			}
			return node.Result{Output: map[string]any{"version": e.Version}}, nil
		},
	})
}
