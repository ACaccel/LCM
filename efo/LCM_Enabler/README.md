# LCM Enabler

![lcm_architecture](./assets/LCM_api.png)

The **LCM Enabler** is the design-time component of the LCM feature: it owns the design
and management of edge-application lifecycle **policies** (services → policies →
triggers + function nodes + connections), stores policy designs, and exposes the
`/api/lcm` REST API used by the LCM GUI (`./frontend/`) and by API clients.

Policy runtime execution lives in the **Rule Framework** (`../Rule_Framework/`); the
LCM Enabler delegates all runtime operations to it through the `IRule_framework`
interface. See `../Rule_Framework/README.md`.

Configuration (`./config`): `LCM_UPLOAD_DIR` and `LCM_TEMPLATES_DIR` override the
upload and template directories; the control-plane base URL used by action nodes is
`LCM_CONTROL_PLANE_BASE_URL` (in `../Rule_Framework/config`). All settings have
built-in defaults.

> **Policy schema note:** a policy has no `type` field. A webhook trigger subscribes
> to an event topic via its `property.event_type`; an incoming webhook event is
> dispatched to every deployed webhook trigger whose `event_type` matches.

## APIs

- Service
  - GET `/api/lcm/service` — list registered service names
  - GET `/api/lcm/service/:service_Name` — a service's stored config
  - POST `/api/lcm/service` — register a service (JSON body)
  - PUT `/api/lcm/service` — reconfigure a service (only with no deployed policies)
  - POST `/api/lcm/service/file` / PUT `/api/lcm/service/file` — register or
    reconfigure from a config file (`-F "file=@config.yaml"`). Both answer
    `{"msg","service_name","policies"}` with `policies` in document order; a YAML
    parse failure is a 400 carrying the parser's line detail. An annotated starter
    config lives at `examples/lcm-test/config-upload-example.yaml`.
  - POST `/api/lcm/service/:service_Name/file` — upload manifest files
  - DELETE `/api/lcm/service/:service_Name` — delete a service

- Policy
  - GET `/api/lcm/service/:service_Name/policy/:policy_Name` — policy info
    `{Name, Status, Config}`
  - POST `/api/lcm/service/:service_Name/policy` — add a policy (PolicyConfig body)
  - PUT / DELETE `/api/lcm/service/:service_Name/policy/:policy_Name` — reconfigure
    or delete
  - POST `/api/lcm/service/policy/deploy` and `.../undeploy` — body
    `{"SvcName": "...", "Policies": ["..."]}`

- Node catalogue
  - GET `/api/lcm/nodetypes` — trigger and function types plus a `schemas` list:
    property fields with types, legal values and conditional visibility, and the
    field names of each port. A port marked `dynamic` has a runtime-defined shape;
    a property of type `object[]` carries an `item` row description rendered as a
    repeatable editor; a `fieldRef` property names a key of the payload arriving on
    `fieldRefPort`. Port counts here are authoritative for the designer, except
    `Module`, whose arity is declared per instance.

- Execution trace
  - GET `/api/lcm/service/:service_Name/policy/:policy_Name/run?since=` — the latest
    run of a deployed policy: executed nodes, inputs and outputs, ports taken,
    stalled branches. `since` enables incremental polling; reset it to 0 when
    `runId` changes. A deployed policy that has never been triggered answers 200
    with `hasRun: false`.

- Module
  - GET / POST `/api/lcm/service/:service_Name/module`, GET / PUT / DELETE
    `/api/lcm/service/:service_Name/module/:module_Name` — list, add, read, replace,
    remove. Renaming is refused (policies refer to modules by name); removal is
    refused while a policy or another module still instantiates it. A module is
    validated by building it, so save time gives exactly the verdict deploy time
    would. Which module a `Module` node instantiates comes from `property.module`,
    defaulting to the node's own name.

- Node config
  - Any `node` or `trigger` entry may set `fill_property_from_input: true`: property
    fields still empty after decoding (`nil`, `""`, empty slice/map — not numeric 0
    or `false`) are filled from the first input packet, matched by yaml tag then Go
    field name.

- Connection data
  - Each `path` entry may carry `carry` (which upstream namespaces continue
    downstream, in clash-resolution order; empty forwards everything) and
    `transFormat` (field renames for the receiving node only; a source may be
    namespace-qualified, e.g. `webhook-0.Value`). See the payload model in
    `../Rule_Framework/README.md`.

- Webhook
  - POST `/api/lcm/webhook` — body `{"SvcName","EventType","Content"}`; dispatches to
    subscribed deployed triggers and answers `{"dispatched": N}`.

- Template
  - POST `/api/lcm/service/template` — body `{"SvcName","TemplateName"}`; appends a
    stored template's policies to a registered service. Template files live in the
    templates directory, matched by file basename; a template whose `service_name`
    is `default` is usable by any service, otherwise only by the named one.

## GUI

`./frontend/` is a React + TypeScript + React Flow single-page app for designing and
managing policies against this API. Development: `cd frontend && npm install &&
npm run dev` (proxies `/api` to the EFO on `:8000`).

The Services header offers a config upload that drives the file endpoints: it
registers the service and opens its first policy on the canvas. If the name is
already registered it asks before switching to the reconfigure endpoint, which
replaces the stored config and is refused while the service has deployed policies.

### Canvas layout

A policy and a module each carry an optional `ui` block recording node positions,
keyed by canvas node id (a node's name; `__in:N` / `__out:N` for module boundary
ports):

```yaml
ui:
  node:
    webhook-0: { x: 120, y: 40 }
```

The engine never reads it, it is optional everywhere, and it is advisory: unknown
keys are dropped, nodes without a key are placed automatically, and deleting the
block restores the automatic layout. Deploying and undeploying do not touch it.

## Examples

- `examples/lcm-test/` — a curl-based functional test of the LCM API surface
  (`run.sh` plus example configs).
- `examples/calm-fourstage/` — a complete deployable reference for the CALM
  four-stage adaptive loop: the policy document (`config-calm.yaml`, including
  a canvas layout), the demo driver (`calm-demo.sh`, self-locating, with
  `--dry-run` and scenario switches), and a step-by-step reproduction runbook
  (`REPRODUCE.md`). `calm-inject.json` appears beside them at runtime; it is the
  driver-managed scenario injection file, not a source file. Engine-side
  documentation for the loop lives in `../Rule_Framework/README.md`.

Note that `templates/` is not an examples folder: files there are live policy
templates served by `POST /api/lcm/service/template`.
