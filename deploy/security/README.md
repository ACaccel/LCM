# Security component: restricted helper contract

The security component ([docs/security.md](../../docs/security.md#host-component)) consists
of `install.py` (the root installer `lcm host apply` runs), `runtime.py` (the collector
and its reports) and `helper.py`, a fixed JSON RPC helper described here. Through the
helper LCM receives no arbitrary root shell, file read/write, service name, destination
URL, or baseline acceptance operation.

The production RPC is one no-argument Python script, invoked with `sudo -n`. Requests
and replies use bounded JSON stdin/stdout and protocol version 1. All privileged inputs,
configuration ancestors, journals, and file destinations are root owned and must not be
group/world writable or symlinks. Private collector status runs with the collector UID,
GID, and empty supplementary groups; root never follows collector-controlled paths.

The helper lives under `/usr/local/libexec/lcm/security-host` and its policy under
`/etc/lcm/security-host`, inside the curated integrity/audit roots. Installation does not
accept resulting AIDE differences or rebuild the baseline. The policy (version 2) pins the
digests of the files the component renders and nothing else: the AIDE timer and
configuration, the managed audit rules, the Nginx observation configuration and the hook
of every granted service and category, plus the optional files the helper writes itself.

## Fixed operations

| Action | Effective behavior |
|---|---|
| `aide.check` | Start the existing single-instance systemd comparison; poll the operation for a fresh protected report. |
| `aide.schedule` | Change one drop-in on the existing daily timer, preserve jitter, reload/restart and verify read-back. |
| `nginx.configure` | Render the thresholds of `auth`, `api`, `upload` and `socket` into the observation configuration and every service hook, validate, reload, verify; dry-run stays on. |
| `audit.profile` | Select only locally approved optional rules; required core remains intact; load and verify essential keys. |
| `aide.profile` | Record a selected approved profile set for local baseline review; keep active scope unchanged. |
| `baseline.request` | Record a baseline review request without altering or accepting the baseline. |

Optional audit changes use only exact managed watch additions/removals and generate
boot rules without `augenrules --load`; unrelated runtime rules are preserved.

The default installer provides no optional profiles. An administrator must separately
review and add optional templates to the root policy before those controls become
available. The helper has no R11 enabling operation; enforcement needs a separately
reviewed helper revision after the representative traffic review.

## Deployment

`lcm host apply` installs and upgrades the helper with the rest of the component and
installs the sudoers rule (exactly `python3 -I` with the helper, no other arguments)
last, after the installer verified that the helper answers a status request. An
installation that needs review is reported and left unchanged; the installer's
transactions make each change reversible ([install, adopt and upgrade](../../docs/security.md#install-adopt-and-upgrade)).

The LCM unit must permit the narrow sudo entry point: it sets neither `NoNewPrivileges`
nor `RestrictSUIDSGID`. Do not broadly relax a sandbox automatically. Verify actual
unprivileged status invocation (`GET /api/v1/security/host`) after installation.

The helper and the installer hold `/run/lcm-security.lock` while they change the
component, and `nginx.configure` also takes `/run/lcm-ingress.lock`, which the ingress
component holds while it changes Nginx, before re-reading and changing the files.
While `/run/lcm-security.pending` (root-owned, less than an hour old) exists, a root
installer operation is waiting for or holding the lock, and the helper refuses every
request with `busy`. Requests nested deeper than 8 levels are refused as `invalid`.

## Recovery and local approval

Every preview persists root-protected intent, digest, expiry, actor, and reason. The
actor is declared by the caller and only a reference; LCM's audit log is the
authoritative record ([security](../../docs/security.md)). Apply
rechecks the digest under the global control lock and persists `running` before any
side effect. A request ID cannot change intent. A later query reconciles AIDE using
its actual unit and a report newer than the recorded execution start. An interrupted
configuration change becomes `indeterminate`; it is never executed again implicitly.
The protected operation journal keeps one digest of the files before the change, of
the candidate files and of the policy before it, never their content, so that it stays
far below its read limit whatever the number of services; the content to restore stays
in the helper's memory. Verified command failures restore only unchanged candidate files. A failed
rollback remains indeterminate and requires administrator review.

The journal holds at most 2,000 operations. When it is full, a preview first removes
finished operations (succeeded, failed, or prepared with an expired preview) last
updated more than 90 days ago, so old history cannot block new previews forever.
Running, indeterminate and awaiting-local-approval operations are never removed. While
the journal is still full, previews are refused. Approval receipts are not removed.

Each fixed command may take 5 seconds and a request 25 seconds; when that deadline
passes, the change unwinds through the same rollback, which has its own 12 second
budget. Both stay below LCM's 45 second limit, so the outcome is in the protected
journal before LCM stops waiting. LCM writes a `security.host.apply.start` audit
entry before it hands an apply to the helper and stores the result with its
`security.host.apply` entry even if the requesting client disconnects.

After manually reviewing differences and completing the existing local sudo baseline
or profile workflow, an administrator can record completion using a root-owned mode600
JSON receipt at `/var/lib/lcm-security-host/approvals/<requestId>.json`:

```json
{"requestId":"review-request-id","previewHash":"64-character-preview-sha256","approvedAt":"2026-11-02T12:00:00Z","baselineDigest":"sha256-of-accepted-baseline"}
```

Use the exact protected journal request ID and preview hash, the actual UTC approval
time, and SHA-256 of `/var/lib/workstation-security/aide/aide.db`. A receipt records
completion only; it does not update the baseline or active profile configuration. The
web API cannot write it. The helper verifies its root ownership and baseline digest.
For `aide.profile`, the local administrator must first apply the requested approved
scope, review/update its baseline through the existing local procedure, and reconcile
the root policy's enabled profile selection and managed configuration hashes. Completion
also requires a fresh, drift-free root policy whose complete `{name, enabled}` profile
selection matches the original request. A receipt alone cannot complete a profile
change while the previous scope remains active; the request stays awaiting local
approval with a scope review reason. `baseline.request` continues to record baseline
review completion without changing any active scope itself.

## Validation

`make security-check` runs the Python tests of the installer, collector and helper;
`make check` includes it. They use only temporary files and fake OS commands and never
invoke live sudo or systemctl.
