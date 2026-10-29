# Policies

A policy is a `Policy` document: a graph of nodes that one trigger starts. Triggers watch signals (check, health, lifecycle and alert changes), git branches, cron schedules and webhooks; other nodes branch, wait, call HTTP and CI APIs, notify, keep state and locks, and run lifecycle actions through the same manager as the console buttons. The node types and their props come from the server (`GET /api/v1/node-types`, the editor's palette); the execution rules are in [`internal/engine/doc.go`](../internal/engine/doc.go) and summarized in [architecture.md](architecture.md#engine-semantics).

Policies are edited in the console's graph editor or as YAML, validated by the server, and take effect when deployed. The editor keeps node positions in the annotation `lcm.dev/layout`, which the engine ignores. Every run is recorded node by node and can be watched live; `lcm run <policy> --dry-run` (or the editor's dry run) plans write nodes instead of acting.

A service's *Policy* tab lists the deployed policies that act on it or listen to it. The server resolves the `service` prop of `lifecycle.*` nodes when it is a literal or uses `params` only (the templates' `{{ params.service }}`, including static module parameters); a node whose service depends on the trigger event is shown as decided at run time.

## Templates

Four built-in templates ([`examples/templates/`](../examples/templates), embedded in the binary) cover the common loops. Pick one from *套用範本* on a service's Policy tab or *從範本建立* on the policy list, or use the API.

| Template | What it does |
|---|---|
| `auto-deploy` | New commit on a branch -> optional GitHub CI check (`ci.status`, optionally by workflow file) -> deploy window -> `lifecycle.deploy` -> wait for the service to turn healthy -> otherwise `lifecycle.rollback`; one message per deployment, on Discord or a konata relay, shows the stages as they finish |
| `scheduled-backup` | Cron (default `Asia/Taipei`, runs once after downtime) -> `lifecycle.backup` with retry and backoff -> optional wait for a heartbeat check -> notify on failure |
| `post-deploy-verify` | After any deployment of the service, wait for health and roll back on timeout |
| `restart-on-down` | Health turns `down` -> restart -> wait for recovery -> notify. It has no rate limit: a service that keeps failing is restarted every time |

```sh
curl -H "Authorization: Bearer $LCM_TOKEN" localhost:7800/api/v1/templates          # parameters of each template
curl -H "Authorization: Bearer $LCM_TOKEN" -X POST localhost:7800/api/v1/templates/restart-on-down/instantiate \
     -d '{"name":"web-restart","params":{"service":"web","notifySecret":"discord-ops"},"dryRun":true}'
```

- The result is an ordinary policy. Its parameters sit in `spec.params` and the nodes read `params.*`, so changing one later means editing the document. Trigger props cannot be expressions, so what a trigger uses (repository, branch, schedule, watched service) is written into the trigger.
- The target service needs at least one critical health check: a service counts as recovered only when it is `healthy` with check results newer than the deployment or restart.
- `notifySecret` names a secret holding a Discord webhook URL; leave it empty to send nothing.
- `auto-deploy` can report to a [BotFleet](https://github.com/ACaccel/pleiades-fleet) konata notification relay instead: set `notifyTarget: konata`, `notifyUrl` to the route (`http://127.0.0.1:<NOTIFY_API_PORT>/notifications/<route>`, the route defined in konata's `notificationRoutes`) and `notifyTokenSecret` to a secret holding konata's `NOTIFY_API_KEY`. Each run gets its own message; konata shows the stages (CI, deploy window, build and install, health check, rollback) and colours it by the outcome.
- A rollback needs an earlier successful deployment recorded by LCM: deploy a known-good version once (`lcm action <service> deploy --version <sha>`) before relying on `auto-deploy`.
- `restart-on-down` needs a runtime that can restart the service ([services](services.md#runtime)); on an `external` service the restart is refused.

## Policy-as-code

The console and API are the default way to configure LCM and take effect at once. Git management is opt-in per resource: services, policies and modules can also live as YAML in a repository that LCM pulls and applies. Documents describe resources only; command allowlists, the `systemctl` privilege prefix and the units LCM may touch stay in host configuration.

Register a repository as a **config source** (admin API, or *設定 > 設定來源* in the console):

```sh
curl -H "Authorization: Bearer $LCM_TOKEN" -X POST localhost:7800/api/v1/config/sources \
     -d '{"name":"infra","repo":"git@host:me/infra.git","branch":"main","path":"lcm","interval":"1m"}'
```

**Sync.**

- LCM fetches the branch into its own clone under `<data dir>/sources`, reads every `*.yaml` and `*.yml` under `path` (recursively; several documents per file; `*.test.yaml` skipped), validates, and applies modules, then services, then policies.
- It syncs every `interval` (default 1m, 10s to 24h), on `POST /config/sources/{name}/sync` (202 at once; read `lastSyncAt`, `lastCommit`, `lastError` and `syncing`; concurrent requests join the running sync), and when a GitHub push webhook (`/hooks/github`) names the repository and branch.
- A document that fails validation leaves its resource unchanged; the error is on the source (`lastError`), on the service timeline, and published as a warning alert signal with source `configsync`.
- The repository URL must not embed a password. Use an SSH key without passphrase (git runs with `BatchMode`, and children get a short environment allowlist, so `SSH_AUTH_SOCK` is unavailable) or a credential helper.

**Ownership.** Resources applied from a source are owned by it (`managedBy` in service and policy views, `GET /config/ownership`). A resource belongs to one source; a second claim is refused. A source never overwrites a resource without an owner (one made in the console or API): if the document equals the resource the source adopts it, which is how resources move under git (`lcm export`, commit, register); if it differs, nothing is written and the claim is a conflict.

**Drift and conflicts.** Editing an owned resource in the console or API works at once and makes it **drifted** (its content differs from what the repository last said). Nothing is overwritten silently:

- When a new commit changes the document of a drifted resource, or defines a different document for an unowned one, the sync skips it, marks a **conflict** and notifies (a `config.conflict` timeline entry for services and a warning signal). Documents unchanged in the repository never conflict.
- `POST /config/ownership/{kind}/{name}/resolve` with `{"use":"git"}` writes the repository document and drops the local edits; `{"use":"current"}` keeps the current content, owns it and closes the conflict (it stays drifted, acknowledged). Drift clears by itself once the repository matches.
- Documents that disappear from the repository mark their resource **orphaned**; it is kept, never deleted. `DELETE /config/ownership/{kind}/{name}` releases a resource to the console; deleting a source releases everything it owned.

**Manual apply and export.** `lcm apply -f` works for unowned resources. For an owned resource it refuses (`skipped`, exit status 3) unless the document equals the current content or `--force` is given; a forced apply leaves the resource owned and drifted, so the repository stays the source of truth. `--dry-run` only validates and diffs. `lcm export` prints canonical YAML in apply order, sorted by name, ready to commit; applying the export back changes nothing. A policy undeployed in the console is not redeployed by a sync unless its document changes.

Opening a pull request from the console is not built; export is the way back to git.
