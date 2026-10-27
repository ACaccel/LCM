# LCM

LCM is a lifecycle control plane for self-hosted services. It tracks each service you operate (where it runs, its version, health and lifecycle stage) and, driven by policy, performs deployment, recovery and backup actions. Every action is guarded, and every decision is auditable and replayable. It targets 1-10 hosts running services under systemd, docker or plain processes, ships as a single binary backed by SQLite, and does not target Kubernetes.

> **Status: rewrite in progress.** The server runs the policy engine, service catalog with lifecycle actions, health monitoring and triggers (signal, git, cron, webhook), serves the full REST and event-stream API, syncs documents from git (policy-as-code), and the `lcm` binary has client commands for it. The web console (React, embedded in the binary) is built; see [Web console](#web-console). The previous implementation is preserved at commit `18a24f6`.

## Build

Requires Go 1.26 or newer. No cgo is needed.

```sh
make build        # writes bin/lcm
make check        # format check, vet, golangci-lint, tests with -race
```

## Run

```sh
export LCM_DATA_DIR=./data               # optional, this is the default

bin/lcm admin set-password               # prompts for the admin password (min 12 characters)
bin/lcm serve                            # listens on 127.0.0.1:7800
```

First login and token use:

```sh
curl -c jar -X POST localhost:7800/api/v1/auth/login \
     -d '{"username":"admin","password":"<password>"}'
curl -b jar -X POST localhost:7800/api/v1/tokens -d '{"name":"ci"}'   # token is shown once
curl -H "Authorization: Bearer <token>" localhost:7800/api/v1/auth/me
```

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
- The token is never a flag value. It comes from `LCM_TOKEN`, or from the file named by `--token-file` or `LCM_TOKEN_FILE` (first non-empty line). A token file that other users can read is refused (`chmod 600`). Create one with `lcm token create --name cli > token`.
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
| `LCM_PRUNE_INTERVAL` | `--prune-interval` | `1h` | How often the retention jobs run |
| `LCM_GITHUB_WEBHOOK_SECRET` | `--github-webhook-secret` | empty | Name (not value) of the secret that verifies GitHub push webhooks on `POST /api/v1/hooks/github`; when empty that endpoint needs an API token |

Config sources keep their clones in `<data dir>/sources` (mode 0700), never in a working copy of yours.

A service is reported `healthy` only when every critical check has passed; until a critical check has its first result the service is `unknown`.

## Policy-as-code

The web UI and API are the default way to configure LCM and take effect at once. Git management is opt-in per resource: services, policies and modules can also be described as YAML documents in a repository, which LCM pulls and applies. Documents describe resources only; command allowlists, the `systemctl` privilege prefix and the units LCM may touch stay in the host configuration and are never read from a document.

Register a repository as a **config source** (admin API, or *Settings > Config sources* in the web console):

```sh
curl -H "Authorization: Bearer $LCM_TOKEN" -X POST localhost:7800/api/v1/config/sources \
     -d '{"name":"infra","repo":"git@host:me/infra.git","branch":"main","path":"lcm","interval":"1m"}'
```

- LCM fetches the branch into its own clone, reads every `*.yaml` and `*.yml` under `path` (recursively, several documents per file allowed, `*.test.yaml` skipped) at the new commit, validates, and applies in the order modules, services, policies. It does this every `interval` (default 1m, 10s to 24h), on `POST /config/sources/{name}/sync` (answers 202 at once; read `lastSyncAt`, `lastCommit`, `lastError` and `syncing` from the source; requests during a running sync join it), and when a GitHub push webhook (`/hooks/github`) names the repository and branch.
- A document that fails validation leaves the resource as it was; the error is on the source (`lastError`), on the service timeline, and published as a warning alert signal with source `configsync`.
- The repository URL must not embed a password. Use an SSH key without a passphrase (git runs with `BatchMode`, and LCM passes only a short list of environment variables to child processes, so `SSH_AUTH_SOCK` is not available) or a git credential helper.
- Resources applied from a source are **owned** by it (`managedBy` in the service and policy views, `GET /config/ownership`). A resource belongs to one source; a second source that claims it is refused. A source never overwrites an existing resource that has no owner (one made in the web UI or API). If the document equals the resource, the source adopts it and nothing changes: this is how resources move under git (`lcm export`, commit, register). If it differs, nothing is written and the claim is a conflict like the one below, notified the same way.

**Editing an owned resource** in the web UI or API works and takes effect immediately; the resource is then **drifted**. Drift is computed: the current version and content differ from what the repository last said. Nothing is overwritten silently:

- When a new commit changes the document of a drifted resource (or defines a different document for an unowned existing one), the sync skips it, marks a **conflict**, and notifies (a `config.conflict` timeline entry for services and a warning signal). Documents that did not change in the repository never conflict.
- Resolve a conflict with `POST /config/ownership/{kind}/{name}/resolve` and `{"use":"git"}` (write the repository document, dropping the local edits, and own the resource) or `{"use":"current"}` (keep the current content, own the resource, close the conflict; the resource stays drifted and acknowledged). Drift also clears by itself once the repository content equals the current document.
- Documents that disappear from the repository mark their resource **orphaned**; it is kept, never deleted. `DELETE /config/ownership/{kind}/{name}` releases a resource to the web UI; deleting a source releases everything it owned.

**Manual apply and export.** `lcm apply -f` works for resources nobody owns. For an owned resource it refuses (`skipped`, exit status 3) unless the document equals the current content or `--force` is given; a forced apply writes the document but leaves the resource owned, so it shows as drifted and the repository stays the source of truth. `--dry-run` only validates and diffs. `lcm export` writes the canonical YAML of GUI-created resources, in apply order and sorted by name, so they can be committed and put under git; applying the export back changes nothing. A policy that was undeployed in the UI is not redeployed by a sync unless its document changes.

Opening a pull request from the UI (write-back) is not built; export is the way back to git for now.

## Web console

`lcm serve` serves the console at `/` on the same listener as the API (`/api/*`). Log in with the admin account from `lcm admin set-password`. Pages: overview, services (detail with timeline, health checks, deployments and configuration), timeline, policies (graph editor with a YAML view), runs (live graph of a run), hosts and settings (API tokens, config sources, ownership, locks). The text is Traditional Chinese and lives in `web/src/strings.ts`; light and dark themes follow the browser. A service document with the annotation `lcm.dev/dashboard-url` (an `http` or `https` link, for example to Grafana) gets an external dashboard link in its header. The policy editor stores node positions as JSON in the policy's `metadata.annotations["lcm.dev/layout"]`; the engine ignores it.

Responses carry a restrictive `Content-Security-Policy` (same-origin scripts only; inline styles are allowed because CodeMirror and React Flow need them). The console uses the REST API and one server-sent-event connection per browser tab; it has no server-side state.

**Build.** The console is a Vite project in `web/` whose output goes to `internal/webui/dist`, which the binary embeds. `make build` builds it first when Node.js 24 and npm are installed. Without Node, `make build` and `go build` still work and the binary answers every console request with a page that says how to build it (a committed empty `internal/webui/dist/.gitkeep` keeps the embed valid).

```sh
make web         # npm ci (when needed) and npm run build
make web-check   # typecheck, lint, unit tests, generated API types up to date
```

**Develop.** Run the Go server, then the Vite dev server, which proxies `/api` to it and reloads on changes:

```sh
bin/lcm serve                              # or go run ./cmd/lcm serve
cd web && npm ci && npm run dev            # http://127.0.0.1:5173
LCM_DEV_API=http://127.0.0.1:9000 npm run dev   # when the server listens elsewhere
```

The API types in `web/src/api/schema.d.ts` are generated from `internal/api/openapi.yaml`. After changing the OpenAPI document run `npm run gen:api` in `web/` and commit the result; `npm run check:api` (part of `make web-check` and CI) fails when it is stale. Tests are Vitest unit and component tests (`npm test`); there is no browser end-to-end suite.

## API v1

The API is described by a hand-written OpenAPI 3.1 document, `internal/api/openapi.yaml`, which the server also serves at `GET /api/v1/openapi.yaml`. It is the reference for endpoints, request and response shapes, and error codes; a test fails when it and the registered routes disagree, and the integration tests check every response against it.

Overview:

- JSON uses camelCase; errors use `{"error":{"code","message","details"}}` with status codes 400, 401, 403, 404, 409, 422, 429 and 500. A 422 lists `details.issues` as `{path, message}`.
- Services, policies and modules are documents sent as YAML or JSON. Every save adds a version; updates need `If-Match: <version>` and a stale one answers 409.
- Lists take `limit` and an opaque `cursor` and answer `{items, nextCursor}`.
- Lifecycle actions on services run in the background (`202` and an action id for `GET /api/v1/actions/{id}`); `?dryRun=true` returns the plan instead.
- `GET /api/v1/events` streams signals, run and node progress, timeline entries, action progress and catalog changes as server-sent events, filtered by `service`, `policy`, `run` and `types`.
- `POST /apply`, `GET /export` and `/config/sources`, `/config/ownership` are the policy-as-code endpoints (admin only).
- Alerts (`/hooks/alerts/{source}`), heartbeats (`/heartbeats/{svc}/{check}`) and webhooks (`/hooks/{name}`, `/hooks/github`) feed LCM from outside.

Authentication: browsers use the `lcm_session` cookie from `POST /api/v1/auth/login`; scripts use `Authorization: Bearer <token>`. Sessions and `admin` tokens may use the whole API except token management, which is session-only, so a token can never create or revoke tokens. `ingest` tokens (for example `lcm token create --name alertmanager --scope ingest`) may only post alerts, heartbeats and webhooks, and call `GET /api/v1/auth/me`. A webhook listener that has an `hmacSecret`, and the GitHub endpoint when `LCM_GITHUB_WEBHOOK_SECRET` is set, accept a valid HMAC signature instead of a token.

The server speaks plain HTTP. To expose it beyond localhost, put a TLS-terminating reverse proxy in front and set `LCM_COOKIE_SECURE=true`.
