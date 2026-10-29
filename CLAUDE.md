# LCM

Self-hosted service lifecycle control plane: a single Go binary with SQLite, a web console and a CLI. The rewrite is complete apart from the items under "Not built yet"; its design and phase plan live in `.plan/redesign/` (not versioned). The legacy implementation is at commit `18a24f6`. User documentation: `README.md` (setup, CLI, configuration) and `docs/` (below).

## Stack

- Go 1.26+, standard library first: `net/http` with `ServeMux` patterns, `log/slog`, `flag`. No web framework, no ORM, no CLI framework.
- SQLite via `modernc.org/sqlite` (pure Go; keep `CGO_ENABLED=0` builds working).
- Web console in `web/`: React 19, TypeScript (strict), Vite, `@xyflow/react`, TanStack Query, React Router, CodeMirror 6, `yaml`; API types generated from OpenAPI (`openapi-typescript`/`openapi-fetch`); plain CSS with variables; Vitest. Built output is embedded through `internal/webui`.
- Hand-written `database/sql` queries; schema changes are new numbered files in `internal/store/migrations/`, applied at startup.

## Commands

```sh
make build   # bin/lcm with version ldflags
make test    # go test -race ./...
make lint    # golangci-lint run (v2; install: go install github.com/golangci/golangci-lint/v2/cmd/golangci-lint@latest)
make fmt     # golangci-lint fmt (gofmt + goimports)
make check   # fmt-check, vet, lint, test, plus web-check when Node is available: run before finishing any change
make web     # npm ci (if needed) + vite build into internal/webui/dist
make web-check  # in web/: tsc, eslint, vitest, generated API types up to date (npm run check)
```

Opt-in end-to-end scenarios (a real throwaway user-scope systemd unit, git repositories and the real clock; about 90 s): `LCM_IT_E2E=1 go test ./internal/app -run TestRealScenarios -v -count=1`. Diagnosis: `LCM_PPROF_LISTEN=127.0.0.1:6060` serves pprof (off by default).

Web development: Node 24 (`export PATH=$HOME/.local/node/bin:$PATH` here), `cd web && npm run dev` proxies `/api` to a running `lcm serve`. After editing `internal/api/openapi.yaml` run `npm run gen:api` in `web/`.

## Directory map

| Path | Contents |
|---|---|
| `cmd/lcm/` | Entry point and subcommands: `serve` (a thin wrapper over `internal/app`), `admin`, `token`, `version`, and the API client commands `apply`, `export`, `validate`, `get`, `run`, `action` (`client.go` holds the shared token and document handling) |
| `internal/app/` | Composition root: builds every component in dependency order, adapters (lock -> catalog, execx -> monitor), monitor sync, retention jobs, `Run`/`Close` |
| `internal/config/` | Env and flag configuration, validation, slog setup |
| `internal/store/` | SQLite handles (writer + reader pool), migrator, `storetest.Open(t)` test helper |
| `internal/auth/` | Password hashing, sessions, API tokens, `Actor` and its context helpers |
| `internal/audit/` | Audit log writer and reader |
| `internal/clock/` | Injectable clock (`clock.Real`, `clock.Fake` for tests) and clock-driven context timeouts |
| `internal/signal/` | In-process signal bus (check, health, lifecycle, alert); lossy by design |
| `internal/model/` | `lcm/v1` Policy, Module and Service documents: strict decoding, value normalization, structural validation |
| `internal/expr/` | Expression and `{{ }}` template compiler over expr-lang, with static reference extraction |
| `internal/node/` | Node SDK: `Spec[P]` (including `ServiceProp`, the prop naming the service a node acts on), registry, props schema reflection and validation, `RunContext` |
| `internal/nodes/core/` | Built-in flow, data and module nodes |
| `internal/nodes/lifecycle/` | `lifecycle.*` nodes over the catalog manager |
| `internal/nodes/{flow,guard,network,notify,stateful}/` | Waiting and polling, time windows, HTTP and CI requests, notifications, state and lock nodes |
| `internal/engine/` | Compiler, scheduler, run state, checkpoints, recovery, concurrency modes, observers, static service targets of a plan; semantics in `doc.go`; `enginetest/` harness |
| `internal/catalog/` | Service documents and versions, lifecycle state machine, deployments, timeline, the `Manager` that performs lifecycle actions, action availability and reconciliation |
| `internal/runtime/` | Runtime adapters (systemd, docker, compose, command, external) and `CapabilitiesOf`, the actions a runtime configuration supports |
| `internal/execx/` | Command execution with bounded lifetime, minimal environment and a `Fake` for tests |
| `internal/monitor/` | Health checks, history, rollups, health aggregation, alert ingestion; `alert/` parses Alertmanager, Uptime Kuma, Gatus and generic payloads |
| `internal/trigger/` | `trigger.*` nodes and the manager running signal, git, cron and webhook watchers |
| `internal/lock/` | Durable named locks with expiry |
| `internal/state/` | Per-policy key-value state and the `state` expression variable |
| `internal/secret/` | Secret resolution from files and environment; values never logged |
| `internal/events/` | In-process event hub behind the SSE stream: bounded, filtered subscriptions fed by the bus, engine observers and catalog hooks |
| `internal/modules/` | Versioned module store; also the engine's module resolver |
| `internal/api/` | HTTP handlers (one file per resource), middleware chain, error envelope, `openapi.yaml` |
| `internal/configsync/` | Policy-as-code: apply and export of Service, Policy and Module documents, config sources (LCM's own git clones), ownership, drift and conflicts, sync loop; semantics in `configsync.go` |
| `internal/client/` | HTTP client of the API behind the `lcm` client commands; sends the token to the configured server only |
| `internal/webui/` | Embeds `dist/` (built console) and serves it with SPA fallback, caching and CSP; `dist/.gitkeep` is the only committed file there |
| `web/` | React 19 + TypeScript + Vite console; `src/api` (generated types, client, SSE), `src/policy` (YAML/graph logic), `src/pages`, `src/strings.ts` (all UI text) |
| `internal/templates/` | Built-in policy templates: descriptors and the renderer over `examples/templates/*.yaml` (embedded through the data-only `examples` package) |
| `internal/buildinfo/` | Version metadata |
| `deploy/` | Host installation: `bootstrap.sh` (root, idempotent), `seed.sh`, `uninstall.sh`, `self-install` (the only sudo command of the `lcm` account), unit, env, sudoers and the self-deploy documents; tested by `internal/app/deploy_test.go`, opt-in end-to-end `LCM_IT_SELFDEPLOY=1 go test ./internal/app -run TestRealSelfDeploy` |
| `docs/` | `services.md` (Service documents, runtimes and actions, checks, lifecycle), `policies.md` (policies, templates, policy-as-code), `architecture.md` (components, data flow, engine summary, security model), `operations.md` (install, self-deploy, backup, upgrade) |

Not built yet: replay and opening pull requests from the UI (export is the way back to git).

## Repo rules

- All code lives under `internal/` except `cmd/` and `examples/examples.go`, which only embeds the example documents; there is no public Go API.
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
- Config sync never overwrites a drifted resource and never deletes one; documents carry no privileges (allowlists and unit scopes stay in host config). Git runs only through `execx` with a fixed environment and the repository after `--`.
- Token and user management stays session-only: an API token must never be able to mint or revoke tokens.
- LCM never records a state change it did not make: an action the runtime cannot perform is refused (`catalog.ErrUnsupported`), never configured as a no-op command.
- Behavior visible to users is documented in one place: `README.md` for setup, CLI and configuration, `docs/services.md`, `docs/policies.md`, `docs/architecture.md` and `docs/operations.md` for the rest; update the page that owns a fact and link to it elsewhere.
- Lint findings are fixed, not disabled; a justified `//nolint:<linter> // reason` is the only exception.
- Web console: UI text only in `web/src/strings.ts` (Traditional Chinese, technical terms in English); no node types or validation rules duplicated from the server (palette and forms come from `/node-types`, validation from `/validate`); the generated `web/src/api/schema.d.ts` is never edited by hand; no UI framework, no chart library.
