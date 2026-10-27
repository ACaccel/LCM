# LCM

Self-hosted service lifecycle control plane: a single Go binary with SQLite. Rewrite in progress (foundation and policy engine phases done). Design and phase plan live in `.plan/redesign/` (not versioned). The legacy implementation is at commit `18a24f6`.

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
| `cmd/lcm/` | Entry point and subcommands (`serve`, `admin`, `token`, `version`) |
| `internal/config/` | Env and flag configuration, validation, slog setup |
| `internal/store/` | SQLite handles (writer + reader pool), migrator, `storetest.Open(t)` test helper |
| `internal/auth/` | Password hashing, sessions, API tokens, `Actor` and its context helpers |
| `internal/audit/` | Audit log writer and reader |
| `internal/clock/` | Injectable clock (`clock.Real`, `clock.Fake` for tests) and clock-driven context timeouts |
| `internal/model/` | `lcm/v1` Policy and Module documents: strict decoding, value normalization, structural validation |
| `internal/expr/` | Expression and `{{ }}` template compiler over expr-lang, with static reference extraction |
| `internal/node/` | Node SDK: `Spec[P]`, registry, props schema reflection and validation, `RunContext` |
| `internal/nodes/core/` | Built-in flow, data and module nodes |
| `internal/engine/` | Compiler, scheduler, run state, checkpoints, recovery, concurrency modes, observers; semantics in `doc.go`; `enginetest/` harness |
| `internal/api/` | HTTP handlers, middleware chain, error envelope |
| `internal/buildinfo/` | Version metadata |

Later phases add `catalog`, `runtime`, `monitor`, `scheduler`, more `nodes/` packages and `web/` as laid out in the design spec.

## Repo rules

- All code lives under `internal/` except `cmd/`; there is no public Go API.
- Every state-changing action writes an audit entry through `audit.Log.Record` with the actor taken from the context (`auth.WithActor`). CLI commands use `auth.CLI`. Never put secrets in audit details or logs.
- Writes go through `store.DB.Write`; reads use `store.DB.Reader()`. Times are stored as Unix milliseconds.
- Code with expiry or time-window logic takes an injected clock (`internal/clock`); tests must not sleep.
- New node types register through `node.Register` in a package under `internal/nodes/`; the engine core does not change for new nodes.
- Tests use a real temp-dir SQLite file (`storetest.Open`); there is no in-memory store implementation.
- API errors use the JSON envelope via `writeError`; JSON fields are camelCase; routes are registered in `internal/api/server.go`.
- Token and user management stays session-only: an API token must never be able to mint or revoke tokens.
- Lint findings are fixed, not disabled; a justified `//nolint:<linter> // reason` is the only exception.
