# Pending acceptance

Capabilities that are delivered and pass the automated checks but still need the operator's confirmation on the real host or device. Behavior and rules stay in the linked pages; this page holds only the acceptance steps, pass criteria and results.

How to use it:

- Work through an item's steps in order and tick each pass criterion.
- When every criterion holds, record the date, environment and outcome under the item's **Result**, update the item's status in [roadmap.md](roadmap.md) and remove its "pending" notes from the linked pages.
- A failed criterion is a defect: record what was seen under **Result** and keep the item open.

| Item | Status | Depends on |
|---|---|---|
| [Production upgrade to lcm/v2](#production-upgrade-to-lcmv2) | pending | - |
| [Xiaomi router on real hardware](#xiaomi-router-on-real-hardware) | pending | production upgrade |

## Production upgrade to lcm/v2

The live instance runs storage schema 13; `lcm/v2` is schema 14, so `self-install` refuses it and self-deploy cannot perform this upgrade. Procedure and rollback: [Upgrade](operations.md#upgrade) and [Upgrade to lcm/v2 resources](operations.md#upgrade-to-lcmv2-resources).

1. Pause the policy `lcm-auto-deploy` and any other deployed policy with triggers in the console.
2. Build the candidate from `main` (`make build`) and compare `/usr/local/bin/lcm storage-schema` (13) with `bin/lcm storage-schema` (14).
3. Stop LCM (`sudo systemctl stop lcm`) and take the [backup](operations.md#backup) of the database, secrets and calendars. Keep a copy of `/usr/local/bin/lcm` and `/etc/lcm/` with it.
4. Install the candidate: `sudo install -o root -g root -m 0755 bin/lcm /usr/local/bin/lcm`, then `sudo systemctl start lcm`.
5. Check the result:

   ```sh
   journalctl -u lcm -n 200 --no-pager | grep -i -e migrat -e 'needs review' -e error
   curl -s http://127.0.0.1:7800/api/v1/health      # reports the new version
   lcm get resources                                # services, the machine
   lcm get policies
   lcm resource observe <machine>
   ```

6. For every policy named in a `converted document needs review` warning: review it (keep or remove the added `acknowledgeImpact`), save a new version and check it with `lcm validate`.
7. Resume `lcm-auto-deploy` and the other paused policies.

Pass criteria:

- [ ] Startup completes without migration errors; health reports the new version.
- [ ] Every service, retired service and the machine is listed with the expected phase, state and health; the machine keeps its ID.
- [ ] Deployment history, timeline and check history of the services are still there.
- [ ] Policies are `lcm/v2`; each warned policy has been reviewed and saved, and the warning no longer appears after a restart.
- [ ] Machine observations continue (`lcm resource history <machine>` shows new samples).
- [ ] With `lcm-auto-deploy` resumed, the next commit on `main` with an equal schema deploys through self-deploy.

**Result:** not run.

## Xiaomi router on real hardware

Verifies login, observation, reboot and recovery against the operator's Xiaomi router; so far they are tested against the simulator only. Rules: [Connections](resources.md#connections), [Routers](resources.md#routers), [Router connections](operations.md#router-connections). The examples use the resource and connection name `home-router` and the default address `http://192.168.31.1`.

### Prepare

1. Record the model, firmware and login hash mode (`newEncryptMode`: 0 is SHA-1, 1 is SHA-256). `init_info` needs no login:

   ```sh
   curl -s http://192.168.31.1/cgi-bin/luci/api/xqsystem/init_info | jq '{hardware, romversion, newEncryptMode}'
   ```

2. Store the router's web admin password as a secret (type it, then Ctrl-D; it stays out of shell history):

   ```sh
   sudo -u lcm sh -c 'umask 077 && cat > /var/lib/lcm/secrets/home-router-password'
   ```

3. Write `/etc/lcm/connections.yaml` (`root:lcm 0640`) without write actions for now:

   ```yaml
   connections:
     - name: home-router
       type: xiaomi
       url: http://192.168.31.1
       passwordSecret: home-router-password
       deviceId: "02:4c:43:4d:00:02"
       allowActions: []
       allowResources: [home-router]
   ```

4. Add `LCM_CONNECTIONS_FILE=/etc/lcm/connections.yaml` to `/etc/lcm/lcm.env`, run `sudo systemctl restart lcm`, then check `journalctl -u lcm -n 50` and `lcm get connections`.
5. Apply the router resource with `lcm apply -f home-router.yaml`:

   ```yaml
   apiVersion: lcm/v2
   kind: Resource
   metadata:
     name: home-router
   spec:
     type: router
     adapter: xiaomi
     connection: home-router
     config:
       interval: 60s
   ```

### Observation

1. `lcm resource observe home-router`, then `lcm get resources home-router -o json`.
2. Wait a few intervals and read `lcm resource history home-router` and `lcm resource events home-router`; open the router page in the console.
3. Write a wrong password into the secret (step 2 of Prepare) and restart LCM, because an existing router session keeps working until a login is needed. Wait one interval, then restore the right password and wait again, this time without a restart.

Pass criteria:

- [ ] The observation's outcome is `ok`, with model, firmware, CPU, memory, uptime, client count and WAN status matching the router's own web page.
- [ ] The resource is `online` and its health is `healthy`; history gains one sample per interval.
- [ ] The console's router page shows the same values and updates.
- [ ] With the wrong password the outcome is `auth-failed` and health `unknown`; with the right one it returns to `ok` without another restart. The router does not lock the account.
- [ ] `journalctl -u lcm` and the audit log contain no password.

### Reboot

Do this when a network interruption of a few minutes is acceptable: the console over Tailscale drops while the router restarts.

1. Declare that the workstation is reached through the router: add `{ type: controlPath, target: home-router }` to the machine resource's `relations` and apply it. `lcm resource impact home-router` should list the machine under `controlDependents`.
2. `lcm resource capabilities home-router` must show `reboot` unavailable because the connection does not allow it.
3. Change `allowActions` to `[reboot]`, restart LCM and check `capabilities` again.
4. Submit, using the current document version from `lcm get resources home-router -o json` (`.version`):

   ```sh
   lcm resource reboot home-router --version <version> --reason 'Router acceptance' \
     --confirm home-router --acknowledge --acknowledge-impact --idempotency-key home-router-accept-1
   lcm resource operation home-router <operation-id>
   ```

5. After the operation finishes, run the same `reboot` command again with the same idempotency key.

Pass criteria:

- [ ] Without `reboot` in `allowActions`, the reboot is unavailable and refused; without `--acknowledge-impact` it is refused with the impact.
- [ ] The router really restarts once (its own uptime resets).
- [ ] The operation moves to `succeeded` within 10 minutes; the events show `router.reachability.changed` (offline, then online) and `router.boot.changed`, and the resource returns to `online` and `healthy`.
- [ ] Repeating the request with the same key returns the same operation and does not restart the router again.
- [ ] The audit log records the request with the operator, reason and acknowledged impact.

**Result:** not run. Record the date, model, firmware, hash mode and LCM version with the outcome.
