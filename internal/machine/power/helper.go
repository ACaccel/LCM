package power

import (
	"context"
	"encoding/json"
	"io"
	"time"

	"github.com/ACaccel/lcm/internal/clock"
)

// System is fakeable; Check must fail closed on capabilities, recovery readiness,
// active sessions and shutdown inhibitors. Request must never force a power action.
type System interface {
	BootID() (string, error)
	Check(context.Context, Action) error
	Request(context.Context, Action) error
}

// ReceiptStore serializes all operations, including the entire OS handoff.
// Implementations must retain IDs permanently and reject missing/corrupt storage.
type ReceiptStore interface {
	Lock(context.Context) (ReceiptTx, error)
}
type ReceiptTx interface {
	Get(controllerID, operationID string) (Receipt, bool, error)
	Put(Receipt) error // Return success only after durable persistence.
	Close() error
}

type Helper struct {
	ControlDisabled bool
	ControllerID    string
	Store           ReceiptStore
	System          System
	Clock           clock.Clock
}

func rejected(code string) Response { return Response{Version: Version, Code: code} }

func (h *Helper) Handle(ctx context.Context, req Request) Response {
	if req.Version != Version || (req.Method != Prepare && req.Method != Submit && req.Method != Query) || req.Binding.validate() != nil {
		return rejected("invalid-request")
	}
	if h.Clock == nil || h.Store == nil || h.System == nil || req.Binding.ControllerID != h.ControllerID {
		return rejected("not-configured")
	}
	if h.ControlDisabled && req.Method != Query {
		return rejected("control-disabled")
	}
	tx, err := h.Store.Lock(ctx)
	if err != nil {
		return rejected("storage-unavailable")
	}
	defer func() { _ = tx.Close() }()
	b := req.Binding
	receipt, found, err := tx.Get(b.ControllerID, b.OperationID)
	if err != nil {
		return rejected("storage-unavailable")
	}
	if found && receipt.Binding != b {
		return rejected("id-collision")
	}
	now := h.Clock.Now().UnixMilli()
	save := func() Response {
		receipt.UpdatedAt = h.Clock.Now().UnixMilli()
		if err := tx.Put(receipt); err != nil {
			return rejected("storage-unavailable")
		}
		return Response{Version: Version, Receipt: &receipt}
	}
	if found && receipt.State == Submitting {
		receipt.State, receipt.Reason = Indeterminate, "handoff-interrupted"
		return save()
	}
	if req.Method == Query {
		if !found {
			return rejected("receipt-missing")
		}
		return Response{Version: Version, Receipt: &receipt}
	}
	if found && (receipt.State != Accepted || receipt.OSRequested) {
		return Response{Version: Version, Receipt: &receipt}
	}
	if now < b.IssuedAt || now >= b.ExpiresAt {
		if !found {
			return rejected("expired-or-future")
		}
		receipt.State, receipt.Reason = Failed, "expired-or-future"
		return save()
	}
	if !found && req.Method == Submit {
		return rejected("receipt-missing")
	}
	boot, err := h.System.BootID()
	if err != nil || boot != b.BootID {
		return rejected("boot-mismatch")
	}
	if err := h.System.Check(ctx, b.Action); err != nil {
		return rejected("preconditions-failed")
	}
	// Recheck the entire validity window after potentially slow system checks.
	now = h.Clock.Now().UnixMilli()
	if now < b.IssuedAt || now >= b.ExpiresAt {
		if !found {
			return rejected("expired-or-future")
		}
		receipt.State, receipt.Reason = Failed, "expired-or-future"
		return save()
	}
	if ctx.Err() != nil {
		return rejected("cancelled")
	}
	if !found {
		receipt = Receipt{Binding: b, State: Accepted}
		return save()
	}
	if req.Method == Prepare {
		return Response{Version: Version, Receipt: &receipt}
	}
	receipt.State = Submitting
	if result := save(); result.Code != "" {
		return result
	}
	// A slow durable fence must not extend the authorization window.
	now = h.Clock.Now().UnixMilli()
	if ctx.Err() != nil || now < b.IssuedAt || now >= b.ExpiresAt {
		receipt.State, receipt.Reason = Failed, "expired-or-cancelled-before-request"
		return save()
	}
	requestCtx, cancel := clock.WithTimeoutCause(ctx, h.Clock, time.Duration(b.ExpiresAt-now)*time.Millisecond, context.DeadlineExceeded)
	defer cancel()
	// Any error after the submitting fence is ambiguous, including command timeout.
	if err := h.System.Request(requestCtx, b.Action); err != nil {
		receipt.State, receipt.Reason = Indeterminate, "os-request-uncertain"
	} else {
		receipt.State, receipt.OSRequested = Accepted, true
	}
	return save()
}

// ProtocolError reports a rejection already encoded on stdout. Command adapters
// must not append stderr diagnostics to this structured response.
type ProtocolError struct{ Code string }

func (e *ProtocolError) Error() string { return e.Code }

// Serve handles exactly one bounded JSON request and emits no raw OS diagnostics.
func (h *Helper) Serve(ctx context.Context, in io.Reader, out io.Writer) error {
	req, err := DecodeRequest(in)
	response := rejected("invalid-request")
	if err == nil {
		response = h.Handle(ctx, req)
	}
	if err := json.NewEncoder(out).Encode(response); err != nil {
		return err
	}
	if response.Code != "" {
		return &ProtocolError{Code: response.Code}
	}
	return nil
}
