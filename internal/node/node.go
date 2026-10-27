// Package node is the SDK for policy node types. A node type is a Spec
// whose props type P is reflected into a schema for the frontend and
// decoded and validated by the engine; registering it with a Registry is all
// that is needed to make it available to policies.
package node

import (
	"errors"
	"log/slog"
	"time"

	"github.com/ACaccel/lcm/internal/clock"
)

// Effect declares whether a node changes the outside world. Dry runs and
// replays skip write nodes, and recovery never re-executes a write node that
// was interrupted.
type Effect string

// Effects.
const (
	EffectNone  Effect = "none"
	EffectRead  Effect = "read"
	EffectWrite Effect = "write"
)

// Valid reports whether e is a known effect.
func (e Effect) Valid() bool {
	switch e {
	case EffectNone, EffectRead, EffectWrite:
		return true
	}
	return false
}

// Mode is the execution mode of a run.
type Mode string

// Modes.
const (
	// ModeLive executes every node.
	ModeLive Mode = "live"
	// ModeDry executes none and read nodes but only plans write nodes.
	ModeDry Mode = "dry"
)

// Valid reports whether m is a known mode.
func (m Mode) Valid() bool { return m == ModeLive || m == ModeDry }

// CategoryTrigger marks node types that start runs. Trigger nodes are not
// executed inside a run: the event that started the run becomes their
// output.
const CategoryTrigger = "trigger"

// Well-known ports. PortError is implicit on every node.
const (
	PortOut   = "out"
	PortError = "error"
)

// Result is what a node run produces.
type Result struct {
	// Port selects the outgoing port; "" means "out".
	Port string
	// Output must be JSON-serializable. It becomes nodes.<id> for
	// downstream expressions.
	Output any
	// Suspend, when non-nil, parks the node; Port and Output are ignored.
	Suspend *Suspend
}

// Suspend parks a node until it is resumed by Engine.Resume with the same
// Key or, when ResumeAt is set, by the clock. The node's Run is then called
// again with RunContext.Resumed set. At least one of Key and ResumeAt is
// required.
type Suspend struct {
	ResumeAt *time.Time
	Key      string
	// Output is stored with the suspension (for example an approval request
	// id) and handed back in Resumed.Output.
	Output any
}

// Resumed describes why a suspended node runs again.
type Resumed struct {
	// Key is the suspension key.
	Key string
	// Payload is the value passed to Engine.Resume; nil for timer resumes.
	Payload any
	// Timer is true when ResumeAt elapsed.
	Timer bool
	// Output is Suspend.Output from the suspension.
	Output any
}

// RunContext describes the execution of one node attempt.
type RunContext struct {
	RunID  string
	Policy string
	NodeID string
	// Attempt starts at 1 and increases with each retry. Resuming a
	// suspended node does not start a new attempt.
	Attempt int
	// IdempotencyKey is "<runID>/<nodeID>": stable across retries,
	// suspensions and crash recovery, so external systems can deduplicate.
	IdempotencyKey string
	Mode           Mode
	// Logger carries run and node attributes.
	Logger *slog.Logger
	// Resumed is non-nil when a suspended node is resumed.
	Resumed *Resumed
	// Clock is the engine clock; use it instead of time.Now.
	Clock clock.Clock
}

// Now returns the engine clock's current time.
func (rc *RunContext) Now() time.Time { return rc.Clock.Now() }

type permanentError struct{ err error }

func (e *permanentError) Error() string { return e.err.Error() }
func (e *permanentError) Unwrap() error { return e.err }

// Permanent marks err as not retryable. A nil err stays nil.
func Permanent(err error) error {
	if err == nil {
		return nil
	}
	return &permanentError{err: err}
}

// IsPermanent reports whether err was marked with Permanent.
func IsPermanent(err error) bool {
	var p *permanentError
	return errors.As(err, &p)
}
