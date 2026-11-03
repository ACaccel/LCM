# Operations

How to run LCM on a host. Build and general configuration reference: [README](../README.md). Local Machine configuration and control: [machines.md](machines.md). Design and security notes: [architecture.md](architecture.md).

## Install

`deploy/` installs LCM on a systemd host whose console is published with `tailscale serve`, and lets LCM deploy itself from `main` afterwards. It runs LCM as the unprivileged system account `lcm`; the standard installation grants only [`self-install`](#self-deploy) as a root command. Machine power permission is a separate, explicitly authorized installation described [below](#local-machine-power-helper).

| Path | Owner, mode | Contents |
|---|---|---|
| `/var/lib/lcm/` | lcm 0750 | Data directory (`lcm.db`, `secrets/`, `sources/`), `work/lcm` (LCM's own clone of the repository, used for self-deploy), `cache/` (Go and npm caches of its builds) |
| `/etc/lcm/lcm.env` | root:lcm 0640 | Server settings ([`deploy/lcm.env`](../deploy/lcm.env)); written only when missing |
| `/opt/lcm/toolchain/{go,node}` | root | Copies of the Go and Node.js toolchains that self-deploy builds with |
| `/usr/local/bin/lcm`, `lcm.prev`, `lcm.failed` | root 0755 | Installed binary, the one it replaced, and the last one a verification rejected |
| `/usr/local/libexec/lcm/self-install` | root 0755 | [`deploy/self-install`](../deploy/self-install) |
| `/etc/sudoers.d/lcm` | root 0440 | [`deploy/sudoers`](../deploy/sudoers): lets `lcm` run exactly `self-install /var/lib/lcm/work/lcm/bin/lcm` |
| `/etc/systemd/system/lcm.service` | root 0644 | [`deploy/lcm.service`](../deploy/lcm.service) |
| `https://<host>:9443` | tailscale serve | Tailnet-only HTTPS proxy to `http://127.0.0.1:7800` |

**Prerequisites:** systemd, sudo, `git`, `curl`, `jq`, `make`, Tailscale signed in with HTTPS certificates; Go and Node.js 24 toolchains in the operator's `~/.local/{go,node}` (or pass `--go-root`, `--node-root`). The repository is public, so neither the clone nor the CI check needs credentials.

### Procedure

1. From a reviewed checkout, build the first binary as the operator: `make build`.
2. Install, as root:

   ```sh
   sudo deploy/bootstrap.sh            # --help lists the options and defaults
   ```

   It validates its inputs before changing anything, then creates the `lcm` account and directories, writes `lcm.env` if missing, copies the toolchains (refusing links that leave them), installs the binary (only when none is installed; `--replace-binary` forces it), `self-install`, the sudoers rule (checked with `visudo`) and the unit (`systemd-analyze verify`), clones `work/lcm` over HTTPS as `lcm`, starts the service and waits for health. When it created the database and runs in a terminal, it asks for the web console's admin password (min 8 characters). When `~/.config/lcm/token` of the operator (`--operator`, default the user who ran sudo) is missing, it creates an admin API token and has the operator's own process write it there (0600). Last, it adds the `tailscale serve` handler, refusing a port that something else uses. Rerunning it changes only what differs.
3. Seed, as the operator:

   ```sh
   deploy/seed.sh
   ```

   It applies monitoring-only services for the host's existing units (below), applies the self-deploy service [`deploy/lcm-self.service.yaml`](../deploy/lcm-self.service.yaml), records a first deployment of the current `main` if there is none (a rollback needs one), and applies the policy [`deploy/lcm-auto-deploy.yaml`](../deploy/lcm-auto-deploy.yaml). It can be rerun; unchanged documents stay unchanged.

Later admin password changes: `sudo -u lcm /usr/local/bin/lcm admin set-password --data-dir /var/lib/lcm`. Run the `lcm` CLI's database commands as `lcm` (`sudo -u lcm`), never as root: the binary is replaced by self-deploy.

**Monitoring-only services.** `seed.sh` lists the systemd units of `card-together*`, `personal-quant-desk*`, `pleiades@*` and `mongodb-pleiades` that exist, and registers one service per application with runtime `external` (LCM only monitors them, so their start, stop and restart are disabled; see [runtimes](services.md#runtime)) and no policies. Long-running units get critical `systemd` checks; oneshot units are represented by their timer as non-critical checks. `http` checks are added only for URLs that answer at seed time (card-together `/health`, metube). The docker check for `metube` is added only when `lcm` is in the `docker` group, which bootstrap does not do because that group is root-equivalent.

### Self-deploy

The `lcm` service builds and installs LCM like any other service; the auto-deploy policy runs it for every new commit on `main` whose `ci.yml` run passed:

1. `trigger.git` sees the commit; `ci.status` waits for CI (unauthenticated GitHub API, 60 requests per hour per IP).
2. `lifecycle.deploy` fetches and detaches `work/lcm` at the commit, runs `make build` (console and binary) with the copied toolchains, then the install step `sudo -n /usr/local/libexec/lcm/self-install /var/lib/lcm/work/lcm/bin/lcm`.
3. `self-install` reads the binary as `lcm` (so a swapped link cannot expose root-only files), checks `lcm version` and requires installed/candidate `lcm storage-schema` values to match. A changed schema or an older binary without that command refuses automatic installation; use the [manual upgrade procedure](#upgrade). Otherwise it keeps the current binary as `lcm.prev`, moves the new one into place and schedules `self-install --verify` in a transient unit `lcm-self-update-<time>` 3 seconds later. It exits at once, so the deployment is recorded as finished before LCM restarts. For the same reason the runtime has only a status command: start, stop and restart are not offered and the deployment skips its restart step. The shared Service write gate blocks this self-deploy path during Machine maintenance; a pending self-install verification also blocks entering Machine maintenance.
4. The verification restarts `lcm.service` and waits up to 60 s for `/api/v1/health` to report the new version. If it does not, it restores `lcm.prev` (keeping the rejected binary as `lcm.failed`), restarts again, logs at error priority and leaves the transient unit failed.
5. The policy's `wait.until` was suspended across the restart and resumes in the new process. The service's `version` check passes only when the running server reports the version of the binary built in the checkout, so after a verification rollback the service stays unhealthy, the wait times out after 3 minutes and `lifecycle.rollback` rebuilds and reinstalls the previous successful commit.

**Trust boundary.** Code on `main` that passes CI is built and run by LCM as `lcm`, the same model as the other auto-deployed projects on this host, and installed as the root-owned `/usr/local/bin/lcm`. Anyone who can push to `main`, or write the `lcm` Service document, therefore controls the `lcm` account and the content of that binary: never run it as root (bootstrap and self-install run it only as `lcm` or `nobody`). The sudoers rule accepts one fixed argv; rules for other units are separate, reviewed changes.

**Inspect and recover.**

```sh
journalctl -u lcm -f                                   # the server
journalctl -u 'lcm-self-update-*' -n 50 --no-pager     # restarts and verifications
systemctl list-units --failed 'lcm-self-update-*'      # rejected updates; clear with systemctl reset-failed
```

- A manual binary-only rollback is appropriate only for a verified equal-schema update. For schema changes or unknown compatibility, follow [Upgrade](#upgrade) and restore the matching database and binary. After a safe equal-schema rollback, deploy the matching commit from the console or `lcm action lcm deploy --version <sha>` so that the catalog agrees.
- To pause self-deploy, pause the policy `lcm-auto-deploy` in the console.
- Progress goes to the BotFleet konata relay route `lcm-deploy`, one Discord message per deployment ([template notifications](policies.md#templates)). It needs the route in konata's `notificationRoutes` (installed settings and the deployer's build snapshot, then a konata restart; see BotFleet's notification relay docs) and konata's `NOTIFY_API_KEY` in the LCM secret `konata-notify-key`:

  ```sh
  grep '^NOTIFY_API_KEY=' /var/lib/pleiades-deployer/inputs/konata.env | cut -d= -f2- \
    | sudo -u lcm sh -c 'umask 077 && cat > /var/lib/lcm/secrets/konata-notify-key'
  ```

  Without them the deployment still runs, but the run ends failed on its last notification.
- A manual `lcm action lcm deploy` has no policy to roll back: if the verification restores `lcm.prev`, the catalog still records the new commit, and the failing `version` check shows the mismatch until you deploy a good commit.
- Verification restores only the binary; it does not restore the database. The schema-equality guard is therefore required, and the installed `self-install` script must itself include that guard. Existing deployments with an older script need an operator-reviewed script update before relying on this protection. It cannot make an older unguarded installation safe retroactively.

### Local Machine power helper

Registering a [Machine](machines.md) enables observation without granting power permission. `LCM_MACHINE_POWER` / `--machine-power` defaults false. Neither the standard install nor registration establishes helper readiness. Wrapper/sudoers delivery and its deployment checks are separate from the controller implementation; source examples are not evidence that anything is installed or validated on the host.

Use the [helper source contract](../internal/machine/power/README.md#deployment-contract-for-parent-integration) as the authority for fixed paths, file ownership/modes, protocol, ledger initialization and OS checks. The deployment must provide:

- A root-owned, version-pinned helper binary and a root-owned wrapper at `/usr/local/libexec/lcm-machine-power`. Use [`deploy/machine-power`](../deploy/machine-power) with the pinned binary `/usr/local/libexec/lcm-machine-helper`. The wrapper accepts no arguments, clears the environment and invokes only that binary's `machine-helper` command; the helper bounds stdin/processing to 30 seconds and the client invocation to 35 seconds. Ancestors must be trusted; never use the service-account-writable self-deploy executable as the privileged helper.
- A reviewed [`deploy/machine-power-sudoers`](../deploy/machine-power-sudoers) rule limited to the exact no-argument wrapper: `lcm ALL=(root) NOPASSWD: /usr/local/libexec/lcm-machine-power ""`. The final empty argument string is required. Do not grant arbitrary `lcm`, shell or `systemctl` execution. Validate the rule with `visudo` before activation.
- Root-only `/etc/lcm/machine-power.json`, with its `controllerID` equal to the registered Machine's immutable ID, not its name. Pin the locally validated systemd major. Keep `recoveryReady` false until startup recovery has been validated to restore unresolved operations and maintenance gates before every write path and automation. Changing registration or helper version requires renewed validation; copying an enabled example is not a readiness check.
- The independent root-owned `/var/lib/lcm-machine-power` receipt ledger and lock, durably provisioned as specified in the contract. Retain them across reboot, controller backup/restore and binary rollback. Never delete/reset the ledger, reuse operation IDs or replace a live lock. Missing or stale restored receipts require control disabled and external reconciliation.

The ledger permanently retains IDs, refuses new receipts at 10,000 entries and fails closed if a write exceeds 16 MiB. It has no automatic pruning; arrange a reviewed capacity/migration procedure before those limits. Disabling new power submissions must still permit receipt queries for recovery; `recoveryReady=false` blocks control while preserving queries against valid existing configuration/storage. Controller queries also remain available with `LCM_MACHINE_POWER=false`.

Before enabling control, independently validate Linux/systemd/logind compatibility, enabled `lcm.service` autostart, startup gates and manual recovery. The helper refuses login sessions and inhibitors; maintenance must be explicitly acknowledged. Journal permissions are a separate deployment choice: basic snapshots remain available when journal access is restricted. Do not run LCM as root or automatically add broad log-reading privileges. Deployment and hardware acceptance for the initial workstation is recorded in the [validation record](machine-validation.md); each new installation still requires these checks. Protocol/fake-command tests do not establish those host deployment properties.

## Uninstall

```sh
sudo deploy/uninstall.sh            # stop and disable LCM; remove the unit, sudoers rule, self-install, binaries and its tailscale serve handler
sudo deploy/uninstall.sh --purge    # also remove /var/lib/lcm, /etc/lcm, /opt/lcm and the lcm account
```

Without `--purge` the database, secrets and checkout stay, so running `bootstrap.sh` again restores the installation. The operator's token file (`~/.config/lcm/token`) is left alone; delete it yourself. The `tailscale serve` handler is removed only when it still proxies to `http://127.0.0.1:7800` (`--ts-port` if it is not 9443).

## Hosts without deploy/

On another layout, follow the same pattern by hand: a dedicated system account owning the data directory, settings in an environment file, a unit like [`deploy/lcm.service`](../deploy/lcm.service), and a sudoers rule for the system units LCM may start, stop and restart (see [Security model](architecture.md#security-model)). Give the account read access to the repositories it deploys (an SSH key without passphrase or a credential helper) and ownership of the working copies named in `source.repo`. Secret files go in `<data dir>/secrets/` (one file per secret, `chmod 600`, owned by the account).

```sh
curl -s http://127.0.0.1:7800/api/v1/health      # {"status":"ok",...}
```

The unit sets neither `NoNewPrivileges=` (it would disable sudo) nor `ProtectSystem=` (it would make `/usr/local/bin` read-only for the commands LCM runs through sudo). Where LCM needs sudo only for `systemctl`, `ProtectSystem=full` is possible.

## Data directory

`LCM_DATA_DIR` (mode 0700) holds:

| Path | Contents |
|---|---|
| `lcm.db` (+ `-wal`, `-shm`) | Controller state: services, machines, policies, runs, history, audit log, locks, tokens, maintenance and operations; privileged helper receipts are stored separately |
| `controller.lock` | Exclusive file lock held by `serve`; a second controller using this directory is refused. Do not delete/replace it to bypass the lock. |
| `secrets/` | Secret files (unless `LCM_SECRETS_DIR` moves them) |
| `calendars/` | Calendar files for `trigger.cron` |
| `sources/` | LCM's own clones of config-source repositories; safe to delete, they are fetched again |

## Backup

The database runs in WAL mode, so copy it with SQLite rather than `cp`:

```sh
sudo -u lcm sqlite3 /var/lib/lcm/lcm.db ".backup '/var/backups/lcm/lcm-$(date +%F).db'"
# or, without the sqlite3 CLI, stop LCM and copy lcm.db together with its -wal file
sudo tar -C /var/lib/lcm -czf /var/backups/lcm/secrets-$(date +%F).tgz secrets calendars
```

Back up `secrets/` separately and store it more carefully than the database. Preserve the matching binary and host configuration too. A normal same-version restore stops LCM, replaces the database (removing stale `-wal` and `-shm` files), then starts it. Reconcile Machine operations/maintenance before restoring automation; a database backup can predate an accepted OS request. Services and policies kept in git config sources can be rebuilt, but Git cannot reconstruct runtime state or power receipts. The root helper ledger is independent: preserve its current state, never roll it back with a controller backup. Schema rollback follows [Upgrade](#upgrade).

## Upgrade

Compare the installed and candidate `lcm storage-schema` outputs before choosing an upgrade path. The command reports the binary's schema contract without starting a controller. Automatic [self-install](#self-deploy) accepts only equal schemas; a differing schema, unavailable command or unknown older binary requires a manual maintenance window. Startup migrations are forward-only, so binary-only rollback after a migration is unsafe.

For a schema-changing or unknown-version upgrade:

1. Prepare an independent recovery path and disable old/new automation, including self-deploy, deployed policy triggers and config sync. Review and preserve unresolved Machine operations/gates; an old binary may not recognize them. Do not rely solely on a new-version gate to constrain an older controller.
2. Stop LCM and take a consistent pre-migration database backup using the [WAL-aware procedure](#backup), paired with the exact old binary, configuration and secrets. Preserve independent root helper receipts in place; do not restore an earlier ledger as part of this backup set.
3. Install the candidate manually, keeping the recovery set. Start one `serve` process for the data directory, with automation disabled, and validate migration, data integrity, API health, observations, operation evidence and maintenance gates before enabling automation. Upgrade an older unguarded `self-install` script separately before re-enabling self-deploy.
4. If validation fails, stop the new controller and restore the matched pre-migration database/binary/configuration set. Keep automation disabled while operations or gates remain unresolved, especially when rolling back to a version that cannot interpret them. Reconcile with current helper receipts and independent observations before resuming work. Never run the old binary against the migrated database as a fallback.

Rollback cannot undo an OS reboot/shutdown request the helper already accepted. A timeout, restored database or older binary does not cancel it. Keep unknown outcomes unverified and require explicit maintenance exit under the [Machine recovery contract](machines.md#maintenance-and-power-requests).

At startup LCM recovers Machine operation/maintenance state before writes and automation, then [reconciles Service states](services.md#lifecycle-states-and-actions) and resumes interrupted policy runs where permitted by the gate. Reconciliation continues every minute; an interrupted deployment is recorded rather than assumed complete.

## Resource use

The target is under 50 MB resident. `LCM_MEMORY_LIMIT` (default `40MiB`, `off` disables) sets a soft limit for the Go runtime so that garbage is collected early instead of letting the heap grow; it is not a cap. Retention settings bound the database: raw check results, rollups, timeline, finished runs, deployment records and idempotency records are pruned by the hourly retention job (`LCM_PRUNE_INTERVAL`). Machine snapshots, minute points and events also use bounded pruning; [retention and evidence exceptions](machines.md#events-gaps-and-history) are documented with their API semantics. The privileged helper ledger has its own permanent capacity limits above. To investigate memory or CPU, set `LCM_PPROF_LISTEN=127.0.0.1:6060`, which serves `net/http/pprof` on its own loopback listener; it is off by default and must stay off in normal operation.

## Remote access

LCM serves plain HTTP. For access beyond localhost run a TLS-terminating reverse proxy (nginx, Caddy) in front, keep LCM on `127.0.0.1`, and set `LCM_COOKIE_SECURE=true`. The event stream (`/api/v1/events`) needs response buffering turned off in the proxy.
