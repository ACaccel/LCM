package monitor

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"regexp"
	"sort"
	"sync"
	"time"

	"github.com/ACaccel/lcm/internal/model"
)

// maxMessage caps stored result messages.
const maxMessage = 256

// Result is the outcome of one check run.
type Result struct {
	OK bool
	// Latency of the probe; zero when it is not meaningful.
	Latency time.Duration
	// Message explains a failure, or carries a short detail on success.
	Message string
}

// Probe runs a check once. It must honour ctx, whose deadline is the check
// timeout, and must not put secrets into the result message.
type Probe func(ctx context.Context) Result

// Built is a check compiled from its definition: props are decoded and
// validated once, at Sync.
type Built struct {
	// Probe runs the check. Nil for passive checks, which are fed by
	// Monitor.Heartbeat instead of being executed.
	Probe Probe
	// Grace is the extra time a passive check waits beyond its interval
	// before it counts as missing.
	Grace time.Duration
}

// Env gives check builders access to collaborators.
type Env struct {
	Runner Runner
}

// Spec defines a check type with props type P.
type Spec[P any] struct {
	Type        string
	Description string
	// Build validates the decoded props and compiles the check. c has its
	// defaults applied.
	Build func(c model.Check, props P, env Env) (Built, error)
}

type definition struct {
	typ, description string
	build            func(model.Check, map[string]any, Env) (Built, error)
}

var typeNameRe = regexp.MustCompile(`^[a-z][a-z0-9_]*$`)

// Registry holds check types. It is safe for concurrent use.
type Registry struct {
	mu   sync.RWMutex
	defs map[string]*definition
}

// NewRegistry returns an empty registry; see DefaultRegistry for the
// built-in types.
func NewRegistry() *Registry {
	return &Registry{defs: map[string]*definition{}}
}

// Register adds a check type. It panics on an invalid or duplicate type,
// which are programming errors found at startup.
func Register[P any](r *Registry, s Spec[P]) {
	if !typeNameRe.MatchString(s.Type) || s.Build == nil {
		panic(fmt.Sprintf("monitor: register %q: need a lowercase type name and a Build function", s.Type))
	}
	d := &definition{typ: s.Type, description: s.Description, build: func(c model.Check, raw map[string]any, env Env) (Built, error) {
		p, err := decodeProps[P](raw)
		if err != nil {
			return Built{}, err
		}
		return s.Build(c, p, env)
	}}
	r.mu.Lock()
	defer r.mu.Unlock()
	if _, dup := r.defs[s.Type]; dup {
		panic(fmt.Sprintf("monitor: register %q: duplicate type", s.Type))
	}
	r.defs[s.Type] = d
}

// TypeInfo describes a registered check type.
type TypeInfo struct {
	Type        string `json:"type"`
	Description string `json:"description"`
}

// Types lists the registered check types, sorted.
func (r *Registry) Types() []TypeInfo {
	r.mu.RLock()
	defer r.mu.RUnlock()
	out := make([]TypeInfo, 0, len(r.defs))
	for _, d := range r.defs {
		out = append(out, TypeInfo{Type: d.typ, Description: d.description})
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Type < out[j].Type })
	return out
}

// Build compiles c, which must have defaults applied. Errors are
// model.Issues with paths relative to the check, such as props.url.
func (r *Registry) Build(c model.Check, env Env) (Built, error) {
	r.mu.RLock()
	d, ok := r.defs[c.Type]
	r.mu.RUnlock()
	if !ok {
		return Built{}, model.Issues{{Path: "type", Message: fmt.Sprintf("unknown check type %q", c.Type)}}
	}
	b, err := d.build(c, c.Props, env)
	if err != nil {
		var is model.Issues
		if errors.As(err, &is) {
			return Built{}, is
		}
		return Built{}, model.Issues{{Path: "props", Message: err.Error()}}
	}
	return b, nil
}

func decodeProps[P any](raw map[string]any) (P, error) {
	var p P
	if raw == nil {
		raw = map[string]any{}
	}
	b, err := json.Marshal(raw)
	if err != nil {
		return p, fmt.Errorf("encode props: %w", err)
	}
	dec := json.NewDecoder(bytes.NewReader(b))
	dec.DisallowUnknownFields()
	if err := dec.Decode(&p); err != nil {
		return p, fmt.Errorf("decode props: %w", err)
	}
	return p, nil
}

// propError builds a validation error located at props.<field>.
func propError(field, format string, args ...any) error {
	return model.Issues{{Path: "props." + field, Message: fmt.Sprintf(format, args...)}}
}

// truncate shortens s to at most n bytes without splitting a rune.
func truncate(s string, n int) string {
	if len(s) <= n {
		return s
	}
	cut := n
	for cut > 0 && !isRuneStart(s[cut]) {
		cut--
	}
	return s[:cut] + "..."
}

func isRuneStart(b byte) bool { return b&0xC0 != 0x80 }
