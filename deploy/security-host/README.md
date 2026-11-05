# Restricted security host controls

This component adds a fixed JSON RPC helper. LCM receives no arbitrary root shell,
file read/write, service name, destination URL, or baseline acceptance operation.
Install only as part of the reviewed console release bundle after Go and Python tests.

The production RPC is one no-argument Python script, invoked with `sudo -n`. Requests
and replies use bounded JSON stdin/stdout and protocol version 1. All privileged inputs,
configuration ancestors, journals, and file destinations are root owned and must not be
group/world writable or symlinks. Private collector status runs with the collector UID,
GID, and empty supplementary groups; root never follows collector-controlled paths.

The helper lives under `/usr/local/libexec/lcm/security-host` and its policy under
`/etc/lcm/security-host`, inside the existing curated integrity/audit roots. Installation
does not accept resulting AIDE differences or rebuild the baseline.

## Fixed operations

| Action | Effective behavior |
|---|---|
| `aide.check` | Start the existing single-instance systemd comparison; poll the operation for a fresh protected report. |
| `aide.schedule` | Change one drop-in on the existing daily timer, preserve jitter, reload/restart and verify read-back. |
| `nginx.configure` | Change numeric thresholds in the existing fixed global/hooks, validate, reload, verify; preserve dry-run. |
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

Use the matching `install.py` and reviewed SHA-256 of `helper.py`. The installer pins
the existing managed files and immutable Machine ID, validates dry-run and matching
Card policies, validates sudoers, and installs the grant last. Root upgrades require a
reviewed migration bundle; rerunning installation never silently resets drift.

The existing LCM unit must permit the narrow sudo entry point. Check actual systemd
`NoNewPrivileges`, `RestrictSUIDSGID`, capability, and namespace restrictions before
claiming the installed control is usable. Do not broadly relax a sandbox automatically.
A release may instead expose host controls read-only until a reviewed service policy
allows the fixed helper. Installation and actual unprivileged status invocation must
both pass before marking deployment complete.

Nginx shares the existing immutable Card policy's fixed hooks. The helper obtains
Card's deployment lock before re-reading and changing them. The current dry-run
policy includes those hooks instead of storing a second threshold copy.

## Recovery and local approval

Every preview persists root-protected intent, digest, expiry, actor, and reason. The
actor is declared by the caller and only a reference; LCM's audit log is the
authoritative record ([security](../../docs/security.md)). Apply
rechecks the digest under the global control lock and persists `running` before any
side effect. A request ID cannot change intent. A later query reconciles AIDE using
its actual unit and a report newer than the recorded execution start. An interrupted
configuration change becomes `indeterminate`; it is never executed again implicitly.
Snapshots and candidate hashes remain in the protected operation journal for local
review. Verified command failures restore only unchanged candidate files. A failed
rollback remains indeterminate and requires administrator review.

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

Run the existing Go checks for `internal/securityhost` after applying migration0013,
and `python3 -m unittest discover -s deploy/security-host -p 'test_*.py'`. Python tests
use only temporary files and fake OS commands; they do not invoke live sudo/systemctl.
