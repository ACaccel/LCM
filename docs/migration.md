# New machine and migration

Two procedures built from the commands documented in [operations.md](operations.md): installing LCM with the host platform components on a fresh machine, and moving an existing LCM host with its managed services to another machine. Examples are generic: `example.com` is the domain, `<svc>` a managed service, `alice` the operator.

## New machine

**Prerequisites**

| Need | For |
|---|---|
| Linux with systemd, `sudo`, `git`, `curl`, `make`, `jq`; an operator account (not root) | [Install](operations.md#install) |
| Go 1.26+ and Node.js 24 toolchains, by default `~alice/.local/{go,node}` | Building LCM and, copied to `/opt/lcm/toolchain`, self-deploy |
| Tailscale signed in, HTTPS certificates enabled | The console at `https://<node>:9443`; `host apply` refuses without it (other layouts: [hosts without `deploy/`](operations.md#hosts-without-deploy)) |
| `nginx`, `certbot`, DNS of the domain pointing at this host, ports 80 and 443 reachable | [Ingress](operations.md#ingress) |
| `python3`, `apt` access | [Security component](security.md#host-component) (installs `auditd` and `aide`) |
| `pg_dump`/`pg_restore`, `mongodump`/`mongorestore`/`bsondump` | [Typed backups](operations.md#backups) of `postgres` and `mongodb` sources; `sqlite` and `files` need nothing |
| Runtime directories (for example a Node.js tree) | [`runtimes`](operations.md#runtimes) of managed services |

**Procedure**

1. As the operator, clone LCM from a reviewed source and `make build`. Root runs only this build.
2. `sudo bin/lcm host init --operator alice` writes `/etc/lcm/host.json` ([flags](operations.md#host-configuration)). Add `--deploy`, `--backup-dir /var/backups/lcm-services` (a directory of its own), `--backup-tool NAME=PATH`, `--runtime NAME=DIR` and `--ingress-domain`/`--ingress-email` as this host needs. Other ingress settings (listen addresses, HSTS, domain-level files, a site to adopt) have no flags: edit the file afterwards (`sudoedit /etc/lcm/host.json`, root-owned, not group-writable). Leave `components.security` `null` for now.
3. `sudo bin/lcm host apply --check`, review, then `sudo bin/lcm host apply` (or `sudo deploy/bootstrap.sh`, which does both steps). Set the console's admin password when asked; `~alice/.config/lcm/token` receives the first admin token ([what apply does](operations.md#procedure)).
4. As the operator, `deploy/seed.sh` registers the self-deploy service and policy.
5. Register the host as a [machine](machines.md#registration-and-configuration) (`lcm apply -f workstation.yaml`) and note its immutable ID (`lcm get resources workstation -o json`, field `id`).
6. For each managed service, as root:
   1. `sudo bin/lcm host allow <svc> --port 3200 --runtime node24 --route /<svc>/ --check`, then without `--check` ([grants](operations.md#service-grants), [routes](operations.md#routes)).
   2. Put its secrets in `/etc/<svc>/env` (root:`<svc>` 0640, created empty by `allow`) and `/etc/<svc>/<instance>.env` for instances. Secrets never go into documents or backup sets.
   3. Create the empty databases of `postgres` and `mongodb` sources, with their users.
7. Apply the Resource and Policy documents, either way:
   - `lcm apply -f <dir>` from a checkout, or
   - register a [config source](policies.md#policy-as-code) (`lcm source add --name <name> --repo <url> --branch main --path lcm`, or console *設定 > 設定來源*; `lcm source sync <name> --wait` syncs at once). LCM clones as the `lcm` account, so a private repository needs an SSH key without passphrase or a credential helper for that account.
8. `lcm resource action <svc> deploy --wait` per service; add `--version <commit>` to deploy a known commit instead of the branch head. The first deploy writes the unit, builds and starts it.
9. Ingress: `sudo bin/lcm host apply --check` shows the rendered site; when the domain resolves to this host, `sudo bin/lcm host cert issue` obtains the first certificate ([certificates](operations.md#certificates)). To replace a hand-made nginx site follow the [cutover](operations.md#cutover-and-revert).
10. Security component: set `components.security.machineId` to the ID from step 5, `sudo bin/lcm host apply --check`, `sudo bin/lcm host apply`. Then review the host and accept the AIDE baseline locally; `apply` never does ([baseline](security.md#aide-baseline)). List root deploy tooling that other projects still use in `watch.deploy` ([adoption](security.md#install-adopt-and-upgrade)).
11. Verify: `lcm host status` (components `up to date`), `lcm get resources` (services `healthy`), `curl http://127.0.0.1:7800/api/v1/health`, and each route over HTTPS.

## Moving to a new machine

### What lives where

| What | Where | How it moves |
|---|---|---|
| LCM state, secrets, calendars | `/var/lib/lcm/{lcm.db,secrets,calendars}` | Copy from the stopped controller ([backup](operations.md#backup)); the daily self backup in `/var/lib/lcm/backups` is the fallback |
| Host configuration | `/etc/lcm/host.json` | Copy, then review: operator, toolchain paths, ports and `ingress.listen` addresses are host specific |
| Server settings, connections file | `/etc/lcm/lcm.env`, the file named by `LCM_CONNECTIONS_FILE` | Copy the settings you changed by hand; the connections file stays root-owned in root-owned directories ([connections](resources.md#connections)) |
| Service secrets | `/etc/<svc>/env`, `<instance>.env`, `backup.env` | Copy by hand as root (root:`<svc>` 0640, `backup.env` root:root 0600). Not in any backup set |
| Service data | `/var/lib/<svc>` | Typed backup sets of the sources the document declares; anything undeclared is lost |
| Backup sets | `<backup.dir>/<svc>/<id>/` | Copy as root, unchanged ([layout](operations.md#backups)) |
| Certificates | `/etc/letsencrypt` | Copy preserving owners and links (`tar --numeric-owner`), or issue anew |
| Not portable | `/var/lib/lcm-deploy`, `/var/lib/lcm-machine-power`, `/var/lib/workstation-security`, `/var/lib/lcm-ingress`, `/opt/<svc>` | Helper ledgers, receipts and the security baseline belong to one host and are never copied. Releases come from a deploy, route options from `apply-route` |

### Steps

Choose a quiet window and lower the DNS TTL of the domain in advance.

**Old host: freeze and back up**

1. Pause every deployed policy so that nothing deploys or restarts meanwhile: `lcm policy pause <policy>` (active runs continue, so wait for them).
2. Note the commit each service runs (console deployments) and `lcm version` with `lcm storage-schema`.
3. For each service: `lcm resource action <svc> stop --wait --acknowledge-impact`, then `lcm resource action <svc> backup --wait`. Stopped services make the sets consistent across sources. Check the new set with `lcm get backups <svc>` (`restorable` is `true`).
4. Copy `/etc/<svc>/*.env`, `host.json`, `lcm.env` and the connections file to the new host over a channel you trust, and the sets as root, for example:

   ```sh
   sudo tar -C /var/backups/lcm-services --numeric-owner -cf - <svc>/<id> | ssh new-host 'sudo tar -C /var/backups/lcm-services -xpf - --numeric-owner'
   ```

   Afterwards the set directory must be root 0700 with root 0600 files and an unchanged `manifest.json`.
5. Stop LCM (`sudo systemctl stop lcm.service`) and copy `lcm.db` with any `-wal`/`-shm`, `secrets/` and `calendars/` with `tar` or `rsync` to a staging directory on the new host. Do not restart it: this is the rollback point (below).

**New host: install**

6. Do steps 1 to 3 of the [new machine](#new-machine) procedure, but instead of `host init` install the copied `host.json` as root:root 0644 and review it (`host init` never replaces a file); the grants of its `services` are applied by `host apply`. Build from the commit the old host ran, so that the storage schemas are equal, and keep `components.security` `null` for now. Then set `LCM_AUTOMATION=off` in `/etc/lcm/lcm.env` and `sudo systemctl restart lcm.service`, so that no policy, trigger or config sync runs until step 12 (API and CLI actions still work). The admin password asked for during `apply` is replaced by the restore.
7. Restore LCM's state: stop `lcm.service`, replace `lcm.db` (removing stale `-wal`/`-shm`) and `secrets/` from the staging directory, set the owner to the new host's `lcm` account (`chown lcm:lcm`; user IDs differ between hosts, so do not extract with `--same-owner`), modes 0600 and 0700, start LCM. The old admin account and tokens apply now; `~alice/.config/lcm/token` from step 3 does not, so create a new one: `(umask 077; sudo -u lcm /usr/local/bin/lcm token create --name cli --data-dir /var/lib/lcm > ~/.config/lcm/token)`.
8. Accept the new device identity. The restored machine resource keeps the device identity of the old host, which reads as `changed` and blocks verified control ([identity](machines.md#changed-device-identity)). Once the first observation of the new host is in (`lcm resource observe <name>`; `status.identity` reads `changed`), run `lcm resource rebind-identity <name> --reason "host moved" --confirm <name>`. The resource keeps its ID, history and Git ownership, so `components.security.machineId` and the helper's `controllerID` stay valid. Operations still open from the old host are closed unverified when they can no longer act; the command is refused when one still can.
9. Restore the services, one at a time:
   1. Copy the service secrets into place and create the empty databases and users.
   2. `lcm resource action <svc> deploy --version <commit> --wait` with the commit noted in step 2 creates the account, unit and release.
   3. `lcm resource action <svc> restore --backup-id <id> --wait` (newest set when omitted); `lcm get backups <svc>` lists the copied sets and the helper verifies every artifact against the manifest. MongoDB users are restored only with `--restore-users --confirm <svc>` ([rules](operations.md#backups)).
   4. `lcm resource action <svc> apply-route` sends the route options to the ingress.
10. Ingress and certificates: copy `/etc/letsencrypt` before the first `host apply` that has `components.ingress` (it then reuses the certificate), or switch DNS first and run `sudo bin/lcm host cert issue`. Set `ingress.listen` to this host's addresses. Check the site before the switch: `curl --resolve example.com:443:203.0.113.10 https://example.com/<svc>/`.
11. Security component and power helper: set `components.security.machineId` to the machine ID of the copied `host.json` (unchanged by step 8) and `sudo bin/lcm host apply`; review and accept a new baseline ([security](security.md#aide-baseline)). For machine power provision the ledger and `machine-power.json` again by hand on this host with the same ID as `controllerID` ([helper](operations.md#local-machine-power-helper)); the old host's receipts do not move.

**Switch and verify**

12. Switch DNS to the new host. Verify: `lcm host status`, `lcm get resources` (health, deployments), routes over HTTPS, a fresh backup of each service (`lcm resource action <svc> backup --wait`), the admin login. Then remove `LCM_AUTOMATION=off`, restart LCM, resume the policies (`lcm policy resume <policy>`) and check that config sync runs without conflicts.
13. Keep the old host stopped for a few days. Then decommission it, as far as LCM goes: `sudo deploy/uninstall.sh --purge` removes LCM, its data, `/etc/lcm` and the account, but not the managed services' units, accounts, `/opt/<svc>`, `/var/lib/<svc>`, the helper files and ledgers, the ingress site or the security component. Remove those by hand (`sudo bin/lcm host apply --remove` with `components.security` `null`, `sudo bin/lcm host ingress revert`), and remove the old node from the tailnet.

### Rollback

- **Before the DNS switch**: the old host is untouched apart from stopped services and paused policies. Start `lcm.service`, start the services (`lcm resource action <svc> start`), resume the policies, stop the new host's LCM. Nothing is lost.
- **After the DNS switch**: the new host holds newer data. Take a backup of each service there, copy the sets back and `restore` them on the old host (its LCM state is then older than the new one's). Switch DNS back only after that.
- Never run both controllers with automation on against the same services, repositories or domain.

## Known limits

- No schema change during a move: both builds must report the same `lcm storage-schema`, because a restored database migrates forward only. Upgrade before or after, never inside it ([upgrade](operations.md#upgrade)).
- The deploy helper is a pinned copy of the build that ran `host apply`; `host allow` refuses `instances`, `groups`, `after`, `wants` and `backup.tools` until `sudo bin/lcm host apply` has pinned the current one ([upgrade to service instances](operations.md#upgrade-to-service-instances)).
- Typed backups cover declared sources only; a `command` source is kept but never restored, and online MongoDB backups are not point-in-time across databases (stop the service, as above).
- MongoDB: the user of `MONGODB_URI` must exist in the new `mongod` before a users restore and be in the set; a users restore replaces every account of that `mongod`. Create the backup user before the first backup that must survive.
- Secrets are never in documents, backup sets or `host.json`: service env files, `backup.env`, the connections file and its password secrets move by hand.
- A restored machine resource reads a changed device identity until it is accepted with `rebind-identity` (step 8); the security baseline, helper ledgers and power receipts start new on the new host.
- Router connections need the new host to reach the router and the connections file in place before LCM starts ([router connections](operations.md#router-connections)).
- LCM does not automate the move itself; every step above is an explicit command.
