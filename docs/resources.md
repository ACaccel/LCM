# Resources

Everything LCM manages is a `Resource`: a software service, the local machine or a network router. Identity, labels, versioned documents, management phase, checks, health, relations, Git ownership, audit and history work the same for every type; type-specific behavior is in [services.md](services.md), [machines.md](machines.md) and [Routers](#routers) below. Upgrading from `lcm/v1` documents is described in [operations.md](operations.md#upgrade-to-lcmv2-resources).

## Document

```yaml
apiVersion: lcm/v2
kind: Resource
metadata:
  name: web                      # lowercase letters, digits and '-'
  labels: { tier: frontend }     # matched by trigger.signal and group selectors
spec:
  type: service                  # service | machine | router
  adapter: systemd               # allowed values depend on type (table below)
  config: { unit: web.service }  # strict, typed by type and adapter; unknown fields are refused
  checks:                        # any type: http, tcp, systemd, docker, command, heartbeat, prometheus
    - { name: http, type: http, props: { url: http://127.0.0.1:8080/health } }
  relations:
    - { type: dependsOn, target: db }
    - { type: runsOn, target: workstation }
```

| `type` | `adapter` | `connection` | `config` |
|---|---|---|---|
| `service` | `systemd`, `docker`, `compose`, `command`, `external` | not allowed | runtime fields of the adapter, `source`, `backup`, `jobs` ([services](services.md)) |
| `machine` | `local` | not allowed | `interval`, `staleAfter`, `filesystems`, `interfaces`, `units`, `journal`, `thresholds` ([machines](machines.md#registration-and-configuration)) |
| `router` | `xiaomi` | required | `interval`, `staleAfter`, `thresholds` ([routers](#routers)) |

- `GET /api/v1/resource-types` lists types, adapters, `needsConnection` and each type's actions; the console builds its forms from it. Validation is the server's (`lcm validate`, `POST /api/v1/validate`).
- `type` and `adapter` never change. Unmanage the resource and create a new one instead.
- Policies and Modules are also `lcm/v2`. `lcm/v1` documents are refused with a pointer to `lcm convert`.
- Documents are versioned like other documents: `PUT` needs `If-Match`, restoring creates a new version, apply/validate/export and Git ownership follow [policy-as-code](policies.md#policy-as-code).

## Identity, phases and archives

A resource has an immutable ID and a name. Names are unique among resources that are not unmanaged.

| Phase | Meaning | Next |
|---|---|---|
| `managed` | Observed and controlled | `retired` |
| `retired` | Observation, checks and control stop; records stay | `managed` (resume), `unmanaged` |
| `unmanaged` | Archived and read-only; the name is free again | none |

- Change phases with `POST /api/v1/resources/{resource}/phase` (`{phase, version, reason}`) or `lcm resource retire|resume|unmanage <name>`. Unmanaging requires a retired resource that no config source owns; types add conditions (an open operation, a running service).
- Archives: `GET /api/v1/resource-archives?type=`. Any incarnation, including archives, is addressable as `@<id>` wherever a resource name is read (`lcm get resources @<id>`); name-keyed history (timeline, deployments) of `@<id>` is that of its name. Writes (save, restore, phase, actions, observe, maintenance, operations) need the active incarnation: an archive answers 409 `precondition_failed`.
- Creating a name again starts a new incarnation: new ID, version 1, separate ID-keyed history.

**History keys.** Timeline, deployments, check results and alerts are keyed by name and follow the name across incarnations. Machine and router snapshots, minute points, events and operations are keyed by immutable ID.

## State, health and operation

Each resource reports four separate dimensions:

| Field | Values |
|---|---|
| `phase` | `managed`, `retired`, `unmanaged` |
| `state` | service: `registered`, `deploying`, `running`, `updating`, `rolling_back`, `stopped`, `failed`; machine, router: `online`, `offline`, `unknown` from observation freshness (offline only for routers with fresh unreachable evidence) |
| `health` | `healthy`, `degraded`, `down`, `unknown`, with `reasons` |
| `operation` | The durable operation whose outcome is still open, if any |

Health combines the resource's checks with its type's native health (machine thresholds, router thresholds and reachability) in the monitor and takes the worst: `down` > `degraded` > `unknown` > `healthy`. A resource with neither checks nor native input is `unknown`. Retiring or unmanaging a resource drops its checks, native input and health at once, without a signal. Every combined change is a `health.changed` signal, so policies can react to machines and routers as well as services. Check rules and alert ingestion are in [services.md](services.md#health-checks); they apply to every type.

## Actions

Every resource lists `actions`: name, `available` with a `reason`, `risk` (`read`, `reversible`, `disruptive`, `destructive`), `policy` (whether policies may run it) and `kind`:

| Kind | Route | Examples |
|---|---|---|
| `action` | `POST /resources/{r}/actions/{action}` for services; own routes for observe and maintenance | `deploy`, `restart`, `observe` |
| `operation` | `POST /resources/{r}/operations` | `reboot`, `graceful-shutdown` |
| `phase` | `POST /resources/{r}/phase` | `retire`, `resume`, `unmanage` |

- Service actions run in the background with idempotent results ([services](services.md#lifecycle-states-and-actions)); only they have `policy: true`.
- Machine and router operations are durable: the intent is stored before anything is sent, then submitted once and observed until `succeeded`, `failed` or `indeterminate`. They need a caller-chosen idempotency key and `If-Match`; an unknown outcome stays open until an administrator resolves it. Only direct administrator requests may submit or resolve them; policies are refused.
- A route the resource's type does not support answers 409 `unsupported`.

## Relations

| `type` | Meaning | Cycles |
|---|---|---|
| `dependsOn` | Availability depends on the target | refused (422) |
| `runsOn` | The target hosts this resource | refused (422) |
| `controlPath` | LCM or its operator reaches this resource through the target | allowed |

- A target may not exist yet; it is stored and listed in the resource detail's `unresolvedRelations` (also in the responses of create, save and restore) and in the impact's `unresolved`. Self-references and duplicate `(type, target)` pairs are invalid; at most 100 relations.
- Only managed resources count: retired ones are neither observed nor controlled, and their relations are ignored.

**Impact.** `GET /api/v1/resources/{resource}/impact[?action=]` (`lcm resource impact <name>`) returns:

- `dependents`: managed resources that depend on it through `dependsOn`/`runsOn`, transitively, nearest first, each with `relation`, `via` and `depth`;
- `controlDependents`: managed resources whose `controlPath` targets it or one of its dependents;
- `unresolved`: its own relation targets that do not exist;
- with `action`, `acknowledgementRequired`.

**Acknowledgement.** A guarded action on a resource with a non-empty impact is refused with 409 `impact_unacknowledged` (details carry the impact) unless the request sets `acknowledgeImpact: true` (CLI `--acknowledge-impact`). Guarded are disruptive and destructive actions and operations plus retiring a service: service `stop`, `restart`, `deploy`, `rollback`, `run-job` and `retire`; machine `reboot` and `graceful-shutdown`; router `reboot`. Entering machine maintenance stops nothing and needs none. A dry run returns the impact instead of refusing. The acknowledged dependents are recorded in the audit entry (`acknowledgedImpact`). In policies, `lifecycle.*` nodes take the prop `acknowledgeImpact`; without it such a node fails permanently, also in dry runs.

Example: the operator reaches the console through the router, so the machine declares it.

```yaml
# in the workstation Machine resource
relations:
  - { type: controlPath, target: home-router }
```

Rebooting `home-router` then lists `workstation` as a control dependent and needs acknowledgement.

## Group actions

`POST /api/v1/resource-groups/actions` (`lcm resource group-action --label k=v [--label ...] <action>`) runs one service lifecycle action on every managed service matching all labels (at least one label; type `service` only; machine and router operations are refused).

- Order: dependencies first; `stop` runs dependents first.
- Nothing runs unless every step plans successfully and any needed acknowledgement is given. `dryRun` returns the ordered plan with each step's impact.
- Steps run one after another; the first failure stops the group and the rest are `notRun`. Each step checks its impact again before it runs, so a step that needs an acknowledgement the request did not give (a relation added meanwhile) fails the group.
- The run answers 202; poll `GET /api/v1/resource-groups/actions/{id}` (`--wait` in the CLI). Group records are in memory: the last 100, until restart. Each step is also a normal action under `/actions/{id}` with its own audit entry, plus one `service.group-action` entry.

## Events and signals

- Signals carry `resource` and `resourceType`. Kinds: `check.changed`, `health.changed`, `lifecycle.changed`, `alert`, and `resource.config.conflict` / `resource.config.invalid` from config sync.
- The SSE stream (`GET /api/v1/events`) has the types `signal`, `run`, `node`, `timeline`, `action`, `resource` (document or phase change), `observation` (stored machine and router events) and `catalog` (policy deploy/undeploy), filtered by `resource`, `resourceId`, `policy`, `run` and `types`. SSE is best effort: reload from the API after a reconnect or `overflow`.

## Connections

Resources that LCM reaches over the network name a **connection** in `spec.connection`. Connections are host configuration, never documents:

- They live in the YAML file named by `LCM_CONNECTIONS_FILE` (`--connections-file`; empty means none). It is not synced from Git and cannot be changed through the API.
- The file is read and validated once at startup; any error stops the server. Restart LCM after changing it.
- A document can name a connection but never widen it: saving a resource is refused (422 at `spec.connection`) when the connection is not configured, has another type than the adapter, or does not list the resource in `allowResources`. Writes not in `allowActions` are refused.
- `GET /api/v1/connections` (`lcm get connections`) returns name, type, `allowActions`, `allowResources` and the resources naming it. URLs, usernames and passwords are never returned.

```yaml
connections:
  - name: home-router
    type: xiaomi                      # the only type
    url: http://192.168.31.1          # http or https host; no credentials, path, query or fragment
    passwordSecret: home-router-password
    username: admin                   # optional, default admin
    deviceId: "02:4c:43:4d:00:02"     # optional MAC-like login ID; see below
    insecureSkipVerify: false         # optional, for https with a self-signed certificate
    allowActions: [reboot]            # writes allowed through this connection; default none
    allowResources: [home-router]     # resources allowed to use it; empty or missing allows none
```

| Rule | Detail |
|---|---|
| File | Regular file owned by root, not a symlink, not writable by group or others (for example `root:lcm 0640`); every directory above it is owned by root, not a symlink and not writable by group or others (for example `/etc/lcm`). At most 1 MiB, one YAML document, unknown fields refused |
| Names | `name` 1-63 lowercase letters, digits or `-`, unique; `allowResources` entries are resource names, no duplicates |
| Allowlists | `allowResources` and `allowActions` are allowlists: an empty or missing list allows no resource or no write |
| Secret | `passwordSecret` names a secret in `LCM_SECRETS_DIR` (or `LCM_SECRET_<NAME>`); it must resolve to a non-empty value at startup, from a regular file (not a symlink) of mode 0600. The value is never logged, audited or returned |

## Routers

`type: router`, `adapter: xiaomi` observes Xiaomi routers with stock firmware through their local web API and reboots them on a direct administrator request.

```yaml
apiVersion: lcm/v2
kind: Resource
metadata:
  name: home-router
spec:
  type: router
  adapter: xiaomi
  connection: home-router
  config:
    interval: 60s          # 15s-1h; default 60s
    staleAfter: 180s       # at least 2x interval, at most 24h; default 3x interval
    thresholds: { cpuPercent: 90, memoryPercent: 90, consecutive: 3, hysteresis: 5 }   # defaults
```

**Status.** Real-device acceptance (login, observation, reboot and recovery on actual hardware) is pending the operator's confirmation. Login (SHA-1 and SHA-256 modes), observation, reboot and unknown outcomes are covered by automated tests against a simulated router only.

### Observation

Each managed router has its own loop at `interval`, at least every 30 seconds while a reboot waits for evidence; at most four router requests run at once, so a router that is down never delays the others. `lcm resource observe <name>` (or `POST .../observe`) takes one sample now. Each attempt is stored with an `outcome`:

| Outcome | Meaning |
|---|---|
| `ok` | The router answered with status |
| `unreachable` | No HTTP answer (refused, dropped, timeout) |
| `auth-failed` | The router rejected the credentials |
| `unavailable` | An answer without usable status |
| `not-configured` | No usable connection or secret on this host |

- Measurements: CPU percent and cores, memory percent, uptime, temperature, client count, WAN link/status/uptime/type and traffic rates and totals, model and firmware, with per-source coverage. WAN addresses, DNS servers, MAC addresses and the serial number are never read.
- `state`: `online` while the latest attempt is fresh and `ok`, `offline` while it is fresh and `unreachable`, otherwise `unknown`.
- Native health: fresh `unreachable` is `down`; CPU or memory at or above its threshold for `consecutive` fresh samples is `degraded` until the value falls to the threshold minus `hysteresis`; a missing CPU or memory value, any other failed outcome and stale data are `unknown`.
- Events (`lcm resource events`, SSE `observation`): `router.reachability.changed`, `router.boot.changed`, `router.health.changed`, `router.threshold.firing` / `.recovered`, `router.collector.changed`. History: `lcm resource history` or `GET .../history[?resolution=minute]`.
- Snapshots, minute points and events share the machine [retention settings and row ceilings](machines.md#events-gaps-and-history).

A rejected login is never retried within a call, so a wrong password costs one attempt per sample. After an authentication failure the password is read again from the secret on the next attempt, so a rotated secret takes effect without a restart.

### Reboot

```sh
lcm resource reboot home-router --version 3 --reason 'Clear stuck WAN' --confirm home-router --acknowledge --idempotency-key home-router-001
lcm resource operation home-router <operation-id>
```

Preconditions, also shown by `lcm resource capabilities <name>`:

- the router is managed, its connection is configured, matches the adapter, allows this resource and lists `reboot` in `allowActions`;
- the latest observation is fresh and `ok`, and reports uptime;
- no other operation of the router is unresolved and local machine maintenance is not active;
- a direct administrator request with the current version, an idempotency key and, when the router has dependents or control dependents, `acknowledgeImpact`.

The request is stored, sent once and never resent, also after a restart; submitting it makes the router's loop observe at once. Outcomes:

| Result | When |
|---|---|
| `succeeded` | A fresh `ok` observation after the submission and within 10 minutes shows a boot after the submission (observed time minus uptime, 1 minute tolerance) and after the boot seen before the request |
| `failed` | The router rejected the request, or it was never delivered (including rejected authentication, and a request LCM could not record or was stopped before sending) |
| `indeterminate` | The request was accepted or its answer was lost, and no evidence arrived within 10 minutes; or LCM stopped after sending it but before recording the outcome |

A retry with the same idempotency key returns the stored operation, closed first if its submission was abandoned. `lcm resource resolve <name> <id>` closes an `indeterminate` operation after review. It needs a reason and the current version, is allowed only after the 10-minute deadline, never turns the operation into a success and is audited as unverified. Retiring or unmanaging a router with an unresolved operation is refused.

### Endpoints and limitations

- The client calls only `xqsystem/init_info`, `xqsystem/login`, `misystem/status`, `xqnetwork/wan_info`, `xqsystem/reboot` and `web/logout`. Endpoints that return WAN or Wi-Fi passwords (such as `pppoe_status`, `misystem/newstatus`, `wifi_detail_all`) are never called. Redirects are not followed.
- Routers sharing a connection share one session and login sequence. The router accepts a login nonce only if it is newer than the last one from the same device ID, so at most one login per second succeeds per device ID. Give each connection to the same router its own `deviceId` (a locally administered MAC such as `02:...`); the default is `02:4c:43:4d:00:01`.
- No configuration changes, no Wi-Fi or client management, no power-on. Rebooting the router can cut the operator's or LCM's own network path; declare that with a `controlPath` relation.
