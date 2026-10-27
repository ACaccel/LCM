package execx

import (
	"context"
	"fmt"
	"strings"
	"sync"
)

// Handler produces the outcome of a fake command.
type Handler func(c Cmd) (Result, error)

// Fake is a Runner for tests. It records every command and answers through
// the first matching rule; commands without a rule succeed with no output.
type Fake struct {
	mu    sync.Mutex
	calls []Cmd
	rules []rule
}

type rule struct {
	match func(Cmd) bool
	h     Handler
}

// On registers a rule. Rules are consulted in registration order.
func (f *Fake) On(match func(Cmd) bool, h Handler) *Fake {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.rules = append(f.rules, rule{match: match, h: h})
	return f
}

// Reply registers a rule that answers commands whose Display contains sub.
func (f *Fake) Reply(sub string, output string, code int) *Fake {
	return f.On(func(c Cmd) bool { return strings.Contains(c.Display(), sub) }, func(Cmd) (Result, error) {
		res := Result{ExitCode: code, Output: output}
		if code != 0 {
			return res, &ExitError{Code: code}
		}
		return res, nil
	})
}

// Run implements Runner.
func (f *Fake) Run(ctx context.Context, c Cmd) (Result, error) {
	if err := c.validate(); err != nil {
		return Result{ExitCode: -1}, err
	}
	if err := ctx.Err(); err != nil {
		return Result{ExitCode: -1}, fmt.Errorf("%s: %w", c.Display(), err)
	}
	f.mu.Lock()
	f.calls = append(f.calls, c)
	var h Handler
	for _, r := range f.rules {
		if r.match(c) {
			h = r.h
			break
		}
	}
	f.mu.Unlock()
	if h == nil {
		return Result{}, nil
	}
	return h(c)
}

// Calls returns a copy of the recorded commands.
func (f *Fake) Calls() []Cmd {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]Cmd(nil), f.calls...)
}

// Displays returns the Display form of every recorded command.
func (f *Fake) Displays() []string {
	calls := f.Calls()
	out := make([]string, len(calls))
	for i, c := range calls {
		out[i] = c.Display()
	}
	return out
}

// Reset forgets recorded commands but keeps the rules.
func (f *Fake) Reset() {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.calls = nil
}
