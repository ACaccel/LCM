# Security observations and delivery

Security integration is disabled by default. Its observation pipeline reconciles
persisted local Machine observations and existing Service health checks, accepts
sanitized collector input, and persists incidents with optional bounded Konata delivery.
Observation and delivery perform no lifecycle or host changes. The optional
[operator controls](#security-console-and-operator-controls) use a separate restricted
helper for explicitly confirmed host changes. The optional [host component](#host-component)
installs the collector, audit watches, integrity checks and that helper.

## Configuration

Set these variables in the existing LCM environment file and restart LCM:

```ini
LCM_SECURITY_ENABLED=true
LCM_SECURITY_MACHINE_ID=<immutable registered Machine id>
LCM_SECURITY_KONATA_URL=http://127.0.0.1:4100/notifications/security-alerts
LCM_SECURITY_TOKEN_SECRET=konata-notify-key
```

Register the local machine resource (`type: machine`) first. Use its `id`,
not its mutable display name. Delivery is optional; to enable it, configure a
Konata route for the intended private Discord channel first. The token variable contains an LCM
secret **name**; the relay bearer value stays in the existing secret directory.
The destination must be a literal loopback HTTP address, with one notification
route and no credentials, query or fragment. The adapter refuses redirects.

Omit both `LCM_SECURITY_KONATA_URL` and `LCM_SECURITY_TOKEN_SECRET` to enable
observation and intake without sending messages. Such incidents show delivery
`disabled`; entering this mode atomically cancels any earlier pending queue and
marks those deliveries disabled. Enabling delivery later does not replay that
historical backlog.
Setting `LCM_SECURITY_ENABLED=false` stops intake, polling and delivery, cancels
pending deliveries, and retains existing incident history.

## Observation and incident behavior

A one-minute reconciler reads current persisted Machine snapshots and Service
check results. Notification delivery runs in a separate worker, so relay timeouts
cannot delay observation reconciliation. Machine resource alerts reuse the Machine document's consecutive
samples and recovery hysteresis; configured Service check failure thresholds
remain authoritative. CPU or memory pressure never causes automatic blocking.
Two consecutive minute polls with missing, unknown or stale Machine observations
produce a separate monitoring-gap incident. Service observations older than twice
their check interval (at least one minute) are classified as gaps rather than
attacks, also after two minute polls to avoid startup noise. Raw monitor failure
messages are not forwarded.

Progress checkpoints and incident conditions survive restarts. The reconciler
uses current state rather than replaying the journal/history or lossy SSE bus.
A Machine and its Services retain separate identities; incident timestamps and
immutable Machine ids allow comparison with their existing monitoring history.

A condition is `(machineId, service, source, fingerprint)`. The first live firing
opens an immutable incident id; repeated events update that episode, and recovery
updates the same message. A later firing after recovery opens a new episode and
message. Updates are coalesced and normally limited to once per fifteen minutes;
recovery and increasing severity bypass that cooldown. Pending updates retain the
latest revision even when ingestion races a delivery attempt.

When a Service resource is unmanaged, reconciliation retires its existing
episodes for the configured Machine, cancels pending delivery and releases active
condition capacity. History explicitly says `retired`, which does not assert
that the service recovered. External ingestion still cannot reference an unknown
Service. Any HTTP request already in flight may complete, but its late result
cannot restore a cancelled queue or overwrite retirement state.

## Collector contract

`POST /api/v1/hooks/security` accepts sessions and admin or `ingest` tokens. An
`ingest` token cannot read incidents or other administrator resources. Body size
is limited to 16 KiB and unknown properties are rejected:

```json
{
  "eventId": "collector-stable-event-id",
  "machineId": "registered-immutable-machine-id",
  "service": "optional-existing-service-name",
  "source": "nginx",
  "fingerprint": "auth-abuse",
  "status": "firing",
  "severity": "warning",
  "summary": "Sanitized route-category traffic summary",
  "observedAt": "2026-11-01T00:00:00Z",
  "historical": false
}
```

Sources are `nginx`, `auditd`, `aide` or `collector`; `machine` and `service` are
reserved for native observations. Status is `firing` or `resolved`; severity is
`info`, `warning` or `critical`. Summary must contain 1–500 printable characters.
Do not submit raw logs, query strings, bodies, cookies, tokens or command output.
`eventId` and `fingerprint` use 1–100 characters from `[A-Za-z0-9._:-]`, starting
with an alphanumeric character. Event ids are globally unique across collectors.
An identical retry returns the original receipt; a changed body for an existing
id returns 409. Idempotency is bounded by event retention/capacity.

The server validates a managed immutable Machine id and any optional Service.
Timestamps allow five minutes of future skew and thirty days of history. Events
older than ten minutes, explicitly historical events, and events older than the
condition's latest observation are stored without modifying live incidents.
Accepted events return 202 with `accepted`, `duplicate`, `incidentId` (possibly
empty for history or recovery without an active incident), and `historical`.

## Typed traffic observations

`POST /api/v1/hooks/security/traffic` accepts the same credentials and 16 KiB
limit, with a separate measurement model:

```json
{
  "machineId": "registered-immutable-machine-id",
  "service": "web",
  "windowStart": "2026-11-01T00:00:00Z",
  "windowEnd": "2026-11-01T00:01:00Z",
  "category": "auth",
  "requests": 12,
  "status429": 0,
  "status5xx": 0,
  "requestTimeSumMs": 120,
  "requestTimeMaxMs": 20,
  "responseBytes": 2400,
  "requestBytes": 900,
  "dryRunHits": 0
}
```

Categories are `auth`, `api`, `upload`, `socket`, `static` and `other`. Windows
must have positive duration, at most one hour, and respect the same history and
future-skew bounds. Counters are non-negative integers; timing values are finite
milliseconds. Subcounters cannot exceed requests and maximum time cannot exceed
the sum. Normal traffic measurements **never open or recover incidents**.
Collectors finalize a window before sending it. Identical Machine/Service/category
windows retry idempotently; changed contents return 409. This history has its own
100,000-row and thirty-day bounds, enough for over seven days of six minute-level
categories. Store averages as sum/count rather than averaging prior averages;
`requestBytes` is Nginx `request_length`, including protocol/header overhead, and
`responseBytes` is `bytes_sent`, not only response bodies.

## Delivery, bounds and inspection

The SQLite outbox holds at most 1,000 pending incidents. A due batch sends at most
20 messages per minute. Failed attempts retry after 1, 2, 4, 8, 16, 32 and then
64 minutes; pending delivery expires after seven days. Each PUT uses the persisted
incident id as its key across retries and restarts. A successful PUT followed by
a crash therefore updates the same Konata message on retry.

The event store retains at most 10,000 envelopes and thirty days of history;
incidents/condition keys are capped at 2,000. Finished incidents without pending
delivery make room first. Fully active capacity refuses new conditions with 503
instead of silently deleting an ongoing episode. Resolved history expires after
thirty days. Finished notification test episodes are also deleted after
`LCM_OPERATION_RETENTION` (default 90 days), even while security is disabled, and
finished host operations likewise. Queue overflow and expiry are counted and recorded on incidents;
they are not treated as successful notifications. Collectors need their own
bounded retry queue for LCM outages and should expose rejected intake.

Administrator endpoints:

- `GET /api/v1/security/status`: enabled/delivery configuration, pending and failed
  counts, cumulative overflow/expiry, latest processing error and last poll time.
- `GET /api/v1/security/traffic?limit=50&cursor=...`: typed category counters,
  latency sums/maxima and window times for Machine/Service history comparison.
- `GET /api/v1/security/incidents?limit=50&cursor=...`: stable newest-first pages,
  source identity, severity, firing/recovered state and delivery failure details.

The [OpenAPI document](../internal/api/openapi.yaml) defines response schemas.
Operational state changes are audited without secret values or raw summaries.

The console groups the module into four tabs: *資安事件* (monitoring status and incidents), *流量*, *主機控制* (the helper's summary, rules, schedule and scans, or the setup guide when no helper is configured) and *通知* (notification settings). The earlier `/security/overview`, `/security/rules` and `/security/checks` addresses redirect to the incident and host tabs.

Schema migration 0011 is additive; follow the existing
[upgrade and database recovery procedure](operations.md) before deployment.

A Konata relay (and its database) on the same workstation shares LCM's failure domain. This system
cannot send while the host or network is down. Discord summaries are not complete
off-host forensic logs, and editing a message does not guarantee a new phone push.

Native Machine reconciliation uses the manager's `@<immutable-id>` selector; an unprefixed selector is a Machine name. Live readiness requires a successful poll timestamp, not only a healthy Machine API response.

## Security console and operator controls

The authenticated **Security** pages expose overview, incidents, measured traffic,
notification rules and integrity checks. Every security read or management route
requires an administrator session/token; ingest tokens remain submission-only.
Browser mutations use the existing same-origin protection. OpenAPI remains the
API contract; the frontend consumes its generated types.

Incident detail and filtering preserve detector state. Operator acknowledgement,
notes and silence are stored separately with actor, reason, version and bounded
history. Handling/settings updates require the displayed strong `If-Match` version
(including `"0"` initially); stale edits return 409. Silence lasts at most 24 hours,
preserves intake/history and defers the latest pending notification until expiry.
Acknowledgement never asserts recovery. Each incident retains 100 handling entries;
notification settings retain 100 effective versions atomically with their reasons.

Persisted notification preferences provide enablement, a 60–86400 second cooldown
and minimum severity. Immutable root enablement and relay configuration take
precedence. Disabling delivery cancels the queue; enabling it does not replay that
cancelled history. Recovery updates remain eligible for previously delivered
incidents even below the minimum firing severity. Schema-11 episodes with queued
notification history conservatively retain recovery eligibility during migration.
The test endpoint creates one labelled server-trusted episode per stable request ID;
it queues recovery only after firing delivery succeeds. Tests are limited to one per
five minutes, cannot crowd a full queue, and terminal failures release active capacity.
Collector-supplied fingerprints cannot bypass severity filtering. Destinations and
credentials cannot be edited or read through this console.

Traffic filters include Machine, Service (`__unassigned__` selects no Service),
category and time. Series require complete contained observations, at most seven
days and 1000 UTC buckets. Counters belong to their observation's start bucket;
latency averages use sum/request count and maxima use source maxima. Observed RPS
uses the union of actual observation intervals, exposed as `observedSeconds`.
Crossing observations are not proportionally split. Missing buckets are absent;
measured zero windows remain distinct. HTTP bytes are not NIC bandwidth, and no
percentiles or source-IP rankings are inferred.

Host changes pass through `internal/securityhost` to a fixed no-argument root-owned
JSON helper. They require a digest-bound preview, reason, explicit confirmation and
stable operation ID. Query the original operation after a lost response; an
indeterminate result is never blindly replayed. Without `LCM_SECURITY_MACHINE_ID`,
`GET /security/host` answers 200 with `configured: false` and every capability
disabled; the console shows setup guidance and stops polling. Missing/incompatible helpers remain
read-only with unknown measurements. Required audit coverage cannot be disabled,
optional profiles require local root approval, and the helper has no R11 enabling
operation. Baseline requests and AIDE profile choices require local sudo review;
the web application cannot replace or accept an integrity baseline. Complete raw
reports remain local; the UI receives bounded status, configured targets and counts.
The `actor` stored in the helper's protected journal is declared by its caller and
is not verified by the helper: anything that can run the helper's sudo entry point
as the `lcm` account (including commands in resource documents, which run as `lcm`)
can name any actor. Treat it as a reference only; LCM's audit log
(`security.host.preview`, `security.host.apply.start`, `security.host.apply`) is the
authoritative record of who requested a change.
The [host helper contract](../deploy/security/README.md) owns the helper's privileged
paths, limits, protected receipts and recovery procedures; the
[host component](#host-component) installs it.

Migrations 0012/0013 add operator state and host operation records. Deploy with a
matched database/binary recovery point; an older schema-11 binary cannot open the
upgraded database. Keep self-deploy paused until its source includes this release.

## Host component

The optional `security` component of [`lcm host apply`](operations.md#install) installs
the host side: kernel audit watches, a curated AIDE integrity check, the sanitized
collector that feeds the [collector contract](#collector-contract), Nginx traffic
observation and the restricted helper of the
[operator controls](#security-console-and-operator-controls). Its source is
[`deploy/security/`](../deploy/security/) (Python 3 standard library), embedded in `lcm`.

### Configuration

```json
"components": {"security": {"machineId": "<immutable Machine ID>", "watch": {"deploy": ["/opt/extra"]}}}
```

`machineId` is the registered Machine the collector reports for. Watched paths are
fixed categories plus `watch` (at most 32 absolute paths per category). Paths that do
not exist are skipped and reported. Audit watches record writes and attribute changes;
AIDE checks the same paths plus `sudo`, `sshd`, `nginx`, the Python binary and the
component's programs.

| Category | Paths |
|---|---|
| `accounts` | `/etc/passwd`, `/etc/group`, `/etc/shadow`, `/etc/gshadow` |
| `ssh` | `/etc/ssh`, `~/.ssh` of root and of accounts under `/home` |
| `sudoers` | `/etc/sudoers`, `/etc/sudoers.d` |
| `cron` | `/etc/crontab`, `/etc/cron.*`, `/var/spool/cron/crontabs` |
| `systemd` | `/etc/systemd/system`, `/etc/systemd/user`, `~/.config/systemd/user` |
| `deploy` | `/usr/local/libexec/lcm*`, `/usr/local/bin/lcm`, `/etc/lcm`, and `/opt/<service>`, `/etc/<service>` of every grant in [`services`](operations.md#service-grants) except `external` ones |
| `nginx` | `/etc/nginx` |
| `probe` | `/var/lib/workstation-security/audit-probe` (acceptance probe only; not extendable) |

The collector forwards one audit summary per category, login UID, effective UID and
result each minute, with the record count, so a deployment that writes thousands of
watched files does not fill its queue. Summaries take the rule key from the record's
own `key=` field and the identities from the fields before `comm=`, so a program name
cannot claim another category or account. AIDE skips `/opt/<service>`: every deployment
changes it, its releases are root-owned and LCM records each deployment (the audit watch
on `/opt/<service>` stays); it checks `/etc/<service>` and the system categories. A
change to the integrity scope itself (a new grant, a new `watch` path) makes the next
AIDE check report the difference until the baseline is reviewed and accepted locally.

The collector treats every line of the Nginx observation log, which the web server's
account can write, as untrusted: a line that is not a well-formed JSON object is
discarded and counted, at most 64 distinct services are counted per window (the rest
as unassigned), and items from that log may hold at most 600 of the 1,000 queue slots
and are dropped first, so they never push out audit, AIDE or health events, which are
also delivered first. Each input's batch is checkpointed with its cursor.

### Installed files

The component keeps the paths of the installation it adopts; none carries a date.

| Path | Contents |
|---|---|
| `/usr/local/lib/workstation-security/{runtime.py,install.py}` | Collector and report programs; the installer copy for the local operator actions |
| `/usr/local/libexec/lcm/security-host/helper.py` | Restricted helper (in a root-only directory) |
| `/etc/sudoers.d/lcm-security-host` | Lets `lcm` run exactly `python3 -I` with the helper |
| `/etc/lcm/security-host/policy.json` | Helper policy: digests of the files the component renders, AIDE schedule, profiles, Nginx thresholds |
| `/var/lib/lcm-security-host/` | Helper journal and local approval receipts |
| `/etc/workstation-security/` | `collector.json`, `aide.conf`, `ingest-token` (root 0600) |
| `/var/lib/workstation-security/` | AIDE baseline `aide/aide.db`, reports, collector state, log rotation state, `backups/transactions/` |
| `/etc/audit/rules.d/50-workstation-security.rules` | Managed watches, keys `wssec_<category>`; approved optional profiles go to `60-lcm-security-optional.rules` |
| `/etc/nginx/conf.d/20-workstation-security.conf` | Observation log format and dry-run zones per category |
| `/etc/nginx/lcm/hooks/<service>/<category>-10-observation.conf` | Dry-run thresholds that ingress locations include |
| `/var/log/nginx/workstation-security.jsonl`, `/etc/logrotate-workstation-nginx-security.conf` | Observation log (root:workstation-security 0640) and its rotation |
| Units `workstation-security-collector.service`, `-audit-status.timer`, `-aide.timer`, `workstation-nginx-security-logrotate.timer` | Collector, audit health each minute, daily AIDE check, log rotation |
| `/etc/lcm/workstation-security.env`, `lcm.service.d/60-workstation-security.conf` | The [configuration](#configuration) LCM reads; written only when missing, then only checked for `LCM_SECURITY_ENABLED=true` and the configured Machine |

### Nginx observation

The [ingress component](operations.md) sets `$lcm_service` and `$lcm_category` (`api`,
`auth`, `upload`, `socket`, `health`, `static`) in every location it renders. The
observation configuration gives both an empty default, so requests outside ingress
locations are neither logged nor counted, and `health` is not logged. The collector
sends traffic per service and category (unknown categories count as `other`).
Thresholds for `auth`, `api`, `upload` and `socket` are kept in the helper policy and
rendered into one hook file per granted service and category; they stay in dry-run
mode. The helper's `nginx.configure` re-renders them under `/run/lcm-ingress.lock`, the
lock the ingress component changes Nginx with; `apply`, removal and revert hold the same
lock from their first Nginx change to the end of any rollback. At most 64 services are
observed. The helper pins only files the component
renders, so ingress changes never count as drift. Without Nginx installed, traffic
observation is off.

### Install, adopt and upgrade

`sudo bin/lcm host apply` stages `install.py`, `runtime.py` and `helper.py` of its build
in a root-only temporary directory, runs `python3 -I install.py plan`, then `apply` with
the wanted state on stdin, and installs the sudoers rule last. `--check` runs only
`plan`, which changes nothing. Upgrading is the same: `make build && sudo bin/lcm host apply`
from the reviewed checkout. What `apply` changes outside the component's own files:

- installs `auditd` and `aide` with `apt-get` when missing, and the system account `workstation-security` (no login shell);
- sets `max_log_file`, `num_logs`, the rotate and `SYSLOG` actions and `log_group` in `/etc/audit/auditd.conf` and `CRON_DAILY_RUN=no` in `/etc/default/aide`, keeping every other line;
- records, then disables, the vendor AIDE timers (`vendor-timers.json`);
- records, then sets, `/var/log/audit` to root:workstation-security 0750 and its logs to 0640 (`audit-dir.json`);
- loads only the managed watches: it never flushes other runtime rules and generates boot rules without `augenrules --load`;
- validates (`nginx -t`) and reloads Nginx, and checks that the helper answers a status request.

`apply` refuses, changing nothing, while the audit configuration is immutable
(`-e 2`), `auditd.conf` names another log file, the collector account has a login shell
or other groups, an output path has a symbolic link or a non-root ancestor, or an
existing policy or collector configuration names another Machine.

**Adoption.** On a host with the earlier hand-installed version, the first `apply`
keeps the AIDE baseline, reports, collector state, the token value, the helper journal
and approvals, the AIDE schedule and the approved profiles. It converts `policy.json` to
version 2 with the thresholds read from the replaced Nginx files, replaces the
observation configuration, removes `/etc/nginx/workstation-security/` (it refuses files
there that no installer made), copies the log rotation state out of its dated directory
and restricts the token to root. It holds the previous helper's lock
(`/var/lib/lcm-security-host/control.lock`) meanwhile. Watches are computed again: on a
host where projects still deploy with their own root tooling (until their services move
to LCM deploys), list those directories in `watch.deploy`, or they stop being watched.
Locations that ingress does not serve yet are not observed.

**Transactions.** Every `apply` and removal records each change in
`/var/lib/workstation-security/backups/transactions/<id>/` with a copy of every file it
replaces, and records the units it enables, starts, disables or stops; a failed run
undoes its own transaction, including the units and the kernel's audit watches, and
restarts the collector when its files were restored.
`sudo bin/lcm host security revert --check|--apply [--transaction <id>]` undoes the last
one when no file it changed was changed since (otherwise it names the file and changes
nothing). Data written since (reports, rotation state, the recorded permissions),
installed packages and the account stay. The newest 10 transactions are kept; older
ones, with their file copies (which can include an ingest token), are deleted.

**Locking.** The installer holds `/run/lcm-security.lock`, waiting up to a minute for it.
While it waits and works, `/run/lcm-security.pending` makes the helper refuse new
requests, so the `lcm` account cannot keep the lock from root by calling the helper
again and again; a file older than an hour, left by an interrupted run, is ignored.

### Ingest token

When `/etc/workstation-security/ingest-token` is missing, `apply` creates an ingest-scope
API token named `security-collector` (`lcm token create --scope ingest`, run as `lcm`),
hands it to the installer on stdin and stores it root-only (0600); when the installer
then fails, `apply` revokes that token again. The collector unit
reads it as a systemd credential (`LoadCredential=`). To rotate it: revoke the old token
(console, or `sudo -u lcm lcm token list` and `token revoke <id>` with
`--data-dir /var/lib/lcm`), delete the file, run `sudo bin/lcm host apply`.

### AIDE baseline

`apply` never creates or accepts a baseline. After reviewing the host
(`sudo python3 -I /usr/local/lib/workstation-security/install.py review` writes a
root-only report: package verification and the digests of LCM's own root tools,
`/usr/local/bin/lcm`, `/usr/local/libexec/lcm*` and the component's programs, read
without following links), accept it locally with
`sudo python3 -I /usr/local/lib/workstation-security/install.py baseline --reviewed-source`
(`--replace-reviewed` replaces an existing one; the previous one is kept as
`aide.db.previous`), then record the receipt the [helper contract](../deploy/security/README.md#recovery-and-local-approval) describes.

### Removal and cleanup

With `components.security` set to `null`, `apply` reports the installed component as
needing manual action. `sudo bin/lcm host apply --remove` removes the sudoers rule first,
then the units, programs, configuration, managed audit rules, Nginx files and the token,
and restores the vendor timers and `/var/log/audit` permissions it recorded. It keeps the
baseline, reports, collector state and transactions in `/var/lib/workstation-security`
(with a copy of the policy), the helper journal, the packages, the account and the
`auditd` settings. Revoke the token in LCM afterwards.

`sudo bin/lcm host security cleanup --check|--apply` removes the leftovers of the
installers the component replaced: the units `workstation-security-ssh-auth-rollback.{service,timer}`
and the directories `/var/lib/workstation-security-{20261101,deploy-20261029,nginx-20261101,lcm-20261101,lcm-repair-20261101,repair-20261101,completion-20261102,ssh-auth-20261029}`
and `/var/lib/lcm-security-{console-20261102,console-release-20261102}`.
A directory a unit, logrotate or cron file still names is refused (run `apply` first).
Other `/var/lib/workstation-security-*` and `/var/lib/lcm-security-*` directories are
only reported.
