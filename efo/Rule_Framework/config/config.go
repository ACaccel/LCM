/*
Package config centralizes Rule_Framework runtime configuration.

Values default to the current hard-coded behavior, so with no environment
override the runtime behaves identically. Only environment-dependent endpoints
are externalized here.
*/
package config

import (
	"os"
	"strconv"
)

const defaultControlPlaneBaseURL = "http://localhost:8000"

// ControlPlaneBaseURL is the base URL action nodes use to reach the EFO control
// plane. Overridable via the LCM_CONTROL_PLANE_BASE_URL environment variable.
func ControlPlaneBaseURL() string {
	if v := os.Getenv("LCM_CONTROL_PLANE_BASE_URL"); v != "" {
		return v
	}
	return defaultControlPlaneBaseURL
}

const defaultTraceMaxEvents = 500

// TraceMaxEvents caps how many node events one execution trace retains. Events
// beyond the cap are counted but not stored, and the trace is flagged truncated.
// Overridable via the LCM_TRACE_MAX_EVENTS environment variable; a non-positive
// or unparsable value falls back to the default.
func TraceMaxEvents() int {
	if v := os.Getenv("LCM_TRACE_MAX_EVENTS"); v != "" {
		if n, err := strconv.Atoi(v); err == nil && n > 0 {
			return n
		}
	}
	return defaultTraceMaxEvents
}

const defaultMaxWaitSeconds = 300

// MaxWaitSeconds caps how long a Wait node may hold a walker. The sleep is not
// interruptible, so an undeploy cannot reclaim the goroutine before it elapses;
// the cap bounds that exposure. Overridable via LCM_MAX_WAIT_SECONDS; a
// non-positive or unparsable value falls back to the default.
func MaxWaitSeconds() float64 {
	if v := os.Getenv("LCM_MAX_WAIT_SECONDS"); v != "" {
		if n, err := strconv.ParseFloat(v, 64); err == nil && n > 0 {
			return n
		}
	}
	return defaultMaxWaitSeconds
}

const defaultHTTPTimeoutSeconds = 30

// HTTPTimeoutSeconds bounds every outbound request an action node makes. The
// shared client previously had no timeout at all, so an endpoint that accepted
// a connection and never answered pinned a walker goroutine for the lifetime of
// the process. Overridable via LCM_HTTP_TIMEOUT_SECONDS.
func HTTPTimeoutSeconds() float64 {
	if v := os.Getenv("LCM_HTTP_TIMEOUT_SECONDS"); v != "" {
		if n, err := strconv.ParseFloat(v, 64); err == nil && n > 0 {
			return n
		}
	}
	return defaultHTTPTimeoutSeconds
}
