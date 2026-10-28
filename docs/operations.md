# Operations

How to run LCM on a host. Build and configuration reference: [README](../README.md) (the environment variables and flags are listed there only). Design and security notes: [architecture.md](architecture.md).

## Install

Run LCM as a dedicated, unprivileged system user so that everything it executes has only that user's rights.

```sh
sudo useradd --system --home-dir /var/lib/lcm --create-home --shell /usr/sbin/nologin lcm
sudo install -m 0755 bin/lcm /usr/local/bin/lcm
sudo install -d -o lcm -g lcm -m 0700 /var/lib/lcm
sudo -u lcm env LCM_DATA_DIR=/var/lib/lcm /usr/local/bin/lcm admin set-password
```

Give the `lcm` user what the services need and nothing else: read access to the repositories it deploys (an SSH key without passphrase or a credential helper), ownership of the working copies named in `source.repo`, and a sudoers rule for the system units it may start, stop and restart (see [Security model](architecture.md#security-model)).

Settings that are not secrets go in `/etc/lcm/lcm.env`, one `NAME=value` per line (`LCM_LISTEN`, `LCM_DATA_DIR`, retention and so on). Secret files go in `/var/lib/lcm/secrets/` (one file per secret, `chmod 600`, owned by `lcm`).

## systemd unit for LCM

`/etc/systemd/system/lcm.service`:

```ini
[Unit]
Description=LCM lifecycle control plane
After=network-online.target
Wants=network-online.target

[Service]
User=lcm
Group=lcm
Environment=LCM_DATA_DIR=/var/lib/lcm
EnvironmentFile=-/etc/lcm/lcm.env
ExecStart=/usr/local/bin/lcm serve
Restart=on-failure
RestartSec=5
# Stop gives running policy nodes time to finish; their runs resume after the next start.
TimeoutStopSec=30
PrivateTmp=true
ProtectSystem=full
# Not NoNewPrivileges: LCM uses sudo to control system units.

[Install]
WantedBy=multi-user.target
```

```sh
sudo systemctl daemon-reload && sudo systemctl enable --now lcm
curl -s http://127.0.0.1:7800/api/v1/health      # {"status":"ok",...}
journalctl -u lcm -f                             # logs; LCM_LOG_FORMAT=json for structured output
```

`ProtectSystem=full` leaves `/var/lib/lcm` writable because the unit runs as a user that owns it; tighten further (`ReadWritePaths=`, `ProtectHome=`) only after checking where your working copies and build outputs live.

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
2. Install the new binary (`lcm version` shows the build) and `sudo systemctl restart lcm`.
3. Schema migrations run at startup and are not reversible: to go back, restore the backup together with the old binary.

On start LCM reconciles service states with reality and resumes interrupted policy runs, so a restart during a deployment is safe; a deployment that was cut off is marked interrupted.

## Resource use

The target is under 50 MB resident. `LCM_MEMORY_LIMIT` (default `40MiB`, `off` disables) sets a soft limit for the Go runtime so that garbage is collected early instead of letting the heap grow; it is not a cap. Retention settings bound the database: raw check results, rollups, timeline, finished runs, deployment records and idempotency records are pruned by the hourly retention job (`LCM_PRUNE_INTERVAL`). To investigate memory or CPU, set `LCM_PPROF_LISTEN=127.0.0.1:6060`, which serves `net/http/pprof` on its own loopback listener; it is off by default and must stay off in normal operation.

## Remote access

LCM serves plain HTTP. For access beyond localhost run a TLS-terminating reverse proxy (nginx, Caddy) in front, keep LCM on `127.0.0.1`, and set `LCM_COOKIE_SECURE=true`. The event stream (`/api/v1/events`) needs response buffering turned off in the proxy.
