package flow

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"time"

	"github.com/ACaccel/lcm/internal/expr"
	"github.com/ACaccel/lcm/internal/node"
	"github.com/ACaccel/lcm/internal/nodes/internal/httpcall"
	"github.com/ACaccel/lcm/internal/nodes/internal/poll"
	"github.com/ACaccel/lcm/internal/secret"
)

// PortTimeout is the port wait.until takes when the timeout passes.
const PortTimeout = "timeout"

// Deps are the collaborators of wait.until.
type Deps struct {
	// Client defaults to a client that follows redirects only within one host.
	Client *http.Client
	// Secrets resolves the probe's secret names.
	Secrets secret.Source
}

func (d Deps) client() *http.Client {
	if d.Client != nil {
		return d.Client
	}
	return httpcall.NewClient()
}

// ProbeProps is the optional HTTP request wait.until sends before each
// evaluation of its condition.
type ProbeProps struct {
	URL           string            `json:"url" lcm:"required" desc:"absolute http or https URL"`
	Method        string            `json:"method" enum:"GET,HEAD,POST" default:"GET"`
	Headers       map[string]string `json:"headers"`
	Body          any               `json:"body" desc:"a string is sent as is, anything else as JSON"`
	Timeout       node.Duration     `json:"timeout" default:"10s"`
	ExpectStatus  []string          `json:"expectStatus" desc:"statuses that make probe.ok true (default 200-299)"`
	MaxBytes      int               `json:"maxBytes" default:"16384"`
	BearerSecret  string            `json:"bearerSecret" desc:"secret name sent as a bearer token"`
	HeaderSecrets map[string]string `json:"headerSecrets" desc:"header name to secret name"`
}

// UntilProps configures wait.until.
type UntilProps struct {
	Interval  node.Duration  `json:"interval" default:"30s" desc:"time between checks"`
	Timeout   node.Duration  `json:"timeout" default:"10m" desc:"give up after this long and take the timeout port"`
	Probe     *ProbeProps    `json:"probe" desc:"request sent before each check"`
	Condition string         `json:"condition" desc:"bare expression over probe, inputs, attempt, elapsedMs and run; defaults to probe.ok"`
	Inputs    map[string]any `json:"inputs" desc:"values for the condition, usually templates such as {{ nodes.deploy.version }}; available as inputs"`
}

// conditionVars are the variables a condition may use besides run.
var conditionVars = []string{"probe", "inputs", "attempt", "elapsedMs"}

const defaultCondition = "probe.ok"

func (p UntilProps) condition() string {
	if p.Condition == "" {
		return defaultCondition
	}
	return p.Condition
}

func validateUntil(p UntilProps) error {
	if p.Interval.D() < time.Second {
		return errors.New("interval: must be at least 1s")
	}
	if p.Timeout.D() < p.Interval.D() {
		return errors.New("timeout: must not be shorter than interval")
	}
	if p.Probe == nil && p.Condition == "" {
		return errors.New("set a probe or a condition")
	}
	if p.Probe == nil && p.condition() == defaultCondition {
		return errors.New("condition: probe.ok needs a probe")
	}
	if p.Probe != nil {
		if err := p.Probe.spec().Validate(); err != nil {
			return fmt.Errorf("probe.%w", err)
		}
	}
	prog, err := expr.Compile(p.condition(), conditionVars)
	if err != nil {
		return fmt.Errorf("condition: %w", err)
	}
	// trigger, nodes and params are not available because the engine
	// evaluates props before the node can supply probe; inputs carries what
	// the condition needs from the run.
	for _, v := range prog.Vars() {
		switch v {
		case expr.VarTrigger, expr.VarNodes, expr.VarParams:
			return fmt.Errorf("condition: %s is not available here; pass what you need through inputs", v)
		}
	}
	return nil
}

func (p *ProbeProps) spec() httpcall.Spec {
	return httpcall.Spec{
		Method: p.Method, URL: p.URL, Headers: p.Headers, Body: p.Body, Timeout: p.Timeout.D(),
		BearerSecret: p.BearerSecret, HeaderSecrets: p.HeaderSecrets, ExpectStatus: p.ExpectStatus, MaxBytes: p.MaxBytes,
	}
}

func registerUntil(r *node.Registry, d Deps) {
	node.Register(r, node.Spec[UntilProps]{
		Type: "wait.until", Category: CategoryFlow, Effect: node.EffectRead,
		Description: "Repeats an optional HTTP probe and a condition until the condition holds or the timeout passes, without holding a worker. Output: satisfied, polls, elapsedMs, probe. Dry runs check once and continue.",
		Ports:       []string{node.PortOut, PortTimeout},
		Validate:    validateUntil,
		Run: func(ctx context.Context, rc *node.RunContext, p UntilProps) (node.Result, error) {
			return runUntil(ctx, rc, d, p)
		},
	})
}

func runUntil(ctx context.Context, rc *node.RunContext, d Deps, p UntilProps) (node.Result, error) {
	now := rc.Now()
	st, resumed := poll.Load(rc.Resumed)
	if !resumed {
		st = poll.Begin(now, p.Timeout.D())
	}
	st.Polls++

	probe := runProbe(ctx, rc, d, p.Probe)
	satisfied, condErr, err := evalCondition(rc, p, probe, st, now)
	if err != nil {
		return node.Result{}, node.Permanent(err)
	}

	out := map[string]any{
		"satisfied": satisfied, "polls": int64(st.Polls), "elapsedMs": now.Sub(st.Started).Milliseconds(), "probe": probe,
	}
	if condErr != "" {
		out["conditionError"] = condErr
	}
	switch {
	case satisfied:
		return node.Result{Output: out}, nil
	case rc.Mode == node.ModeDry:
		// Write nodes upstream were only planned, so the condition cannot
		// become true; continue as if it had instead of sending the dry run
		// down the failure path.
		out["dry"] = true
		return node.Result{Output: out}, nil
	case st.Expired(now):
		return node.Result{Port: PortTimeout, Output: out}, nil
	}
	at := st.NextWake(now, p.Interval.D())
	extra := map[string]any{"lastProbe": probe}
	if condErr != "" {
		extra["conditionError"] = condErr
	}
	return node.Result{Suspend: &node.Suspend{ResumeAt: &at, Output: st.Output(extra)}}, nil
}

// runProbe sends the probe, if any. A probe that fails is not a node
// failure: it is the thing being waited out, so it becomes probe.ok=false.
func runProbe(ctx context.Context, rc *node.RunContext, d Deps, p *ProbeProps) any {
	if p == nil {
		return nil
	}
	resp, err := httpcall.Do(ctx, rc.Clock, d.client(), d.Secrets, p.spec())
	if err == nil {
		return resp.Map()
	}
	if resp.Status != 0 {
		return resp.Map()
	}
	if ctx.Err() != nil || errors.Is(err, secret.ErrNotFound) || errors.Is(err, secret.ErrInvalidName) {
		// Cancellation and misconfiguration are not conditions to wait out.
		return map[string]any{"status": int64(0), "ok": false, "error": err.Error(), "fatal": true}
	}
	return map[string]any{"status": int64(0), "ok": false, "error": err.Error()}
}

// evalCondition evaluates the condition. A runtime error (for example the
// probe returned no JSON where the condition expects a field) counts as not
// satisfied and is reported, since the next probe may succeed.
func evalCondition(rc *node.RunContext, p UntilProps, probe any, st poll.State, now time.Time) (ok bool, condErr string, err error) {
	if m, isMap := probe.(map[string]any); isMap && m["fatal"] == true {
		return false, "", fmt.Errorf("probe: %v", m["error"])
	}
	prog, cerr := expr.Compile(p.condition(), conditionVars)
	if cerr != nil {
		return false, "", fmt.Errorf("condition: %w", cerr)
	}
	env := expr.Env{
		"probe": probe, "inputs": p.Inputs, "attempt": int64(st.Polls), "elapsedMs": now.Sub(st.Started).Milliseconds(),
		"run":     map[string]any{"id": rc.RunID, "policy": rc.Policy, "attempt": int64(rc.Attempt)},
		"trigger": nil, "nodes": map[string]any{}, "params": nil,
	}
	v, eerr := prog.Eval(env)
	if eerr != nil {
		return false, eerr.Error(), nil
	}
	b, isBool := v.(bool)
	if !isBool {
		return false, "", fmt.Errorf("condition must return a boolean, got %T", v)
	}
	return b, "", nil
}
