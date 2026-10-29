# LCM

LCM is a lifecycle control plane for self-hosted services. It keeps a catalog of the services you run (how to control them, their version, health and lifecycle state), checks them, and performs deployments, rollbacks, restarts, backups and jobs, either from the web console and CLI or automatically through policies. Every action is locked per service, previewable as a dry run, and recorded in the timeline and audit log. It targets 1-10 hosts running services under systemd, docker or plain processes, ships as a single Go binary with SQLite, and does not target Kubernetes.

**Status.** The rewrite is complete apart from run replay and opening pull requests from the console. The previous implementation is preserved at commit `18a24f6`.

## Documentation

| Document | Contents |
|---|---|
| [docs/services.md](docs/services.md) | Service documents: runtimes and the actions they allow, deployments, health checks, lifecycle states, reconciliation, timeline |
| [docs/policies.md](docs/policies.md) | Policies, built-in templates, policy-as-code (git config sources, ownership, drift) |
| [docs/architecture.md](docs/architecture.md) | Components, data flow, engine semantics, security model (authentication, tokens, privileges, secrets) |
| [docs/operations.md](docs/operations.md) | Installing on a host with `deploy/` (including self-deploy), backup, upgrade, remote access |
| [`internal/api/openapi.yaml`](internal/api/openapi.yaml) | The API v1 reference, also served at `GET /api/v1/openapi.yaml` |

## Quick start

Requires Go 1.26 or newer (no cgo); Node.js 24 to build the console.

```sh
make build                       # bin/lcm, with the console embedded when Node is installed
bin/lcm admin set-password       # create the admin account (min 8 characters)
bin/lcm serve                    # http://127.0.0.1:7800, data in ./data
```

Log in to the console, add a service under *服務 > 建立服務* (see [services](docs/services.md)), then attach a policy from a template on its *Policy* tab. For the CLI and scripts, create a token under *設定 > API token* (or `bin/lcm token create --name cli > token && chmod 600 token`).

The console (Traditional Chinese, light and dark themes) shows an overview, services with their timeline, health checks, deployments, policies and configuration, a global timeline, the policy graph editor, live runs, hosts and settings. It uses the REST API and one server-sent-event stream; it has no server-side state.

## CLI

| Command | Purpose |
|---|---|
| `lcm serve` | Run the HTTP server |
| `lcm admin set-password [--username admin]` | Create the admin user or change its password; reads from the terminal without echo, or from stdin when piped |
| `lcm token create --name <name> [--scope admin\|ingest]` | Create an API token and print it once; the default scope is `admin` |
| `lcm token list` / `lcm token revoke <id>` | List or revoke API tokens |
| `lcm version` | Print version information |
| `lcm apply -f <file\|dir> [-f ...] [--dry-run] [--force]` | Apply Service, Policy and Module documents; prints a result and a diff per document |
| `lcm export [kind[/name]]` | Print resources as YAML (`service`, `policy`, `module`, or everything) |
| `lcm validate -f <file\|dir>` | Check documents on the server without applying them |
| `lcm get services\|policies\|runs\|locks\|sources [name] [-o table\|json\|yaml] [--limit n]` | List or show resources |
| `lcm run <policy> [--trigger id] [--event json] [--dry-run] [--wait]` | Start a policy run |
| `lcm action <service> <action> [--version v] [--job j] [--dry-run] [--wait]` | Run a lifecycle action; `--dry-run` prints the plan |

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
| `LCM_ENGINE_WORKERS` | `--engine-workers` | `0` | Policy engine worker pool size; `0` selects `max(GOMAXPROCS*2, 4)` |
| `LCM_SECRETS_DIR` | `--secrets-dir` | `<data dir>/secrets` | Directory of secret files (one file per secret, mode 0600 recommended); created with mode 0700. Environment variables `LCM_SECRET_<NAME>` are the fallback |
| `LCM_CALENDAR_DIR` | `--calendar-dir` | `<data dir>/calendars` | Directory that `trigger.cron` calendar files are confined to |
| `LCM_SYSTEMCTL` / `LCM_DOCKER` / `LCM_GIT` | `--systemctl` / `--docker` / `--git` | `systemctl` / `docker` / `git` | Program paths; a missing program only logs a warning at startup |
| `LCM_SYSTEMCTL_PRIVILEGE` | `--systemctl-privilege` | `sudo -n` | Prefix for system-scope `systemctl` start, stop and restart; `none` (or an empty flag value) runs it directly |
| `LCM_RUNTIME_STATUS_TIMEOUT` | `--runtime-status-timeout` | `15s` | Timeout of runtime status queries |
| `LCM_RUNTIME_ACTION_TIMEOUT` | `--runtime-action-timeout` | `5m` | Timeout of runtime start, stop and restart |
| `LCM_LOCK_TTL` | `--lock-ttl` | `1h` | Expiry of a service lock whose holder crashed; must exceed the longest lifecycle action (max `168h`) |
| `LCM_MONITOR_CONCURRENCY` | `--monitor-concurrency` | `8` | Health probes running at the same time |
| `LCM_MONITOR_RAW_RETENTION` | `--monitor-raw-retention` | `168h` | How long raw check results are kept |
| `LCM_MONITOR_ROLLUP_RETENTION` | `--monitor-rollup-retention` | `2160h` | How long one-minute rollups are kept; must not be shorter than the raw retention |
| `LCM_ALERT_SERVICE_LABELS` | `--alert-service-labels` | `lcm_service,service` | Comma-separated alert labels that name the affected service |
| `LCM_TIMELINE_RETENTION` | `--timeline-retention` | `2160h` | How long service timeline entries are kept |
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
- Services, policies and modules are versioned documents sent as YAML or JSON; updates need `If-Match: <version>` and a stale one answers 409.
- Lists take `limit` and an opaque `cursor` and answer `{items, nextCursor}`.
- Lifecycle actions run in the background (202 and an id for `GET /api/v1/actions/{id}`); `?dryRun=true` returns the plan instead.
- `GET /api/v1/events` streams signals, run and node progress, timeline entries, action progress and catalog changes as server-sent events, filtered by `service`, `policy`, `run` and `types`.
- Alerts (`/hooks/alerts/{source}`), heartbeats (`/heartbeats/{svc}/{check}`) and webhooks (`/hooks/{name}`, `/hooks/github`) feed LCM from outside.

Browsers authenticate with a session cookie, scripts with `Authorization: Bearer <token>`; token scopes and what each may call are in the [security model](docs/architecture.md#security-model).

## Development

```sh
make check       # format check, vet, golangci-lint, go test -race, and the web checks when Node is available
make web         # npm ci (when needed) and build the console into internal/webui/dist
make web-check   # typecheck, lint, Vitest, generated API types up to date
```

The console is a Vite project in `web/`; its output goes to `internal/webui/dist`, which the binary embeds. Without Node, `make build` and `go build` still work and the console answers with a page that says how to build it (the committed `internal/webui/dist/.gitkeep` keeps the embed valid). To work on it, run `bin/lcm serve` and then `cd web && npm ci && npm run dev` (http://127.0.0.1:5173, proxying `/api`; set `LCM_DEV_API` when the server listens elsewhere). After changing `internal/api/openapi.yaml`, run `npm run gen:api` in `web/` and commit `web/src/api/schema.d.ts`.
