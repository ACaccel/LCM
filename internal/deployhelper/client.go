package deployhelper

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"sync"
	"time"

	"github.com/ACaccel/lcm/internal/execx"
	"github.com/ACaccel/lcm/internal/hostsetup"
)

// WrapperPath is the root-owned wrapper that the sudoers rule allows. It is
// a deployment contract, never a document property.
const WrapperPath = hostsetup.DeployWrapper

// Caller calls the deploy helper.
type Caller interface {
	Call(ctx context.Context, req Request) (Response, error)
}

// Error is a reply that refused the request or reports a failed
// operation. The reply itself is returned with it.
type Error struct {
	Code    Code
	Message string
}

func (e *Error) Error() string {
	if e.Message == "" {
		return "deploy helper: " + string(e.Code)
	}
	return fmt.Sprintf("deploy helper: %s: %s", e.Code, e.Message)
}

// CodeOf returns the code of an *Error in err's chain, or "".
func CodeOf(err error) Code {
	var e *Error
	if errors.As(err, &e) {
		return e.Code
	}
	return ""
}

// ErrOutcomeUnknown wraps every failure after which the helper may or may
// not have carried out the request: the reply was lost, cut off or
// malformed, or the call timed out. Query the operation (or send the same
// request again) before deciding anything.
var ErrOutcomeUnknown = errors.New("deploy helper outcome unknown; query the operation before retrying")

// Margins of a call beyond the helper's budget: copying trees, which only
// their size bounds, and starting sudo and the helper.
const (
	copyMargin  = 15 * time.Minute
	replyMargin = 2 * time.Minute
)

// Timeout bounds one call: the longest the helper's commands may take for
// it (budget) and a margin. A call that times out leaves the helper running
// as root; its outcome is in the ledger.
func Timeout(req Request) time.Duration {
	if req.Op == OpBuild || req.Op == OpInstall {
		return budget(req) + copyMargin
	}
	return budget(req) + replyMargin
}

// Client runs the helper through sudo.
type Client struct {
	Runner execx.Runner
}

// Call sends one request. A reply with a code returns an *Error together
// with the reply. For query, the reply describes the operation looked up:
// a recorded failure is returned without an error, and only a query that
// was itself refused (code not_found, for example) returns one.
func (c Client) Call(ctx context.Context, req Request) (Response, error) {
	if err := req.Validate(); err != nil {
		return Response{}, err
	}
	if c.Runner == nil {
		return Response{}, errors.New("deploy helper client not configured")
	}
	raw, err := json.Marshal(req)
	if err != nil {
		return Response{}, err
	}
	unknown := func(why string, cause error) error {
		err := fmt.Errorf("%w: %s", ErrOutcomeUnknown, why)
		if cause != nil {
			err = fmt.Errorf("%w (%w)", err, cause)
		}
		return err
	}
	res, runErr := c.Runner.Run(ctx, execx.Cmd{
		Argv:  []string{"/usr/bin/sudo", "-n", "--", WrapperPath},
		Stdin: raw, Dir: "/", Timeout: Timeout(req), MaxOutput: MaxResponse + 1,
		Env: []string{"LC_ALL=C"},
	})
	if res.Truncated || len(res.Output) > MaxResponse {
		return Response{}, unknown("the reply exceeds its limit", nil)
	}
	resp, err := DecodeResponse([]byte(res.Output))
	if err != nil {
		if runErr != nil {
			return Response{}, unknown("no reply", runErr)
		}
		return Response{}, unknown("malformed reply", err)
	}
	if resp.OperationID != req.OperationID || (req.Op != OpQuery && resp.Op != req.Op) {
		return Response{}, unknown("the reply is for another request", nil)
	}
	if req.Op == OpQuery && resp.Op != OpQuery && resp.State != "" {
		return resp, nil
	}
	if resp.Code != "" {
		return resp, &Error{Code: resp.Code, Message: resp.Message}
	}
	if runErr != nil {
		return resp, unknown("the helper exited with an error", runErr)
	}
	return resp, nil
}

// Fake is a Caller for tests. It validates and records every request and
// answers with Handle, or with success when Handle is nil.
type Fake struct {
	Handle func(Request) (Response, error)

	mu       sync.Mutex
	requests []Request
}

// Call implements Caller.
func (f *Fake) Call(ctx context.Context, req Request) (Response, error) {
	if err := req.Validate(); err != nil {
		return Response{}, err
	}
	if err := ctx.Err(); err != nil {
		return Response{}, err
	}
	f.mu.Lock()
	f.requests = append(f.requests, req)
	f.mu.Unlock()
	if f.Handle != nil {
		return f.Handle(req)
	}
	resp := Response{Version: Version, Op: req.Op, OperationID: req.OperationID}
	if req.Op.Changes() {
		resp.State = StateSucceeded
	}
	return resp, nil
}

// Requests returns the recorded requests.
func (f *Fake) Requests() []Request {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]Request(nil), f.requests...)
}
