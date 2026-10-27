# LCM

Self-hosted service lifecycle control plane: a single Go binary with SQLite. Rewrite in progress (foundation, policy engine, catalog, monitor, triggers, integration and the HTTP API v1 done; apply/export/config sync, the CLI client and the web UI remain). Design and phase plan live in `.plan/redesign/` (not versioned). The legacy implementation is at commit `18a24f6`.

## Stack

- Go 1.26+, standard library first: `net/http` with `ServeMux` patterns, `log/slog`, `flag`. No web framework, no ORM, no CLI framework.
- SQLite via `modernc.org/sqlite` (pure Go; keep `CGO_ENABLED=0` builds working).
- Hand-written `database/sql` queries; schema changes are new numbered files in `internal/store/migrations/`, applied at startup.

## Commands

```sh
make build   # bin/lcm with version ldflags
make test    # go test -race ./...
make lint    # golangci-lint run (v2; install: go install github.com/golangci/golangci-lint/v2/cmd/golangci-lint@latest)
make fmt     # golangci-lint fmt (gofmt + goimports)
make check   # fmt-check, vet, lint, test: run before finishing any change
```

## Directory map

| Path | Contents |
|---|---|
| `cmd/lcm/` | Entry point and subcommands (`serve`, `admin`, `token`, `version`); `serve` is a thin wrapper over `internal/app` |
| `internal/app/` | Composition root: builds every component in dependency order, adapters (lock -> catalog, execx -> monitor), monitor sync, retention jobs, `Run`/`Close` |
| `internal/config/` | Env and flag configuration, validation, slog setup |
| `internal/store/` | SQLite handles (writer + reader pool), migrator, `storetest.Open(t)` test helper |
| `internal/auth/` | Password hashing, sessions, API tokens, `Actor` and its context helpers |
| `internal/audit/` | Audit log writer and reader |
| `internal/clock/` | Injectable clock (`clock.Real`, `clock.Fake` for tests) and clock-driven context timeouts |
| `internal/signal/` | In-process signal bus (check, health, lifecycle, alert); lossy by design |
| `internal/model/` | `lcm/v1` Policy, Module and Service documents: strict decoding, value normalization, structural validation |
| `internal/expr/` | Expression and `{{ }}` template compiler over expr-lang, with static reference extraction |
| `internal/node/` | Node SDK: `Spec[P]`, registry, props schema reflection and validation, `RunContext` |
| `internal/nodes/core/` | Built-in flow, data and module nodes |
| `internal/nodes/lifecycle/` | `lifecycle.*` nodes over the catalog manager |
| `internal/nodes/{flow,guard,network,notify,stateful}/` | Waiting and polling, time windows, HTTP and CI requests, notifications, state and lock nodes |
| `internal/engine/` | Compiler, scheduler, run state, checkpoints, recovery, concurrency modes, observers; semantics in `doc.go`; `enginetest/` harness |
| `internal/catalog/` | Service documents and versions, lifecycle state machine, deployments, timeline, and the `Manager` that performs lifecycle actions |
| `internal/runtime/` | Runtime adapters (systemd, docker, compose, command, external) |
| `internal/execx/` | Command execution with bounded lifetime, minimal environment and a `Fake` for tests |
| `internal/monitor/` | Health checks, history, rollups, health aggregation, alert ingestion; `alert/` parses Alertmanager, Uptime Kuma, Gatus and generic payloads |
| `internal/trigger/` | `trigger.*` nodes and the manager running signal, git, cron and webhook watchers |
| `internal/lock/` | Durable named locks with expiry |
| `internal/state/` | Per-policy key-value state and the `state` expression variable |
| `internal/secret/` | Secret resolution from files and environment; values never logged |
| `internal/events/` | In-process event hub behind the SSE stream: bounded, filtered subscriptions fed by the bus, engine observers and catalog hooks |
| `internal/modules/` | Versioned module store; also the engine's module resolver |
| `internal/api/` | HTTP handlers (one file per resource), middleware chain, error envelope, `openapi.yaml` |
| `internal/buildinfo/` | Version metadata |

Not built yet: `web/`, the full API v1 surface (services, policies, runs, monitor, webhooks, alert ingest) and replay.

## Repo rules

- All code lives under `internal/` except `cmd/`; there is no public Go API.
- Every state-changing action writes an audit entry through `audit.Log.Record` with the actor taken from the context (`auth.WithActor`). CLI commands use `auth.CLI`. Never put secrets in audit details or logs.
- Writes go through `store.DB.Write`; reads use `store.DB.Reader()`. Times are stored as Unix milliseconds.
- Code with expiry or time-window logic takes an injected clock (`internal/clock`); tests must not sleep.
- Components are constructed and connected only in `internal/app`; a new component gets wired there, with an end-to-end test in `internal/app`.
- New node types register through `node.Register` in a package under `internal/nodes/`; the engine core does not change for new nodes.
- Tests use a real temp-dir SQLite file (`storetest.Open`); there is no in-memory store implementation.
- API errors use the JSON envelope via `writeError`; component errors are mapped in one place (`fail` in `internal/api/mapping.go`); JSON fields are camelCase.
- Routes are registered in `internal/api/routes.go` and every one must be described in `internal/api/openapi.yaml` (tests enforce both directions and check responses against the schemas). Handlers stay thin: parse, call a component, map errors.
- Documents (Service, Policy, Module) are versioned; `PUT` takes `If-Match` and a mismatch is 409. Lists page with `limit` and an opaque `cursor`.
- API tokens carry a scope (`admin` or `ingest`); routes use `requireAdmin` unless they are ingest endpoints (`requireAny`) or session-only (`requireSession`).
- Token and user management stays session-only: an API token must never be able to mint or revoke tokens.
- Lint findings are fixed, not disabled; a justified `//nolint:<linter> // reason` is the only exception.
