# Services

A service is a [Resource](resources.md) with `type: service`: how LCM controls the process, where its source is, how to back it up and how to check it. LCM installs nothing inside the service; it acts from outside through system commands (`systemctl`, `docker`, `git`, the operator's shell commands), always through `execx` with a timeout and a minimal environment. Structural rules are enforced by the server (`POST /api/v1/validate`, `lcm validate`); this page explains the service fields and actions. Identity, phases, relations, impact acknowledgement and group actions are shared by every type and described in [resources.md](resources.md).

```yaml
apiVersion: lcm/v2
kind: Resource
metadata:
  name: web                        # lowercase letters, digits and '-'
  labels: { tier: frontend }       # matched by trigger.signal label filters and group actions
  annotations:
    lcm.dev/dashboard-url: https://grafana.example/d/web   # link in the console header
spec:
  type: service
  adapter: systemd
  config:
    unit: web.service
    source:                        # optional: enables deploy and rollback
      url: https://github.com/acme/web.git   # where new versions come from
      branch: main
      build: make build
      install: ./install.sh
    backup: { command: ./backup.sh }   # optional: enables the backup action (managed services: typed sources, see below)
    jobs:                          # optional: named commands for run-job
      reindex: { command: ./reindex.sh, timeout: 10m }
    route:                         # optional: how the host's nginx site presents the service (see Route)
      proxy: [{ path: api/, upstream: /api/ }]
  checks:
    - { name: http, type: http, props: { url: http://127.0.0.1:8080/health } }
  relations:
    - { type: dependsOn, target: db }
```

Services always run on the LCM host. `relations` feed the [impact preview and group ordering](resources.md#relations).

## Runtime

The adapter decides which process actions exist; its fields sit directly in `config`, and only the fields of the chosen adapter are accepted. An action the runtime cannot perform is refused by the server and its dry-run, and its console button is disabled; LCM never records a state change it did not make.

| `adapter` | Fields | Status from | Start, stop, restart |
|---|---|---|---|
| `systemd` | `unit`, `scope` (`system` default, or `user`) | `systemctl is-active` | `systemctl start/stop/restart`; system scope runs behind `LCM_SYSTEMCTL_PRIVILEGE` (`sudo -n`), limited by the sudoers rule ([security model](architecture.md#security-model)) |
| `docker` | `container` | `docker inspect` | `docker start/stop/restart` |
| `compose` | `project`, `file` | `docker compose ps` | `docker compose up -d / stop / restart` |
| `command` | `status`, `start`, `stop`, `restart` (each optional) | `status` exit code: 0 active, other inactive; unknown without `status` or when the shell cannot run it (126, 127) | the matching command (`sh -c`); restart falls back to stop then start. A missing command makes that action unsupported |
| `external` | none | unknown | none: LCM only monitors it |
| `managed` | `artifact`, `start`, `startArgs`, `runtime`, `migrate`, `keepReleases`, `publish`, `limits`, `instances` ([below](#managed-services)) | the [deploy helper](operations.md#deploy-helper)'s `status` of `<name>.service`: active, inactive (also before the first activation), failed, otherwise unknown; unknown without the helper, and for a [static-only](#static-content) service. With [instances](#instances): the enabled instances together (all active: active; one failed: failed; all inactive, or none enabled: inactive; otherwise unknown) | the helper's `start/stop/restart`; none for a static-only service. With instances also of one instance |

LCM's own service uses a `command` runtime with only `status`, because it must not stop or restart itself ([self-deploy](operations.md#self-deploy)).

### Managed services

A managed service needs no deployment code in its repository: the restricted [deploy helper](operations.md#deploy-helper) builds it as its build account `lcmb-<name>`, installs the artifact as a root-owned release under `/opt/<name>/releases`, and runs it as its own account from a unit of a built-in template. The host's administrator grants the service first (`sudo bin/lcm host allow <name> --port N`, [service grants](operations.md#service-grants)); its port, runtimes, build inputs and directories come only from `/etc/lcm/host.json`, never from the document.

```yaml
metadata: { name: web }           # at most 27 characters, not starting with "lcm": the account, unit and directories
spec:
  type: service
  adapter: managed
  config:
    source: { url: https://github.com/acme/web.git, branch: main, build: npm ci && npm run build, timeout: 20m }
    artifact: dist/                # the directory of the build tree installed as a release; "." is the whole tree
    runtime: node24                # optional: a runtime of host.json granted to the service
    start: server/main.mjs         # release-relative file, run with the runtime's node or python3, or executed itself
    startArgs: [--quiet]           # optional, at most 32
    migrate: [node, scripts/migrate.mjs]   # optional: run as the service account before the switch
    keepReleases: 3                # 2-20, default 3
    publish:                       # optional: static content, see below
      dir: client/dist             # a directory of the release; "." is the whole release
      retain: [assets/]            # optional, at most 16 directories below dir, each ending with "/"
      retainDays: 14               # 1-90, default 14; only with retain
    limits:                        # optional: tighten the unit
      memoryHigh: 768M             # K, M or G (powers of 1024), 1M-16G
      memoryMax: 1G                # memoryHigh <= memoryMax
      stopTimeout: 60s             # whole seconds, 1s-10m
```

| Field | Rule |
|---|---|
| `source` | Required, with `build` (run by the helper, not by LCM); `install` is refused because the helper installs; `timeout` is the build's limit, at most 2h |
| `artifact`, `start` | Required clean relative paths of letters, digits and `._-/` without `..` |
| `startArgs`, `migrate` arguments | Letters, digits and `._/=:,@+-`; `migrate` starts with a release-relative executable or a program of the runtime. `migrate` must be idempotent: it runs again when the same commit is deployed again and when a deploy follows a rollback |
| `runtime` | A runtime name; whether `host.json` grants it is checked by the helper when the service is deployed |
| `publish` | `dir` is a release path like `artifact` (`.` is the whole release); `retain` entries are such paths ending with `/`, without components that start with `.` |
| `limits` | Become `MemoryHigh=`, `MemoryMax=` and `TimeoutStopSec=` of the unit. They only tighten it: without them the unit has no memory limit and systemd's default stop timeout |
| Without `start` | Needs `publish`; `startArgs`, `migrate` and `limits` are refused ([static content](#static-content)) |

The other runtime fields (`unit`, `stop`, `restart`, `status`, ...) are refused, and the managed fields are refused for every other adapter. The helper checks every request again and refuses what the host does not grant; its message is shown as the error of the action.

### Instances

`instances` runs several processes of one release, each as its own unit `<name>@<instance>.service`, such as the bots of one repository:

```yaml
config:
  start: dist/bot/main.js             # default of every instance
  limits: { memoryHigh: 384M, memoryMax: 512M }
  instances:
    alpha:       { start: dist/bot/alpha/index.js }
    log-archive: { start: dist/bot/log-archive/index.js, startArgs: [], limits: { memoryMax: 1G } }
checks:
  - { name: alpha-relay, instance: alpha, type: tcp, props: { address: 127.0.0.1:4100 } }
```

| Rule | |
|---|---|
| Names | 1-16 instances; names of lowercase letters and digits in groups joined by single `-`, starting with a letter, at most 24 characters, not `backup` |
| Resolution | `start` is the instance's, else the service's (one of them is required). `startArgs` present on the instance (even `[]`) replaces the service's as a whole. `limits` are merged field by field over the service's; `memoryHigh <= memoryMax` holds for the merged result |
| Service-level only | `artifact`, `runtime`, `migrate` (once per activation, with the shared env file and no `PORT` or `HOST`), `publish`, `keepReleases`, `backup`, `route` (a static or published route only: a grant with instances has no service port to proxy to) |
| Host | The grant must list every instance (`host allow --instance NAME[:PORT]`, [service grants](operations.md#service-grants)): ports, groups and units to start after come from `host.json` only. A static-only service cannot have instances |

- **Desired state.** Each instance's desired state is its unit's enablement on the host. `stop` with an instance (`lcm resource action <name> stop --instance alpha`, API `{"instance": "alpha"}`) stops and disables it, and it stays stopped across deployments, service-wide `start`/`restart` and reboots until `start` or `restart` names it again. The last enabled instance cannot be stopped this way (`lastInstance`; stop the service). An administrator's `systemctl enable`/`disable` of an instance unit changes the same state; LCM reads it every minute.
- **Lifecycle.** The service keeps one state. Instance actions are allowed while it is `running` or `failed`, take the service's lock, and are audited and on the timeline with the instance (`restart alpha succeeded`). Afterwards the state follows the enabled instances: active makes `running` (so restarting the failed instance recovers a `failed` service), failed makes `failed`.
- **Deployments.** One activation writes every instance unit, switches the release once and restarts all enabled instances in one call, so that all run the same release; [details](#managed-deployments).
- **Checks.** `instance` binds a check to an instance. A check of an instance an operator stopped is paused: it does not run, does not count towards health, reads `unknown` with `paused` set and is left out of the policy variable `resource` ([policies](policies.md)), so `auto-deploy` still passes. Instance checks are critical by default: one bot down makes the service `down`; `critical: false` makes it `degraded` instead.
- **Detail.** `GET /api/v1/resources/{name}` lists `service.instances` with each instance's port, state, enablement, health from its own checks and available actions; the console shows them on the overview.

### Static content

With `publish`, every activation copies `publish.dir` of the release to the service's publish directory `/opt/<name>/www`, which the service's route serves when the host grants it (`sudo bin/lcm host allow <name> --route PREFIX --publish`, [routes](operations.md#routes)); a document cannot choose another place, and the helper refuses `publish` while `route.static` in `host.json` is not that directory.

- **Order.** New and changed files are written first, each through a temporary file and a rename, every one but the `.html` files; the `.html` files follow one by one. A page that a browser loads during the switch never names an asset that is not there yet. Unchanged files are left alone.
- **Retention.** Every published file gets the time of the activation as its modification time. Files of the publish directory that the release does not contain are removed, except below a `retain` directory: those stay until they are `retainDays` old, that is `retainDays` after the last release that contained them, so that pages loaded before a deployment still find their assets.
- **What is published.** Only directories and regular files owned by root (every release is): a symbolic link in `publish.dir` or on the way to it fails the activation, and entries whose name starts with `.` are skipped. Install's bounds apply (200,000 entries, 4 GiB).
- **Failures.** A failure while writing fails the activation before anything is switched or restarted: files already written stay, unreferenced, and the `.html` files not yet replaced are the old ones. A failure to remove an old file is only noted in the deployment log.
- **Rollback.** A rollback activates the earlier release, so it publishes that release's content again; newer files of retained directories stay for their `retainDays`.
- **Static-only services.** A managed service without `start` runs no process. Its grant has no port, and the helper writes no unit: an activation publishes and switches the `current` link, `start`, `stop` and `restart` are `unsupported`, and the status is `unknown` like an external service's. Deploy, rollback and backup work as for any managed service.

The deployment log records what each activation published (`published <dir> to /opt/<name>/www: N written, N replaced, ...`).

**Without the helper.** When the deploy component is not installed (`/usr/local/libexec/lcm-deploy` is missing or not root-owned) or LCM's data directory is not `/var/lib/lcm`, `start`, `stop`, `restart`, `deploy`, `rollback`, `backup` and `restore` of managed services are unavailable with the reason `helperUnavailable` and refused before anything is recorded; jobs are unaffected. The reason message names the cause (wrapper missing, wrapper not a root-owned file only root can write, or another data directory) and the console shows it. The wrapper is checked at every use, so enabling the component needs no restart of LCM.

## Source

`source` says where new versions come from and how to build and install them.

| Field | Meaning |
|---|---|
| `url` | Required. A remote (`https://`, `ssh://`, `git@host:path`) or the absolute path of a local development repository. It must not carry credentials (only the ssh user `git`) and must not point into LCM's build directory |
| `branch` | Required. Branch a deploy without `--version` takes the head of |
| `build`, `install` | Optional `sh -c` commands run in the build checkout. A command that exits with status 75 asks for the operator and promises it changed nothing: the deployment is recorded as failed with the code `deploy.needsUpgrade` and the last line of the output, a service that still runs stays `running`, and policies can test the code (`auto-deploy` then does not roll back). LCM's own [self-deploy](operations.md#self-deploy) uses it for a storage schema change |
| `timeout` | Limit of each step, default 15m |

- **Build checkout.** LCM owns one checkout per service at `<data dir>/work/<name>` (`/var/lib/lcm/work/<name>` with `deploy/`); a document cannot name it. The first deploy clones `url` there (an existing empty directory counts as missing; a non-empty directory without `.git` is refused). Every later deploy first sets the checkout's `origin` to `url`, so editing `url` switches the source at the next deploy, then fetches.
- **Uncommitted changes.** A deploy or rollback refuses to start while tracked files in the checkout have uncommitted changes; untracked files (build output) do not count.
- **Local development repository.** With a path as `url`, LCM clones and fetches it like a remote: commits on its branch are deployed without being pushed, and its working tree is never touched. LCM runs Git as its own account `lcm`, with a fixed environment that a document cannot widen (no `safe.directory`), so the repository must satisfy two conditions:
  - It is owned by `lcm`. Git refuses a repository owned by another account, root included, with `detected dubious ownership`. Keep a dedicated repository for LCM, for example `sudo -u lcm git init --bare /srv/lcm-repos/<name>.git` (or `sudo chown -R lcm: <repo>`), and commit or push to it as `lcm` (`sudo -u lcm git ...`).
  - `lcm` can reach it: every parent directory must be searchable by `lcm` and the files readable. A repository below a home directory with mode `0750` is not (`fatal: repository ... does not exist`). Put it in a shared location such as `/srv`.

  A failed clone or fetch reports Git's last output line in the deployment error (`deployment failed at clone: exit status 128: fatal: ...`); the full output is in the deployment log.
- **Other commands.** `backup` and jobs of a service with a source run in the build checkout; before the first deploy has cloned it they are refused.
- **Refused URLs.** A `url` inside `<data dir>/work/` is refused when the document is saved or validated: those checkouts only move to commits LCM deploys. `trigger.git` refuses them for the same reason ([policies](policies.md#templates)).

A document that still uses `source.repo` or `source.remote` is refused with an issue naming the field; replace them with `source.url`.

### Backup and restore of managed services

A managed service declares the data to keep instead of a command; the service's repository needs no backup code:

```yaml
config:
  backup:
    sources:
      - { type: files,    path: media/ }        # a file or directory below /var/lib/<name>
      - { type: sqlite,   path: app.sqlite }    # a consistent copy, also while the service runs
      - { type: postgres, database: app }       # pg_dump -Fc as the service account (peer authentication, PG* variables)
      - { type: mongodb,  database: app, users: true }   # mongodump; users also saves the admin users and roles
      - { type: command,  argv: [bin/dump, --all] }      # a program of the current release; its standard output is the dump
    mode: online                                # or stopped: the unit is stopped for the backup and started again
    timeout: 30m                                # of the producing process, at most 2h
    keep: { daily: 7, weekly: 4 }               # the default
```

The command form `backup: {command, timeout}` stays for every other adapter, and `sources` are refused for them; a managed service takes only `sources`. Paths are clean relative paths (no `..`, not absolute, not `.`) and may not overlap; at most 16 sources.

**How a backup runs.** The [deploy helper](operations.md#backups) starts an unprivileged worker as the service's own account in a sandbox like its unit's; root only moves the finished files into `<backup.dir>/<name>/<id>/`, which only root can enter, writes a `manifest.json` with the size and SHA-256 of every file, and keeps the set read-only to the service. LCM stores the checksum of the manifest. Sources and what each produces:

| Source | Artifact | Notes |
|---|---|---|
| `files` | one tar of regular files and directories | Links, devices, sockets and pipes are skipped and counted (`skipped`); group/other write and special mode bits are dropped; ownership is not stored |
| `sqlite` | `VACUUM INTO` copy, checked with `quick_check` | Needs no `sqlite3` binary. `-wal`, `-shm` and `-journal` files are not part of the copy |
| `postgres` | `pg_dump -Fc` | `pg_dump` must be installed and owned by root (`/usr/bin` or `/usr/local/bin`); otherwise the backup is refused with that message |
| `mongodb` | `mongodump --gzip` archive (of `database`, or with `all: true` of every database but `local`, `admin` included), plus users and roles of `admin` with `users: true` | `MONGODB_URI` in the service's env file, or in the root-only `/etc/<name>/backup.env`, reaches the tools in a private `--config` file, never in their arguments. `users: true` dumps the whole instance's `admin` database (users, roles and `admin.system.version`), so use it when the `mongod` serves only this service. A source with `all` must be the only `mongodb` source; `all` and `users` need a `MONGODB_URI` without a database path, and use `all` only with a `mongod` dedicated to the service ([connection strings and cautions](operations.md#backups)). Online mode is not a point-in-time snapshot across databases (`mode: stopped` stops every instance) |
| `command` | the program's standard output | Kept, but never restored by LCM: it has no restore command |

After every successful backup LCM asks the helper to apply `keep`: the newest set of each of the last `daily` days and `weekly` weeks (UTC) stays, the newest set always, and the rest is removed; unreadable sets are never removed. The records (`GET /api/v1/resources/{name}/backups`, console tab *備份*) stay for `LCM_TIMELINE_RETENTION` after their set is removed.

**Restore.** `restore` (console: the button of a set on the *備份* tab, or *還原* for the newest) puts a set back: the helper verifies every artifact against the manifest (and LCM sends the checksum it recorded), stops the unit if it is active, copies what the restore will replace to a `pre-restore-…` set, replaces the sources and starts the unit again. Files and SQLite databases are extracted next to their target and swapped in; databases are restored with `pg_restore --clean` / `mongorestore --drop` (the database must exist). An `all` set restores every database it holds except `admin`, `config` and `local` (`--nsExclude`): the collections in the set are replaced, databases and collections created since stay. A restore leaves the instance's MongoDB users and roles alone unless it asks for them: `restoreUsers` (CLI `--restore-users --confirm <name>`, which prints the plan and asks unless `--yes`; API `{"restoreUsers": true, "confirm": "<name>"}`; console: an option in the restore dialog, offered only for a set that holds users, with the service name typed to confirm) also restores the users and roles of a set saved with `users: true`, which replaces every account of that `mongod` (the URI user needs the `restore` role). The set was written by the service's own account, so restore users only from a set made while the service could be trusted; the plan says so. The pre-restore copy then holds the current accounts, and LCM's own undo after a failed restore restores them as well. A failed replacement is undone from the pre-restore copy; when that fails too the unit is left stopped on purpose (`leftStopped`, [restoring](operations.md#backups)). Otherwise LCM waits `15s` (a service that was running) and checks that the unit is active; if not, it restores the pre-restore set (`ErrRestoreUndone`). To undo a good restore later, restore the pre-restore set named in the result. The service's lifecycle state is not changed by a restore; a service that was stopped stays stopped. A restore is `disruptive` (needs `acknowledgeImpact` like a stop), never runs in a group, and has no policy node: it replaces data and is a decision for a person.

An outcome LCM cannot determine (lost reply, helper still running, interrupted) is recorded as `interrupted` with the operation ID to query; it is never assumed to have succeeded.

## Route

The optional `route` section of `config` (valid for every adapter) describes how the host's [nginx site](operations.md#ingress) presents the service. It carries no privileges: the domain, the prefix, the port and the static directory of a route come only from `services.<name>` in `/etc/lcm/host.json` ([routes](operations.md#routes)), and the root helper validates the section again. All paths are relative to the route's prefix. The section is strictly decoded: unknown fields are errors, and issues are reported under `spec.config.route`.

```yaml
config:
  route:
    proxy:                          # extra locations to the approved port
      - { path: api/, upstream: /api/, bodyLimit: 3m, cookiePath: true, category: api }
      - { path: socket.io/, upstream: /socket.io/, websocket: true, timeout: 75s, category: socket }
      - { path: health, exact: true, upstream: /health, category: health }
    static:                         # needs route.static in host.json
      spa: true
      notFound: 404.html
      immutable: [assets/, _astro/]
      downloads: [{ path: download/app.apk, type: application/vnd.android.package-archive }]
    redirects: [{ from: en/, to: /, code: 301 }]
    headers:
      csp: "default-src 'self'; img-src 'self' data:"
      cspReportOnly: false
```

With prefix `/web/` on `example.org`, `path: api/` serves `https://example.org/web/api/` and sends it to `http://127.0.0.1:<port>/api/`.

| Field | Meaning | Default and limits |
|---|---|---|
| `proxy[].path` | Location below the prefix; ends with `/` unless `exact` | at most 32 entries; components of letters, digits and `._-`, none starting with `.` |
| `proxy[].exact` | Match the path alone instead of everything below it | `false` |
| `proxy[].upstream` | Absolute path on the service; ends with `/` like `path` unless `exact` | required |
| `proxy[].bodyLimit` | Largest request body (`512k`, `3m`) | nginx default; at most `64m` |
| `proxy[].cookiePath` | Rewrite the cookie path `/` of the responses to the prefix | `false` |
| `proxy[].websocket` | Pass connection upgrades through, no buffering | `false` |
| `proxy[].timeout` | Read and send timeout (`75s`, `5m`) | nginx default; at most `1h` |
| `proxy[].category` | `api`, `auth`, `upload`, `socket`, `health` or `static`: names the location for the [security component's](security.md) hooks | `api` |
| `static.spa` | Unknown paths answer `index.html` instead of 404 | `false` |
| `static.notFound` | Page served with a 404, relative to the static directory | none |
| `static.immutable` | Directories (`assets/`) cached for a year; never fall back to `index.html` | at most 16 |
| `static.downloads` | Files served as attachments with a fixed `type` (a short allowlist: `application/vnd.android.package-archive`, `application/zip`, `application/pdf`, ...) | at most 16 |
| `redirects[]` | `from` (below the prefix) to `to` (written from the prefix's root, `/` is the prefix) with `code` | `301`; `302`, `307`, `308`; at most 32 |
| `headers.csp` | Content-Security-Policy in a strict subset of the CSP grammar: printable ASCII directives with quoted keywords or plain sources; no `"`, `\`, `$`, braces or line breaks | at most 4096 characters, 64 directives |
| `headers.cspReportOnly` | Send it as `Content-Security-Policy-Report-Only` | `false` |

Without a `route` section the route proxies everything below the prefix to the service's port, or serves its static directory; `proxy` locations take precedence over the static directory, and `static` needs a static directory in the grant. A `proxy` entry needs a port in the grant. Locations may not collide with another route.

**Applying.** LCM sends the section to the [deploy helper](operations.md#deploy-helper) (`ingress.apply`) in the background after a document is saved, restored or synced from Git, when its `route` differs from the last recorded outcome (removing the section sends "no options" so that the host drops them), and once at every start for routes whose last outcome was not `applied`. The helper renders the whole site, tests it with nginx and reloads it, or puts the previous site back. Each send records its operation ID on the timeline before the call, so a lost reply is settled from the helper's ledger; an outcome that cannot be confirmed stays `unknown` and is never taken as success. When the helper answers `busy` (another change of the nginx site holds its lock; the helper already waited 30 s for it), LCM asks again after 5 s, 15 s and 30 s with a new operation ID each time (a busy answer changed nothing) and records `failed` only when the fourth attempt is busy too. Every send writes an audit entry (`service.apply-route`).

**The host decides.** A refusal (no route granted in `host.json`, the site never installed, a location that collides) or a failed apply never rejects the document write. The outcome is `service.route` of the resource detail and the *路由* panel of the service overview, with the helper's message and whether it concerns the current section:

| State | Meaning |
|---|---|
| `applied` | The helper applied the site (`changes` empty: it was up to date) |
| `refused` | The host does not grant what the route needs, or refused the change |
| `failed` | nginx rejected the site or a probe failed; the previous site was put back |
| `unknown` | The reply was lost and the ledger could not confirm the outcome |
| `skipped` | Nothing was sent: the deploy helper is not installed or `components.ingress` is off |
| `pending` | A request is in flight, or LCM stopped before recording its outcome |

`dropped` lists stored options of other services that no longer fit `host.json` (for example a grant lost its port); the site leaves them out and those routes have their defaults until their options are sent again. The host components view shows the same list.

After fixing the cause on the host (for example `sudo bin/lcm host allow web --route /web/`), run the `apply-route` action (`lcm resource action web apply-route`, console button *重新套用路由*). It sends the section again, is offered only for a service that has a `route` section and a callable helper, and fails when the route is not applied. Restarting LCM also retries.

## Lifecycle states and actions

The lifecycle state is what LCM did to the service; health (below) is what the checks observe. The console shows both side by side.

| State | Meaning |
|---|---|
| `registered` | Known, never acted on |
| `deploying`, `updating`, `rolling_back` | An action is running (first deploy, deploy over a running service, rollback) |
| `running`, `stopped` | Result of the last action, kept true by reconciliation |
| `failed` | An action failed and the process is not running, or a deployment failed; stays until an action or the operator changes it |

| Action | Allowed from | Needs | Steps |
|---|---|---|---|
| `start` | registered, stopped, failed | runtime start | start |
| `stop` | registered, running, failed | runtime stop | stop |
| `restart` | running, stopped, failed | runtime restart | restart, or start when inactive |
| `deploy` | registered, running, stopped, failed | `source` | clone `url` into the [build checkout](#source) when it is missing; otherwise refuse if tracked files changed, set `origin` to `url` and `git fetch --prune`; resolve the version (default: head of the branch); `git checkout --detach`; `build`; `install`; restart (skipped when the runtime cannot restart, so `install` must apply the new version). Managed services: [helper steps](#managed-deployments) after the checkout |
| `rollback` | running, stopped, failed | `source` and an earlier successful deployment | as deploy, to the previous successful commit, without fetching (a missing checkout is cloned). Managed services first try to [switch back to the installed release](#managed-deployments) |
| `backup` | registered, running, stopped, failed | `backup` | run the command (in the build checkout when the service has a source); managed services: the [typed backup](#backup-and-restore-of-managed-services) and its retention |
| `restore` | registered, running, stopped, failed | typed `backup` with a restorable source | [restore](#backup-and-restore-of-managed-services) a set (`--backup-id`, the newest when omitted) |
| `run-job` | registered, running, stopped, failed | `jobs` | run the named job |
| `apply-route` | registered, running, stopped, failed | `route` and a callable deploy helper | send the [route](#route) to the helper again; the state does not change |

Run an action with `lcm resource action <service> <action> [--version <commit>] [--job <job>] [--backup-id <id>] [--instance <name>] [--restore-users --confirm <name>] [--dry-run] [--wait]` or `POST /api/v1/resources/{resource}/actions/{action}` (202 and an id for `GET /api/v1/actions/{id}`). Every action takes the lock `resource:<name>`, so actions on one service never overlap, and writes an audit entry and a timeline entry. The action renews its lock every third of `LCM_LOCK_TTL` while it runs, so the TTL only bounds a lock left by a crashed process, not the length of an action. Once the runtime has done the work, the new state is recorded even if the request is cancelled. A `start`, `stop` or `restart` of a managed service that the deploy helper refuses before changing anything (`refused`, `not_found`, `busy`; for example before the first deployment, when no unit exists yet: "deploy the service first") leaves the state alone. `?dryRun=true` (console: *預覽*) returns the steps and the [impact](resources.md#relations) without locking or changing anything; `stop`, `restart`, `deploy`, `rollback`, `restore` and `run-job` on a service that others depend on or are controlled through need `acknowledgeImpact`. Build, install, backup and job commands run with `sh -c`; step timeouts default to 15 minutes (`source.timeout`) and 30 minutes (`timeout` of backup and jobs).

The resource detail (`GET /api/v1/resources/{name}`) lists each action's availability with a reason: `notConfigured` (the console hides the button), `unsupported`, `helperUnavailable` ([managed services](#managed-services) without the deploy helper), `invalidState`, `notManaged` (retired) or `busy` (the button is disabled and says why). A rollback marked available may still find no target; the dry-run is definitive.

### Managed deployments

After LCM has fetched and detached its own checkout as `lcm` (unchanged), the helper does the rest; LCM runs neither `build` nor a restart itself:

1. `build`: the helper copies the checkout, checks that its `HEAD` is the commit and runs `source.build` as the build account `lcmb-<name>` in its sandbox, with the `runtime` in `PATH` (dry-run step `step.helperBuild`).
2. `install`: `artifact` of that build becomes a new release (`step.helperInstall`).
3. `activate`: `migrate` runs as the service account in the new release, the [static content](#static-content) is published, then `current` switches, the unit is written and the service restarted; releases beyond `keepReleases` are removed (`step.helperActivate`; `step.helperPublish` for a static-only service). The deployment log ends with `installed release <id>`.

With [instances](#instances) the activation sends each instance's resolved `start` and `limits`. The helper writes every instance unit and switches `current` once; a unit that does not verify or load puts back all units and the links. Then it stops and disables the units of instances no longer in the document, and the single `<name>.service` of a service converted to instances, before anything starts; enables new instances; restarts every enabled instance in one call; and removes the orphaned unit files. An instance an operator stopped gets its new unit but stays stopped. The deployment log has one line per instance (`instance alpha restarted`, `instance log-archive kept stopped (stopped with --instance)`, `removed unit bots.service`). A restart that fails leaves the release current: the service is `failed` and a policy such as `auto-deploy` rolls back all instances together.

- **Durable intent.** Every helper call has a fresh operation ID, written to the running deployment's log (`deploy helper <op> operation <id>`) before the request is sent; the helper's own output follows indented (`  | `).
- **Lost replies.** After a timeout or a lost reply LCM queries the helper's ledger by that ID: a recorded success continues the deployment, a recorded failure fails it, and an operation that is still running, was interrupted or cannot be looked up fails the deployment with `outcome unknown, query <id>`. It is never assumed to have succeeded. An ID the ledger does not know was not carried out.
- **Failures.** A refused (`refused`, `busy`, `not_found`) or failed operation fails the deployment with the helper's message. The service is left `failed` when the helper may have changed it: an unknown outcome, or an `activate` that switched the release before a later step failed (the log says so; there is no automatic switch back, a rollback or a policy does that). When the helper certainly changed nothing (the checkout, `build` or `install` failed, or `activate` was refused or failed before it switched, for example in `migrate`), the state follows the runtime's status instead: `running` while the service runs, `failed` otherwise.
- **Rollback.** Without rebuilding, a rollback activates the release recorded by the target deployment (or else the newest installed release built from the target commit) when the helper still keeps it and its manifest has the current `artifact` and `runtime` (`step.helperActivatePrevious`); otherwise it rebuilds the commit with the steps above. The old release starts with the current `start`, `startArgs`, `runtime`, `publish` and `limits` of the document, not those it was deployed with, and its static content is published again. A rollback never runs `migrate`: data the newer release migrated stays as it is, so migrations must stay compatible with the previous release or be restored from a backup.
- **Restarts of LCM.** A deployment that was running when LCM stopped is closed as `interrupted`, its error naming what the ledger reports for the last helper operation (for example `deploy helper build operation <id> succeeded`, `was not carried out: the helper has no record of it`, or `outcome unknown, query <id>`). When the ledger proves that the last operation was a successful `activate` of the deployment, LCM made that change, so the deployment is finished as `succeeded` instead: its log notes that the outcome was confirmed from the ledger and ends with `installed release <id>`, which rollback and the next deploy read. Reconciliation then takes the state from the helper's status.
- **Helper errors in the API.** A dry-run or action request whose precheck gets a helper error answers 409 `precondition_failed` for `refused`, `not_found` and `disabled` (the host configuration does not enable the component, for example `components.ingress` for a route) and 409 `conflict` for `busy`, with the helper's message, and 502 `bad_gateway` for a failed or unknown operation. A managed `deploy` dry run asks the helper a read-only question (`releases`), so a service the administrator has not granted is refused in the dry run as in the real action.

**Reconciliation.** At startup and every minute LCM compares each state with what the runtime reports and corrects what can no longer be true (it also records the state of each instance, which pauses the checks of stopped ones) (for a managed service this is a `status` call to the deploy helper through `sudo`, so the host's auth log shows one entry per managed service per minute): `running` with an inactive or failed runtime becomes `failed`; `registered` or `stopped` becomes `running` when the runtime is active and `failed` when it reports failed; a transient state with no action running (the process died mid-action) becomes `running` or `failed`. `failed` is left alone, and an `unknown` runtime status (external services) changes nothing. Retired services are skipped. Corrections appear on the timeline as `reconcile`.

**Phases.** Retirement is a [management phase](resources.md#identity-phases-and-archives), not a lifecycle state:

- `retire` is refused during a transient state. A `running` or `failed` service is stopped first when its runtime can stop it; a failed stop aborts the retirement. When the runtime cannot stop it, the service is retired with its state unchanged (for example `running`), because LCM never records a change it did not make. Retiring needs `acknowledgeImpact` when other resources depend on the service.
- A retired service keeps its last state, accepts no actions, and its checks stop. `resume` returns it to managed and recomputes the state from the runtime.
- `unmanage` requires a retired service in `registered`, `stopped` or `failed` with no action running. A service left retired in `running` must be resumed, stopped (by an action, or outside LCM so reconciliation records `failed`), retired and then unmanaged.

## Health checks

Checks apply to every resource type; the rules below are the same for machines and routers. LCM polls every check itself on its own interval; only heartbeats and alerts are pushed from outside.

| Field | Default | Meaning |
|---|---|---|
| `interval` | 30s (min 1s) | Time between runs |
| `timeout` | 5s | Limit of one run; must be shorter than the interval |
| `threshold` | 1 | Consecutive results needed to change status (up or down); 2 or 3 suppresses one-off blips |
| `critical` | true | A failing critical check makes the resource `down`, a non-critical one `degraded` |
| `instance` | none | Managed services with [instances](#instances): the instance the check belongs to; paused while an operator keeps it stopped |

| `type` | Props | Passes when |
|---|---|---|
| `http` | `url`, `method`, `expectStatus` (default 200-399), `bodyContains`, `headers`, `insecureSkipVerify` | the status is accepted and the body contains the text |
| `tcp` | `address` | a connection opens |
| `tls` | `host`, `port` (443), `serverName` (default `host`), `minValidity` (Go duration, default `504h` = 21 days, at least `1h`) | the served certificate chains to the system roots, matches `serverName` and stays valid for at least `minValidity`; the message states the days left and the `notAfter` date, or the verification error |
| `systemd` | `unit`, `scope` | the unit is active |
| `docker` | `container` | the container runs and is healthy when it defines a healthcheck |
| `command` | `command` (argv, no shell), `expectExit` (default 0) | the exit code matches |
| `heartbeat` | `grace` | `POST /api/v1/heartbeats/{resource}/{check}` arrived within interval plus grace |
| `prometheus` | `url`, `query`, `op`, `threshold` | the instant query satisfies the comparison |

Check health: `down` if a critical check is down, else `degraded` if a non-critical check is down, else `unknown` until every critical check has a first result, else `healthy`; paused checks do not count. For machines and routers it is [combined with native health](resources.md#state-health-and-operation). After a restart a status older than twice the interval reads as unknown until a fresh result arrives. Each run stores a short message (`status 200`, `unit is inactive`, `exit code 0` or the error); status changes, not individual runs, become `check.changed` and `health.changed` signals and timeline entries. Raw results are kept 7 days and one-minute rollups 90 days (`LCM_MONITOR_*_RETENTION`); the console's latency charts and uptime figures read them.

**Alerts from other monitors.** `POST /api/v1/hooks/alerts/{source}` accepts Alertmanager, Uptime Kuma, Gatus and a generic JSON format (payloads: [`internal/monitor/alert`](../internal/monitor/alert/alert.go)). The resource comes from the first alert label listed in `LCM_ALERT_RESOURCE_LABELS` (default `lcm_resource`, `resource`, `lcm_service`, `service`); any resource type can receive alerts. Firing alerts show on the resource and become `alert` signals that policies can react to. Use an `ingest` token for these senders.

## Timeline

Each resource name has a timeline; for services it holds `config.changed` (document versions and phase changes), `lifecycle.changed`, `deployment`, `action`, `backup`, `job`, `check.changed`, `health.changed`, `alert` and config sync `config.conflict` / `config.invalid` entries, written by the component that made the change. Timeline and deployments are keyed by name, so they continue across incarnations ([history keys](resources.md#identity-phases-and-archives)). It is a log for people, kept for `LCM_TIMELINE_RETENTION`; the audit log is the record of who did what.
