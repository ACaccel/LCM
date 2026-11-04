# Local machines

A machine is a [Resource](resources.md) with `type: machine` and `adapter: local`: the Linux host running LCM. It adds OS observations, bounded history, maintenance and restricted power requests. Identity, phases, archives, checks, relations and impact acknowledgement are shared by every type and described in [resources.md](resources.md); this page covers what is specific to machines. Only one active local machine is allowed; a retired one still occupies that slot.

Basic monitoring uses Linux `/proc`, `/sys` and filesystem calls, with bounded `systemctl` queries for selected units. No extra monitoring packages are required. Journal collection uses the host's `journalctl`; full power control requires Linux/systemd and a separately installed helper. Implementation and acceptance are complete; the [validation record](machine-validation.md) distinguishes automated checks from operator-confirmed deployment and hardware acceptance. The helper is not installed or enabled by registration.

## Registration and configuration

Create it from the console's *資源* page or apply a document:

```yaml
apiVersion: lcm/v2
kind: Resource
metadata:
  name: workstation
spec:
  type: machine
  adapter: local
  config:
    interval: 30s
    staleAfter: 90s
    filesystems: [/]
    units: [lcm.service]
    journal: true
    thresholds:
      cpuPercent: 90
      memoryPercent: 90
      diskPercent: 90
      consecutive: 3
      hysteresis: 5
```

```sh
lcm validate -f workstation.yaml
lcm apply -f workstation.yaml --dry-run
lcm apply -f workstation.yaml
lcm get resources --type machine
lcm get resources workstation -o json
lcm export resource/workstation
```

Polling defaults to `30s`, with `staleAfter` three times the interval (`90s` at the default). The interval must be `5s`–`1h`; freshness must allow at least two intervals and at most `24h`. Write durations with explicit units.

The filesystem default is `/`; an explicit empty list disables filesystem collection. `interfaces` and `units` default empty, and `journal` defaults false. Lists select literal sources, not discovery or globs; use clean absolute paths on local filesystems, literal interface names and exact systemd unit names. Journal collection requires an explicit nonempty unit allowlist. A document grants no journal or power privileges. Probe limits and coverage details are defined in the [observation contract](../internal/machine/observe/README.md).

Threshold defaults are the values shown above. CPU, memory and filesystem/inode use are percentages; a threshold must be greater than zero and at most 100. `consecutive` is 1–120 fresh samples. `hysteresis` must be positive and below every threshold; an active threshold clears when the value falls to the threshold minus hysteresis. Missing or stale intervals do not count as consecutive unhealthy samples. Selected units participate in health: active is healthy; other observed states can fire after the configured consecutive count. Select units expected to stay active, rather than completed oneshot units.

Documents are versioned like every resource document, including Git ownership, drift and orphan handling ([policy-as-code](policies.md#policy-as-code)). Removing a document from Git never shuts down, retires or unmanages the machine. `checks` and `relations` are allowed as on any resource.

## Observations and health

```sh
lcm resource observe workstation
lcm resource history workstation --limit 100
lcm resource events workstation --limit 100
lcm resource capabilities workstation
```

`observe` collects a read-only OS snapshot and persists it; it does not operate power. Background polling continues during maintenance. Snapshots include OS/init, boot ID, uptime, CPU/load, memory/swap, selected filesystem capacity/inodes, interface state, available PSI and selected unit states. No process environment or command-line inventory is collected. The native health below is [combined with the resource's checks](resources.md#state-health-and-operation); `state` is `online` while the latest observation is fresh and successful, otherwise `unknown` (never `offline`: same-host LCM cannot observe its host being off).

Read these dimensions separately:

- **Freshness:** `status.fresh` indicates whether the latest observation is within `staleAfter`. Missing, future-dated or stale observations read as health `unknown`. A changed document also reads unknown until observed with that configuration.
- **Coverage:** each source has its own state/reason. Available means readable and parsed, not healthy. Missing optional PSI or journal access reduces that source's coverage without alone declaring the machine unhealthy. Failed required observations produce unknown health.
- **Health:** native health is `healthy`, `degraded` or `unknown`, with reasons. Threshold transitions use persisted consecutive-sample baselines and hysteresis. Combined health changes are `health.changed` signals that policies can react to; a degraded or unknown machine is not evidence of power-off, and no policy node can reboot it.
- **Identity:** a hash derived from the local machine-id is bound on observation; the raw machine-id is not exposed. Missing or changed identity blocks verified control. Cloned machine-ids cannot be distinguished by this mechanism. Boot ID identifies a boot, separately from device identity.

CPU utilization requires two valid counter samples from the same boot. The first sample, counter reset, boot change or stale interval leaves `cpuPercent` unknown; zero must not substitute for it. Required CPU health can therefore remain unknown until the next valid sample. Load averages retain their raw values with CPU count for context.

### Events, gaps and history

Polling persists boot, health, threshold and collector transitions. It only sees differences between samples and cannot capture every short-lived change. Journal events cover selected units in accessible system journals in the default namespace. They are generic structured unit records, not a full OS audit or inferred OOM/crash diagnosis.

Only allowlisted structured metadata is retained: source, opaque cursor/deduplication identity, boot ID, source and ingestion times, unit, severity, classification and historical status. `MESSAGE`, command lines, environment, stack traces, hostnames and process/user identifiers are excluded from stored events. Original logs remain in journald. See the [parser and checkpoint contract](../internal/machine/observe/README.md#journal-polling) for exact attribution and field rules.

Snapshots, transition baselines, normalized events, deduplication and the scan cursor commit atomically before publication. A failed database write cannot advance the durable cursor or publish a committed event. Filtered scanned rows can advance the checkpoint without retaining unrelated metadata. Cursors are opaque and must not be sorted as timestamps.

Backfill has time, row, command-time and output limits. Rotation/vacuum, an unavailable cursor, permission changes, malformed output or exceeded lookback can produce recorded gaps and reduced coverage; unknown loss counts remain unknown. An invalid cursor is recorded before restarting a bounded scan. A batch limit means pending work, not necessarily event loss. Recovery records are tagged historical and must not be replayed as live alerts. LCM does not supply reliable Machine alert delivery or a power policy node.

Stored events reach SSE as the `observation` type, filterable by `resourceId`. SSE is best effort: after reconnect or overflow, query the durable observation, event and operation APIs again. Historical events may refresh the console through SSE; they are not live notifications.

Default retention is 7 days of raw snapshots, 90 days of minute points and 90 days of events, configured by the [shared retention settings](../README.md#configuration). Minute points are grouped by minute and boot, retain the last measurements and average valid CPU samples; they are not averages of every metric. Retention deletes in bounded batches, keeps the latest snapshot baseline, and keeps receipts/completion evidence in durable operations independently of event retention. Raw snapshots and minute points have a 200,000-row pruning ceiling (plus retained current raw baselines); event ingestion keeps at most the last 200,000 event IDs, so bursts may shorten the time window. These tables and ceilings are shared with [routers](resources.md#routers). SQLite can retain freed pages for reuse; these are row budgets, not a disk quota. The latest raw baseline can outlive the normal window. Query raw history with `GET /api/v1/resources/{resource}/history`; add `?resolution=minute` for minute points. The console switches between raw/minute points and plots the last 100 loaded CPU/memory samples without filling gaps; the CLI history command currently reads raw samples. History/events use `limit` and opaque `cursor` pagination; `lcm resource` pages accept 1–199 entries and JSON or `-o yaml` output.

## Maintenance and power requests

Power defaults disabled (`LCM_MACHINE_POWER=false`, `lcm serve --machine-power=false`). Enabling the flag does not install the helper or establish readiness. See [separately authorized installation](operations.md#local-machine-power-helper); low-level paths, protocol and OS checks belong to the [helper contract](../internal/machine/power/README.md).

Only direct authenticated administrator requests from the console, CLI or API can enter/exit maintenance, submit power requests or resolve outcomes. No Machine power node is exposed to policies or triggers, and internal policy actors are rejected. Administrator tokens remain programmatic credentials; this release does not prove human presence or provide a general approval service. `reboot` and `graceful-shutdown` require a managed Linux/systemd host, fresh verified healthy observations, acknowledged maintenance, trusted helper configuration, verified controller autostart and successful systemd/logind checks. The helper rejects every login session and inhibitor and repeats checks at submission; no force override exists. Capability output explains disabled prerequisites but does not reserve or guarantee a later submission.

Maintenance persists a common local write gate across restarts. It blocks service actions, deploy/rollback, backup/jobs, resource document and phase writes, router reboots, config-sync apply, Policy/Module changes, new/resumed policy runs and self-deploy. Observation and read access continue. Entry refuses conflicting in-flight writes rather than interrupting them. Startup restores/reconciles Machine operations and gates before automation and manual writes become available.

Review `GET /api/v1/resources/{resource}/maintenance/preview` (or `lcm resource maintenance <name> preview`) before entry. The API requires its `previewHash`; the CLI requires `--preview-hash`. Entry rechecks the machine version and the ordered known local service names/versions/states under the write gate, rejects a changed hash and persists the reviewed inventory. Supervisor autostart is explicitly unknown until checked by the operator.

The workload view is partial: registered services do not inventory every process, container or dependency. Maintenance does not automatically stop or orchestrate workloads. Prepare them yourself before entry. Acknowledge controller downtime, unknown workloads and manual recovery. OS/systemd/Docker autostart can start workloads outside LCM's actions; maintenance does not change those settings or guarantee they remain stopped.

All machine management/control writes in `lcm resource` require `--version <current-version> --reason <text> --confirm <exact-name> --acknowledge`. `observe` is exempt. Current API reasons are recorded for maintenance and resolution; CLI reasons for power and management-phase changes are local checks only. Example sequence for an independently authorized maintenance window, using the current version returned by `get`:

```sh
lcm resource maintenance workstation preview
lcm resource maintenance workstation enter --preview-hash <reviewed-hash> --version 1 --reason 'Scheduled maintenance' --confirm workstation --acknowledge
lcm resource reboot workstation --version 1 --reason 'Scheduled maintenance' --confirm workstation --acknowledge --idempotency-key workstation-maint-001
lcm resource operations workstation
lcm resource operation workstation <operation-id>
```

Use `shutdown` in place of `reboot` for a graceful-shutdown request. When other managed resources depend on or are controlled through the machine, `reboot` and `shutdown` also need `--acknowledge-impact` ([impact](resources.md#relations)). Caller-chosen idempotency keys are 1–128 characters, starting with a letter/digit and otherwise containing letters, digits, `_`, `.`, `:` or `-`. Keep the operation ID, key and original document version. If the response is ambiguous, inspect operations and retry only the same action/key/version; a new key could request a second operation. There is no command that cancels an accepted OS request.

The controller stores intent before helper prepare/submit. The helper's independent durable receipt is queried after restart or uncertain handoff; recovery never blindly resubmits. States include `pending`, `submitting`, `observing`, `succeeded`, `failed` and `indeterminate`. Acceptance and a successful CLI exit are not proof of completion:

- **Reboot:** success needs a matching accepted helper receipt, the same verified device, a new boot ID and fresh required healthy observations after the request. Restarting only the LCM process does not qualify.
- **Shutdown:** same-host LCM cannot prove power-off. Disconnect, timeout or a later boot ID change cannot establish that shutdown completed; the operation remains unverified. Power-on is unsupported and requires manual intervention.
- **Unknown outcome:** an administrator can resolve an `indeterminate` operation with fresh verified observations, recording actor and reason. On the original boot, resolution additionally requires the helper binding to have expired and a query proving only a prepared or failed receipt; an accepted, missing or uncertain receipt cannot be resolved merely by waiting. Resolution preserves the unverified state and does not exit maintenance. Even verified reboot success leaves maintenance active until explicit exit.

The helper binding's `validUntil` is at most five minutes after request creation and no later than the authorizing health observation becomes stale. Preparation and submission recheck prerequisites; the controller revalidates persisted observations before its submission fence. This authorization window is separate from the ten-minute reboot observation `deadline`. Expiry never revokes an OS request already accepted. Missing uptime or other required evidence cannot establish reboot success.

Maintenance exit requires no unresolved operation and verified healthy observations newer than maintenance entry and any subsequent manual resolution. Collect or wait for a new snapshot after those steps; a still-fresh pre-maintenance or pre-resolution result is insufficient.

```sh
lcm resource resolve workstation <operation-id> --version 1 --reason 'Reviewed unverified outcome' --confirm workstation --acknowledge
lcm resource maintenance workstation exit --version 1 --reason 'Recovery verified' --confirm workstation --acknowledge
```

## Retirement and archives

`lcm resource retire <name>`, `resume <name>` and `unmanage <name>` use the same write-confirmation flags above and follow the [shared phase rules](resources.md#identity-phases-and-archives). Retirement stops observation/control while retaining registration and history; resume returns to managed. Unmanage requires a retired machine, archives it and frees its active slot. None changes OS power or destroys hardware. Unresolved operations block phase changes; release Git ownership before unmanage.

`GET /api/v1/resource-archives?type=machine` discovers archived machines; the console opens their read-only details by immutable ID. `@<immutable-id>` selects an archive in every resource read, version, history, event and operation endpoint, for example `lcm get resources @<immutable-id> -o json` or `lcm resource events @<immutable-id>`. Archive reads remain subject to retention. Reusing a name creates a separate incarnation with a new ID; a plain name selects the active machine and never combines its history with older ones. The upgrade to `lcm/v2` kept every machine ID, so helper `controllerID` and `LCM_SECURITY_MACHINE_ID` values stay valid.

## Optional tools and unavailable features

SSH machines, cloud VMs, remote service execution, independent power-on/off evidence, hardware collectors and full monitoring integrations remain [roadmap items](roadmap.md). Installing an external tool does not add an LCM connector. Relevant official references from the monitoring assessment:

- [Linux proc](https://docs.kernel.org/filesystems/proc.html) and [journalctl](https://www.freedesktop.org/software/systemd/man/latest/journalctl.html): native observations and bounded event reads.
- [lm-sensors](https://github.com/lm-sensors/lm-sensors) and [smartmontools](https://github.com/smartmontools/smartmontools): optional hardware tools; no LCM collector, automatic hardware detection, module loading or disk self-tests are included.
- [Netdata Agent](https://learn.netdata.cloud/docs/netdata-agent/installation): a separate detailed host dashboard/monitor, not required for basic LCM monitoring.
- [node_exporter releases](https://github.com/prometheus/node_exporter/releases) and the [Prometheus integration guide](https://prometheus.io/docs/guides/node-exporter/): optional metrics collection/storage. LCM's Prometheus check queries the Prometheus API; exporter `/metrics` is not that API.

LCM cannot monitor or notify while its own host is off. Independent external monitoring and reliable alert delivery need separate infrastructure. No dependencies, journal permissions, helper installation or real power operations are performed by registering a machine.

## Validation

See the [validation record](machine-validation.md) for automated checks, synthetic load measurements, browser coverage and operator-confirmed deployment acceptance; it predates the `lcm/v2` resource model.
