# Security observations and delivery

Security integration is disabled by default. Its observation pipeline reconciles
persisted local Machine observations and existing Service health checks, accepts
sanitized collector input, and persists incidents with bounded Konata delivery.
Observation and delivery perform no lifecycle or host changes. The optional
[operator controls](#security-console-and-operator-controls) use a separate restricted
helper for explicitly confirmed host changes.

## Configuration

Set these variables in the existing LCM environment file and restart LCM:

```ini
LCM_SECURITY_ENABLED=true
LCM_SECURITY_MACHINE_ID=<immutable registered Machine id>
LCM_SECURITY_KONATA_URL=http://127.0.0.1:4100/notifications/security-alerts
LCM_SECURITY_TOKEN_SECRET=konata-notify-key
```

Register the local machine resource (`type: machine`) first. Use its `id`,
not its mutable display name. Configure the Konata route for the intended private
Discord channel before enabling delivery. The token variable contains an LCM
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
  "service": "card-together",
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
thirty days. Queue overflow and expiry are counted and recorded on incidents;
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
Schema migration 0011 is additive; follow the existing
[upgrade and database recovery procedure](operations.md) before deployment.

Konata, its MongoDB and LCM share the workstation's failure domain. This system
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
indeterminate result is never blindly replayed. Missing/incompatible helpers remain
read-only with unknown measurements. Required audit coverage cannot be disabled,
optional profiles require local root approval, and the helper has no R11 enabling
operation. Baseline requests and AIDE profile choices require local sudo review;
the web application cannot replace or accept an integrity baseline. Complete raw
reports remain local; the UI receives bounded status, configured targets and counts.
The [host helper contract](../deploy/security-host/README.md) owns installation,
privileged paths, limits, protected receipts and recovery procedures.

Migrations 0012/0013 add operator state and host operation records. Deploy with a
matched database/binary recovery point; an older schema-11 binary cannot open the
upgraded database. Keep self-deploy paused until its source includes this release.
