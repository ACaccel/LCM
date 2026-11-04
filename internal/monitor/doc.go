// Package monitor answers "is this resource healthy, since when, and what
// did LCM do about it". It is deliberately not a metrics system: it keeps
// short check histories for decisions and for a resource panel.
//
// # Checks
//
// Resources of every type declare model.Check values; they are handed to
// Sync as Def values. Check types (http, tcp, systemd, docker, command, heartbeat,
// prometheus) live in a Registry like node types: props are decoded and
// validated once at Sync, and external programs run behind the Runner
// interface so tests use a fake.
//
// Each check runs on its own interval on an injected clock, with a global
// concurrency limit. A check changes status (up, down) only after Threshold
// consecutive results in the new direction. The checks of a resource are
// healthy, degraded (a non-critical check is down), down (a critical check
// is down) or unknown (a critical check has no result yet, or no check has a
// result).
//
// # Combined health
//
// Resource types that evaluate health themselves (machine and router
// thresholds, reachability) report it through SetNative. A resource's health
// is the worst of its checks' health and its native health, in the order
// down > degraded > unknown > healthy; a resource with neither input does
// not appear at all. Native input lives in memory and is reported again by
// the types after a restart; the combined health is stored.
//
// # Signals and persistence
//
// Status changes publish signal.KindCheck and combined health changes
// publish signal.KindHealth, after the change is stored, so a subscriber that
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
// Resources and Resource report the current state; Series, CheckSummary and
// ResourceSummary read history; FiringAlerts reads alert state.
package monitor
