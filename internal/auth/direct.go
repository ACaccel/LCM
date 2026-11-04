package auth

import "context"

type directKey struct{}

// WithDirect marks ctx as a request an operator made directly through the
// console, the CLI or the API, as opposed to a policy run. Components that
// reserve an action for administrators (machine power, router reboot)
// refuse contexts without the mark.
func WithDirect(ctx context.Context) context.Context {
	return context.WithValue(ctx, directKey{}, true)
}

// IsDirect reports whether ctx carries the WithDirect mark.
func IsDirect(ctx context.Context) bool {
	direct, _ := ctx.Value(directKey{}).(bool)
	return direct
}
