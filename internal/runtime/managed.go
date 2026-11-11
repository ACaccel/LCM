package runtime

import (
	"context"
	"crypto/rand"
	"errors"
	"fmt"
	"strconv"
	"strings"

	"github.com/ACaccel/lcm/internal/deployhelper"
	"github.com/ACaccel/lcm/internal/execx"
	"github.com/ACaccel/lcm/internal/model"
)

// DeployHelper is the restricted deploy helper through which managed
// services are built, installed and run.
type DeployHelper interface {
	deployhelper.Caller
	// Ready returns nil when the helper can be called, otherwise an error
	// wrapping ErrHelperUnavailable that says why not.
	Ready() error
}

// ErrHelperUnavailable is matched by the error of every action on a managed
// service while the deploy helper is not installed.
var ErrHelperUnavailable = errors.New("the deploy helper is not installed on this host; enable components.deploy in /etc/lcm/host.json and run sudo lcm host apply")

// Why the deploy helper cannot be called, as the Cause of an
// UnavailableError.
const (
	// CauseNotInstalled: the sudo wrapper does not exist.
	CauseNotInstalled = "notInstalled"
	// CauseUntrusted: the wrapper is not a root-owned file only root can
	// write, or cannot be inspected.
	CauseUntrusted = "untrusted"
	// CauseWorkDir: LCM's build directory is not the one the helper builds
	// from.
	CauseWorkDir = "workDir"
)

// UnavailableError says why the deploy helper cannot be called. It matches
// ErrHelperUnavailable.
type UnavailableError struct {
	// Cause is one of the Cause constants.
	Cause string
	// Params are the paths the cause names, by name, for translation.
	Params map[string]string
	// Detail is the English explanation.
	Detail string
}

func (e *UnavailableError) Error() string {
	if e.Cause == CauseNotInstalled {
		return fmt.Sprintf("%s (%s)", ErrHelperUnavailable, e.Detail)
	}
	return "the deploy helper cannot be used on this host: " + e.Detail
}

// Is makes errors.Is(err, ErrHelperUnavailable) true.
func (e *UnavailableError) Is(target error) bool { return target == ErrHelperUnavailable }

// HelperError is a helper operation that was refused, that failed, or whose
// outcome is unknown.
type HelperError struct {
	Op          deployhelper.Op
	OperationID string
	// Code is the helper's code; empty when Unknown.
	Code deployhelper.Code
	// Unknown is set when the helper may or may not have carried out the
	// operation: it must be inspected before anything is assumed.
	Unknown bool
	Message string
	// Log is the helper's sanitized log tail, if any.
	Log string
}

func (e *HelperError) Error() string {
	if e.Unknown {
		return fmt.Sprintf("deploy helper %s: outcome unknown, query %s: %s", e.Op, e.OperationID, e.Message)
	}
	if e.Message == "" {
		return fmt.Sprintf("deploy helper %s %s: %s", e.Op, e.OperationID, e.Code)
	}
	return fmt.Sprintf("deploy helper %s %s: %s: %s", e.Op, e.OperationID, e.Code, e.Message)
}

// NewOperationID returns a fresh random operation ID (a version 4 UUID).
func NewOperationID() string {
	var b [16]byte
	// crypto/rand.Read does not fail on supported platforms.
	_, _ = rand.Read(b[:])
	b[6] = b[6]&0x0f | 0x40
	b[8] = b[8]&0x3f | 0x80
	return fmt.Sprintf("%x-%x-%x-%x-%x", b[0:4], b[4:6], b[6:8], b[8:10], b[10:16])
}

// CallHelper sends one request to the helper. A changing operation whose
// outcome is unknown (lost reply, timeout, cancellation) is looked up in the
// helper's ledger before anything is decided: the recorded reply is
// returned when it finished, and an operation that is still running, was
// interrupted or cannot be looked up is a *HelperError with Unknown set.
// A refused or failed operation is a *HelperError with the helper's code.
// A request the helper has no record of was never carried out.
func CallHelper(ctx context.Context, h DeployHelper, req deployhelper.Request) (deployhelper.Response, error) {
	if h == nil {
		return deployhelper.Response{}, ErrHelperUnavailable
	}
	if err := h.Ready(); err != nil {
		return deployhelper.Response{}, err
	}
	req.Version = deployhelper.Version
	// An invalid request is never sent, so there is nothing to look up.
	if err := req.Validate(); err != nil {
		return deployhelper.Response{}, fmt.Errorf("deploy helper %s: invalid request: %w", req.Op, err)
	}
	resp, err := h.Call(ctx, req)
	if err == nil {
		if !req.Op.Changes() || resp.State == deployhelper.StateSucceeded {
			return resp, nil
		}
		// A reply that does not say "succeeded" is no proof of a change.
		return queryOutcome(ctx, h, req, fmt.Errorf("the reply reports state %q instead of succeeded", resp.State), true)
	}
	if code := deployhelper.CodeOf(err); code != "" {
		return resp, &HelperError{Op: req.Op, OperationID: req.OperationID, Code: code, Message: olderHelperHint(code, resp.Message), Log: resp.Log}
	}
	if !req.Op.Changes() {
		return resp, fmt.Errorf("deploy helper %s: %w", req.Op, err)
	}
	// After a cancellation or timeout the root helper keeps running, and
	// may not have recorded the operation yet.
	cut := ctx.Err() != nil || errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) || errors.Is(err, execx.ErrTimeout)
	return queryOutcome(ctx, h, req, err, cut)
}

// olderHelperHint explains a refusal of a field the pinned helper does not
// know: it was installed by an older build than this LCM.
func olderHelperHint(code deployhelper.Code, msg string) string {
	if code == deployhelper.CodeRefused && (strings.Contains(msg, "unknown key") || strings.Contains(msg, "unknown field")) {
		return msg + "; the pinned deploy helper is older than this LCM: run sudo bin/lcm host apply"
	}
	return msg
}

// queryOutcome asks the ledger what became of req after callErr left its
// outcome unknown. Without a record, req was not carried out unless
// mayStillRun: the call was cut short, or the helper did reply, so the
// record may only be missing because the helper has not written it yet.
func queryOutcome(ctx context.Context, h DeployHelper, req deployhelper.Request, callErr error, mayStillRun bool) (deployhelper.Response, error) {
	unknown := func(msg string) error {
		return &HelperError{Op: req.Op, OperationID: req.OperationID, Unknown: true, Message: msg}
	}
	// The question outlives a cancelled action: the answer decides what is
	// recorded.
	qctx, cancel := context.WithTimeout(context.WithoutCancel(ctx), deployhelper.Timeout(deployhelper.Request{Op: deployhelper.OpQuery}))
	defer cancel()
	q, err := h.Call(qctx, deployhelper.Request{Version: deployhelper.Version, Op: deployhelper.OpQuery, OperationID: req.OperationID})
	switch {
	case deployhelper.CodeOf(err) == deployhelper.CodeNotFound && mayStillRun:
		return deployhelper.Response{}, unknown(fmt.Sprintf("%v; the helper has no record yet, but it may still be starting the operation", callErr))
	case deployhelper.CodeOf(err) == deployhelper.CodeNotFound:
		return deployhelper.Response{}, &HelperError{Op: req.Op, OperationID: req.OperationID, Code: deployhelper.CodeFailed,
			Message: fmt.Sprintf("the helper has no record of the operation, so it was not carried out (%v)", callErr)}
	case err != nil:
		return deployhelper.Response{}, unknown(fmt.Sprintf("%v; the query failed too: %v", callErr, err))
	case q.Op != req.Op:
		return deployhelper.Response{}, unknown(fmt.Sprintf("%v; the ledger records another operation under this ID", callErr))
	}
	switch q.State {
	case deployhelper.StateSucceeded:
		return q, nil
	case deployhelper.StateFailed:
		code := q.Code
		if code == "" {
			code = deployhelper.CodeFailed
		}
		return q, &HelperError{Op: req.Op, OperationID: req.OperationID, Code: code, Message: q.Message, Log: q.Log}
	case deployhelper.StateRunning:
		return q, unknown(fmt.Sprintf("%v; the operation is still running", callErr))
	case deployhelper.StateInterrupted:
		return q, unknown("the helper stopped before the operation finished; inspect the host: " + q.Message)
	}
	return q, unknown(fmt.Sprintf("%v; the ledger reports no state", callErr))
}

// managed controls a service through the deploy helper, which acts only on
// the unit it wrote for the service.
type managed struct {
	helper DeployHelper
	opts   Options
}

func (m *managed) Status(ctx context.Context, r model.Runtime) (Status, error) {
	if !r.RunsProcess() {
		return Status{State: StateUnknown, Detail: map[string]string{"reason": staticOnly}}, nil
	}
	if m.helper == nil {
		return Status{State: StateUnknown, Detail: map[string]string{"reason": ErrHelperUnavailable.Error()}}, nil
	}
	if err := m.helper.Ready(); err != nil {
		return Status{State: StateUnknown, Detail: map[string]string{"reason": err.Error()}}, nil
	}
	ctx, cancel := context.WithTimeout(ctx, m.opts.StatusTimeout)
	defer cancel()
	resp, err := CallHelper(ctx, m.helper, deployhelper.Request{Op: deployhelper.OpStatus, OperationID: NewOperationID(), Service: r.Service})
	var he *HelperError
	switch {
	case errors.As(err, &he) && he.Code == deployhelper.CodeNotFound:
		// The helper has written no unit: nothing of the service runs.
		return Status{State: StateInactive, Detail: map[string]string{"reason": he.Message}}, nil
	case errors.As(err, &he) && he.Code == deployhelper.CodeRefused:
		return Status{State: StateUnknown, Detail: map[string]string{"reason": he.Message}}, nil
	case err != nil:
		return Status{State: StateUnknown}, err
	case resp.Unit == nil:
		return Status{State: StateUnknown}, fmt.Errorf("runtime: the deploy helper's status of %s has no unit", r.Service)
	}
	u := resp.Unit
	st := Status{Detail: map[string]string{"activeState": u.ActiveState, "subState": u.SubState}}
	if u.MainPID > 0 {
		st.Detail["pid"] = strconv.Itoa(u.MainPID)
	}
	if u.Release != "" {
		st.Detail["release"] = u.Release
	}
	st.State = StateOf(u.ActiveState)
	for _, in := range resp.Instances {
		st.Instances = append(st.Instances, InstanceStatus{
			Name: in.Name, State: StateOf(in.ActiveState), Enabled: in.UnitFileState == "enabled",
			Disabled: in.LoadState != "not-found" && in.UnitFileState == "disabled",
			SubState: in.SubState, PID: in.MainPID, Port: in.Port, Granted: in.Granted,
		})
	}
	return st, nil
}

// staticOnly explains the status of a managed service without start.
const staticOnly = "static content only: the service runs no process"

func (m *managed) action(ctx context.Context, r model.Runtime, op deployhelper.Op) error {
	if !r.RunsProcess() {
		return fmt.Errorf("%w: %s (%s)", ErrUnsupported, op, staticOnly)
	}
	_, err := CallHelper(ctx, m.helper, deployhelper.Request{Op: op, OperationID: NewOperationID(), Service: r.Service})
	return err
}

func (m *managed) Start(ctx context.Context, r model.Runtime) error {
	return m.action(ctx, r, deployhelper.OpStart)
}

func (m *managed) Stop(ctx context.Context, r model.Runtime) error {
	return m.action(ctx, r, deployhelper.OpStop)
}

func (m *managed) Restart(ctx context.Context, r model.Runtime) error {
	return m.action(ctx, r, deployhelper.OpRestart)
}

// InstanceAction starts, stops or restarts one instance of a managed
// service with instances. The helper keeps a stopped instance disabled, so
// it stays stopped across deployments, and refuses to stop the last
// enabled one.
func (m *managed) InstanceAction(ctx context.Context, r model.Runtime, action, instance string) error {
	if len(r.Instances) == 0 {
		return fmt.Errorf("%w: %s of an instance (the service has no instances)", ErrUnsupported, action)
	}
	op := map[string]deployhelper.Op{InstanceStart: deployhelper.OpStart, InstanceStop: deployhelper.OpStop, InstanceRestart: deployhelper.OpRestart}[action]
	if op == "" {
		return fmt.Errorf("%w: %s of an instance", ErrUnsupported, action)
	}
	_, err := CallHelper(ctx, m.helper, deployhelper.Request{Op: op, OperationID: NewOperationID(), Service: r.Service, Instance: instance})
	return err
}
