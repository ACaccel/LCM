package engine

import (
	"context"
	"errors"
	"time"

	"github.com/ACaccel/lcm/internal/node"
)

// Mode re-exports node.Mode for callers of Start.
type Mode = node.Mode

// Execution modes.
const (
	ModeLive = node.ModeLive
	ModeDry  = node.ModeDry
)

// RunStatus is the lifecycle state of a run.
type RunStatus string

// Run statuses. Dropped is terminal and marks an event discarded by the
// drop concurrency mode.
const (
	RunQueued      RunStatus = "queued"
	RunRunning     RunStatus = "running"
	RunSuspended   RunStatus = "suspended"
	RunSucceeded   RunStatus = "succeeded"
	RunFailed      RunStatus = "failed"
	RunCancelled   RunStatus = "cancelled"
	RunInterrupted RunStatus = "interrupted"
	RunDropped     RunStatus = "dropped"
)

// Terminal reports whether the status is final.
func (s RunStatus) Terminal() bool {
	switch s {
	case RunQueued, RunRunning, RunSuspended:
		return false
	}
	return true
}

// NodeStatus is the state of a node within a run.
type NodeStatus string

// Node statuses.
const (
	NodePending   NodeStatus = "pending"
	NodeReady     NodeStatus = "ready"
	NodeRunning   NodeStatus = "running"
	NodeRetrying  NodeStatus = "retrying"
	NodeSuspended NodeStatus = "suspended"
	NodeCompleted NodeStatus = "completed"
	NodeFailed    NodeStatus = "failed"
	NodeDead      NodeStatus = "dead"
)

// Errors returned by the engine.
var (
	ErrClosed            = errors.New("engine: closed")
	ErrPolicyNotDeployed = errors.New("engine: policy not deployed")
	ErrPolicyPaused      = errors.New("engine: policy paused")
	ErrUnknownTrigger    = errors.New("engine: unknown trigger node")
	ErrInvalidMode       = errors.New("engine: invalid mode")
	ErrDropped           = errors.New("engine: event dropped: a run is active")
	ErrRunNotFound       = errors.New("engine: run not found")
	ErrRunNotActive      = errors.New("engine: run is not active")
	ErrNotSuspended      = errors.New("engine: node is not suspended")
	ErrKeyMismatch       = errors.New("engine: resume key does not match")
	ErrValueTooLarge     = errors.New("engine: value too large")
	ErrAlreadyRecovered  = errors.New("engine: recover already ran")
	ErrNotPolicy         = errors.New("engine: only Policy documents can be deployed")
)

// Context causes that tell the engine why a node context ended.
var (
	errNodeTimeout = errors.New("node timeout")
	errRunStopped  = errors.New("run stopped")
	errShutdown    = errors.New("engine shutting down")
)

// RunInfo identifies the run and node an extra variable is evaluated for.
type RunInfo struct {
	RunID   string
	Policy  string
	Version int
	NodeID  string
	Mode    Mode
	Trigger any
}

// Var is an extra top-level expression variable such as service or state.
// Value is called (outside engine locks) each time a node whose props
// reference the variable is about to run; it must be safe for concurrent
// use and return a JSON-compatible value.
type Var struct {
	Name  string
	Value func(ctx context.Context, info RunInfo) (any, error)
}

// NodeCall describes a node execution offered to an Interceptor.
type NodeCall struct {
	RunID   string
	Policy  string
	Version int
	NodeID  string
	Type    string
	Effect  node.Effect
	Mode    Mode
	Attempt int
	// Props are the resolved props as JSON values.
	Props any
}

// Interception is the outcome an Interceptor supplies for a node: Err, when
// non-nil, is treated exactly like an error returned by the node.
type Interception struct {
	Result node.Result
	Err    error
}

// Interceptor may supply a node's result instead of executing it, for
// example a recorded response during replay. Intercept returns false to let
// the node run normally. It is called from worker goroutines and must be
// safe for concurrent use.
type Interceptor interface {
	Intercept(ctx context.Context, call NodeCall) (Interception, bool)
}

// RunView is a run with its per-node decision trace.
type RunView struct {
	ID          string     `json:"id"`
	Policy      string     `json:"policy"`
	Version     int        `json:"version"`
	Status      RunStatus  `json:"status"`
	Mode        Mode       `json:"mode"`
	TriggerNode string     `json:"triggerNode"`
	Event       any        `json:"event"`
	Error       string     `json:"error,omitempty"`
	CreatedAt   time.Time  `json:"createdAt"`
	StartedAt   *time.Time `json:"startedAt,omitempty"`
	FinishedAt  *time.Time `json:"finishedAt,omitempty"`
	Nodes       []NodeView `json:"nodes"`
}

// NodeView is one node of a RunView, in topological order.
type NodeView struct {
	ID         string     `json:"id"`
	Type       string     `json:"type"`
	Status     NodeStatus `json:"status"`
	Port       string     `json:"port,omitempty"`
	Props      any        `json:"props,omitempty"`
	Output     any        `json:"output,omitempty"`
	Attempts   int        `json:"attempts,omitempty"`
	Error      string     `json:"error,omitempty"`
	StartedAt  *time.Time `json:"startedAt,omitempty"`
	FinishedAt *time.Time `json:"finishedAt,omitempty"`
	SuspendKey string     `json:"suspendKey,omitempty"`
	ResumeAt   *time.Time `json:"resumeAt,omitempty"`
}

// Node returns the view of the node with the given scoped id.
func (v RunView) Node(id string) (NodeView, bool) {
	for _, n := range v.Nodes {
		if n.ID == id {
			return n, true
		}
	}
	return NodeView{}, false
}
