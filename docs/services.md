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
      repo: /var/lib/lcm/work/web  # LCM's own checkout, never a developer's
      branch: main
      build: make build
      install: ./install.sh
    backup: { command: ./backup.sh }   # optional: enables the backup action
    jobs:                          # optional: named commands for run-job
      reindex: { command: ./reindex.sh, timeout: 10m }
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
| `command` | `status`, `start`, `stop`, `restart` (each optional) | `status` exit code: 0 active, other inactive; unknown without `status` | the matching command (`sh -c`); restart falls back to stop then start. A missing command makes that action unsupported |
| `external` | none | unknown | none: LCM only monitors it |

LCM's own service uses a `command` runtime with only `status`, because it must not stop or restart itself ([self-deploy](operations.md#self-deploy)).

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
| `deploy` | registered, running, stopped, failed | `source` | refuse if tracked files changed; `git fetch --prune`; resolve the version (default: head of the branch); `git checkout --detach`; `build`; `install`; restart (skipped when the runtime cannot restart, so `install` must apply the new version) |
| `rollback` | running, stopped, failed | `source` and an earlier successful deployment | as deploy, to the previous successful commit, without fetching |
| `backup` | registered, running, stopped, failed | `backup` | run the command (in the source checkout when there is one) |
| `run-job` | registered, running, stopped, failed | `jobs` | run the named job |

Run an action with `lcm resource action <service> <action> [--version <commit>] [--job <job>] [--dry-run] [--wait]` or `POST /api/v1/resources/{resource}/actions/{action}` (202 and an id for `GET /api/v1/actions/{id}`). Every action takes the lock `resource:<name>`, so actions on one service never overlap, and writes an audit entry and a timeline entry. `?dryRun=true` (console: *預覽*) returns the steps and the [impact](resources.md#relations) without locking or changing anything; `stop`, `restart`, `deploy`, `rollback` and `run-job` on a service that others depend on or are controlled through need `acknowledgeImpact`. Build, install, backup and job commands run with `sh -c`; step timeouts default to 15 minutes (`source.timeout`) and 30 minutes (`timeout` of backup and jobs).

The resource detail (`GET /api/v1/resources/{name}`) lists each action's availability with a reason: `notConfigured` (the console hides the button), `unsupported`, `invalidState`, `notManaged` (retired) or `busy` (the button is disabled and says why). A rollback marked available may still find no target; the dry-run is definitive.

**Reconciliation.** At startup and every minute LCM compares each state with what the runtime reports and corrects what can no longer be true: `running` with an inactive or failed runtime becomes `failed`; `registered` or `stopped` becomes `running` when the runtime is active and `failed` when it reports failed; a transient state with no action running (the process died mid-action) becomes `running` or `failed`. `failed` is left alone, and an `unknown` runtime status (external services) changes nothing. Retired services are skipped. Corrections appear on the timeline as `reconcile`.

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

| `type` | Props | Passes when |
|---|---|---|
| `http` | `url`, `method`, `expectStatus` (default 200-399), `bodyContains`, `headers`, `insecureSkipVerify` | the status is accepted and the body contains the text |
| `tcp` | `address` | a connection opens |
| `systemd` | `unit`, `scope` | the unit is active |
| `docker` | `container` | the container runs and is healthy when it defines a healthcheck |
| `command` | `command` (argv, no shell), `expectExit` (default 0) | the exit code matches |
| `heartbeat` | `grace` | `POST /api/v1/heartbeats/{resource}/{check}` arrived within interval plus grace |
| `prometheus` | `url`, `query`, `op`, `threshold` | the instant query satisfies the comparison |

Check health: `down` if a critical check is down, else `degraded` if a non-critical check is down, else `unknown` until every critical check has a first result, else `healthy`. For machines and routers it is [combined with native health](resources.md#state-health-and-operation). After a restart a status older than twice the interval reads as unknown until a fresh result arrives. Each run stores a short message (`status 200`, `unit is inactive`, `exit code 0` or the error); status changes, not individual runs, become `check.changed` and `health.changed` signals and timeline entries. Raw results are kept 7 days and one-minute rollups 90 days (`LCM_MONITOR_*_RETENTION`); the console's latency charts and uptime figures read them.

**Alerts from other monitors.** `POST /api/v1/hooks/alerts/{source}` accepts Alertmanager, Uptime Kuma, Gatus and a generic JSON format (payloads: [`internal/monitor/alert`](../internal/monitor/alert/alert.go)). The resource comes from the first alert label listed in `LCM_ALERT_RESOURCE_LABELS` (default `lcm_resource`, `resource`, `lcm_service`, `service`); any resource type can receive alerts. Firing alerts show on the resource and become `alert` signals that policies can react to. Use an `ingest` token for these senders.

## Timeline

Each resource name has a timeline; for services it holds `config.changed` (document versions and phase changes), `lifecycle.changed`, `deployment`, `action`, `backup`, `job`, `check.changed`, `health.changed`, `alert` and config sync `config.conflict` / `config.invalid` entries, written by the component that made the change. Timeline and deployments are keyed by name, so they continue across incarnations ([history keys](resources.md#identity-phases-and-archives)). It is a log for people, kept for `LCM_TIMELINE_RETENTION`; the audit log is the record of who did what.
