package api

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
)

// Error codes returned in the error envelope.
const (
	codeBadRequest         = "bad_request"
	codeValidation         = "validation_failed"
	codeUnauthenticated    = "unauthenticated"
	codeInvalidCredentials = "invalid_credentials" //nolint:gosec // error code, not a credential
	codeForbidden          = "forbidden"
	codeCrossOrigin        = "cross_origin"
	codeNotFound           = "not_found"
	codeMethodNotAllowed   = "method_not_allowed"
	codeRateLimited        = "rate_limited"
	codeUnavailable        = "unavailable"
	codeInternal           = "internal"

	codeAlreadyExists   = "already_exists"
	codeVersionConflict = "version_conflict"
	codeLockHeld        = "lock_held"
	codeInvalidState    = "invalid_state"
	codePrecondition    = "precondition_failed"
	codeConflict        = "conflict"
)

// maxBodyBytes caps JSON request bodies.
const maxBodyBytes = 1 << 20

type errorBody struct {
	Code    string         `json:"code"`
	Message string         `json:"message"`
	Details map[string]any `json:"details"`
}

type errorEnvelope struct {
	Error errorBody `json:"error"`
}

// writeJSON writes v as the JSON response body.
func writeJSON(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	// The status line is already sent; an encoding failure can only be a
	// closed connection, which the access log records.
	_ = json.NewEncoder(w).Encode(v)
}

// writeError writes the standard error envelope.
func writeError(w http.ResponseWriter, status int, code, message string, details map[string]any) {
	if details == nil {
		details = map[string]any{}
	}
	writeJSON(w, status, errorEnvelope{Error: errorBody{Code: code, Message: message, Details: details}})
}

// decodeJSON parses a request body into dst, rejecting unknown fields and
// trailing data. On failure it has already written a 400 response.
func decodeJSON(w http.ResponseWriter, r *http.Request, dst any) bool {
	dec := json.NewDecoder(http.MaxBytesReader(w, r.Body, maxBodyBytes))
	dec.DisallowUnknownFields()
	if err := dec.Decode(dst); err != nil {
		var tooLarge *http.MaxBytesError
		if errors.As(err, &tooLarge) {
			writeError(w, http.StatusRequestEntityTooLarge, codeBadRequest, "request body too large", nil)
			return false
		}
		writeError(w, http.StatusBadRequest, codeBadRequest, "invalid JSON body", map[string]any{"reason": err.Error()})
		return false
	}
	if _, err := dec.Token(); !errors.Is(err, io.EOF) {
		writeError(w, http.StatusBadRequest, codeBadRequest, "invalid JSON body", map[string]any{"reason": "unexpected data after JSON value"})
		return false
	}
	return true
}

func notFoundHandler(w http.ResponseWriter, r *http.Request) {
	writeError(w, http.StatusNotFound, codeNotFound, fmt.Sprintf("no route for %s %s", r.Method, r.URL.Path), nil)
}

func methodNotAllowedHandler(allowed string) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Allow", allowed)
		writeError(w, http.StatusMethodNotAllowed, codeMethodNotAllowed,
			fmt.Sprintf("method %s not allowed", r.Method), map[string]any{"allowed": allowed})
	}
}
