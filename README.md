# LCM

LCM is a lifecycle control plane for self-hosted services. It tracks each service you operate (where it runs, its version, health and lifecycle stage) and, driven by policy, performs deployment, recovery and backup actions. Every action is guarded, and every decision is auditable and replayable. It targets 1-10 hosts running services under systemd, docker or plain processes, ships as a single binary backed by SQLite, and does not target Kubernetes.

> **Status: rewrite in progress.** The server runs the policy engine, service catalog with lifecycle actions, health monitoring and triggers (signal, git, cron, webhook), and serves the full REST and event-stream API. Git config sync, `apply`/`export`, the API client commands and the web UI are not built yet. The previous implementation is preserved at commit `18a24f6`.

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

Admin and token commands open the database directly, so local filesystem access is administrator access. They are recorded in the audit log as actor `system` / `cli`.

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

A service is reported `healthy` only when every critical check has passed; until a critical check has its first result the service is `unknown`.

## API v1

The API is described by a hand-written OpenAPI 3.1 document, `internal/api/openapi.yaml`, which the server also serves at `GET /api/v1/openapi.yaml`. It is the reference for endpoints, request and response shapes, and error codes; a test fails when it and the registered routes disagree, and the integration tests check every response against it.

Overview:

- JSON uses camelCase; errors use `{"error":{"code","message","details"}}` with status codes 400, 401, 403, 404, 409, 422, 429 and 500. A 422 lists `details.issues` as `{path, message}`.
- Services, policies and modules are documents sent as YAML or JSON. Every save adds a version; updates need `If-Match: <version>` and a stale one answers 409.
- Lists take `limit` and an opaque `cursor` and answer `{items, nextCursor}`.
- Lifecycle actions on services run in the background (`202` and an action id for `GET /api/v1/actions/{id}`); `?dryRun=true` returns the plan instead.
- `GET /api/v1/events` streams signals, run and node progress, timeline entries, action progress and catalog changes as server-sent events, filtered by `service`, `policy`, `run` and `types`.
- Alerts (`/hooks/alerts/{source}`), heartbeats (`/heartbeats/{svc}/{check}`) and webhooks (`/hooks/{name}`, `/hooks/github`) feed LCM from outside.

Authentication: browsers use the `lcm_session` cookie from `POST /api/v1/auth/login`; scripts use `Authorization: Bearer <token>`. Sessions and `admin` tokens may use the whole API except token management, which is session-only, so a token can never create or revoke tokens. `ingest` tokens (for example `lcm token create --name alertmanager --scope ingest`) may only post alerts, heartbeats and webhooks, and call `GET /api/v1/auth/me`. A webhook listener that has an `hmacSecret`, and the GitHub endpoint when `LCM_GITHUB_WEBHOOK_SECRET` is set, accept a valid HMAC signature instead of a token.

The server speaks plain HTTP. To expose it beyond localhost, put a TLS-terminating reverse proxy in front and set `LCM_COOKIE_SECURE=true`.
