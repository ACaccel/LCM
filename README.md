# LCM

LCM manages self-hosted software services, the Linux workstation running the controller and network routers as one kind of object, the `lcm/v2` [Resource](docs/resources.md). It checks resources and performs deployments, rollbacks, restarts, backups and jobs from the console, CLI or policies. The local machine adds OS observations, bounded event history, maintenance and durable manual power requests; routers add observation and controlled reboots. Relations between resources drive impact previews, acknowledgement of disruptive actions and ordered group actions. LCM ships as one Go binary with SQLite; Kubernetes is outside its scope.

**Status.** Services and the local machine are complete and accepted (operator-confirmed on 2026-11-03; see the [validation record](docs/machine-validation.md), which predates the resource model). The unified Resource model and Xiaomi routers were delivered on 2026-11-05 with automated checks; router acceptance on real hardware is pending the operator's confirmation. Upgrading replaces `lcm/v1` documents, API paths and CLI commands without aliases; see the [upgrade notes](docs/operations.md#upgrade-to-lcmv2-resources). Power is disabled by default and requires a separately installed restricted helper. SSH machines, cloud VMs and other resource types remain in the [roadmap](docs/roadmap.md). The previous implementation is preserved at commit `18a24f6`.

## Documentation

| Document | Contents |
|---|---|
| [docs/roadmap.md](docs/roadmap.md) | Planned capabilities, priorities, dependencies, and completion criteria |
| [docs/resources.md](docs/resources.md) | The shared Resource model: document, types, phases, state and health, actions, relations, impact, group actions, connections, routers |
| [docs/services.md](docs/services.md) | Service resources: runtime adapters and the actions they allow, deployments, health checks, lifecycle states, reconciliation, timeline |
| [docs/machines.md](docs/machines.md) | The local machine resource: native monitoring, event coverage, maintenance, power evidence and archives |
| [docs/security.md](docs/security.md) | Security console, incidents, traffic, notifications and restricted host controls |
| [docs/policies.md](docs/policies.md) | Policies, built-in templates, policy-as-code (git config sources, ownership, drift) |
| [docs/architecture.md](docs/architecture.md) | Components, data flow, engine semantics, security model (authentication, tokens, privileges, secrets) |
| [docs/operations.md](docs/operations.md) | Installing on a host with `deploy/` (including self-deploy), router connections, backup, upgrade (including `lcm/v2`), remote access |
| [`internal/api/openapi.yaml`](internal/api/openapi.yaml) | The API v1 reference, also served at `GET /api/v1/openapi.yaml` |

## Quick start

Requires Go 1.26 or newer (no cgo); Node.js 24 to build the console.

```sh
make build                       # bin/lcm, with the console embedded when Node is installed
bin/lcm admin set-password       # create the admin account (min 8 characters)
bin/lcm serve                    # http://127.0.0.1:7800, data in ./data
```

Log in to the console, add a service under *資源 > 建立資源* (see [services](docs/services.md)), then attach a policy from a template on its *Policy* tab. For the CLI and scripts, create a token under *設定 > API token* (or `bin/lcm token create --name cli > token && chmod 600 token`).

The console (Traditional Chinese, light and dark themes) shows resources of every type with a type filter, a detail page per type (monitoring/history, maintenance, operation evidence, relations and impact), the timeline, policies, live runs, security and settings. It uses the REST API and one server-sent-event stream.

## CLI

| Command | Purpose |
|---|---|
| `lcm serve` | Run the HTTP server |
| `lcm admin set-password [--username admin]` | Create the admin user or change its password; reads from the terminal without echo, or from stdin when piped |
| `lcm token create --name <name> [--scope admin\|ingest]` | Create an API token and print it once; the default scope is `admin` |
| `lcm token list` / `lcm token revoke <id>` | List or revoke API tokens |
| `lcm version` | Print version information |
| `lcm storage-schema` | Report the binary's supported storage schema without starting a controller; used for upgrade compatibility checks |
| `lcm convert -f <file\|dir> [-f ...]` | Print `lcm/v1` documents converted to `lcm/v2`, offline; [rules](docs/policies.md#policy-as-code) |
| `lcm apply -f <file\|dir> [-f ...] [--dry-run] [--force]` | Apply Resource, Policy and Module documents; prints a result and a diff per document |
| `lcm export [kind[/name]]` | Print documents as YAML (`resource`, `policy`, `module`, or everything) |
| `lcm validate -f <file\|dir>` | Check documents on the server without applying them |
| `lcm get resources\|policies\|runs\|locks\|sources\|connections [name] [--type t] [-o table\|json\|yaml] [--limit n]` | List or show objects; `--type service\|machine\|router` filters resources; `@<id>` selects an archived resource |
| `lcm resource action <service> <action> [--version v] [--job j] [--dry-run] [--wait] [--acknowledge-impact]` | Run a service lifecycle action; `--dry-run` prints the plan and impact |
| `lcm resource <command> <name> [flags]` | `observe`, `history`, `events`, `capabilities`, `maintenance`, `reboot`, `shutdown`, `operations`, `operation`, `resolve`, `retire`, `resume`, `unmanage`, `impact`; [confirmation and recovery rules](docs/machines.md#maintenance-and-power-requests) |
| `lcm resource group-action --label k=v [...] <action> [--dry-run] [--wait] [--acknowledge-impact]` | Run one service action on matching services in dependency order; [rules](docs/resources.md#group-actions) |
| `lcm run <policy> [--trigger id] [--event json] [--dry-run] [--wait]` | Start a policy run |

Admin and token commands open the database directly, so local filesystem access is administrator access. They are recorded in the audit log as actor `system` / `cli`.

The other commands talk to a running server over the API:

- The server is `LCM_SERVER` (default `http://127.0.0.1:7800`) or `--server`.
- The token is never a flag value. It comes from `LCM_TOKEN`, or from the file named by `--token-file` or `LCM_TOKEN_FILE` (first non-empty line). A token file that other users can read is refused (`chmod 600`). Create one in the console or with `lcm token create --name cli > token`.
- `-f` takes files or directories (searched recursively for `*.yaml` and `*.yml`, skipping `*.test.yaml`) and `-` for standard input; files may hold several documents separated by `---`.
- Exit status: `0` success, `1` server or runtime error (including a failed run or action with `--wait`), `2` usage, `3` the command ran but documents were invalid or not applied.
- Tables are plain aligned columns without colour.

## Configuration

Environment variables configure the server; each can be overridden by the `serve` flag shown. Invalid values stop startup with an error. Secrets are never configured here: the admin password is set through the CLI only.

| Variable | Flag | Default | Description |
|---|---|---|---|
| `LCM_LISTEN` | `--listen` | `127.0.0.1:7800` | Listen address; a warning is logged when it is not loopback |
| `LCM_DATA_DIR` | `--data-dir` | `./data` | Data directory (mode 0700); the database is `lcm.db` inside it |
| `LCM_LOG_FORMAT` | `--log-format` | `text` | `text` or `json` |
| `LCM_LOG_LEVEL` | `--log-level` | `info` | `debug`, `info`, `warn` or `error` |
| `LCM_COOKIE_SECURE` | `--cookie-secure` | `false` | Set the `Secure` attribute on the session cookie; enable behind HTTPS |
| `LCM_SESSION_TTL` | `--session-ttl` | `168h` | Login session lifetime |
| `LCM_TRUSTED_PROXIES` | `--trusted-proxies` | empty | Comma-separated CIDR prefixes (or single addresses) of reverse proxies. Only when the peer is one of them, failed logins are counted against the rightmost `X-Forwarded-For` address that is not a trusted proxy; empty uses the peer address. Invalid entries stop startup |
| `LCM_ENGINE_WORKERS` | `--engine-workers` | `0` | Policy engine worker pool size; `0` selects `max(GOMAXPROCS*2, 4)` |
| `LCM_SECRETS_DIR` | `--secrets-dir` | `<data dir>/secrets` | Directory of secret files (one regular file per secret, mode 0600; a looser mode or a symlink is refused); created with mode 0700. Environment variables `LCM_SECRET_<NAME>` are the fallback |
| `LCM_CALENDAR_DIR` | `--calendar-dir` | `<data dir>/calendars` | Directory that `trigger.cron` calendar files are confined to |
| `LCM_SYSTEMCTL` / `LCM_DOCKER` / `LCM_GIT` | `--systemctl` / `--docker` / `--git` | `systemctl` / `docker` / `git` | Program paths; a missing program only logs a warning at startup |
| `LCM_JOURNALCTL` | `--journalctl` | `journalctl` | Program used for bounded local Machine journal reads; unavailable access is shown as source coverage |
| `LCM_MACHINE_POWER` | `--machine-power` | `false` | Permit new Machine power submissions only after the separately installed helper and recovery prerequisites pass; receipt queries remain available when disabled |
| `LCM_SYSTEMCTL_PRIVILEGE` | `--systemctl-privilege` | `sudo -n` | Prefix for system-scope `systemctl` start, stop and restart; `none` (or an empty flag value) runs it directly |
| `LCM_RUNTIME_STATUS_TIMEOUT` | `--runtime-status-timeout` | `15s` | Timeout of runtime status queries |
| `LCM_RUNTIME_ACTION_TIMEOUT` | `--runtime-action-timeout` | `5m` | Timeout of runtime start, stop and restart |
| `LCM_LOCK_TTL` | `--lock-ttl` | `1h` | Expiry of a resource lock whose holder crashed; must exceed the longest lifecycle action (max `168h`) |
| `LCM_MONITOR_CONCURRENCY` | `--monitor-concurrency` | `8` | Health probes running at the same time |
| `LCM_MONITOR_RAW_RETENTION` | `--monitor-raw-retention` | `168h` | How long raw check results and machine/router snapshots are kept |
| `LCM_MONITOR_ROLLUP_RETENTION` | `--monitor-rollup-retention` | `2160h` | How long check rollups and machine/router minute points are kept; must not be shorter than raw retention |
| `LCM_ALERT_RESOURCE_LABELS` | `--alert-resource-labels` | `lcm_resource,resource,lcm_service,service` | Comma-separated alert labels that name the affected resource; the first present wins |
| `LCM_CONNECTIONS_FILE` | `--connections-file` | empty | Host [connections file](docs/resources.md#connections) for network-reached resources such as routers; read once at startup; it must be owned by root in root-owned directories, and an invalid file or unresolvable password secret stops startup; empty means no connections |
| `LCM_TIMELINE_RETENTION` | `--timeline-retention` | `2160h` | How long timeline entries and machine/router events are kept; unresolved evidence has retention exceptions |
| `LCM_RUN_RETENTION_KEEP` | `--run-retention-keep` | `200` | Finished runs kept per policy regardless of age |
| `LCM_RUN_RETENTION_AGE` | `--run-retention-age` | `720h` | Finished runs younger than this are kept; older ones beyond the newest N are deleted |
| `LCM_DEPLOYMENT_RETENTION_KEEP` | `--deployment-retention-keep` | `100` | Deployment records kept per service; the idempotency records of finished actions follow the run retention age |
| `LCM_MEMORY_LIMIT` | `--memory-limit` | `40MiB` | Soft memory limit of the Go runtime (a size such as `40MiB` or `64M`; `0` or `off` disables it). A `GOMEMLIMIT` in the environment is respected unless this is set explicitly |
| `LCM_PPROF_LISTEN` | `--pprof-listen` | empty | Serve `net/http/pprof` on this loopback address for diagnosis; off when empty |
| `LCM_PRUNE_INTERVAL` | `--prune-interval` | `1h` | How often the retention jobs run |
| `LCM_GITHUB_WEBHOOK_SECRET` | `--github-webhook-secret` | empty | Name (not value) of the secret that verifies GitHub push webhooks on `POST /api/v1/hooks/github`; when empty that endpoint needs an API token |

## API

The API is described by the hand-written OpenAPI 3.1 document [`internal/api/openapi.yaml`](internal/api/openapi.yaml); a test fails when it and the registered routes disagree, and the integration tests check every response against it.

- JSON uses camelCase; errors are `{"error":{"code","message","details"}}`, and a 422 lists `details.issues` as `{path, message}`.
- Resource, Policy and Module documents (`lcm/v2`) accept YAML or JSON; updates need `If-Match: <version>` and a stale one answers 409. `GET /api/v1/resource-types` describes types, adapters and actions.
- Lists take `limit` and an opaque `cursor` and answer `{items, nextCursor}`.
- Resources live under `/api/v1/resources` (`@<id>` reads any incarnation), archives under `/resource-archives`, host connections under `/connections` (read only).
- Service actions run in the background (`POST /api/v1/resources/{resource}/actions/{action}`, 202 and an id for `GET /api/v1/actions/{id}`); `?dryRun=true` returns the plan and impact instead. Disruptive actions on resources others depend on need `acknowledgeImpact` (409 `impact_unacknowledged` otherwise); group actions use `/resource-groups/actions`.
- Machine and router operations are durable `/api/v1/resources/{resource}/operations` records; acceptance does not prove completion. See the [machine](docs/machines.md#maintenance-and-power-requests) and [router](docs/resources.md#reboot) completion rules.
- `GET /api/v1/events` streams signals, run and node progress, timeline entries, action progress, resource changes and machine/router observations as server-sent events, filtered by `resource`, `resourceId`, `policy`, `run` and `types`. SSE is best effort; clients recover current state and history from the API after disconnect or overflow. A stream checks its token or session again at every heartbeat (15 s) and ends when it was revoked, logged out or expired; a session stream also ends at the session expiry.
- Alerts (`/hooks/alerts/{source}`), heartbeats (`/heartbeats/{resource}/{check}`) and webhooks (`/hooks/{name}`, `/hooks/github`) feed LCM from outside. Webhooks answer 503 with `Retry-After` until startup recovery has finished, so they never start runs or watchers ahead of it.

Browsers authenticate with a session cookie, scripts with `Authorization: Bearer <token>`; token scopes and what each may call are in the [security model](docs/architecture.md#security-model).

## Development

```sh
make check       # format check, vet, golangci-lint, go test -race, and the web checks when Node is available
make web         # npm ci (when needed) and build the console into internal/webui/dist
make web-check   # typecheck, lint, Vitest, generated API types up to date
```

The console is a Vite project in `web/`; its output goes to `internal/webui/dist`, which the binary embeds. Without Node, `make build` and `go build` still work and the console answers with a page that says how to build it (the committed `internal/webui/dist/.gitkeep` keeps the embed valid). To work on it, run `bin/lcm serve` and then `cd web && npm ci && npm run dev` (http://127.0.0.1:5173, proxying `/api`; set `LCM_DEV_API` when the server listens elsewhere). After changing `internal/api/openapi.yaml`, run `npm run gen:api` in `web/` and commit `web/src/api/schema.d.ts`.
