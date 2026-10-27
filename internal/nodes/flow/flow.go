// Package flow provides the waiting nodes: flow.wait and wait.until. Both
// wait by suspending, so no worker is held while a policy sleeps, and both
// keep their progress in the suspension output so that a restart resumes
// them where they were.
package flow

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/ACaccel/lcm/internal/node"
)

// CategoryFlow is the category of the nodes in this package.
const CategoryFlow = "flow"

// WaitProps configures flow.wait.
type WaitProps struct {
	Duration *node.Duration `json:"duration" desc:"how long to wait"`
	Until    string         `json:"until" desc:"RFC 3339 time to wait for"`
}

func validateWait(p WaitProps) error {
	if (p.Duration == nil) == (p.Until == "") {
		return errors.New("set exactly one of duration and until")
	}
	if p.Duration != nil && *p.Duration < 0 {
		return errors.New("duration: must not be negative")
	}
	if p.Until != "" {
		if _, err := time.Parse(time.RFC3339, p.Until); err != nil {
			return fmt.Errorf("until: %q is not an RFC 3339 time", p.Until)
		}
	}
	return nil
}

// Register adds the flow node types to r.
func Register(r *node.Registry, d Deps) {
	node.Register(r, node.Spec[WaitProps]{
		Type: "flow.wait", Category: CategoryFlow,
		Description: "Waits for a duration or until a time, without holding a worker. Dry runs do not wait.",
		Validate:    validateWait,
		Run:         runWait,
	})
	registerUntil(r, d)
}

func runWait(_ context.Context, rc *node.RunContext, p WaitProps) (node.Result, error) {
	if rc.Mode == node.ModeDry {
		return node.Result{Output: map[string]any{"skipped": true}}, nil
	}
	if rc.Resumed != nil {
		return node.Result{Output: map[string]any{"waitedUntil": savedUntil(rc.Resumed)}}, nil
	}
	now := rc.Now()
	var target time.Time
	if p.Duration != nil {
		target = now.Add(p.Duration.D())
	} else {
		target, _ = time.Parse(time.RFC3339, p.Until) // checked by Validate
	}
	if !target.After(now) {
		return node.Result{Output: map[string]any{"waitedUntil": target.UTC().Format(time.RFC3339Nano)}}, nil
	}
	target = target.UTC()
	return node.Result{Suspend: &node.Suspend{
		ResumeAt: &target, Output: map[string]any{"until": target.Format(time.RFC3339Nano)},
	}}, nil
}

func savedUntil(r *node.Resumed) any {
	if m, ok := r.Output.(map[string]any); ok {
		return m["until"]
	}
	return nil
}
