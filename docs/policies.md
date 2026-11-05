# Policies

A policy is an `lcm/v2` `Policy` document: a graph of nodes that one trigger starts. Triggers watch signals (check, health, lifecycle, alert and config sync changes of any [resource](resources.md)), git branches, cron schedules and webhooks; other nodes branch, wait, call HTTP and CI APIs, notify, keep state and locks, and run service lifecycle actions through the same manager as the console buttons. The node types and their props come from the server (`GET /api/v1/node-types`, the editor's palette); the execution rules are in [`internal/engine/doc.go`](../internal/engine/doc.go) and summarized in [architecture.md](architecture.md#engine-semantics).

Policies are edited in the console's graph editor or as YAML, validated by the server, and take effect when deployed. The editor keeps node positions in the annotation `lcm.dev/layout`, which the engine ignores. Saving a deployed policy deploys it, so the editor first shows the diff against the running version; Ctrl+Z takes back the last deleted nodes or edges while nothing else has changed. Every run is recorded node by node and can be watched live; `lcm run <policy> --dry-run` (or the editor's dry run) plans write nodes instead of acting.

A service's *Policy* tab lists the deployed policies that act on it or listen to it. The server resolves the `resource` prop of `lifecycle.*` nodes when it is a literal or uses `params` only (the templates' `{{ params.resource }}`, including static module parameters); a node whose target depends on the trigger event is shown as decided at run time.

## Resources in policies

- `trigger.signal` filters on `kinds`, `resources` (names), `types` (`service`, `machine`, `router`), `labels`, `severities` and `statuses`. Health changes of machines and routers are `health.changed` signals like those of services. Config sync problems with resources are `resource.config.conflict` / `resource.config.invalid` signals, not `alert`.
- `lifecycle.*` nodes name their target in the prop `resource` (a service). Their output and the action result carry `resource`. The prop `acknowledgeImpact: true` allows a disruptive action on a service that other managed resources depend on or are controlled through; without it the node fails permanently, also in dry runs ([impact](resources.md#relations)).
- The expression variable `resource` holds every managed and retired resource by name ([fields](architecture.md#engine-semantics)), for example `resource[params.resource].health`.
- Machine and router power operations have no node: policies cannot reboot or shut down anything but services.

## Templates

Four built-in templates ([`examples/templates/`](../examples/templates), embedded in the binary) cover the common loops. Pick one from *套用範本* on a service's Policy tab or *從範本建立* on the policy list, or use the API. The target service is the parameter `resource`.

| Template | What it does |
|---|---|
| `auto-deploy` | New commit on a branch -> optional GitHub CI check (`ci.status`, optionally by workflow file) -> deploy window -> `lifecycle.deploy` -> wait for the service to turn healthy -> otherwise `lifecycle.rollback`; one message per deployment, on Discord or a konata relay, shows the stages as they finish |
| `scheduled-backup` | Cron (default `Asia/Taipei`, runs once after downtime) -> `lifecycle.backup` with retry and backoff -> optional wait for a heartbeat check -> notify on failure |
| `post-deploy-verify` | After any deployment of the service, wait for health and roll back on timeout |
| `restart-on-down` | Health turns `down` -> restart -> wait for recovery -> notify. It has no rate limit: a service that keeps failing is restarted every time |

```sh
curl -H "Authorization: Bearer $LCM_TOKEN" localhost:7800/api/v1/templates          # parameters of each template
curl -H "Authorization: Bearer $LCM_TOKEN" -X POST localhost:7800/api/v1/templates/restart-on-down/instantiate \
     -d '{"name":"web-restart","params":{"resource":"web","notifySecret":"discord-ops"},"dryRun":true}'
```

- The result is an ordinary policy. Its parameters sit in `spec.params` and the nodes read `params.*`, so changing one later means editing the document. Trigger props cannot be expressions, so what a trigger uses (repository, branch, schedule, watched service) is written into the trigger.
- The target service needs at least one critical health check: a service counts as recovered only when it is `healthy` with check results newer than the deployment or restart.
- `notifySecret` names a secret holding a Discord webhook URL; leave it empty to send nothing.
- `auto-deploy` can report to a [BotFleet](https://github.com/ACaccel/pleiades-fleet) konata notification relay instead: set `notifyTarget: konata`, `notifyUrl` to the route (`http://127.0.0.1:<NOTIFY_API_PORT>/notifications/<route>`, the route defined in konata's `notificationRoutes`) and `notifyTokenSecret` to a secret holding konata's `NOTIFY_API_KEY`. Each run gets its own message; konata shows the stages (CI, deploy window, build and install, health check, rollback) and colours it by the outcome.
- A rollback needs an earlier successful deployment recorded by LCM: deploy a known-good version once (`lcm resource action <service> deploy --version <sha>`) before relying on `auto-deploy`.
- `restart-on-down` needs a runtime that can restart the service ([services](services.md#runtime)); on an `external` service the restart is refused.

## Policy-as-code

The console and API are the default way to configure LCM and take effect at once. Git management is opt-in per document: resources, policies and modules can also live as YAML in a repository that LCM pulls and applies. Documents describe resources only; command allowlists, the `systemctl` privilege prefix and the units LCM may touch stay in host configuration.

Register a repository as a **config source** (admin API, or *設定 > 設定來源* in the console):

```sh
curl -H "Authorization: Bearer $LCM_TOKEN" -X POST localhost:7800/api/v1/config/sources \
     -d '{"name":"infra","repo":"git@host:me/infra.git","branch":"main","path":"lcm","interval":"1m"}'
```

**Sync.**

- LCM fetches the branch into its own clone under `<data dir>/sources`, reads every `*.yaml` and `*.yml` under `path` (recursively; several documents per file; `*.test.yaml` skipped), validates, and applies modules, then resources, then policies.
- It syncs every `interval` (default 1m, 10s to 24h), on `POST /config/sources/{name}/sync` (202 at once; read `lastSyncAt`, `lastCommit`, `lastError` and `syncing`; concurrent requests join the running sync), and when a GitHub push webhook (`/hooks/github`) names the repository and branch.
- A document that fails validation leaves its resource unchanged; the error is on the source (`lastError`), on the resource timeline (`config.invalid`) and published with source `configsync`: a `resource.config.invalid` signal for a Resource, a warning `alert` signal for a Policy or Module. `lcm/v1` documents fail this way; convert them (below).
- The repository URL (of a source or a `trigger.git` node) must not embed credentials: `https://token@host/...` is refused like `user:password@`, and a URL with a scheme may only carry the ssh user `git` (`ssh://git@host/...`); scp-like `git@host:path` is accepted. Sources stored before this rule are shown without their userinfo; replace them with a credential-free URL. Use an SSH key without passphrase (git runs with `BatchMode`, and children get a short environment allowlist, so `SSH_AUTH_SOCK` is unavailable) or a credential helper.

**Ownership.** Documents applied from a source are owned by it (`managedBy` in resource and policy views, `GET /config/ownership`, kinds `resource`, `policy`, `module`). A resource belongs to one source; a second claim is refused. A source never overwrites a resource without an owner (one made in the console or API): if the document equals the resource the source adopts it, which is how resources move under git (`lcm export`, commit, register); if it differs, nothing is written and the claim is a conflict.

**Drift and conflicts.** Editing an owned resource in the console or API works at once and makes it **drifted** (its content differs from what the repository last said). Nothing is overwritten silently:

- When a new commit changes the document of a drifted resource, or defines a different document for an unowned one, the sync skips it, marks a **conflict** and notifies (a `config.conflict` timeline entry and a `resource.config.conflict` signal for resources; a warning `alert` signal for policies and modules). Documents unchanged in the repository never conflict.
- `POST /config/ownership/{kind}/{name}/resolve` with `{"use":"git"}` writes the repository document and drops the local edits; `{"use":"current"}` keeps the current content, owns it and closes the conflict (it stays drifted, acknowledged). Drift clears by itself once the repository matches.
- A sync or apply writes a document only if it is still at the version the sync read: when the console or API changes it in between, that write fails with a version conflict (the source's `lastError` and the apply result) and the next sync sees the edit as drift.
- Documents that disappear from the repository mark their resource **orphaned**; it is kept, never deleted. `DELETE /config/ownership/{kind}/{name}` releases a resource to the console; deleting a source releases everything it owned.

**Manual apply and export.** `lcm apply -f` works for unowned resources. For an owned resource it refuses (`skipped`, exit status 3) unless the document equals the current content or `--force` is given; a forced apply leaves the resource owned and drifted, so the repository stays the source of truth. `--dry-run` only validates and diffs. `lcm export` prints canonical YAML in apply order, sorted by name, ready to commit; applying the export back changes nothing. A policy undeployed in the console is not redeployed by a sync unless its document changes.

**Converting `lcm/v1` documents.** `lcm convert -f <file|dir>` rewrites `Service`, `Machine`, `Policy` and `Module` documents as `lcm/v2` offline and prints them; `lcm/v2` input passes through unchanged. It maps the runtime `type` to `adapter`, moves runtime, `source`, `backup` and `jobs` (or machine `monitoring`) into `config`, turns `dependsOn` into relations and drops `host`. In policies and modules it renames the `lifecycle.*` prop `service` and the `trigger.signal` prop `services`, and rewrites the `service` expression variable, the signal field `trigger.service` (also `trigger?.service` and `trigger["service"]`) and `nodes.<id>.service` of lifecycle nodes; `params.service` and string literals are left alone. It adds `acknowledgeImpact: true` to `lifecycle.stop`, `restart`, `deploy`, `rollback` and `run-job` nodes that do not set it, because v1 had no impact guard, and warns once per document; remove it where the impact should stop the node. A `lock`/`unlock` name with the literal prefix `service:` becomes `resource:`, the lock lifecycle actions now hold. References it cannot rewrite (a name assembled from strings, a computed lock name with `service:`, any other `service` member) are printed as warnings and need review; exit status 3 means some documents did not convert. Convert a whole source in one commit, for example `lcm convert -f lcm/web.yaml > web.yaml.new && mv web.yaml.new lcm/web.yaml` per file; converted documents equal the upgraded stored ones, so no drift appears ([upgrade](operations.md#upgrade-to-lcmv2-resources)).

Opening a pull request from the console is not built; export is the way back to git.
