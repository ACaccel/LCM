# Operations

How to run LCM on a host. Build and general configuration reference: [README](../README.md). Resource model, connections and routers: [resources.md](resources.md). Local machine configuration and control: [machines.md](machines.md). Design and security notes: [architecture.md](architecture.md).

## Install

`lcm host apply` installs LCM on a systemd host whose console is published with `tailscale serve`, and lets LCM deploy itself from `main` afterwards. It installs the host files embedded in the binary (the reviewed files in [`deploy/`](../deploy/)) as described by the root-owned [host configuration](#host-configuration), and can be rerun at any time. It runs LCM as the unprivileged system account `lcm`; the standard installation grants only [`self-install`](#self-deploy) as a root command. Machine power, the security host helper and the deploy helper are optional components, off by default ([machine power](#local-machine-power-helper), [security host](#security-console-host-controls), [deploy helper](#deploy-helper)).

| Path | Owner, mode | Contents |
|---|---|---|
| `/var/lib/lcm/` | lcm 0750 | Data directory (`lcm.db`, `secrets/`, `sources/`), `work/` (build checkouts; `work/lcm` is LCM's own, used for self-deploy), `cache/` (Go and npm caches of its builds) |
| `/etc/lcm/host.json` | root 0644 | [Host configuration](#host-configuration) of `lcm host apply` |
| `/etc/lcm/lcm.env` | root:lcm 0640 | Server settings ([`deploy/lcm.env`](../deploy/lcm.env)); written only when missing |
| `/opt/lcm/toolchain/{go,node}` | root | Copies of the Go and Node.js toolchains that self-deploy builds with |
| `/usr/local/bin/lcm`, `lcm.prev`, `lcm.failed` | root 0755 | Installed binary, the one it replaced, and the last one a verification rejected |
| `/usr/local/libexec/lcm/self-install` | root 0755 | [`deploy/self-install`](../deploy/self-install) |
| `/etc/sudoers.d/lcm` | root 0440 | [`deploy/sudoers`](../deploy/sudoers): lets `lcm` run exactly `self-install /var/lib/lcm/work/lcm/bin/lcm` |
| `/etc/systemd/system/lcm.service` | root 0644 | [`deploy/lcm.service`](../deploy/lcm.service) |
| `https://<host>:9443` | tailscale serve | Tailnet-only HTTPS proxy to `http://127.0.0.1:7800` |

**Prerequisites:** systemd, sudo, `git`, `curl`, `make`, `jq` (for `seed.sh`), Tailscale signed in with HTTPS certificates; Go and Node.js 24 toolchains, by default in the operator's `~/.local/{go,node}`. The repository is public, so neither the clone nor the CI check needs credentials.

### Host configuration

`/etc/lcm/host.json` says what `lcm host apply` installs. It must be a regular file owned by root and not writable by group or others, in root-owned directories that are not symbolic links and not writable by group or others. Decoding is strict: unknown, duplicate or differently cased keys and trailing data are refused. It holds no secrets and no host name; the tailnet name comes from `tailscale status`. `lcm host init` writes a first one (it never replaces an existing file).

```json
{
  "operator": "alice",
  "repository": "https://github.com/ACaccel/LCM.git",
  "toolchains": {"go": "/home/alice/.local/go", "node": "/home/alice/.local/node"},
  "tailscale": {"port": 9443},
  "components": {"machinePower": false, "securityHost": null}
}
```

| Key | `host init` flag (default) | Meaning |
|---|---|---|
| `operator` | `--operator` (`$SUDO_USER`) | Non-root administrator account: owns the toolchains to copy and gets the first API token |
| `repository` | `--repository` (`https://github.com/ACaccel/LCM.git`) | https URL without credentials that self-deploy clones into `work/lcm` |
| `toolchains.go`, `toolchains.node` | `--go-root`, `--node-root` (`~OPERATOR/.local/{go,node}`) | Absolute toolchain directories; they and their ancestors must not be symbolic links, must be owned by root or the operator and writable by no one else; copied to `/opt/lcm/toolchain` |
| `tailscale.port` | `--ts-port` (`9443`) | HTTPS port of `tailscale serve` |
| `components.machinePower` | `--machine-power` (`false`) | Install the [machine power helper](#local-machine-power-helper) |
| `components.securityHost` | `--security-host-machine-id` (`null`) | `{"machineId": "<immutable Machine ID>"}` installs the [security host helper](#security-console-host-controls) |
| `components.deploy` | (`false`) | Install the [deploy helper](#deploy-helper) and the build account |
| `runtimes` | (none) | Named [runtimes](#runtimes) that managed services may use: `{"node24": "/home/alice/.local/node"}`; names `^[a-z][a-z0-9]{0,15}$`, directories checked like `toolchains` and copied to `/opt/lcm/runtimes/<name>` |
| `backup.dir` | (none) | Where service backups will be kept (typed backups, a later version); absolute, not under `/var/lib/lcm` |
| `services` | `lcm host allow` | The [deploy allowlist](#service-grants): `{"web": {"port": 3200, "runtimes": ["node24"], "inputs": {"assets": "/srv/inputs/web/assets"}}}`; `route` and `extraPaths` are reserved for the ingress component and refused |

### Procedure

1. From a reviewed checkout, build the first binary as the operator: `make build`.
2. Install, as root, with the build of that checkout:

   ```sh
   sudo deploy/bootstrap.sh            # first run: writes host.json from its flags (--help), then runs host apply
   ```

   or step by step:

   ```sh
   sudo bin/lcm host init --operator alice   # writes /etc/lcm/host.json; review it
   sudo bin/lcm host apply --check           # what would change
   sudo bin/lcm host apply
   ```

   `bootstrap.sh` passes `--replace-binary` and `--check` on to `host apply`; its first-run flags fail once `host.json` exists, and `--ts-host NAME` only refuses to run when the node's tailnet name differs. `host apply` validates everything before it changes anything (tools, operator account, toolchains and their links, tailscale state and port, the staged binary running `lcm version` as `nobody`, `visudo -cf` on the staged sudoers rules, an existing `work/lcm` tracking `repository`). It then creates the `lcm` account and directories, writes `lcm.env` if missing, copies the toolchains (read without leaving them, refusing links that leave them and entries not owned by root or the operator, and checked as `lcm` before they replace the previous copy), installs the binary (only when none is installed; `--replace-binary` forces it), `self-install`, the sudoers rule (checked with `visudo -c` and removed again when that fails) and the unit (`systemd-analyze verify`, restoring the previous unit when that fails), clones `work/lcm` over HTTPS as `lcm`, enables and starts the service (restarting it for a changed unit) and waits up to 60 s for health, then installs the enabled optional components. When it created the database and runs in a terminal, it asks for the web console's admin password (min 8 characters) with echo off and passes it to `lcm admin set-password` through a pipe; no process of another account gets the terminal. When `~/.config/lcm/token` of the operator is missing, it creates an admin API token and has the operator's own process write it there (0600). Last, it adds the `tailscale serve` handler, refusing a port that something else uses.
3. Seed, as the operator:

   ```sh
   deploy/seed.sh
   ```

   It applies the optional monitoring-only services of a local inventory (below), applies the self-deploy service [`deploy/lcm-self.service.yaml`](../deploy/lcm-self.service.yaml), records a first deployment of the current `main` if there is none (a rollback needs one), and applies the policy [`deploy/lcm-auto-deploy.yaml`](../deploy/lcm-auto-deploy.yaml), which sends no notifications unless you [configure them](#self-deploy). It can be rerun; unchanged documents stay unchanged.

Later admin password changes: `sudo -u lcm /usr/local/bin/lcm admin set-password --data-dir /var/lib/lcm`. Run the `lcm` CLI's database commands as `lcm` (`sudo -u lcm`), never as root: the binary is replaced by self-deploy.

**Monitoring-only services (optional).** `seed.sh` reads `${LCM_SEED_INVENTORY:-$HOME/.config/lcm/seed-inventory}`; without that file it registers none. Copy [`deploy/seed-inventory.example`](../deploy/seed-inventory.example) and edit it: one entry per line, `service NAME UNIT_REGEX [HTTP_URL]` for systemd units or `container NAME CONTAINER [HTTP_URL]` for a docker container, `#` comments. `seed.sh` rejects an invalid name (`^[a-z0-9-]{1,63}$`, not `lcm`), a duplicate, an unknown kind, a bad regex, or a URL that is not `http(s)://` or has credentials, before it changes anything. Each entry becomes one service resource with adapter `external` (LCM only monitors it, so its start, stop and restart are disabled; see [runtimes](services.md#runtime)) and no policies. Long-running units matching the regex get critical `systemd` checks; oneshot units are represented by their timer as non-critical checks. An `http` check is added only when the URL answers at seed time. The docker check is added only when `lcm` is in the `docker` group, which bootstrap does not do because that group is root-equivalent.

### Rerunning, checking and updates

Rerun `sudo bin/lcm host apply` from a reviewed build for upgrades and repairs; it changes only what differs from the configuration and the embedded files.

- Each step prints one line per result: `unchanged`, `would change: <what>` (`--check`), `changed: <what>`, or `needs manual action: <what>` for a difference it does not fix itself. Exit status: 0 converged; 3 with `--check` while changes or manual actions are pending; 1 on an error or a remaining manual action; 2 for usage errors. `--check` changes nothing; it stages the binary in a temporary directory to validate it.
- Drift it fixes: changed, missing or replaced root files (a file owned by someone else is replaced, never taken over), their modes, directory owners and modes, toolchain copies that differ from their source, a missing checkout, token file or `tailscale serve` handler, a disabled, stopped or not reloaded unit.
- Older builds: when the installed `/usr/local/bin/lcm` reports a later commit time (`lcm version`) than the build running `apply`, `apply` and `--check` print a warning first and mark each file they would replace; they do not stop.
- What it keeps: an existing `lcm.env` (reported when it differs from the embedded one; one owned by someone other than root needs manual action), the installed `/usr/local/bin/lcm` (self-deploy keeps it current), and components switched off in `host.json`, which are reported as `installed but disabled` and never removed.
- Root runs only a build of a reviewed checkout, because whoever can push to `main` controls the self-deployed binary. `bootstrap.sh` refuses a `--binary` that resolves to `/usr/local/bin/lcm` or its `.prev`/`.failed` copies, lies under `/var/lib/lcm` or is owned by `lcm`, before running it. `host init` and `host apply` refuse to run as root from those paths too; that check catches mistakes, but it runs inside the binary it judges, so it cannot stop an untrusted one.
- `lcm host status` runs as any user and only reads. For each component (`core`, `machine-power`, `security-host`, `deploy`) and its root files it compares the installed SHA-256 with this build's: `up to date`, `outdated` (content, owner or mode), `missing` (partly installed), `not installed`, or `unknown` (not readable as this user, as the sudoers rules are). The pinned machine and deploy helpers have no embedded version: status checks only their owner and mode and shows their digest, and `host apply` replaces them with the reviewed build it runs from.
- The console shows the same comparison on the machine's Overview tab (`GET /api/v1/host/components`, read as the `lcm` account and cached for 30 s). It warns only about `outdated` or `missing` files and names the update command; a component that is not installed is silent, and so are files the account cannot read (the sudoers rules and the security host helper are root-only by design; `sudo bin/lcm host status` compares them).
- Helper updates: LCM never changes root files itself. When a self-deployed commit changes a host file, `lcm host status` shows `outdated`; review the change, then run `make build && sudo bin/lcm host apply` in the updated reviewed checkout. An installed security host helper is the exception: it needs its [reviewed upgrade](#security-console-host-controls).

### Self-deploy

The `lcm` service builds and installs LCM like any other service; the auto-deploy policy runs it for every new commit on `main` whose `ci.yml` run passed:

1. `trigger.git` sees the commit; `ci.status` waits for CI (unauthenticated GitHub API, 60 requests per hour per IP).
2. `lifecycle.deploy` points `origin` of `work/lcm` at the service's `source.url`, fetches and detaches the checkout at the commit, runs `make build` (console and binary) with the copied toolchains, then the install step `sudo -n /usr/local/libexec/lcm/self-install /var/lib/lcm/work/lcm/bin/lcm`.
3. `self-install` reads the binary as `lcm` (so a swapped link cannot expose root-only files), checks `lcm version` and requires installed/candidate `lcm storage-schema` values to match. A changed schema or an older binary without that command refuses automatic installation; use the [manual upgrade procedure](#upgrade). Otherwise it keeps the current binary as `lcm.prev`, moves the new one into place and schedules `self-install --verify` in a transient unit `lcm-self-update-<time>` 3 seconds later. It exits at once, so the deployment is recorded as finished before LCM restarts. For the same reason the runtime has only a status command: start, stop and restart are not offered and the deployment skips its restart step. The shared write gate blocks this self-deploy path during machine maintenance; a pending self-install verification also blocks entering Machine maintenance.
4. The verification restarts `lcm.service` and waits up to 60 s for `/api/v1/health` to report the new version. If it does not, it restores `lcm.prev` (keeping the rejected binary as `lcm.failed`), restarts again, logs at error priority and leaves the transient unit failed.
5. The policy's `wait.until` was suspended across the restart and resumes in the new process. The service's `version` check passes only when the running server reports the version of the binary built in the checkout, so after a verification rollback the service stays unhealthy, the wait times out after 3 minutes and `lifecycle.rollback` rebuilds and reinstalls the previous successful commit.

**Trust boundary.** Code on `main` that passes CI is built and run by LCM as `lcm`, the same model as the other auto-deployed projects on this host, and installed as the root-owned `/usr/local/bin/lcm`. Anyone who can push to `main`, or write the `lcm` service Resource document, therefore controls the `lcm` account and the content of that binary: never run it as root (self-install runs it only as `lcm`; run `lcm host` as root only from a build of a reviewed checkout). The sudoers rule accepts one fixed argv; rules for other units are separate, reviewed changes.

**Inspect and recover.**

```sh
journalctl -u lcm -f                                   # the server
journalctl -u 'lcm-self-update-*' -n 50 --no-pager     # restarts and verifications
systemctl list-units --failed 'lcm-self-update-*'      # rejected updates; clear with systemctl reset-failed
```

- A manual binary-only rollback is appropriate only for a verified equal-schema update. For schema changes or unknown compatibility, follow [Upgrade](#upgrade) and restore the matching database and binary. After a safe equal-schema rollback, deploy the matching commit from the console or `lcm resource action lcm deploy --version <sha>` so that the catalog agrees.
- To pause self-deploy, pause the policy `lcm-auto-deploy` in the console.
- **Optional: deployment notifications.** The shipped policy sends none (`notifyUrl` and `notifyTokenSecret` are empty, and the notify nodes skip). To get one message per deployment ([template notifications](policies.md#templates)):
  - konata relay (for example BotFleet's notification relay): define a route for LCM in the relay's configuration, store the relay's bearer key as an LCM secret from your own secret store, then seed with the route and the secret's name. `seed.sh` applies a rendered copy of the policy and leaves the tracked file unchanged; set both variables on every rerun, because a rerun without them applies the no-notification policy again.

    ```sh
    sudo -u lcm sh -c 'umask 077 && cat > /var/lib/lcm/secrets/<secret-name>'    # paste the key, then Ctrl-D
    LCM_SEED_NOTIFY_URL=http://127.0.0.1:<port>/notifications/<route> \
      LCM_SEED_NOTIFY_TOKEN_SECRET=<secret-name> deploy/seed.sh
    ```

  - Discord: store a webhook URL as a secret and set `notifyTarget: discord` and `notifySecret: <secret-name>` in the policy's `spec.params`.

  A configured destination whose secret is missing fails its notification node, and with it the run.
- A manual `lcm resource action lcm deploy` has no policy to roll back: if the verification restores `lcm.prev`, the catalog still records the new commit, and the failing `version` check shows the mismatch until you deploy a good commit.
- Verification restores only the binary; it does not restore the database. The schema-equality guard is therefore required, and the installed `self-install` script must itself include that guard. Existing deployments with an older script need an operator-reviewed script update before relying on this protection. It cannot make an older unguarded installation safe retroactively.

### Local Machine power helper

Registering a [Machine](machines.md) enables observation without granting power permission. `LCM_MACHINE_POWER` / `--machine-power` defaults false. Neither the standard install (`components.machinePower` defaults false) nor registration establishes helper readiness, and installed files are not evidence that the host was validated.

Use the [helper source contract](../internal/machine/power/README.md#deployment-contract-for-parent-integration) as the authority for fixed paths, file ownership/modes, protocol, ledger initialization and OS checks. The deployment must provide:

- A root-owned, version-pinned helper binary and a root-owned wrapper at `/usr/local/libexec/lcm-machine-power`. With `components.machinePower: true`, `lcm host apply` installs [`deploy/machine-power`](../deploy/machine-power) there and the pinned binary `/usr/local/libexec/lcm-machine-helper`, a copy of the reviewed executable that runs `apply`; it replaces the pin when that executable differs, which requires renewed validation. The wrapper accepts no arguments, clears the environment and invokes only that binary's `machine-helper` command; the helper bounds stdin/processing to 30 seconds and the client invocation to 35 seconds. Ancestors must be trusted; never use the service-account-writable self-deploy executable as the privileged helper.
- A reviewed [`deploy/machine-power-sudoers`](../deploy/machine-power-sudoers) rule limited to the exact no-argument wrapper: `lcm ALL=(root) NOPASSWD: /usr/local/libexec/lcm-machine-power ""`. The final empty argument string is required. Do not grant arbitrary `lcm`, shell or `systemctl` execution. `lcm host apply` installs it as `/etc/sudoers.d/lcm-machine-power` after the wrapper and the pin, validated with `visudo` (staged, then the whole configuration; it is removed again when that fails).
- Root-only `/etc/lcm/machine-power.json`, with its `controllerID` equal to the registered Machine's immutable ID, not its name. Pin the locally validated systemd major. Keep `recoveryReady` false until startup recovery has been validated to restore unresolved operations and maintenance gates before every write path and automation. Changing registration or helper version requires renewed validation; copying an enabled example is not a readiness check.
- The independent root-owned `/var/lib/lcm-machine-power` receipt ledger and lock, durably provisioned as specified in the contract. Retain them across reboot, controller backup/restore and binary rollback. Never delete/reset the ledger, reuse operation IDs or replace a live lock. Missing or stale restored receipts require control disabled and external reconciliation.

Provision the last two by hand, once: `lcm host apply` reports them as not provisioned but never creates them, because a ledger initialized again after a loss would hide receipts.

The ledger permanently retains IDs, refuses new receipts at 10,000 entries and fails closed if a write exceeds 16 MiB. It has no automatic pruning; arrange a reviewed capacity/migration procedure before those limits. Disabling new power submissions must still permit receipt queries for recovery; `recoveryReady=false` blocks control while preserving queries against valid existing configuration/storage. Controller queries also remain available with `LCM_MACHINE_POWER=false`.

Before enabling control, independently validate Linux/systemd/logind compatibility, enabled `lcm.service` autostart, startup gates and manual recovery. The helper refuses login sessions and inhibitors; a power request enters maintenance itself and is confirmed with a reason and the machine name. Journal permissions are a separate deployment choice: basic snapshots remain available when journal access is restricted. Do not run LCM as root or automatically add broad log-reading privileges. Deployment and hardware acceptance for the initial workstation is recorded in the [validation record](machine-validation.md); each new installation still requires these checks. Protocol/fake-command tests do not establish those host deployment properties.

### Deploy helper

With `components.deploy: true`, `lcm host apply` installs what managed services are built, installed and run with. LCM never gets a general root command: it can only send requests to one restricted helper, and the helper does only what the [`services` allowlist](#service-grants) of `host.json` grants.

| Path | Owner, mode | Contents |
|---|---|---|
| `/usr/local/libexec/lcm-deploy` | root 0755 | Wrapper [`deploy/lcm-deploy`](../deploy/lcm-deploy): no arguments, a cleared environment, executes only the pinned helper's `deploy-helper` command |
| `/usr/local/libexec/lcm-deploy-helper` | root 0755 | Pinned helper: a copy of the reviewed build that ran `host apply` (status `pinned`), never the self-deployed binary |
| `/etc/sudoers.d/lcm-deploy` | root 0440 | [`deploy/lcm-deploy-sudoers`](../deploy/lcm-deploy-sudoers): `lcm ALL=(root) NOPASSWD: /usr/local/libexec/lcm-deploy ""`; installed last and checked with `visudo` (staged, then the whole configuration; the previous rule is restored, or the new one removed, when that fails) |
| `/var/lib/lcm-deploy/` | root 0700 | The helper's operation ledger and locks; `apply` creates the directory, the helper refuses to run without it |
| `/var/lib/lcm-build/`, `/var/cache/lcm-build/` | root 0755 | Build and cache roots, with one directory per [service grant](#service-grants) |
| `/opt/lcm/runtimes/<name>/` | root | [Runtime](#runtimes) copies |

The protocol (operations, fields, replies, error codes, ledger) is documented in [`internal/deployhelper/protocol.go`](../internal/deployhelper/protocol.go). LCM calls `sudo -n -- /usr/local/libexec/lcm-deploy` with one JSON request on stdin; the helper reads `/etc/lcm/host.json` itself, with the same trust checks as `host apply`, and refuses every service, runtime or input it does not grant.

**Trust boundary.**

- The helper is a pinned copy of a reviewed build; updating it takes `sudo bin/lcm host apply` from a reviewed checkout. Whoever can push to LCM's `main` controls the `lcm` account, not the helper.
- Documents and requests cannot widen the allowlist: ports, runtimes, build inputs and data directories come only from `host.json`, which only root writes (`lcm host allow`).
- Builds run third-party code as the service's own build account `lcmb-<service>`, which owns nothing but that service's build trees and cache: no LCM data, tokens or secrets, no env file or data of any service, and nothing of another service's builds. Whoever can push to a service's deployed branch controls that service's accounts, never root, the `lcm` account or another service.
- Root copies other accounts' trees (the `lcm`-owned checkout, the build account's artifact) only through descriptors confined to the tree, accepting entries of their owner only, bounded in entries and size, and refusing links that leave the copy. A release is root-owned, so a service cannot change its own code.
- The helper uses only accounts that host setup created (their comment is `LCM service <name>` or `LCM build <name>`) and never replaces a unit it did not write: `/etc/systemd/system/<service>.service` must be absent or start with its marker line; a unit of that name anywhere else in the systemd search path (including generated, transient and runtime-control units) and any `<service>.service.d` drop-in directory are refused.
- Every changing request carries an operation ID that LCM chooses. The helper records the operation before and after it runs it, so after a lost reply or timeout LCM queries the ledger instead of guessing, and a repeated ID never runs twice. Records of finished operations are removed after 90 days, and the oldest finished ones beyond 10,000 records. A build holds its service's build lock and the other operations the service lock, so a long build never blocks stop or restart; a second operation needing a held lock is refused as `busy`. The client's timeouts exceed the helper's own worst case for each operation.

**Build sandbox.** `build` copies the checkout without its `.git` directory (builds get the commit as `LCM_COMMIT` and must not rely on git metadata; the checkout's `HEAD` must be the requested commit, read from `.git` without running git as root). It runs `sh build.sh` (the command, written to a root-owned file so that systemd expands nothing) with `systemd-run --wait --pipe --collect` as the build account `lcmb-<service>` in the build's `src/`, with:

- `ProtectSystem=strict` (writable only: `src/`, `home/`, the service's cache), `ProtectHome=yes`, `PrivateTmp=yes`, `PrivateDevices=yes`, `NoNewPrivileges=yes`, no capabilities, `RestrictSUIDSGID=yes`, the kernel, clock and hostname protections;
- `/var/lib/lcm-build` and `/var/cache/lcm-build` replaced by empty read-only file systems with only this build's directories bound back, `ProtectProc=invisible` and `ProcSubset=pid`;
- `InaccessiblePaths=` LCM's data, `/etc/lcm`, the helper ledgers and `backup.dir`;
- `IPAddressDeny=localhost`: the network for dependencies, but not LCM's API or the local services;
- `RuntimeMaxSec=` the request's timeout, `MemoryMax=4G`, `TasksMax=4096`.

The environment is fixed: `PATH` (the requested runtimes' `bin` first), `HOME` (the build's `home/`), `LANG`, `LCM_COMMIT`, `LCM_CACHE_DIR`, `XDG_CACHE_HOME`, `npm_config_cache`, `GOCACHE`, `GOMODCACHE` and, with inputs, `LCM_INPUTS_DIR=/run/lcm-build/inputs`: each input is bound read-only at `/run/lcm-build/inputs/<name>` (on a read-only tmpfs); the build account needs read permission on its files.

**Disk use.** A build does not start with less than 2 GiB or 100,000 inodes free on `/var/lib/lcm-build`, empties a cache that grew beyond 4 GiB first, and is stopped when its source, home and cache together exceed 16 GiB or 2,000,000 entries, or when less than 512 MiB or 20,000 inodes remain free (measured every 10 s). The last 3 builds per service are kept. `install` removes the oldest releases beyond 20 that are neither current nor previous.

**Service units.** `activate` writes `/etc/systemd/system/<service>.service` from the built-in template: `User=`/`Group=` the service account, `WorkingDirectory=/opt/<service>/current`, `ExecStart=` the runtime's `node` or `python3` with the release file, or the release's own executable, plus the start arguments, `EnvironmentFile=/etc/<service>/env`, `Environment=PORT=<port> HOST=127.0.0.1` (and `PATH` with the runtime), `Restart=on-failure`, `ReadWritePaths=/var/lib/<service>`, the build sandbox's hardening with `UMask=0027` and `ProtectProc=invisible`, also hiding the build trees, and `SocketBindAllow=tcp:<port>` with `SocketBindDeny=any`, `RestrictAddressFamilies=AF_INET AF_INET6 AF_UNIX` and `PrivateIPC=yes`. The service may bind only its port, but systemd cannot also restrict the address, so `HOST=127.0.0.1` is what it is told rather than enforced; outbound connections stay open. The env file must not set `PORT` or `HOST` in any form (it is read the way systemd reads it, including `export`, quotes and continuations), which come from `host.json`. An optional migration runs first, as the service account in the new release with the same sandbox and env file; when it fails nothing is switched. Then the `current` and `previous` links switch atomically, a changed unit is checked with `systemd-analyze verify` and systemd reloads when the unit changed or reports `NeedDaemonReload` (the previous unit and links are put back when either fails), the unit is enabled and restarted, and releases beyond `keepReleases` (default 3) are removed, never the current or previous one. Health checks and switching back on failure are LCM's.

#### Runtimes

`runtimes` names the execution environments that services may use, so that documents refer to `node24` rather than to a path. `host apply` copies each one to `/opt/lcm/runtimes/<name>` like the toolchains (read without leaving the source, entries of root or the operator only, links must stay inside), and checks the copy as `nobody` by running `bin/node --version`, `bin/python3 --version` or `bin/go version` when present; a copy without one of them is not run. The copy is root-owned, so neither LCM nor a service can change it.

#### Service grants

```sh
sudo bin/lcm host allow web --port 3200 --runtime node24 --input assets=/srv/inputs/web/assets [--check]
```

`host allow` adds or updates `services.<name>` in `host.json`: it validates the whole configuration, rewrites the file atomically keeping the order and values of everything else (refusing when the file changed after it was read), and creates the service's account and directories with the same steps as `apply`. It does not copy runtimes or install the deploy helper; it names `sudo bin/lcm host apply` when those are missing. `--check` changes nothing and exits 3 while changes are pending.

- Names match `^[a-z][a-z0-9-]{0,26}$` (room for the build account's `lcmb-` prefix in a 32-character user name) and do not start with `lcm`. Ports are 1024-65535, unique, and neither 7800 nor `tailscale.port`; they are not checked against other listeners. Runtimes must be in `runtimes`. Inputs are absolute directories outside LCM's data, owned by root or the operator and writable by no one else, like the toolchains.
- For each service, `apply` creates the system accounts `<name>` (comment `LCM service <name>`, home `/var/lib/<name>`) and `lcmb-<name>` (comment `LCM build <name>`, home `/var/lib/lcm-build/<name>`), both without a login shell; `/opt/<name>` and `/opt/<name>/releases` (root 0755), `/var/lib/<name>` (`<name>:<name>` 0750), `/etc/<name>` (root:`<name>` 0750), an empty `/etc/<name>/env` (root:`<name>` 0640, never replaced; put the service's secrets there), `/var/lib/lcm-build/<name>` (root:`lcmb-<name>` 0750) and `/var/cache/lcm-build/<name>` (`lcmb-<name>` 0750).
- An existing account must be one host setup created (its comment above, a system account with that home and no login shell), and existing directories must already have the owners above; otherwise, and when another unit or a drop-in of that name exists, `apply` reports a manual action and `allow` refuses. Nothing is taken over.
- A service removed from `services` is reported by `apply`; its account, directories, releases and unit are kept.

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
[deployment and recovery contract](../deploy/security-host/README.md). With
`components.securityHost` set to `{"machineId": "<immutable Machine ID>"}`, `lcm host apply`
runs the embedded `install.py` with `python3 -I` from a root-only temporary directory,
with that Machine ID and the SHA-256 of the embedded `helper.py`. `install.py` refuses an
existing installation, so `apply` reports an installed helper that differs, a policy for
another Machine or an incomplete installation as `needs manual action` and changes nothing;
upgrade it as the contract describes. Verify actual
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
