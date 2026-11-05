# Operations

How to run LCM on a host. Build and general configuration reference: [README](../README.md). Resource model, connections and routers: [resources.md](resources.md). Local machine configuration and control: [machines.md](machines.md). Design and security notes: [architecture.md](architecture.md).

## Install

`deploy/` installs LCM on a systemd host whose console is published with `tailscale serve`, and lets LCM deploy itself from `main` afterwards. It runs LCM as the unprivileged system account `lcm`; the standard installation grants only [`self-install`](#self-deploy) as a root command. Machine power permission is a separate, explicitly authorized installation described [below](#local-machine-power-helper).

| Path | Owner, mode | Contents |
|---|---|---|
| `/var/lib/lcm/` | lcm 0750 | Data directory (`lcm.db`, `secrets/`, `sources/`), `work/` (build checkouts; `work/lcm` is LCM's own, used for self-deploy), `cache/` (Go and npm caches of its builds) |
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

**Monitoring-only services.** `seed.sh` lists the systemd units of `card-together*`, `personal-quant-desk*`, `pleiades@*` and `mongodb-pleiades` that exist, and registers one service resource per application with adapter `external` (LCM only monitors them, so their start, stop and restart are disabled; see [runtimes](services.md#runtime)) and no policies. Long-running units get critical `systemd` checks; oneshot units are represented by their timer as non-critical checks. `http` checks are added only for URLs that answer at seed time (card-together `/health`, metube). The docker check for `metube` is added only when `lcm` is in the `docker` group, which bootstrap does not do because that group is root-equivalent.

### Self-deploy

The `lcm` service builds and installs LCM like any other service; the auto-deploy policy runs it for every new commit on `main` whose `ci.yml` run passed:

1. `trigger.git` sees the commit; `ci.status` waits for CI (unauthenticated GitHub API, 60 requests per hour per IP).
2. `lifecycle.deploy` points `origin` of `work/lcm` at the service's `source.url`, fetches and detaches the checkout at the commit, runs `make build` (console and binary) with the copied toolchains, then the install step `sudo -n /usr/local/libexec/lcm/self-install /var/lib/lcm/work/lcm/bin/lcm`.
3. `self-install` reads the binary as `lcm` (so a swapped link cannot expose root-only files), checks `lcm version` and requires installed/candidate `lcm storage-schema` values to match. A changed schema or an older binary without that command refuses automatic installation; use the [manual upgrade procedure](#upgrade). Otherwise it keeps the current binary as `lcm.prev`, moves the new one into place and schedules `self-install --verify` in a transient unit `lcm-self-update-<time>` 3 seconds later. It exits at once, so the deployment is recorded as finished before LCM restarts. For the same reason the runtime has only a status command: start, stop and restart are not offered and the deployment skips its restart step. The shared write gate blocks this self-deploy path during machine maintenance; a pending self-install verification also blocks entering Machine maintenance.
4. The verification restarts `lcm.service` and waits up to 60 s for `/api/v1/health` to report the new version. If it does not, it restores `lcm.prev` (keeping the rejected binary as `lcm.failed`), restarts again, logs at error priority and leaves the transient unit failed.
5. The policy's `wait.until` was suspended across the restart and resumes in the new process. The service's `version` check passes only when the running server reports the version of the binary built in the checkout, so after a verification rollback the service stays unhealthy, the wait times out after 3 minutes and `lifecycle.rollback` rebuilds and reinstalls the previous successful commit.

**Trust boundary.** Code on `main` that passes CI is built and run by LCM as `lcm`, the same model as the other auto-deployed projects on this host, and installed as the root-owned `/usr/local/bin/lcm`. Anyone who can push to `main`, or write the `lcm` service Resource document, therefore controls the `lcm` account and the content of that binary: never run it as root (bootstrap and self-install run it only as `lcm` or `nobody`). The sudoers rule accepts one fixed argv; rules for other units are separate, reviewed changes.

**Inspect and recover.**

```sh
journalctl -u lcm -f                                   # the server
journalctl -u 'lcm-self-update-*' -n 50 --no-pager     # restarts and verifications
systemctl list-units --failed 'lcm-self-update-*'      # rejected updates; clear with systemctl reset-failed
```

- A manual binary-only rollback is appropriate only for a verified equal-schema update. For schema changes or unknown compatibility, follow [Upgrade](#upgrade) and restore the matching database and binary. After a safe equal-schema rollback, deploy the matching commit from the console or `lcm resource action lcm deploy --version <sha>` so that the catalog agrees.
- To pause self-deploy, pause the policy `lcm-auto-deploy` in the console.
- Progress goes to the BotFleet konata relay route `lcm-deploy`, one Discord message per deployment ([template notifications](policies.md#templates)). It needs the route in konata's `notificationRoutes` (installed settings and the deployer's build snapshot, then a konata restart; see BotFleet's notification relay docs) and konata's `NOTIFY_API_KEY` in the LCM secret `konata-notify-key`:

  ```sh
  grep '^NOTIFY_API_KEY=' /var/lib/pleiades-deployer/inputs/konata.env | cut -d= -f2- \
    | sudo -u lcm sh -c 'umask 077 && cat > /var/lib/lcm/secrets/konata-notify-key'
  ```

  Without them the deployment still runs, but the run ends failed on its last notification.
- A manual `lcm resource action lcm deploy` has no policy to roll back: if the verification restores `lcm.prev`, the catalog still records the new commit, and the failing `version` check shows the mismatch until you deploy a good commit.
- Verification restores only the binary; it does not restore the database. The schema-equality guard is therefore required, and the installed `self-install` script must itself include that guard. Existing deployments with an older script need an operator-reviewed script update before relying on this protection. It cannot make an older unguarded installation safe retroactively.

### Local Machine power helper

Registering a [Machine](machines.md) enables observation without granting power permission. `LCM_MACHINE_POWER` / `--machine-power` defaults false. Neither the standard install nor registration establishes helper readiness. Wrapper/sudoers delivery and its deployment checks are separate from the controller implementation; source examples are not evidence that anything is installed or validated on the host.

Use the [helper source contract](../internal/machine/power/README.md#deployment-contract-for-parent-integration) as the authority for fixed paths, file ownership/modes, protocol, ledger initialization and OS checks. The deployment must provide:

- A root-owned, version-pinned helper binary and a root-owned wrapper at `/usr/local/libexec/lcm-machine-power`. Use [`deploy/machine-power`](../deploy/machine-power) with the pinned binary `/usr/local/libexec/lcm-machine-helper`. The wrapper accepts no arguments, clears the environment and invokes only that binary's `machine-helper` command; the helper bounds stdin/processing to 30 seconds and the client invocation to 35 seconds. Ancestors must be trusted; never use the service-account-writable self-deploy executable as the privileged helper.
- A reviewed [`deploy/machine-power-sudoers`](../deploy/machine-power-sudoers) rule limited to the exact no-argument wrapper: `lcm ALL=(root) NOPASSWD: /usr/local/libexec/lcm-machine-power ""`. The final empty argument string is required. Do not grant arbitrary `lcm`, shell or `systemctl` execution. Validate the rule with `visudo` before activation.
- Root-only `/etc/lcm/machine-power.json`, with its `controllerID` equal to the registered Machine's immutable ID, not its name. Pin the locally validated systemd major. Keep `recoveryReady` false until startup recovery has been validated to restore unresolved operations and maintenance gates before every write path and automation. Changing registration or helper version requires renewed validation; copying an enabled example is not a readiness check.
- The independent root-owned `/var/lib/lcm-machine-power` receipt ledger and lock, durably provisioned as specified in the contract. Retain them across reboot, controller backup/restore and binary rollback. Never delete/reset the ledger, reuse operation IDs or replace a live lock. Missing or stale restored receipts require control disabled and external reconciliation.

The ledger permanently retains IDs, refuses new receipts at 10,000 entries and fails closed if a write exceeds 16 MiB. It has no automatic pruning; arrange a reviewed capacity/migration procedure before those limits. Disabling new power submissions must still permit receipt queries for recovery; `recoveryReady=false` blocks control while preserving queries against valid existing configuration/storage. Controller queries also remain available with `LCM_MACHINE_POWER=false`.

Before enabling control, independently validate Linux/systemd/logind compatibility, enabled `lcm.service` autostart, startup gates and manual recovery. The helper refuses login sessions and inhibitors; a power request enters maintenance itself and is confirmed with a reason and the machine name. Journal permissions are a separate deployment choice: basic snapshots remain available when journal access is restricted. Do not run LCM as root or automatically add broad log-reading privileges. Deployment and hardware acceptance for the initial workstation is recorded in the [validation record](machine-validation.md); each new installation still requires these checks. Protocol/fake-command tests do not establish those host deployment properties.

## Uninstall

```sh
sudo deploy/uninstall.sh            # stop and disable LCM; remove the unit, sudoers rule, self-install, binaries and its tailscale serve handler
sudo deploy/uninstall.sh --purge    # also remove /var/lib/lcm, /etc/lcm, /opt/lcm and the lcm account
```

Without `--purge` the database, secrets and checkout stay, so running `bootstrap.sh` again restores the installation. The operator's token file (`~/.config/lcm/token`) is left alone; delete it yourself. The `tailscale serve` handler is removed only when it still proxies to `http://127.0.0.1:7800` (`--ts-port` if it is not 9443).

## Hosts without deploy/

On another layout, follow the same pattern by hand: a dedicated system account owning the data directory, settings in an environment file, a unit like [`deploy/lcm.service`](../deploy/lcm.service), and a sudoers rule for the system units LCM may start, stop and restart (see [Security model](architecture.md#security-model)). Give the account read access to the repositories it deploys (an SSH key without passphrase or a credential helper); LCM clones each service's `source.url` into `<data dir>/work/<name>` itself ([source](services.md#source)). Secret files go in `<data dir>/secrets/` (one file per secret, `chmod 600`, owned by the account).

```sh
curl -s http://127.0.0.1:7800/api/v1/health      # {"status":"ok",...}
```

The unit sets neither `NoNewPrivileges=` (it would disable sudo) nor `ProtectSystem=` (it would make `/usr/local/bin` read-only for the commands LCM runs through sudo). Where LCM needs sudo only for `systemctl`, `ProtectSystem=full` is possible.

## Data directory

`LCM_DATA_DIR` (mode 0700) holds:

| Path | Contents |
|---|---|
| `lcm.db` (+ `-wal`, `-shm`) | Controller state: resources, policies, modules, runs, history, audit log, locks, tokens, maintenance and operations; privileged helper receipts are stored separately |
| `controller.lock` | Exclusive file lock held by `serve`; a second controller using this directory is refused. Do not delete/replace it to bypass the lock. |
| `secrets/` | Secret files (unless `LCM_SECRETS_DIR` moves them) |
| `calendars/` | Calendar files for `trigger.cron` |
| `sources/` | LCM's own clones of config-source repositories; safe to delete, they are fetched again |
| `work/<name>` | Build checkout of each service with a [source](services.md#source); cloned again by the next deploy when deleted. Backups and jobs run in it, so delete one only when its build output is disposable |

## Backup

The database runs in WAL mode, so copy it with SQLite rather than `cp`:

```sh
sudo -u lcm sqlite3 /var/lib/lcm/lcm.db ".backup '/var/backups/lcm/lcm-$(date +%F).db'"
# or, without the sqlite3 CLI, stop LCM and copy lcm.db together with its -wal file
sudo tar -C /var/lib/lcm -czf /var/backups/lcm/secrets-$(date +%F).tgz secrets calendars
```

Back up `secrets/` separately and store it more carefully than the database. Preserve the matching binary and host configuration too, including the connections file. A normal same-version restore stops LCM, replaces the database (removing stale `-wal` and `-shm` files), then starts it. Reconcile Machine operations/maintenance before restoring automation; a database backup can predate an accepted OS request. Resources and policies kept in git config sources can be rebuilt, but Git cannot reconstruct runtime state or power receipts. The root helper ledger is independent: preserve its current state, never roll it back with a controller backup. Schema rollback follows [Upgrade](#upgrade).

## Upgrade

Compare the installed and candidate `lcm storage-schema` outputs before choosing an upgrade path. The command reports the binary's schema contract without starting a controller. Automatic [self-install](#self-deploy) accepts only equal schemas; a differing schema, unavailable command or unknown older binary requires a manual maintenance window. Startup migrations are forward-only, so binary-only rollback after a migration is unsafe.

For a schema-changing or unknown-version upgrade:

1. Prepare an independent recovery path and disable old/new automation, including self-deploy, deployed policy triggers and config sync. Review and preserve unresolved Machine operations/gates; an old binary may not recognize them. Do not rely solely on a new-version gate to constrain an older controller.
2. Stop LCM and take a consistent pre-migration database backup using the [WAL-aware procedure](#backup), paired with the exact old binary, configuration and secrets. Preserve independent root helper receipts in place; do not restore an earlier ledger as part of this backup set.
3. Install the candidate manually, keeping the recovery set. Start one `serve` process for the data directory, with automation disabled, and validate migration, data integrity, API health, observations, operation evidence and maintenance gates before enabling automation. Upgrade an older unguarded `self-install` script separately before re-enabling self-deploy.
4. If validation fails, stop the new controller and restore the matched pre-migration database/binary/configuration set. Keep automation disabled while operations or gates remain unresolved, especially when rolling back to a version that cannot interpret them. Reconcile with current helper receipts and independent observations before resuming work. Never run the old binary against the migrated database as a fallback.

Rollback cannot undo an OS reboot/shutdown request the helper already accepted. A timeout, restored database or older binary does not cancel it. Keep unknown outcomes unverified and require explicit maintenance exit under the [Machine recovery contract](machines.md#maintenance-and-power-requests).

At startup LCM recovers machine and router operation/maintenance state before writes and automation, then [reconciles service states](services.md#lifecycle-states-and-actions) and resumes interrupted policy runs where permitted by the gate. Reconciliation continues every minute; an interrupted deployment is recorded rather than assumed complete.

### Upgrade to lcm/v2 resources

Storage schema 14 replaces Service and Machine with one `lcm/v2` Resource model ([resources.md](resources.md)). The schema changes, so self-install refuses it: follow the manual procedure above, including the backup, and keep the pre-upgrade database/binary pair. Older binaries refuse the migrated database. There is no compatibility layer.

**What the migration does.** Migration 0014 runs at the first start in one transaction:

- Services become `type: service` resources with new IDs; retired services become phase `retired` (state `stopped`); deleted services that left versions become `unmanaged` archives. Machines become `type: machine` resources and keep their IDs, so helper `controllerID` and `LCM_SECURITY_MACHINE_ID` stay valid.
- Every stored document version, Policy and Module (including the module snapshots deployed plans load from), Git ownership record and stored repository document is converted to `lcm/v2` with the same rules as `lcm convert`; timeline, deployments, checks, alerts, run events and machine history are carried over. Queued, running and suspended runs get their signal events and their lifecycle nodes' checkpointed output and props renamed (`service` to `resource`), so they resume against the converted policy.
- It stops with an error, leaving the database unchanged, when a Service and a Machine share a name or both are Git-owned under one name; rename or release one with the old binary first.
- What the conversion reports (expressions it cannot rewrite, such as a resource name built from strings, and the added `acknowledgeImpact`) is stored and logged as `converted document needs review` warnings at every start, naming the document and version, as long as that version is the policy's or module's latest. Saving a new version ends the warning; fix each named policy or module and check it with `lcm validate`.

**Git config sources.** After the upgrade, a source that holds any `lcm/v1` document is not synced at all: nothing is applied, no ownership or orphan flag changes, and the source's `lastError` names the files and `lcm convert`. Resources keep their migrated configuration. Convert the files with [`lcm convert`](policies.md#policy-as-code), commit and sync. Converted documents equal the migrated ones, so they produce no drift.

**Interface changes** (no aliases):

| Area | Before | Now |
|---|---|---|
| Documents | `lcm/v1` `Service`, `Machine` | `lcm/v2` `Resource` (`type`, `adapter`, `config`, `relations`); Policy and Module are `lcm/v2`; `lcm/v1` is refused |
| API | `/services*`, `/machines*`, `/machine-archives`, `/hosts`, `/heartbeats/{svc}/{check}` | `/resources*`, `/resource-archives`, `/resource-types`, `/connections`, `/resource-groups/actions`, `/heartbeats/{resource}/{check}`; export and ownership kind `resource` |
| CLI | `lcm action`, `lcm machine`, `lcm get services\|machines` | `lcm resource ...`, `lcm get resources [--type t]`, `lcm get connections`, `lcm convert` |
| Policies | `service` variable, `lifecycle.*` prop and output `service`, `trigger.signal` `services`, `trigger.service`, template param `service` | `resource` (variable adds `type`, `adapter`, `phase`), `resources` plus `types`, `trigger.resource`, template param `resource`; converted automatically |
| Signals and SSE | field `service`; machine events in their own category | fields `resource` and `resourceType`; SSE types `resource` and `observation`, filters `resource` and `resourceId` |
| Config sync | conflicts as `alert` signals | `resource.config.conflict` / `resource.config.invalid` signals for resources |
| Configuration | `LCM_ALERT_SERVICE_LABELS` | `LCM_ALERT_RESOURCE_LABELS` (default `lcm_resource,resource,lcm_service,service`); setting the old variable stops startup with an error naming the new one |
| Lock | `service:<name>` | `resource:<name>`; `lock`/`unlock` nodes with a literal `service:` name are converted, computed names are reported for a manual change |

**Behavior changes.**

- Unmanaging requires a retired resource, machines included; DELETE is gone.
- Retiring a service stops it first when its runtime can; a running service whose runtime cannot stop it stays retired and `running` and cannot be unmanaged until it is resumed and stopped ([phases](services.md#lifecycle-states-and-actions)).
- Disruptive actions on resources with dependents or control dependents need `acknowledgeImpact` ([impact](resources.md#relations)). A v1 `dependsOn` becomes a `dependsOn` relation, so conversion adds `acknowledgeImpact: true` to every `lifecycle.*` node that stops, restarts, deploys, rolls back or runs a job and did not set it; such policies behave as in v1 and are named in the migration warnings. Remove the prop where an action on a service with dependents should stop the policy; review `lcm resource impact <name>` before re-enabling automation.
- Check, timeline and deployment history stays keyed by name; machine and router observations, events and operations are keyed by immutable ID.

### Upgrade to service source URLs (schema 15)

Storage schema 15 replaces `source.repo` and `source.remote` of services with `source.url`, and LCM keeps each build checkout at `<data dir>/work/<name>` itself ([source](services.md#source)). The schema changes, so self-install refuses it (`storage schema unknown or changed`) and an auto-deploy of this version fails and rolls back: pause self-deploy before the commit reaches `main`, follow the manual procedure in [Upgrade](#upgrade) and keep the pre-upgrade database/binary pair. Older binaries refuse the migrated database.

**What the migration does.** Migration 0015 runs at the first start in one transaction:

- For every stored version of every service document, and the repository documents config sync recorded for services, it reads `remote.<remote>.url` (default `origin`) from `<repo>/.git/config` with `git config --file` (the `LCM_GIT` program, 10 s each) and writes it as `source.url`, removing `repo` and `remote`. Each checkout and remote is read once.
- The current version of a managed or retired service must use `<data dir>/work/<name>` as `source.repo`, where LCM keeps the checkout after the upgrade. Older versions and archives may name other paths; when such a path no longer exists, the version takes the URL of the resource's current version.
- It refuses, changes nothing and leaves the schema at 14, so the previous binary still starts, when the current `source.repo` is another path, a checkout or its remote cannot be read, the URL carries credentials, is relative or points into `<data dir>/work/`, or an old checkout is gone and no version of that resource names a readable one. `lcm serve` exits with every problem and its fix; the unit's restarts repeat the same refusal until the old binary is back.

**Pre-checks** (old binary running):

1. List the service versions with a source and the checkout each names:

   ```sh
   sudo -u lcm sqlite3 -readonly /var/lib/lcm/lcm.db "SELECT r.name, r.phase, r.version, v.version,
     json_extract(v.document, '$.spec.config.source.repo'), json_extract(v.document, '$.spec.config.source.remote')
     FROM resources r JOIN resource_versions v ON v.resource_id = r.id
     WHERE r.type = 'service' AND json_extract(v.document, '$.spec.config.source') IS NOT NULL ORDER BY r.name, v.version"
   ```

2. For each current version (`r.version` equals `v.version`) of a managed or retired service, the path must be `/var/lib/lcm/work/<name>`. Otherwise move the checkout there and save the service with that `source.repo`, or remove `source`, with the old binary.
3. For each distinct path and remote, `sudo -u lcm git config --file <repo>/.git/config --get remote.<remote>.url` must print a URL without credentials (for `lcm`: `https://github.com/ACaccel/LCM.git`). Paths of older versions that no longer exist are fine when the current version passes.
4. Config sources: `GET /api/v1/config/sources` lists them; note which own services with a source (`GET /api/v1/config/ownership`).

**After the upgrade.** `lcm export resource/<name>` shows `source.url`. Git documents that still use `source.repo` or `source.remote` fail to sync with an issue naming the field and the resource keeps its migrated configuration; replace the fields with the exported `source.url` and commit. The migration rewrote the recorded repository documents too, so a repository updated to the same content shows no drift. `lcm convert` does not convert a v1 `source` ([rules](policies.md#policy-as-code)).

### Upgrade to bounded retention (schema 16)

Storage schema 16 only adds indexes (timeline reads, audit filters and the retention jobs); no row changes. The schema changes, so self-install refuses it (`storage schema unknown or changed`): pause self-deploy before the commit reaches `main` and follow the manual procedure in [Upgrade](#upgrade). Building the indexes at the first start takes time proportional to `resource_events` and `audit_log`; older binaries refuse the migrated database.

**Behavior changes.** The first retention pass after the start deletes what the new limits no longer keep, in bounded batches:

- Document versions beyond the newest 50 per Resource, Policy and Module, except versions still in use. Reading, diffing or restoring a deleted version returns 404.
- Machine/router operations, security host operations and notification test episodes finished more than 90 days ago, including their receipts and completion evidence.
- Failed-login audit entries beyond the newest 10,000.
- Snapshot, point and event ceilings now apply per resource (200,000 rows each) instead of to all resources together.

Set the [retention settings](../README.md#configuration) before the start to keep more. The security host helper purges its own finished operations only after it is reinstalled ([host controls](#security-console-host-controls)).

### Upgrade checks for stricter host configuration

This release keeps the storage schema, so self-install applies it automatically. It refuses host configuration that earlier versions accepted; check the host before it reaches `main`, or pause self-deploy until you have:

1. **Connections file** (when `LCM_CONNECTIONS_FILE` is set): it must be owned by root, and every directory above it (for example `/etc` and `/etc/lcm`) must be owned by root and not writable by group or others. Check with `namei -l /etc/lcm/connections.yaml`; fix with `sudo chown root:lcm /etc/lcm/connections.yaml && sudo chmod 0640 /etc/lcm/connections.yaml`.
2. **`allowResources`**: an empty or missing list now allows no resource. Add the name of every router resource that uses each connection, for example `allowResources: [gateway]`; otherwise LCM refuses to save those resources and their observation and reboots stop.
3. **Secret files**: files in the secrets directory must be regular files, not symlinks, of mode 0600. List offenders with `sudo find /var/lib/lcm/secrets -mindepth 1 \( ! -type f -o -perm /077 \) -ls` and fix them with `chmod 600`. An unresolvable connection password stops startup; other secrets fail the node or check that uses them.
4. **Repository URLs**: a config source or `trigger.git` URL with userinfo other than the ssh user `git` (for example `https://token@host/...`) is refused. Policies that use one are refused at deploy and are not restored at startup; existing sources keep syncing but are shown without the userinfo. Replace them with a credential-free URL.

If the new binary cannot start, the self-install verification restores the previous one; fix the configuration and deploy again.

### Router connections

To manage a [router](resources.md#routers), store its web password as a secret (for example `/var/lib/lcm/secrets/home-router-password`, owned by `lcm`, mode 0600), write the connections file (for example `/etc/lcm/connections.yaml`, `root:lcm 0640`, owned by root in root-owned directories, listing the router in `allowResources`), set `LCM_CONNECTIONS_FILE` in `/etc/lcm/lcm.env` and restart LCM. A wrong file or unresolvable secret stops startup; check `journalctl -u lcm`. Then apply the router document. Real-device acceptance of the router adapter is still pending.

## Resource use

The target is under 50 MB resident. `LCM_MEMORY_LIMIT` (default `40MiB`, `off` disables) sets a soft limit for the Go runtime so that garbage is collected early instead of letting the heap grow; it is not a cap. Retention settings bound the database: raw check results, rollups, timeline, finished runs, deployment records, idempotency records, document versions (`LCM_VERSION_RETENTION_KEEP`), finished operations (`LCM_OPERATION_RETENTION`) and failed-login audit entries (`LCM_FAILED_LOGIN_RETENTION_KEEP`) are pruned by the hourly retention job (`LCM_PRUNE_INTERVAL`) in bounded batches. The rest of the audit log is kept forever. Machine and router snapshots, minute points and events have age limits and per-resource row ceilings; [retention and evidence exceptions](machines.md#events-gaps-and-history) are documented with their API semantics. Pruning frees pages for reuse; it does not shrink `lcm.db`. The privileged helper ledger has its own permanent capacity limits above. To investigate memory or CPU, set `LCM_PPROF_LISTEN=127.0.0.1:6060`, which serves `net/http/pprof` on its own loopback listener; it is off by default and must stay off in normal operation.

## Remote access

LCM serves plain HTTP. For access beyond localhost run a TLS-terminating reverse proxy (nginx, Caddy) in front, keep LCM on `127.0.0.1`, and set `LCM_COOKIE_SECURE=true`. Login rate limiting counts failures per client address; behind a proxy every client has the proxy's address, so one client can lock out all of them. When the proxy sets `X-Forwarded-For` (replacing or appending to a client-supplied value), set `LCM_TRUSTED_PROXIES` to its address, for example `127.0.0.1/32,::1/128` for a local proxy. The event stream (`/api/v1/events`) needs response buffering turned off in the proxy.


## Security console host controls

Use the authenticated Security navigation for incident handling, measured traffic,
notification versions and host checks. Handling and settings changes require the
displayed version and a reason. A notification test uses the configured relay route;
wait for its firing and recovery revisions on the same message before accepting it.

Install the restricted security helper only through a reviewed release; follow its
[deployment and recovery contract](../deploy/security-host/README.md). Verify actual
LCM-context `GET /api/v1/security/host` availability after installation, rather than
relying on a root-only status probe. Preserve existing timer jitter, required audit
coverage, Nginx dry-run and locally reviewed AIDE baseline. The web baseline action
records a review request only. No optional profiles are enabled by default.

A helper installed before schema 16 refuses every preview once its operation journal
holds 2,000 entries, permanently. The current helper first purges old finished entries
([journal capacity](../deploy/security-host/README.md#recovery-and-local-approval));
reinstall the reviewed `helper.py` with its matching `install.py` and SHA-256 to get the
fix. LCM keeps its own record of finished host operations for `LCM_OPERATION_RETENTION`.

Schema 11→13 needs an explicit upgrade with maintenance, paused automation and a
matching DB/binary/health-artifact snapshot. Preserve notification environment and
credentials. Validate fresh Machine/security observations and host availability before
exiting maintenance. After exit might have succeeded, reconcile rather than rewinding
later database history. Keep LCM self-deploy paused until its source includes both the
existing security backend and the console release. See [security semantics](security.md#security-console-and-operator-controls).
