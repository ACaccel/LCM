# Operations

How to run LCM on a host. Build and configuration reference: [README](../README.md) (the environment variables and flags are listed there only). Design and security notes: [architecture.md](architecture.md).

## Install

`deploy/` installs LCM on a systemd host whose console is published with `tailscale serve`, and lets LCM deploy itself from `main` afterwards. It runs LCM as the unprivileged system account `lcm`; the only root command that account may run is [`self-install`](#self-deploy).

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

**Monitoring-only services.** `seed.sh` lists the systemd units of `card-together*`, `personal-quant-desk*`, `pleiades@*` and `mongodb-pleiades` that exist, and registers one service per application with runtime `external` (LCM has no actions on them) and no policies. Long-running units get critical `systemd` checks; oneshot units are represented by their timer as non-critical checks. `http` checks are added only for URLs that answer at seed time (card-together `/health`, metube). The docker check for `metube` is added only when `lcm` is in the `docker` group, which bootstrap does not do because that group is root-equivalent.

### Self-deploy

The `lcm` service builds and installs LCM like any other service; the auto-deploy policy runs it for every new commit on `main` whose `ci.yml` run passed:

1. `trigger.git` sees the commit; `ci.status` waits for CI (unauthenticated GitHub API, 60 requests per hour per IP).
2. `lifecycle.deploy` fetches and detaches `work/lcm` at the commit, runs `make build` (console and binary) with the copied toolchains, then the install step `sudo -n /usr/local/libexec/lcm/self-install /var/lib/lcm/work/lcm/bin/lcm`.
3. `self-install` reads the binary as `lcm` (so a swapped link cannot expose root-only files), checks that it answers `lcm version`, keeps the current binary as `lcm.prev`, moves the new one into place and schedules `self-install --verify` in a transient unit `lcm-self-update-<time>` 3 seconds later. It exits at once, so the deployment is recorded as finished before LCM restarts. For the same reason the runtime has only a status command: start, stop and restart are not offered and the deployment skips its restart step.
4. The verification restarts `lcm.service` and waits up to 60 s for `/api/v1/health` to report the new version. If it does not, it restores `lcm.prev` (keeping the rejected binary as `lcm.failed`), restarts again, logs at error priority and leaves the transient unit failed.
5. The policy's `wait.until` was suspended across the restart and resumes in the new process. The service's `version` check passes only when the running server reports the version of the binary built in the checkout, so after a verification rollback the service stays unhealthy, the wait times out after 3 minutes and `lifecycle.rollback` rebuilds and reinstalls the previous successful commit.

**Trust boundary.** Code on `main` that passes CI is built and run by LCM as `lcm`, the same model as the other auto-deployed projects on this host, and installed as the root-owned `/usr/local/bin/lcm`. Anyone who can push to `main`, or write the `lcm` Service document, therefore controls the `lcm` account and the content of that binary: never run it as root (bootstrap and self-install run it only as `lcm` or `nobody`). The sudoers rule accepts one fixed argv; rules for other units are separate, reviewed changes.

**Inspect and recover.**

```sh
journalctl -u lcm -f                                   # the server
journalctl -u 'lcm-self-update-*' -n 50 --no-pager     # restarts and verifications
systemctl list-units --failed 'lcm-self-update-*'      # rejected updates; clear with systemctl reset-failed
```

- Manual rollback of the binary: `sudo install -m 0755 /usr/local/bin/lcm.prev /usr/local/bin/lcm && sudo systemctl restart lcm`. Then deploy the matching commit from the console or `lcm action lcm deploy --version <sha>` so that the catalog agrees.
- To pause self-deploy, pause the policy `lcm-auto-deploy` in the console.
- A manual `lcm action lcm deploy` has no policy to roll back: if the verification restores `lcm.prev`, the catalog still records the new commit, and the failing `version` check shows the mismatch until you deploy a good commit.
- The verification does not restore the database: a new binary that applied a migration and then failed runs the previous binary on the migrated schema. Back up the database before merging changes that add migrations.

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
| `lcm.db` (+ `-wal`, `-shm`) | The whole state: services, policies, runs, history, audit log, locks, tokens |
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

Back up `secrets/` separately and store it more carefully than the database. A restore is: stop LCM, put the database file in place (remove stale `-wal` and `-shm` files), start LCM. Services and policies kept in git config sources can also be rebuilt from the repositories.

## Upgrade

1. Back up the database.
2. Install the new binary (`lcm version` shows the build) and `sudo systemctl restart lcm`. With [self-deploy](#self-deploy) this happens on every merge to `main`.
3. Schema migrations run at startup and are not reversible: to go back, restore the backup together with the old binary.

On start, and every minute afterwards, LCM reconciles service states with reality and resumes interrupted policy runs, so a restart during a deployment is safe; a deployment that was cut off is marked interrupted.

## Resource use

The target is under 50 MB resident. `LCM_MEMORY_LIMIT` (default `40MiB`, `off` disables) sets a soft limit for the Go runtime so that garbage is collected early instead of letting the heap grow; it is not a cap. Retention settings bound the database: raw check results, rollups, timeline, finished runs, deployment records and idempotency records are pruned by the hourly retention job (`LCM_PRUNE_INTERVAL`). To investigate memory or CPU, set `LCM_PPROF_LISTEN=127.0.0.1:6060`, which serves `net/http/pprof` on its own loopback listener; it is off by default and must stay off in normal operation.

## Remote access

LCM serves plain HTTP. For access beyond localhost run a TLS-terminating reverse proxy (nginx, Caddy) in front, keep LCM on `127.0.0.1`, and set `LCM_COOKIE_SECURE=true`. The event stream (`/api/v1/events`) needs response buffering turned off in the proxy.
