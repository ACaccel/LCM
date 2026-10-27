// Package monitor answers "is this service healthy, since when, and what did
// LCM do about it". It is deliberately not a metrics system: it keeps short
// check histories for decisions and for a service panel.
//
// # Checks
//
// Services declare model.Check values; the catalog hands them to Sync as Def
// values. Check types (http, tcp, systemd, docker, command, heartbeat,
// prometheus) live in a Registry like node types: props are decoded and
// validated once at Sync, and external programs run behind the Runner
// interface so tests use a fake.
//
// Each check runs on its own interval on an injected clock, with a global
// concurrency limit. A check changes status (up, down) only after Threshold
// consecutive results in the new direction. A service is healthy, degraded
// (a non-critical check is down), down (a critical check is down) or
// unknown (no results yet).
//
// # Signals and persistence
//
// Status changes publish signal.KindCheck and service health changes publish
// signal.KindHealth, after the change is stored, so a subscriber that
// re-reads storage sees it. Current state, raw results (7 days by default)
// and one-minute rollups (90 days) live in SQLite. After a restart the
// stored statuses resume without emitting changes; a status older than
// twice the check interval reads as unknown until a fresh result arrives.
//
// # Inputs from outside
//
// Heartbeat feeds heartbeat checks. IngestAlert accepts webhooks from
// Alertmanager, Uptime Kuma, Gatus and a generic JSON format (see package
// alert), tracks which alerts are firing and publishes signal.KindAlert when
// an alert starts or stops firing.
//
// # Queries
//
// Services and Service report the current state; Series, CheckSummary and
// ServiceSummary read history; FiringAlerts reads alert state.
package monitor
