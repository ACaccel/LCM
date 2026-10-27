# LCM

LCM is a lifecycle control plane for self-hosted services. It tracks each service you operate (where it runs, its version, health and lifecycle stage) and, driven by policy, performs deployment, recovery and backup actions. Every action is guarded, and every decision is auditable and replayable. It targets 1-10 hosts running services under systemd, docker or plain processes, ships as a single binary backed by SQLite, and does not target Kubernetes.

> **Status: rewrite in progress.** Only the foundation exists so far: configuration, structured logging, the SQLite store, admin login, API tokens, the audit log, and a minimal `/api/v1` surface. The policy engine, service catalog, monitoring and web UI are not implemented yet. The previous implementation is preserved at commit `18a24f6`.

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
| `lcm token create --name <name>` | Create an API token and print it once |
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

## API v1

JSON uses camelCase. Errors use `{"error":{"code","message","details"}}`. Browsers authenticate with the `lcm_session` cookie; scripts use `Authorization: Bearer <token>`. Token management requires a browser session, so a token cannot create tokens.

| Endpoint | Auth | Description |
|---|---|---|
| `GET /api/v1/health` | none | Status and version; checks the database |
| `POST /api/v1/auth/login` | none | Start a session; failed attempts are delayed and limited per IP |
| `POST /api/v1/auth/logout` | session | End the session |
| `GET /api/v1/auth/me` | any | Current actor |
| `GET /api/v1/tokens`, `POST /api/v1/tokens`, `DELETE /api/v1/tokens/{id}` | session | List, create, revoke tokens |
| `GET /api/v1/audit` | any | Audit log, newest first; `limit` (default 100, max 1000) and `before` cursor |

The server speaks plain HTTP. To expose it beyond localhost, put a TLS-terminating reverse proxy in front and set `LCM_COOKIE_SECURE=true`.
