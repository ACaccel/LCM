# Rule Framework

The **Rule Framework** is the runtime component of the LCM feature: it executes the
policies that the **LCM Enabler** (`../LCM_Enabler/`, design and management) defines.
The LCM Enabler holds the engine through the `IRule_framework` interface, injected at
construction in `main.go`. Dependency direction is acyclic: `LCM_Enabler/interfaces`
(design model, leaf) ← `Rule_Framework/interfaces` (node ABI + `IRule_framework`) ←
`Rule_Framework/{policy,nodes}` (engine).

## Layout

- `rule_framework.go` — engine facade implementing `IRule_framework` (`Deploy`,
  `Undeploy`, `IsDeployed`, `DeployedPolicies`, `ExecuteWebhook`, `LatestRun`,
  `TerminateService`); owns the registry of running policies per service.
- `interfaces/` — the node-execution ABI (`IPolicyNode`, `ExecInfo`, `NodeExecResult`,
  `NodeDescription`, `JoinMode`, …) and `IRule_framework`.
- `policy/` — the policy engine: `Policy` (lifecycle), `PolicyList`/`ListNode` (node
  DAG), `DataPacket` (inter-node data and transforms), `Module` (reusable sub-graphs),
  `Trace` (execution recorder).
- `nodes/` — the node library:
  - `Triggers/`: `Webhook` (subscribes to an `event_type`) and `Timer` (fires every
    `interval_seconds`; a tick arriving while the previous run is still active is
    dropped, never queued).
  - `Functions/`: flow nodes `If`, `Compare`, `Merge`, `Wait`; the generic
    `HTTPRequest`; the `IEEE1935_*` action nodes (VF M&O-semantic wrappers over the
    EFO control plane); and the `CALM_*` nodes forming the four-stage adaptive loop
    (see below). `Module` is built by `Policy.Init` from the service's module config
    rather than registered here.
  - `Functions/calm/`: the CALM shared package — LinUCB selector, context builder,
    stability guard, recovery manager, reward calculator, autoscaler, collection
    helpers, and the per-service cross-slot state store.
  - `Functions/httpnode/`, `nodes/utils/`: shared HTTP and encode/decode helpers.
- `config/` — environment-driven settings: `LCM_CONTROL_PLANE_BASE_URL` (default
  `http://localhost:8000`, injected into nodes as `ExecInfo.ControlPlaneBaseURL`),
  `LCM_TRACE_MAX_EVENTS`, `LCM_MAX_WAIT_SECONDS`, `LCM_HTTP_TIMEOUT_SECONDS`.

## Policy lifecycle

A deployed policy runs one `Policy.Execute` goroutine plus one goroutine per trigger.
Triggers wait for their own events and hand them to the loop; the loop owns the
policy's lifecycle and its single set of data packets. `Undeploy` closes a shutdown
channel that reaches every goroutine — including a trigger parked mid-handover — and
waits for them to exit, so undeployment is complete when the call returns.

A run is retired in a fixed order: **cancel, wait, reset**. Each run owns a context
whose cancellation releases every walker at each point one can block, a `WaitGroup`
confirms the walkers have left, and only then are the packets reset. Two consequences:

- **Runs are serialized.** A new trigger event waits for the previous run to finish.
  A node that ignores cancellation (an HTTP call without a context) delays the next
  event for as long as it blocks; long-running nodes should select on
  `ExecInfo.RunDone` (`utils.RunContext`).
- **Untaken branches do not leak.** A parked walker — the unrouted side of an `If`,
  an input that never arrives — is released when the run is retired.

## Webhook dispatch

Each webhook trigger's `event_type` is registered at deploy time; an empty or
undecodable value fails the deploy. `ExecuteWebhook(svc, eventType, content)` pushes
the content to every subscribed trigger of the service's deployed policies and
returns the dispatch count.

## Execution trace

Each deployed policy owns a `Recorder` holding the **latest run only**; a new trigger
event replaces the previous run. Events are captured at the single point both the
top-level and module walkers pass through, so module internals appear too,
distinguished by a `scope` path. Every mutation carries its run id, so stragglers
from a superseded run are dropped. A run is complete exactly when no walker is
running and none can be released (`inflight - blocked == 0`); permanently parked
nodes are reported in `waiting`.

## The payload model

What travels on a connection is one namespace per executed node, keyed by node name.
A node emits only its own fields; the engine files them under the node's name and
carries the upstream namespaces along. Nodes never see this form: each node receives
a flattened view and returns a flat map. Flattening folds the namespaces in a
deterministic order — the edge's `carry` list when set, otherwise the namespaces
sorted by name with the immediate upstream last — so the same policy always resolves
a field-name clash the same way.

`If`, `Compare`, `Merge` and `Wait` decide routing or timing and emit nothing; what
arrived travels on unchanged, and a rejoin delivers the namespaces of every input
port. These nodes are marked `Passthrough` in their schema.

A module is a namespace boundary in both directions: entry nodes receive the
flattened payload filed under `__in:N` per input port, and the module's result is
flattened again before the enclosing walker files it under the module node's name,
so inner node names never leak into the enclosing policy.

## Connections

Each connection carries two independent, optional settings:

- `carry` — which upstream namespaces continue downstream, in clash-resolution
  order. Empty forwards everything.
- `transFormat` — field renames applied for the receiving node only. A source may be
  a bare field name or namespace-qualified (`webhook-0.Value`) to reach a value the
  flattening shadowed.

A port holds exactly one connection; a second edge on the same output or input port
is rejected at deploy time.

## Join modes

By default a node runs once every connected input port has been produced (`JoinAll`).
`JoinAny` runs on the first arrival and skips the remaining ports; it is required
after a branching node, since only one branch runs. `Merge` chooses between the two
behaviors from its `mode` property via the `JoinModeDecider` interface.

Readiness is not routing: an executed node marks **every** output port ready, not
only the ports it selected. A downstream node cannot infer which branch ran from
token arrival and must read the payload instead.

## Modules

A module is a sub-graph defined once per service and instantiated in policies as a
node of type `Module`; `property.module` names which one, defaulting to the node's
own name. Construction refuses configurations that cannot run — a module that
reaches itself, or an unwired boundary port — and `ValidateModule` gives the design
layer the same verdict at save time by building the module and discarding it.

`Module.Execute` enters at every wired input port; ports holding `nil` are skipped.
One inner node can back several module output ports, and can be an exit and an
ordinary node at once. The inner walk completes when nothing is runnable — every
walker finished or parked on an input no remaining walker will produce — after which
parked walkers are cancelled so the next run finds the inner packets untouched.

## Node schema

`nodes/Functions/schema.go` and `nodes/Triggers/schema.go` publish a design-time
schema for every node: property fields, types, legal values, conditional visibility,
and the field names of each port. Field names are derived by reflection from the
same tags the runtime decodes with, so the schema cannot drift from the wire. Only
what reflection cannot recover — enums, request-conditional properties, secret and
file-path hints, hand-declared dynamic ports — is written in the metadata table.
Adding a node means one entry in `registrations` plus optional metadata.

## Action nodes

The `IEEE1935_*` nodes call the EFO control plane over HTTP using
`ExecInfo.ControlPlaneBaseURL`; `HTTPRequest` is the generic form taking method, URL,
body and headers from its properties. All share one client with a configurable
timeout so an unresponsive endpoint cannot hold a walker for the life of the process.

## Branching: `If` and `Compare`

`If` evaluates a condition list joined by one combinator, routing to port 0 on true
and port 1 on false; the port is the verdict and nothing is added to the payload.
`Compare` is the two-input form: each condition names a field of input 0 and a field
of input 1. In both, a missing or non-numeric field fails its condition (including
under `!=`) and an empty condition list routes to false. A condition's field may be
qualified as `node.field` when two upstream nodes emit the same name.

## CALM four-stage loop

**CALM** (Context-Aware Lifecycle Management) is an adaptive control loop expressed
entirely as a policy graph: a `Timer` trigger supplies the clock, and five `CALM_*`
nodes carry the four stages — telemetry, decision, placement, post-verification.
The decision stage selects one of four policy modes per slot with a LinUCB
contextual bandit, guarded by a stability mechanism and backed by a recovery state
machine. All shared logic lives in `nodes/Functions/calm/`; the nodes are thin
shells over it.

### The graph

```
timer-0        Timer                   one tick = one slot
   |
   v
telemetry-0    CALM_Telemetry          read the cluster and Jaeger once; publish one
   |                                   flat state vector plus svc_<name>_* detail
   v
selector-0     CALM_PolicySelector     fold in the previous slot's reward, build the
   | | | |                             context, ask the stability guard, run LinUCB;
   | | | |                             the chosen arm IS the output port
   v v v v
mode-latency / mode-balanced / mode-energy / mode-resilient
               CALM_PolicyMode x4      one instance per mode, each carrying its own
   | | | |                             thirteen knobs; produces a placement plan
   v v v v
apply-0        CALM_PlacementApply     4 in-ports, JoinAny; writes the plan's replica
   |                                   targets through PATCH .../services/{service}
   v
wait-0         Wait                    let the placement take effect
   |
   v
verify-0       CALM_PostVerify         re-measure, judge against the SLO, advance the
                                       recovery state machine, compute the reward
```

The selector's four output ports are the four modes in canonical order —
`latency_priority` 0, `balanced` 1, `energy_saving` 2, `resilient` 3 — which is also
the LinUCB arm order and the context vector's one-hot layout; snapshots are only
meaningful under this ordering. Only one mode branch runs per slot, so
`CALM_PlacementApply` fixes `JoinAny` and reads `ExecInfo.ActiveInPort` only. Every
connection leaves `carry` and `transFormat` empty, which forwards all upstream
namespaces and lets `verify-0` read fields published by `telemetry-0` four hops
upstream.

### Cross-slot state

Four feedback edges close the loop across slots — the severe-violation guard bypass,
the violation rate, the previous mode, and the reward that trains the bandit. A run
walks forward only, so none of them can be a graph edge; they live in the `calm`
store: one `LoopState` per service name, holding the learner, the guard, the
recovery state machine, the slot counter and the pending decision, behind its own
mutex.

The store is memory only. An EFO restart resets every learner; undeploy does not, so
redeploying the same service name resumes the learner. The selector's optional
`state_file` property writes a version-guarded JSON snapshot after every committed
decision and restores it once at startup.

The reward computed by `CALM_PostVerify` at the end of slot *k* is folded into
LinUCB by the selector at the start of slot *k+1*, before the new context is built —
equivalent to an end-of-slot update, since nothing between those points reads the
learner. A slot is consumed only when the selector commits a decision; a run held
for unavailable telemetry emits the previous mode and does not advance the slot
counter, so the guard's dwell and switch budget count decisions, not trigger events.

### Configuration

Every value has a built-in default, so the nodes run with no external file.
`CALM_CONFIG_FILE` names an optional YAML overlay; anything it omits keeps its
default, down to a single knob of a mode or a single field of a service row. An
unreadable or malformed file, or an unrecognised key, is a load-time error rather
than a silent fallback. The defaults are calibrated at simulation scale: in
particular `service_rate_per_replica`, `latency_slo_ms` and `dmr_slo` decide whether
the autoscaler ever fires and should be set per service for a given deployment.

### Extension points

Each item is a deliberate simplification with the structure already in place:

- **Radio KPIs.** `calm.RadioCollector` ships with a stub that returns a masked
  sample; the context builder substitutes a neutral 0.5 for the four radio
  dimensions while masked. A real RAN feed implements the interface; the context
  layout does not change. Synthetic values can be injected through
  `CALM_Telemetry`'s `inject_file`.
- **Energy.** `calm.EnergyCollector` likewise ships as a stub, so the reward uses a
  replica-count proxy (`C_energy = replica_total / replica_capacity`). A power feed
  (IPMI/RAPL/PDU) implements the interface; the reward formula structure is
  unchanged.
- **`queueing_headroom_ms`** (`L − 1/μ − W_radio − W_ho`, the latency budget left
  for queueing) defaults to `+Inf`, since computing it needs a per-service latency
  decomposition. Setting a calibrated value re-enables the conditional `dmr_risk`
  term, the offload double threshold, and the migration benefit guard.
- **Migration, offload, consolidation, radio compression.** The plan structure and
  the mode knobs carry them, but the plan builder produces empty sets; a non-empty
  set is reported by `CALM_PlacementApply` as `unapplied_actions` rather than
  silently dropped.
- **One decision domain.** A deployed policy reads and scales a single cluster
  (`CALM_Telemetry`'s `target_cluster`); multiple clusters are served by deploying
  one policy per cluster.

### Known limitations

- The plan stage performs no capacity pre-check: a plan may request more replicas
  than the cluster can host, in which case Kubernetes holds the excess as Pending
  pods until a later scale-in.
- The Jaeger read path goes through the EFO's cluster proxy; the control node
  resolves its kubeconfig from its own process user, so a control node running as
  root requires a kubeconfig at `/root/.kube/config` on the control machine.

### Demo

A complete deployable example — policy document, demo driver and reproduction
runbook — lives at `../LCM_Enabler/examples/calm-fourstage/`. Prerequisites: an EFO
build containing the CALM nodes on `:8000` (the driver verifies this via
`GET /api/lcm/nodetypes` and refuses to deploy otherwise), a demo application
registered in the inventory and deployed on the target cluster, Jaeger reachable
through the cluster proxy, and `curl` + `jq`.

```
calm-demo.sh --dry-run -n 5 -i 20    # rehearsal: plans, no PATCH
calm-demo.sh -n 12                   # steady state
calm-demo.sh -n 12 -s radio-degrade  # synthetic low SINR, high PRB
calm-demo.sh -n 12 -s overload       # synthetic DMR above the SLO
```

The driver deploys the policy, rewrites the injection file per scenario, prints one
line per slot (mode, reward, recovery state, guard verdict, applied changes) and
ends with a summary and the cluster's deployment table. The trace endpoint
(`GET /api/lcm/service/<svc>/policy/<policy>/run`) returns the latest run only, so
the driver harvests each slot as soon as the verify stage reports; the GUI RunPanel
shows the same trace per node, including all four arm scores, the guard verdict and
the reward breakdown.
