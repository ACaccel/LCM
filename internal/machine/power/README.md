# Restricted local power helper

This package is a handoff component, independent of Machine models and stores.
It is wired into the CLI and Machine coordinator, but is not installed or enabled
by default. No actual power operation is part of its tests. Linux/systemd is required.

## Coordinator API

- Construct `Binding` with lowercase UUID controller/operation/current boot IDs,
  `Reboot` or `GracefulShutdown`, Unix-millisecond `IssuedAt` and `ExpiresAt`
  (at most five minutes apart), then set `RequestHash = power.Hash(binding)`.
- Call `Client{Runner: runner}.Call(ctx, Request{Version: power.Version,
  Method: power.Prepare, Binding: binding})` only after durable controller intent,
  identity, administrator authorization, maintenance and recovery gates exist.
- Persist the exact binding and prepared receipt before `Submit`. Every call,
  including `Query`, uses that same binding. IDs must never be reused.
- On timeout, transport error, lost response or controller restart, query first.
  An absent receipt is not permission to create a replacement operation or retry
  an uncertain handoff. A new operation requires coordinator reconciliation.
- `Accepted` with `OSRequested=false` is prepared only. `Submitting` is the
  durable pre-command fence. Any recovered `Submitting` becomes `Indeterminate`
  without issuing an OS command. `Accepted` with `OSRequested=true` means only
  that the fixed OS request returned success. All command errors are uncertain.
  `Failed` is terminal (currently a prepared request expiring before submission).
- No receipt proves completion. Reboot completion needs independent coordinator
  identity/new-boot/health verification. Shutdown cannot prove power-off locally,
  even if the machine subsequently boots again.

Protocol v1 is one JSON object on a deadline-capable stdin pipe followed by EOF, one JSON response on
stdout, at most 4 KiB each. The privileged entry bounds pipe reads to 30 s.
Protocol rejections use the response code; the CLI adapter suppresses redundant
stderr text for `ProtocolError` so combined execx output remains valid JSON. Unknown fields, duplicate keys, trailing values,
invalid bindings and unsupported versions/actions fail closed. Request hash is
SHA-256 hex of Go `encoding/json`'s `Binding` encoding in declared field order
with `requestHash` set to the empty string (the field remains present).
It binds the helper request, not an arbitrary controller document hash.

`Helper`, `System`, `ReceiptStore`, `ReceiptTx` and `clock.Clock` allow deterministic
coordinator and crash-window fixtures. Production uses `ServeLocal`; the command
entry is `(*app).machineHelper(ctx, args)` in `cmd/lcm/machine_helper.go`.

## Deployment contract for parent integration

This is a recommendation for a separately authorized installation, not an
installer or sudoers modification:

1. Install a root-owned wrapper at exactly
   `/usr/local/libexec/lcm-machine-power`, with no arguments accepted. Its
   directories and pinned executable must be root-owned and unwritable by other
   users. The wrapper must reject extra arguments, clear the environment and
   exec exactly `/usr/local/libexec/lcm-machine-helper machine-helper`,
   where that binary is a root-owned, version-pinned copy. Never execute the service-account-writable
   self-deploy binary. Never forward caller arguments or use a caller PATH,
   interpreter, command, config path or storage path. Enforce an overall deadline
   including stdin reading; the client already bounds its invocation to 35 s.
2. Grant only that exact no-argument wrapper to the dedicated LCM account through
   noninteractive sudo. Do not grant sudo to `lcm`, `systemctl`, a shell, or an
   arbitrary executable. The client argv is exactly
   `/usr/bin/sudo -n -- /usr/local/libexec/lcm-machine-power`.
   A proposed sudoers command specification for account `lcm` is
   `lcm ALL=(root) NOPASSWD: /usr/local/libexec/lcm-machine-power ""`; the final
   empty argument string is essential to match no arguments. This is a proposed
   deployment rule only; this change installs no policy.
3. Provision `/etc/lcm/machine-power.json` as root, regular file mode `0600`:
   `{"version":1,"controllerID":"<stable lowercase UUID>","systemdVersion":258,"recoveryReady":false}`.
   Pin the systemd major to the locally validated version (minimum 248);
   changes fail closed until separately validated and configured. `recoveryReady`
   must stay false until startup restores unresolved operations/maintenance
   gates before all write paths and automation. The helper additionally requires
   fixed `lcm.service` to report `is-enabled` exactly `enabled`.
4. Initially provision `/var/lib/lcm-machine-power` root-owned mode `0700`,
   with root-owned regular single-link `lock` and `receipts.json`, both `0600`.
   The lock is empty; the initial ledger is `{"version":1,"receipts":{}}`.
   Durably sync initial files and their directory before enabling control.
   All configuration/storage ancestors must be root-owned, unwritable by
   group/others and free of symlinks. Existing insecure paths are rejected,
   never repaired by the helper.
5. Preserve the ledger and lock across reboot, binary rollback and controller
   backup/restore. Never initialize a missing ledger during recovery. Never
   replace/delete a live lock or reset/roll back the ledger. A stale but valid
   ledger backup cannot be detected by local files alone; restoration requires
   disabling control and external reconciliation first.

The ledger permanently retains every ID. New receipts are refused once 10,000
are retained; ledger writes exceeding 16 MiB fail closed. There is deliberately
no automatic pruning or ID reuse. Deployment
must plan a reviewed capacity/migration strategy before reaching this limit.
Writes use exclusive nonblocking `flock`, a same-directory exclusive temporary
file, file `fsync`, atomic rename and directory `fsync`. Lock contention and
storage/permission/corruption errors fail closed. A crash may leave a root-only
unused temporary file; it is never used as receipt evidence.

## Fixed OS boundary and limitations

Only `/usr/bin/systemctl` and `/usr/bin/busctl` are used, through bounded direct
argv with a clean inherited environment and no shell. Checks pin the systemd
version, verify controller autostart and require logind `CanReboot`/`CanPowerOff`
to return `yes`. Every login session and every inhibitor is conservatively
rejected, including idle sessions and unrelated/delay inhibitors. There is no
caller override. Submit repeats these checks.

The final command is `/usr/bin/systemctl --system --no-ask-password
--check-inhibitors=yes reboot` or the same with `poweroff`. There is no `--force`,
`systemd-run`, remote transport, power-on or arbitrary unit/path option. OS checks
are snapshots; the final explicit inhibitor check remains necessary. Errors do
not reveal session identities, inhibitor contents or raw OS diagnostics.

Actual systemd/logind compatibility, root installation, recovery ordering and
real reboot/shutdown require separately authorized integration validation. This
package's fake-command tests do not establish those deployment properties.

References: [systemctl inhibitor checks](https://www.freedesktop.org/software/systemd/man/258/systemctl.html),
[logind capability/session/inhibitor interfaces](https://www.freedesktop.org/software/systemd/man/258/org.freedesktop.login1.html),
[busctl structured output](https://www.freedesktop.org/software/systemd/man/258/busctl.html).
