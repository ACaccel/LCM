package api

import (
	"errors"
	"net/http"
	"regexp"
	"time"

	"github.com/ACaccel/lcm/internal/catalog"
	"github.com/ACaccel/lcm/internal/configsync"
	"github.com/ACaccel/lcm/internal/engine"
	"github.com/ACaccel/lcm/internal/lock"
	"github.com/ACaccel/lcm/internal/machine"
	"github.com/ACaccel/lcm/internal/maintenance"
	"github.com/ACaccel/lcm/internal/model"
	"github.com/ACaccel/lcm/internal/modules"
	"github.com/ACaccel/lcm/internal/monitor"
	"github.com/ACaccel/lcm/internal/monitor/alert"
	"github.com/ACaccel/lcm/internal/resource"
	"github.com/ACaccel/lcm/internal/router"
	"github.com/ACaccel/lcm/internal/security"
	"github.com/ACaccel/lcm/internal/securityhost"
	"github.com/ACaccel/lcm/internal/state"
	"github.com/ACaccel/lcm/internal/trigger"
)

// pkgPrefix matches the "catalog: " style prefix of component errors.
var pkgPrefix = regexp.MustCompile(`^[a-z]+: `)

// publicMessage turns a component error into a client message.
func publicMessage(err error) string {
	return pkgPrefix.ReplaceAllString(err.Error(), "")
}

// fail maps an error returned by a component to the response the API
// conventions prescribe. extra is merged into the error details and may be
// nil. Anything unrecognized is an internal error.
func (s *Server) fail(w http.ResponseWriter, r *http.Request, err error, extra map[string]any) {
	details := func(kv map[string]any) map[string]any {
		out := map[string]any{}
		for k, v := range kv {
			out[k] = v
		}
		for k, v := range extra {
			out[k] = v
		}
		return out
	}
	var (
		issues model.Issues
		stErr  *catalog.StateError
		busy   *lock.BusyError
		impact *resource.ImpactError
	)
	switch {
	case errors.As(err, &impact):
		d := toImpactDTO(impact.Impact)
		d.Action, d.AcknowledgementRequired = impact.Action, true
		writeError(w, http.StatusConflict, codeImpactUnacknowledged, publicMessage(err),
			details(map[string]any{"resource": impact.Impact.Resource, "action": impact.Action, "impact": d}))
	case errors.Is(err, securityhost.ErrConflict):
		writeError(w, http.StatusConflict, codeConflict, publicMessage(err), details(nil))
	case errors.Is(err, securityhost.ErrUnavailable):
		writeError(w, http.StatusServiceUnavailable, codeUnavailable, publicMessage(err), details(nil))
	case errors.Is(err, securityhost.ErrNotFound):
		writeError(w, http.StatusNotFound, codeNotFound, publicMessage(err), details(nil))
	case errors.Is(err, security.ErrVersionConflict):
		writeError(w, http.StatusConflict, codeVersionConflict, publicMessage(err), details(nil))
	case errors.Is(err, security.ErrTestRate):
		w.Header().Set("Retry-After", "300")
		writeError(w, http.StatusTooManyRequests, codeRateLimited, publicMessage(err), details(nil))
	case errors.Is(err, trigger.ErrNotReady):
		w.Header().Set("Retry-After", "10")
		writeError(w, http.StatusServiceUnavailable, codeUnavailable, publicMessage(err), details(nil))
	case errors.Is(err, security.ErrDisabled):
		writeError(w, http.StatusServiceUnavailable, codeUnavailable, publicMessage(err), details(nil))
	case errors.Is(err, security.ErrConflict):
		writeError(w, http.StatusConflict, codeConflict, publicMessage(err), details(nil))
	case errors.Is(err, security.ErrCapacity):
		writeError(w, http.StatusServiceUnavailable, codeUnavailable, publicMessage(err), details(nil))
	case errors.Is(err, errUnsupported):
		writeError(w, http.StatusConflict, codeUnsupported, publicMessage(err), details(nil))
	case errors.Is(err, machine.ErrNotFound), errors.Is(err, router.ErrNotFound), errors.Is(err, resource.ErrNotFound):
		writeError(w, http.StatusNotFound, codeNotFound, publicMessage(err), details(nil))
	case errors.Is(err, resource.ErrExists):
		writeError(w, http.StatusConflict, codeAlreadyExists, publicMessage(err), details(nil))
	case errors.Is(err, machine.ErrConflict), errors.Is(err, router.ErrConflict), errors.Is(err, resource.ErrConflict):
		writeError(w, http.StatusConflict, codeVersionConflict, publicMessage(err), details(nil))
	case errors.Is(err, machine.ErrForbidden), errors.Is(err, router.ErrForbidden):
		writeError(w, http.StatusForbidden, codeForbidden, publicMessage(err), details(nil))
	case errors.Is(err, machine.ErrPrecondition), errors.Is(err, machine.ErrUnsupported), errors.Is(err, maintenance.ErrActive),
		errors.Is(err, router.ErrPrecondition), errors.Is(err, router.ErrUnsupported):
		writeError(w, http.StatusConflict, codePrecondition, publicMessage(err), details(nil))
	case errors.As(err, &issues):
		d := details(map[string]any{"issues": issuesBody(issues)})
		writeError(w, http.StatusUnprocessableEntity, codeValidation, "validation failed", d)

	case errors.Is(err, catalog.ErrNotFound), errors.Is(err, modules.ErrNotFound),
		errors.Is(err, engine.ErrPolicyNotFound), errors.Is(err, engine.ErrRunNotFound),
		errors.Is(err, engine.ErrPolicyNotDeployed), errors.Is(err, lock.ErrNotHeld),
		errors.Is(err, monitor.ErrUnknownCheck), errors.Is(err, alert.ErrUnknownSource),
		errors.Is(err, trigger.ErrNoListener), errors.Is(err, configsync.ErrNotFound):
		writeError(w, http.StatusNotFound, codeNotFound, publicMessage(err), details(nil))

	case errors.Is(err, modules.ErrExists), errors.Is(err, configsync.ErrExists), errors.Is(err, engine.ErrPolicyExists):
		writeError(w, http.StatusConflict, codeAlreadyExists, publicMessage(err), details(nil))
	case errors.Is(err, modules.ErrConflict), errors.Is(err, engine.ErrVersionConflict):
		writeError(w, http.StatusConflict, codeVersionConflict, publicMessage(err), details(nil))

	case errors.As(err, &stErr):
		writeError(w, http.StatusConflict, codeInvalidState, publicMessage(err),
			details(map[string]any{"resource": stErr.Resource, "action": stErr.Action, "state": string(stErr.State)}))
	case errors.As(err, &busy):
		writeError(w, http.StatusConflict, codeLockHeld, publicMessage(err), details(lockDetails(busy.Holder)))
	case errors.Is(err, catalog.ErrLockHeld), errors.Is(err, lock.ErrHeld):
		writeError(w, http.StatusConflict, codeLockHeld, publicMessage(err), details(nil))

	case invalidField(err) != "":
		is := model.Issues{{Path: invalidField(err), Message: publicMessage(err)}}
		writeError(w, http.StatusUnprocessableEntity, codeValidation, "validation failed",
			details(map[string]any{"issues": issuesBody(is)}))

	case errors.Is(err, catalog.ErrPrecondition), errors.Is(err, resource.ErrPrecondition):
		writeError(w, http.StatusConflict, codePrecondition, publicMessage(err), details(nil))
	case errors.Is(err, catalog.ErrStateChanged), errors.Is(err, engine.ErrPolicyPaused),
		errors.Is(err, engine.ErrRunNotActive), errors.Is(err, engine.ErrNotSuspended),
		errors.Is(err, engine.ErrKeyMismatch), errors.Is(err, engine.ErrDropped):
		writeError(w, http.StatusConflict, codeConflict, publicMessage(err), details(nil))

	case errors.Is(err, engine.ErrClosed), errors.Is(err, monitor.ErrClosed):
		writeError(w, http.StatusServiceUnavailable, codeUnavailable, "the server is shutting down", nil)

	default:
		s.internalError(w, r, err)
	}
}

// lockDetails describes a lock holder for error details and lock lists.
func lockDetails(i lock.Info) map[string]any {
	return map[string]any{
		"lock":       i.Name,
		"owner":      i.Owner,
		"acquiredAt": i.AcquiredAt.UTC().Format(time.RFC3339),
		"expiresAt":  i.ExpiresAt.UTC().Format(time.RFC3339),
	}
}

// invalidField names the request field a validation sentinel is about, or
// returns "" when err is not one. The path becomes the issue path, so the
// 422 envelope has the same shape for every kind of validation failure.
func invalidField(err error) string {
	switch {
	case errors.Is(err, catalog.ErrInvalidVersion):
		return "version"
	case errors.Is(err, catalog.ErrUnknownJob):
		return "job"
	case errors.Is(err, engine.ErrInvalidMode):
		return "mode"
	case errors.Is(err, engine.ErrUnknownTrigger):
		return "trigger"
	case errors.Is(err, engine.ErrValueTooLarge):
		return "event"
	case errors.Is(err, engine.ErrNotPolicy):
		return "kind"
	case errors.Is(err, lock.ErrInvalid):
		return "name"
	case errors.Is(err, state.ErrInvalidKey):
		return "key"
	}
	return ""
}
