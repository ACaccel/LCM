# Operations

How to run LCM on a host. Build and general configuration reference: [README](../README.md). Resource model, connections and routers: [resources.md](resources.md). Local machine configuration and control: [machines.md](machines.md). Design and security notes: [architecture.md](architecture.md).

## Install

`lcm host apply` installs LCM on a systemd host whose console is published with `tailscale serve`, and lets LCM deploy itself from `main` afterwards. It installs the host files embedded in the binary (the reviewed files in [`deploy/`](../deploy/)) as described by the root-owned [host configuration](#host-configuration), and can be rerun at any time. It runs LCM as the unprivileged system account `lcm`; the standard installation grants only [`self-install`](#self-deploy) as a root command. Machine power, the security host helper, the deploy helper and the nginx ingress are optional components, off by default ([machine power](#local-machine-power-helper), [security host](#security-console-host-controls), [deploy helper](#deploy-helper), [ingress](#ingress)).

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
| `backup.dir` | (none) | Where the [typed backups](#backups) of managed services are kept; a directory of its own: absolute, not a system directory (`/`, `/var`, `/var/lib`, `/opt`, `/home`, ...) nor inside `/etc`, `/usr`, `/tmp` and the like, not `/var/backups/lcm` (where `host upgrade` keeps its sets; use for example `/var/backups/lcm-services`), not `/var/lib/lcm*`, `/var/cache/lcm-build` or `/opt/lcm`, and not overlapping a service's `/opt/<name>`, `/var/lib/<name>` or `/etc/<name>`, a runtime or an input. `apply` creates it as root 0700; an existing directory must already be root-owned 0700, as `apply` never changes the mode of one that exists. Without it the backup and restore operations are refused |
| `components.ingress` | (`null`) | The [ingress](#ingress) site: domain, listen addresses, ACME webroot, certbot settings, HSTS, domain-level files, the hand-made site to adopt |
| `services` | `lcm host allow` | The [deploy allowlist](#service-grants): `{"web": {"port": 3200, "runtimes": ["node24"], "inputs": {"assets": "/srv/inputs/web/assets"}, "route": {"prefix": "/web/"}}}`; `route` and `external` are described under [routes](#routes); `extraPaths` is reserved for a later version and refused |

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

A build with another storage schema is installed by [`lcm host upgrade`](#lcm-host-upgrade) instead, which backs up, checks the migration and rolls back on failure.

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
2. `lifecycle.deploy` points `origin` of `work/lcm` at the service's `source.url`, fetches and detaches the checkout at the commit, runs `make build` (console and binary) with the copied toolchains, then the install step, a short script that ends with `sudo -n /usr/local/libexec/lcm/self-install /var/lib/lcm/work/lcm/bin/lcm`. Before that command it compares `lcm storage-schema` of the installed binary (the running server's) with the candidate's. When they differ, or the candidate cannot report one, it stops with exit status 75 and the line `storage schema 16 -> 17 needs the operator: make build && sudo bin/lcm host upgrade`. A build or install script exiting 75 is recorded as a failed deployment with the code `deploy.needsUpgrade`, changes nothing and leaves the service in the state it had (running). The auto-deploy policy recognises the code, announces it (when [notifications](#self-deploy) are on) and does not roll back: a rollback would only rebuild and reinstall the version that already runs. Run the command on the host, as described in [Upgrade](#upgrade); the next commit with an unchanged schema deploys normally.
3. `self-install` reads the binary as `lcm` (so a swapped link cannot expose root-only files), checks `lcm version` and requires installed/candidate `lcm storage-schema` values to match. A changed schema or an older binary without that command refuses automatic installation (the step 2 check normally stops it first; this one is the root-side guard and stays unchanged); run [`lcm host upgrade`](#lcm-host-upgrade) (or the [manual procedure](#manual-procedure)). Otherwise it keeps the current binary as `lcm.prev`, moves the new one into place and schedules `self-install --verify` in a transient unit `lcm-self-update-<time>` 3 seconds later. It exits at once, so the deployment is recorded as finished before LCM restarts. For the same reason the runtime has only a status command: start, stop and restart are not offered and the deployment skips its restart step. The shared write gate blocks this self-deploy path during machine maintenance; a pending self-install verification also blocks entering Machine maintenance.
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
- A schema-changing commit therefore ends as a failed `deploy.needsUpgrade` deployment while the old version keeps running. After `lcm host upgrade` the server [records the upgrade](#lcm-host-upgrade) itself, so the `version` check turns up without redeploying. The seeded service and policy documents carry this behaviour: rerun `deploy/seed.sh` (as the operator) on a host seeded earlier to apply the new `lcm` install script and `lcm-auto-deploy` policy; until then a schema change fails at `self-install` and rolls back as before.
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
| `<backup.dir>/` | root 0700 | [Backup sets](#backups) of managed services, one directory per service; `apply` creates it when `backup.dir` is set |

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
- `/var/lib/lcm-build` and `/var/cache/lcm-build` replaced by empty read-only file systems with only this build's directories bound back, and `ProtectProc=invisible` (other processes are invisible; `/proc/cpuinfo`, `meminfo` and `stat` stay readable);
- `InaccessiblePaths=` LCM's data, `/etc/lcm`, the helper ledgers and `backup.dir`;
- `IPAddressDeny=localhost`: the network for dependencies, with the loopback addresses (`127.0.0.0/8`, `::1`) blocked, where cgroup v2 and its BPF firewall work (see [Sandbox requirements](#sandbox-requirements)). This is not isolation from the host: other addresses of the machine (LAN, tailnet), Unix sockets (including abstract ones) and address families are not restricted, so a service that listens on a non-loopback address or on a Unix socket the build account may open stays reachable from a build;
- `RuntimeMaxSec=` the request's timeout, `MemoryMax=4G`, `TasksMax=4096`.

The environment is fixed: `PATH` (the requested runtimes' `bin` first), `HOME` (the build's `home/`), `LANG`, `LCM_COMMIT`, `LCM_CACHE_DIR`, `XDG_CACHE_HOME`, `npm_config_cache`, `GOCACHE`, `GOMODCACHE` and, with inputs, `LCM_INPUTS_DIR=/run/lcm-build/inputs`: each input is bound read-only at `/run/lcm-build/inputs/<name>` (on a read-only tmpfs); the build account needs read permission on its files.

**Sandbox requirements.** Two restrictions depend on the host and are not refused when missing, because systemd accepts the settings either way:

| Setting | Needs | Without it |
|---|---|---|
| `SocketBindAllow=`/`SocketBindDeny=` (service may bind only its port) | systemd built with libbpf: `+BPF_FRAMEWORK` in `systemctl --version` (not Ubuntu 24.04's systemd 255.4) | no per-service port restriction |
| `IPAddressDeny=localhost` (build cannot reach loopback, including the LCM API) | cgroup v2 mounted at `/sys/fs/cgroup` and a kernel that attaches cgroup BPF programs (the journal logs `bpf-firewall ... failed` when it cannot, as in containers without `CAP_BPF`) | builds can reach loopback services |

`sudo bin/lcm host apply` checks the systemd feature string and the cgroup v2 mount and prints a `WARNING` note in its `deploy` step; `lcm host status` repeats the note when the deploy component is installed. It cannot detect a kernel or container that refuses the BPF attach. Where the port restriction matters, use a systemd with `+BPF_FRAMEWORK`, or treat the service's account permissions and the firewall as the only barrier.

**Disk use.** A build does not start with less than 2 GiB or 100,000 inodes free on `/var/lib/lcm-build` (and on `/var/cache/lcm-build` when that is another file system), empties a cache that grew beyond 4 GiB first, and is stopped when its source, home and cache together exceed 16 GiB or 2,000,000 entries, or when less than 512 MiB or 20,000 inodes remain free (measured every 10 s). A tree nested deeper than 256 directory levels, or one that takes more than 30 s to measure, counts as over the limit as well (the cache is emptied, a running build stopped); the trees are walked through directory descriptors, so deep chains cost no more per entry than flat ones. The last 3 builds per service are kept; older ones are removed after a build only while no other operation holds the service's lock, so an install never loses the build it copies. `install` removes the oldest releases beyond 20 that are neither current nor previous.

**Service units.** `activate` writes `/etc/systemd/system/<service>.service` from the built-in template: `User=`/`Group=` the service account, `WorkingDirectory=/opt/<service>/current`, `ExecStart=` the runtime's `node` or `python3` with the release file, or the release's own executable, plus the start arguments, `EnvironmentFile=/etc/<service>/env`, `Environment=PORT=<port> HOST=127.0.0.1` (and `PATH` with the runtime), `Restart=on-failure`, `ReadWritePaths=/var/lib/<service>`, the build sandbox's hardening with `UMask=0027` and `ProtectProc=invisible`, also hiding the build trees, and `SocketBindAllow=tcp:<port>` with `SocketBindDeny=any`, `RestrictAddressFamilies=AF_INET AF_INET6 AF_UNIX AF_NETLINK` (netlink lets Node list the network interfaces) and `PrivateIPC=yes`. With a systemd built with libbpf (`+BPF_FRAMEWORK` in `systemctl --version`) the service may bind only its port; without it, which includes Ubuntu 24.04's systemd 255, systemd accepts the two settings and ignores them, so any port the account may bind is allowed (see [Sandbox requirements](#sandbox-requirements)). Even then systemd cannot also restrict the address, so `HOST=127.0.0.1` is what it is told rather than enforced; outbound connections stay open. The env file must not set `PORT` or `HOST` in any form (it is read the way systemd reads it, including `export`, quotes and continuations), which come from `host.json`. An optional migration runs first, as the service account in the new release with the same sandbox and env file; when it fails nothing is switched. Then the `current` and `previous` links switch atomically, a changed unit is checked with `systemd-analyze verify` and systemd reloads when the unit changed or reports `NeedDaemonReload` (the previous unit and links are put back when either fails), the unit is enabled and restarted, and releases beyond `keepReleases` (default 3) are removed, never the current or previous one. Health checks and switching back are LCM's: a rollback of the [managed service](services.md#managed-deployments) activates the earlier release.

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

#### Backups

With `backup.dir` set in `host.json`, the deploy helper backs up and restores the data of a managed service ([what to declare](services.md#backup-and-restore-of-managed-services)). The helper's operations `backup`, `restore`, `backups` and `prune` are described in the protocol reference; this section is the host side.

**Layout.** `<backup.dir>/<service>/<id>/` (root 0700, ids like releases: UTC time and eight hex digits, `pre-restore-` before them for the copy a restore takes) holds `manifest.json` and one artifact per source (`000-files.tar`, `001-sqlite.db`, `002-postgres.dump`, `003-mongodb.archive` with `-users` and `-roles`, `004-command.out`), all root 0600. The manifest (strictly decoded, root-written) records service, id, kind, mode, creation time, the release that was current, each source with its artifacts or `absent` (and a SQLite database's permission bits), and each artifact's name, size and SHA-256. Nothing in the backup directory is writable by the service; the unit and the build sandbox hide it. `.stage-*`, `.tmp-*` and `.del-*` entries are leftovers of an operation that did not finish and are removed by the next backup, restore or prune. A tree that cannot be removed (the service keeps changing a directory in it) is renamed `.del-stuck-*`, and a leftover the sweep cannot remove is skipped and named in the reply's log; neither blocks the service's operations, and the next sweep tries again.

**Trust.** The producing process is a restricted subcommand of the pinned helper (`lcm backup-worker`), started with `systemd-run` as the service account:

- Sandbox as the service unit's: `ProtectSystem=strict` with only the staging output and the service's own data directory writable (a backup does not write it, but SQLite must create the `-shm` file of a cleanly closed WAL database there), `ProtectHome`, `PrivateTmp`, `PrivateDevices`, no capabilities, `NoNewPrivileges`, `SocketBindDeny=any`, `RestrictAddressFamilies=AF_INET AF_INET6 AF_UNIX`, `UMask=0077`, LCM's data and `/etc/lcm`, the ledgers and the build trees inaccessible, `EnvironmentFile=/etc/<service>/env`, and `RuntimeMaxSec`, `MemoryMax=2G`, `TasksMax=512`, `LimitFSIZE` and `LimitCORE=0`.
- The backup directory is replaced by an empty read-only file system with only this operation's staging directories bound back: `in` (the specification root wrote, read-only) and `out` (the worker's output).
- Root treats `out` as hostile. The service account may still hold a descriptor of it after the worker ends (through `/proc` or one passed over a socket), so root first takes the directory over (owner root, 0700, through a descriptor checked against the directory it looked at) and measures it again; the worker also marks itself undumpable, which only narrows this because the programs it starts are dumpable again. Every directory in it is taken over the same way, through descriptors and within bounds, before it is read or removed. Root refuses any entry but the result and the artifacts it expects, opens the directory through a tree confined to it, accepts only regular files owned by the service account, checks that the backup file system has room (the 512 MiB and 20,000 inodes a running backup must leave, beyond the set), copies them into a new root-owned directory while hashing the bytes copied, writes the manifest and renames the directory into place. Sizes are bounded (a set up to 32 GiB, a file archive up to 2,000,000 entries), the staging output is measured every 10 s like a build, and an operation does not start with less than 2 GiB or 100,000 inodes free.
- A restored SQLite database keeps the permission bits of the file it replaces, else those recorded in the set, else `0640`; special bits are never set.
- A restore verifies the manifest and the SHA-256 of every artifact while copying it to a staging directory readable by the service account, and the worker checks them again. It runs as the service account, so a restore can write nothing a compromised service could not. Before anything is replaced the helper checks the room for the staged set, the pre-restore copy (sized from the data being replaced) and the staging of an undo. The worker extracts through a root confined to the data directory, refuses absolute names, `..`, links, devices, pipes, special mode bits and duplicate names, and swaps each source in next to its target; a link planted in the data directory is replaced, never followed. A SQLite file's `-wal`, `-shm` and `-journal` are removed first.
- Postgres and MongoDB are handled by their official tools (`pg_dump`, `pg_restore`, `mongodump`, `mongorestore`) run by the worker as the service account. The helper finds them in `/usr/bin` or `/usr/local/bin` as root and refuses, before stopping anything, a tool that is missing or that anyone but root could have replaced (the file, its links and every directory above must be root's and not group- or world-writable). Authentication is whatever the service account has: peer authentication or the `PG*` variables of its env file, `MONGODB_URI` for MongoDB.
- `mode: stopped` stops the unit around the backup and starts it again if it was running, also when the backup failed. The helper always issues the stop for a unit it wrote, so a service that crashes and waits to restart (`activating`, `auto-restart`) is stopped too; if the unit is not inactive or failed afterwards the operation is refused with nothing changed. A restore stops the unit the same way, before the pre-restore copy.

**Residual risk: files held open.** Taking `out` over stops the account from adding entries or growing the set by creating files, but a file it already had open for writing can keep growing after the worker ended, on the backup file system, until root copies it (the copy takes the size it saw and stops there, so the set stays within its limits) and the staging directory is removed. Keep `backup.dir` on the same file system as `/var/lib`, where the service can already fill the disk, or put a quota on the backup file system; `lcm host apply` prints a note when they differ.

**Retention.** `keep.daily`/`keep.weekly` (default 7 and 4) keep the newest set of each of the last N UTC days and ISO weeks that have one; the newest set is never removed, nor a set whose manifest cannot be read, nor a `pre-restore` set younger than 7 days. A set is renamed before it is deleted, so a half-removed set is never listed. LCM runs `prune` after every successful backup.

**Restoring.** Use the console or `lcm resource action <service> restore [--backup-id <id>]`. A restore stops the service, so schedule it like a deployment. If the restore or the check afterwards fails the pre-restore copy is put back; if even that fails the service is deliberately left stopped on half-restored data (the result says `leftStopped`) and the message names the set to restore by hand (`restore` with that `backupId`). The pre-restore copy is kept for 7 days at least. A restore never touches the `admin` users and roles of a MongoDB instance; `users: true` saves them for a service with a `mongod` of its own and the helper restores them only for a `restoreUsers` request.

**On a new machine.** Install LCM and the deploy component, set `backup.dir`, `host allow` the service and run `apply`, create the (empty) databases of `postgres` and `mongodb` sources, copy the set directory `<backup.dir>/<service>/<id>/` from the old machine to the same place (root 0700, files root 0600, manifest unchanged), and deploy the service once so that its unit and account exist. Then restore: `lcm resource action <service> restore --backup-id <id>`, or without the id for the newest set. LCM has no record of a copied set, so it asks the helper to list the sets (`backups`) and records the one it restores; a set whose manifest cannot be read is not offered. The restore verifies every artifact against the manifest, so a damaged copy is refused before anything is changed.

**Prerequisites.** `sqlite` and `files` need nothing installed. `pg_dump`/`pg_restore` and `mongodump`/`mongorestore` must be installed for their sources. Do not put `backup.dir` under `/home`, `/root` or `/run/user`: the sandbox hides them.

### Ingress

With `components.ingress` set, LCM owns one nginx site for the domain and renders it as a whole from `host.json` and the services' route options; it keeps certbot and its timer. Nothing is installed for it when it is `null`, and turning it off removes nothing.

| Path | Owner, mode | Contents |
|---|---|---|
| `/etc/nginx/sites-available/lcm-ingress.conf` | root 0644 | The rendered site; its first line is LCM's marker, and a file without it is never replaced |
| `/etc/nginx/sites-enabled/lcm-ingress.conf` | root link | Link to the site |
| `/var/lib/lcm-ingress/` | root 0700 | `routes/<service>.json` (accepted route options), `last-apply.json`, `previous.conf` (the site before the last change), `adopted.json` (the link cutover disabled), `reverted.json` |
| `/var/www/lcm-ingress/well-known/` | root 0755 | Copies of the domain-level files nginx serves |
| `<acmeWebroot>` (`/var/www/lcm-acme`) | root 0755 | certbot's webroot challenges, served on port 80 |
| `/etc/letsencrypt/renewal-hooks/deploy/lcm-ingress-reload` | root 0755 | [`deploy/lcm-ingress-reload`](../deploy/lcm-ingress-reload): `nginx -t`, then reload, after certbot saved a renewed certificate |
| `/run/lcm-ingress.lock` | root 0600 | Serializes every change of the site |

```json
"components": {
  "ingress": {
    "domain": "example.org",
    "listen": ["192.0.2.10"],
    "acmeWebroot": "/var/www/lcm-acme",
    "tls": {"email": "ops@example.org", "keyType": "ecdsa"},
    "hsts": false,
    "wellKnown": {"assetlinks.json": "/etc/lcm/ingress/assetlinks.json"},
    "adopt": "example.org",
    "legacyLock": "/run/lock/example-nginx-deploy.lock"
  }
}
```

| Key | Meaning |
|---|---|
| `domain` | Lower-case domain name, the site's `server_name` |
| `listen` | IP addresses (canonical form, at most 8) both servers bind to; empty binds all addresses (IPv4 and IPv6) |
| `acmeWebroot` | Default `/var/www/lcm-acme`; not in a system tree, home directories or `/var/lib`, and not overlapping a static directory |
| `tls.email`, `tls.keyType` | certbot's account email and key type (`ecdsa`, the default, or `rsa`) |
| `hsts` | Send `Strict-Transport-Security: max-age=31536000` from every location (default off) |
| `wellKnown` | Domain-level files: `assetlinks.json`, `security.txt` or `apple-app-site-association`, each naming a root-owned file of at most 64 KiB; the content is copied and served at `/.well-known/<name>` with a fixed media type. The rest of `/.well-known/` answers 404 |
| `adopt` | The hand-made site in `sites-enabled` whose link the first application disables ([cutover](#cutover-and-revert)) |
| `legacyLock` | A lock file below `/run` that hand-made scripts still take while they edit nginx; while it exists, every change of the site takes it too |

`apply` needs nginx (`/usr/sbin/nginx`) and reports certbot when it is missing. With the component on it also installs the [deploy helper](#deploy-helper) files (wrapper, pinned helper, sudoers rule, ledger), through which LCM applies route changes; with `components.deploy` false the helper refuses every deploy and backup operation.

#### Routes

```sh
sudo bin/lcm host allow web --port 3200 --route /web/
sudo bin/lcm host allow site --external --route / --static /var/www/site/current
```

A route's prefix, port and static directory come only from `services.<name>` in `host.json`:

- `route.prefix`: `/` or an absolute path ending with `/`, of letters, digits and `._-` components, none starting with `.` (so `/.well-known/` is never a prefix); unique across services. Prefixes may nest: the longest one wins, and a location of one service may not lie in another service's longer prefix.
- `route.static`: the directory whose content appears at the prefix: `/app/x.js` is `<static>/x.js`, whatever the prefix. LCM writes nginx `root <static>` for the prefix `/` and `alias <static>/` for any other prefix. So for `/app/` with files in `<dir>/app/...` set `route.static` to `<dir>/app`; with files directly in `<dir>/...` set it to `<dir>`. It may not be in a system tree, a home directory, `/var/lib`, `/var/log`, `/var/cache`, `/opt/lcm` or `backup.dir`, and every directory down to it must be root-owned and not writable by group or others.
- `route.static` may be a symbolic link, such as `/var/www/site/current` pointing to `releases/<id>`, when the link is root's (in directories only root can write, so only root can switch it). Its target, resolved once and named without `..`, must pass every rule above, with no further link anywhere in its path. The site names the link, not the target, so nginx follows it per request and a release switch needs no new site; the target is checked again whenever the site is rendered (`apply`, `ingress.apply`, `host cert issue`), and a refused target stops that change and keeps the installed site.
- `external: true`: LCM does not own the service's process or accounts. `apply` and `allow` create nothing for it, the deploy helper refuses its deploy and backup operations, and `port` is optional when the route has a static directory. An external service has no runtimes or inputs.

Without route options a route proxies everything below the prefix to `http://127.0.0.1:<port>/` (stripping the prefix), or serves the static directory (`try_files $uri $uri/ =404`). `/prefix` (without the final `/`) redirects to `/prefix/`. The presentation options (proxy locations, SPA fallback, 404 page, immutable directories, downloads, redirects, CSP) belong to the service's document; their grammar is the reference in [`internal/ingress`](../internal/ingress/options.go).

**What reaches nginx.** Documents carry no nginx syntax: locations come from fixed templates, every value is checked against a strict grammar (paths of letters, digits and `._-` components, fixed vocabularies for categories, media types and redirect codes, sizes up to 64m and timeouts up to 1h, a CSP subset without quotes, backslashes, `$`, braces, commas or line breaks) and checked again by the renderer, which writes it inside double quotes. Every location repeats the full header set (`X-Content-Type-Options`, `Referrer-Policy`, HSTS when on, the service's CSP or CSP-Report-Only), because `add_header` in a location replaces the inherited ones, and sets `$lcm_service` and `$lcm_category`. Proxy locations include `/etc/nginx/lcm/hooks/<service>/<category>-*.conf` (a glob without matches is valid), where the security component adds its observation rules.

**Applying.** `apply`, `host cert issue`, `host ingress revert` and the deploy helper's `ingress.apply` change the site the same way, holding `/run/lcm-ingress.lock` (and `legacyLock` while it exists):

1. Render the whole site; refuse it when a value, a static directory or a well-known file fails its checks.
2. Copy the well-known files, write the site file atomically (the previous content goes to `previous.conf`), enable it and disable the adopted link.
3. Test the complete configuration with `nginx -t`, exactly what a reload loads.
4. Reload nginx and probe every route on the first listen address (or `127.0.0.1`) with the domain as `Host` and TLS server name; a probe passes with any answer except a server error of nginx itself (`502`-`504` mean the service is not answering yet and pass).
5. On any failure put the previous file and links back, and reload again if nginx had loaded the change. The result is recorded in `last-apply.json`.

`host apply --check` prints the rendered file when it would change it, together with the adoption. LCM sends a service's route options to the deploy helper (`ingress.apply`, see the [protocol](../internal/deployhelper/protocol.go)); the helper keeps them in `/var/lib/lcm-ingress/routes/` and puts the previous ones back when the site fails. It refuses while the site has never been installed or was reverted, and while the adopted site is enabled again, so that cutover stays an administrator's step. `ingress.status` reports the digest of the site the configuration renders, the installed one and the last result; `lcm host status` run as root compares them too (as another user it only checks that the installed site is LCM's).

#### Certificates

certbot keeps the certificate and renews it with its own timer. `apply` makes the renewal configuration `/etc/letsencrypt/renewal/<domain>.conf` use the webroot: it sets `authenticator = webroot`, `webroot_path = <acmeWebroot>,` and the domain's `[[webroot_map]]` entry, keeps every other line, and reports an `installer` other than `None` (certbot would edit nginx's files on renewal) as a manual action. It installs the deploy hook above and reports other deploy hooks, such as a hand-made reload, without removing them.

On a new machine, after `apply` (which serves port 80 with the ACME location until a certificate exists):

```sh
sudo bin/lcm host cert issue
```

It applies the site without TLS, runs `certbot certonly --webroot -w <acmeWebroot> -d <domain> --email <email> --key-type <keyType> --agree-tos -n`, then applies the full site with the port 443 server. It refuses when the domain already has a certificate.

#### Cutover and revert

To take over a host whose site was written by hand, name that site in `adopt`, then:

1. `sudo bin/lcm host apply --check`: review the rendered site and the line that disables the adopted site (its file in `sites-available` stays).
2. Stop the scripts that still edit nginx, or set `legacyLock` to the lock they take. Avoid the days around a certificate renewal.
3. `sudo bin/lcm host apply`: the first application disables the adopted link and records it in `adopted.json`.

To go back:

```sh
sudo bin/lcm host ingress revert
```

It disables LCM's site, enables the adopted site again with the link it recorded (or `sites-available/<adopt>`), tests and reloads nginx (putting LCM's site back when that fails), then removes `lcm-ingress.conf` (kept as `previous.conf`) and marks the site reverted: the deploy helper refuses `ingress.apply` from then on. The next `apply` installs the site again; set `components.ingress` to `null` first to stay reverted.

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
| `backups/` | LCM's own [backups](#backup) (mode 0700): one directory per backup with `lcm.db`, `secrets/` and `manifest.json` |

## Backup

**LCM's own state.** The server backs itself up once a day (the retention loop makes one when the last is 23 hours old): `VACUUM INTO` writes a consistent copy of `lcm.db` while LCM runs, the readable secret files of the secrets directory are copied next to it, and `manifest.json` records the schema and every file's SHA-256. It runs as the `lcm` account without privileges, so it also works without the deploy component. Backups are in `<data dir>/backups/<id>/` (0700), the newest one of each of the last `LCM_SELF_BACKUP_KEEP` days (default 7; 0 turns it off) is kept, and each is a record of the resource `lcm` (`GET /api/v1/resources/lcm/backups`, kind `self`), so the console shows when the last one was made. They sit on the same disk as the database: they protect against a damaged or wrongly changed database, not against losing the disk. Copy the directory elsewhere for that. To restore: stop LCM, replace `lcm.db` with the set's copy (removing `lcm.db-wal` and `lcm.db-shm`), restore `secrets/` if needed and start the matching binary. LCM never restores itself. A secret that was rotated stays readable in the copies made before the rotation, for up to `LCM_SELF_BACKUP_KEEP` days: after rotating one, delete the older backups (or wait) if the old value must not survive.

**By hand.** The database runs in WAL mode, so copy it with SQLite rather than `cp`:

```sh
sudo -u lcm sqlite3 /var/lib/lcm/lcm.db ".backup '/var/backups/lcm/lcm-$(date +%F).db'"
# or, without the sqlite3 CLI, stop LCM and copy lcm.db together with its -wal file
sudo tar -C /var/lib/lcm -czf /var/backups/lcm/secrets-$(date +%F).tgz secrets calendars
```

Back up `secrets/` separately and store it more carefully than the database. Preserve the matching binary and host configuration too, including the connections file. A normal same-version restore stops LCM, replaces the database (removing stale `-wal` and `-shm` files), then starts it. Reconcile Machine operations/maintenance before restoring automation; a database backup can predate an accepted OS request. Resources and policies kept in git config sources can be rebuilt, but Git cannot reconstruct runtime state or power receipts. The root helper ledger is independent: preserve its current state, never roll it back with a controller backup. Schema rollback follows [Upgrade](#upgrade).

## Upgrade

Compare the installed and candidate `lcm storage-schema` outputs before choosing an upgrade path. The command reports the binary's schema contract without starting a controller. Automatic [self-install](#self-deploy) accepts only equal schemas and, for a differing schema, fails with the command to run (`make build && sudo bin/lcm host upgrade`). Startup migrations are forward-only, so binary-only rollback after a migration is unsafe.

### `lcm host upgrade`

```sh
make build && sudo bin/lcm host upgrade --check   # preflight and plan, changes nothing
sudo bin/lcm host upgrade
```

Run it as root from a build of a reviewed checkout, like `host apply` (the self-deployed binary is refused). Run it inside `tmux` or `systemd-run --scope` when you are on SSH: it rolls back on `SIGHUP` (an SSH drop), `SIGINT` and `SIGTERM`, but not when something kills it with `SIGKILL`. Pause `lcm-auto-deploy` first if a commit could arrive meanwhile, and review unresolved Machine operations and maintenance gates beforehand: the command does not read them. It prints one line per step in the `host apply` format.

1. **Preflight**, no changes: root; valid `host.json`; the installed binary's `version` and `storage-schema` (run as `lcm`, strict parsing) against this build's (run as `nobody` from the staged copy of the running executable, the file `host apply` installs); the database is at the installed schema (read by this build as `lcm`, read-only); no `lcm.env` setting of `LCM_AUTOMATION`; self-install's lock `/run/lcm-self-install.lock` is taken (and held until the command ends; a running self-install refuses the upgrade) and no update is still being verified (10 minutes); `/var/backups/lcm` root-owned; free space for the backup set (twice the database with its WAL, plus the binary, `/etc/lcm` and the secrets) and for a restore next to the database. It refuses a lower schema (forward-only migrations) and a build whose commit is older than the installed binary's unless `--allow-older`, printing both versions. Equal schemas exit 0 without changes: self-deploy or `host apply --replace-binary` installs those.
2. Stop `lcm.service` and wait until it is inactive (60 s).
3. Back up to `/var/backups/lcm/upgrade-<UTC stamp>/` (root, 0700): `database/lcm.db` with any `-wal`/`-shm` (a plain copy of the stopped database; this build has no online backup path), `bin/lcm` (the old binary; `lcm.prev` stays self-install's), `etc-lcm/` (all of `/etc/lcm`) and `secrets/` (`LCM_SECRETS_DIR` from `lcm.env`, default `<data dir>/secrets`). `manifest.json` records both versions and schemas, the SHA-256 and size of every file, and the original source, owner and mode of the files a rollback restores. Each file is opened below its directory and its size, mode and owner come from the opened descriptor, which must be the file first looked at. Copies are checked against what was read, and the copy stops, removing the partial set, when less than 8 MiB would stay free before any file, or when `/etc/lcm` or the secrets grew by more than 1 MiB since the preflight sized them. The database files must belong to `lcm` and the binary to root, in the preflight and again on the opened descriptor; anything else is refused, not copied. A set of the same name that already exists (two runs in one second) is refused and left alone; only a set this run created is ever removed. `etc-lcm/` and `secrets/` keep modes but not owners (a manual restore sets them: `lcm` owns secrets); entries of other owners than root and `lcm` are refused.
4. Install the staged build as `/usr/local/bin/lcm` (atomic replace) and verify it.
5. Start with automation off and wait up to 120 s for `/api/v1/health` to report the new version, which counts only while systemd reports the unit `active` with a main process in the unit's control group that holds every IPv4 socket listening on port 7800 (found through `/proc/net/tcp` and `/proc/<MainPID>/fd`), with the same process and restart count (`NRestarts`) across the answer and across a settle delay of the unit's `RestartSec` plus one second, so another process on the health port, or a process that exits right after answering, cannot pass. A runtime drop-in `/run/systemd/system/lcm.service.d/zz-lcm-upgrade.conf` sets [`LCM_AUTOMATION=off`](../README.md#configuration) for this start only (so interrupted runs, policy triggers, config sync and self-deploy stay idle while the migration is checked), and the command checks that the effective (last) `LCM_AUTOMATION` that systemd loaded is `off` before starting. Admin-initiated actions through the API (manual runs, config sync, lifecycle actions) still work during this start, so nobody should use the console meanwhile. Then the database must report the new storage schema.
6. Write the upgrade marker `/var/lib/lcm/host-upgrade.json` (versions, commits and schemas of both builds, the time), owned by `lcm`, mode 0600: the file is created exclusively next to its name, owned and moded through its descriptor and renamed over the name, so a link or file `lcm` left there is replaced, never followed. It is bookkeeping: if it cannot be written, the upgrade still succeeds, a warning names the command that records the deployment by hand (`lcm resource action lcm deploy --version <commit>`), and a rollback removes the marker.
7. Remove the drop-in, check that the effective setting is no longer `off`, restart normally (automation on) and wait for health again with the same unit checks. This start reads the marker: when it names the commit this server runs, the server adds a successful deployment of the `lcm` service attributed to the system actor `host-upgrade` (action `deploy`, log naming both builds, previous version taken from the catalog) and removes the marker. Nothing is added when the newest successful deployment already is that commit; a marker for another build, or an unreadable one, is left alone and logged. LCM thereby records only what the root command attested and the server itself observes, and the `version` check agrees with the catalog again.
8. Keep the newest three `upgrade-*` sets (and always the one just written); other entries of the directory are never touched.

**Failure.** Any error after the stop, including `Ctrl-C` or a termination signal, rolls back: before step 4 only the stop happened and LCM starts again; after it, LCM is stopped (and must be confirmed stopped), the backup files are verified against the manifest, the database files are replaced (stale `-wal` and `-shm` removed first) and the old binary is put back, and only then the old binary starts and must report its own version healthy. The old binary never runs on a migrated database. The set is kept and its path printed. If the rollback itself fails (unverifiable backup, stop not confirmed, old version not healthy) the message says `ROLLBACK FAILED` and the host is left as it is, for the manual steps below. After step 7 the new controller has run with automation on, so a rollback then also discards what it did since; external effects of resumed runs are not undone. A rollback never touches the root helper ledger ([backup](#backup)).

Exit status: 0 done or nothing to upgrade; 1 error, including a completed or failed rollback; 2 usage; 3 with `--check` when an upgrade would run. A killed command (power loss, `SIGKILL`) can leave LCM stopped or the drop-in in `/run`: restore from the set by the manual steps (a reboot removes the drop-in, and the next run removes it).

The upgrade installs only the binary. When the build also changes embedded host files (for example the `self-install` script), `lcm host status` shows `core` as `outdated` until `sudo bin/lcm host apply` installs them.

### Manual procedure

Use it when `lcm host upgrade` cannot run (no `host.json`, an unusual layout, a failed rollback). For a schema-changing or unknown-version upgrade:

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

### Upgrade to typed backups (schema 17)

Storage schema 17 adds the `backups` table (one row per backup set, pre-restore copy and restore, and per backup of LCM's own database); no existing row changes. The schema changes, so self-install refuses it (`storage schema unknown or changed`): pause self-deploy before the commit reaches `main`, then `make build && sudo bin/lcm host upgrade` ([Upgrade](#upgrade)); the upgrade backs up the database first and restores it when the new binary does not come up healthy. Older binaries refuse the migrated database.

**After the upgrade.** The self backup makes its first copy within an hour (`<data dir>/backups`); set `LCM_SELF_BACKUP_KEEP=0` before the start to turn it off. A managed service gets typed backups by adding `backup.sources` ([services](services.md#backup-and-restore-of-managed-services)); set `backup.dir` in `host.json` and run `sudo bin/lcm host apply`, which creates the directory (root 0700; an existing one of another mode is refused, run `chmod 700` on it yourself if it is meant for backups), and apply it once after the new binary is installed so the pinned helper contains the backup operations (`lcm host status` shows a helper that differs from this build). Services using the command form of `backup` are unchanged.

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

To watch the proxy's certificate, give a resource a [`tls` check](services.md#health-checks) (days left and `notAfter` show in its message) and, to be told, instantiate the `cert-expiry` [template](policies.md#templates). It connects without privileges and does not renew anything.


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
