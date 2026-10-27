// Package auth implements the administrator account, browser sessions and
// API tokens, and carries the authenticated actor through the request
// context.
package auth

import "context"

// Kind classifies who performed an action.
type Kind string

// Actor kinds. KindAI is reserved for the AI administrator planned for a
// later milestone.
const (
	KindUser   Kind = "user"
	KindToken  Kind = "token"
	KindAI     Kind = "ai"
	KindSystem Kind = "system"
)

// Valid reports whether k is a known kind.
func (k Kind) Valid() bool {
	switch k {
	case KindUser, KindToken, KindAI, KindSystem:
		return true
	}
	return false
}

// Actor identifies the principal behind a request or command.
type Actor struct {
	Kind Kind
	ID   string
	Name string
}

// CLI is the actor for commands run locally through the lcm binary.
var CLI = Actor{Kind: KindSystem, ID: "cli", Name: "cli"}

type actorKey struct{}

// WithActor returns a context carrying a.
func WithActor(ctx context.Context, a Actor) context.Context {
	return context.WithValue(ctx, actorKey{}, a)
}

// ActorFrom returns the actor stored in ctx.
func ActorFrom(ctx context.Context) (Actor, bool) {
	a, ok := ctx.Value(actorKey{}).(Actor)
	return a, ok
}
