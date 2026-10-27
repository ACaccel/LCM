# CALM Four-Stage Loop — Testbed Reproduction Runbook

End-to-end procedure for running the CALM loop on a three-machine testbed (one
control machine, one compute machine, one app store) behind a single reachable
host. Commands run on the EFO machine (the repository checkout) unless marked
otherwise.

Fill in the placeholders once; every command below uses them:

```bash
TB_HOST=<testbed host or IP>
TB_USER=<ssh user on the testbed machines>
TB_PASS=<ssh password>            # or use key-based auth and drop sshpass
CTRL_PORT=<ssh port of the control machine>
CLUSTER=<cluster name>            # e.g. clus-01
DEMO=Orchestrator/efo/LCM_Enabler/examples/calm-fourstage
```

Terminology: one **slot** = one `Timer` tick = one full pass of the policy graph
(telemetry → selector → mode → apply → wait → verify).

## 1. Bring up the environment

```bash
bash tb_reset.sh        # reset the three machines and rebuild/start the EFO
```

The final step of `tb_reset.sh` runs the EFO in the foreground; the script does
not return. The environment is up once the HTTP route table is printed and the
EFO listens on `127.0.0.1:8000`. Keep that terminal open and continue in another.

Confirm the running build contains the CALM nodes, then create the cluster and
base services:

```bash
curl -s http://127.0.0.1:8000/api/lcm/nodetypes | grep -o 'CALM_[A-Za-z]*' | sort -u
# expect five types: PlacementApply / PolicyMode / PolicySelector / PostVerify / Telemetry

bash tb_run.sh          # join machines, create the cluster, deploy Jaeger and the app store
```

The longest step is the wait for the control node; after `{"message":"pong"}`
the rest completes quickly. Verify with
`ssh -p $CTRL_PORT $TB_USER@$TB_HOST kubectl get pods -A` that Jaeger is Running.

## 2. Control-machine kubeconfig

The EFO's Jaeger proxy is served by the control node process; when that process
runs as root it resolves its kubeconfig from root's home. Provide one:

```bash
sshpass -p "$TB_PASS" ssh -p $CTRL_PORT $TB_USER@$TB_HOST \
  "echo $TB_PASS | sudo -S sh -c 'mkdir -p /root/.kube && cp /home/$TB_USER/.kube/config /root/.kube/config'"

curl -s "http://127.0.0.1:8000/api/clusters/$CLUSTER/jaeger/api/services"
# expect {"data":["jaeger"],...} rather than an HTTP 500 error
```

Repeat this step after every testbed reset.

## 3. Onboard the demo service

The demo service's OpenTelemetry service name must equal its application name in
the inventory — Jaeger queries are exact-match. `mircoservice/mircoserviceA`
satisfies this; a service whose OTEL name differs will produce no latency samples
and is treated as always-healthy.

```bash
curl -s -X POST -F "file=@mircoservice/mircoserviceA.zip" http://127.0.0.1:8000/api/manifests
curl -s -X POST -H "Content-Type: application/json" \
  -d '{"File_Name": "mircoserviceA.zip"}' http://127.0.0.1:8000/api/images        # slowest step
curl -s -X POST -F "file=@mircoservice/mircoserviceA.yaml" -F "app_Name=mircoserviceA" \
  -F "manifest_Number=1" -F "manifest1=mircoserviceA.zip" http://127.0.0.1:8000/api/apps
curl -s -X POST -H "Content-Type: application/json" \
  -d '{"App_Name":"mircoserviceA","Allocated_CPU":"0.5","Allocated_Memory":"500M","Replicas":"1"}' \
  http://127.0.0.1:8000/api/clusters/$CLUSTER/deployments
curl -s -X POST -H "Content-Type: application/json" \
  -d '{"App_Name":"mircoserviceA","Selected_Nodes":["<worker node name>"]}' \
  http://127.0.0.1:8000/api/clusters/$CLUSTER/services
# each call answers null on success
```

Smoke-test and confirm Jaeger registers the service (the service's ClusterIP is
read with kubectl; the app listens on port 8000):

```bash
sshpass -p "$TB_PASS" ssh -p $CTRL_PORT $TB_USER@$TB_HOST \
  'IP=$(kubectl get svc -n mircoservicea mircoservicea -o jsonpath={.spec.clusterIP});
   for i in $(seq 1 10); do curl -s -X POST -o /dev/null -w "%{http_code}\n" "http://$IP:8000/work?n=5000"; done'
sleep 8
curl -s "http://127.0.0.1:8000/api/clusters/$CLUSTER/jaeger/api/services"
# expect {"data":["jaeger","mircoserviceA"],...}
```

The built-in SLO defaults (`service_rate_per_replica` 200/s, `latency_slo_ms` 30,
`dmr_slo` 0.10) suit this service at the `n=5000` work size; measure a baseline
p99 under light load and adjust via `CALM_CONFIG_FILE` if your service differs.

## 4. Dry run

Verify the loop without touching real replicas:

```bash
bash $DEMO/calm-demo.sh -n 3 -i 20 -c $CLUSTER --dry-run -s normal
```

Each slot prints one line (mode, reward, recovery state, guard verdict, planned
changes). Verify the reward arithmetic by hand:
`reward = −(0.4 × replica_total / 20 + 0.3 × switched) / 2.2` — with two services
at one replica each and no switch this is −0.036 once the plan converges.

## 5. Closed loop, steady state

Keep a light request load running (about 10 req/s), then run without `--dry-run`:

```bash
sshpass -p "$TB_PASS" ssh -p $CTRL_PORT $TB_USER@$TB_HOST \
  'IP=$(kubectl get svc -n mircoservicea mircoservicea -o jsonpath={.spec.clusterIP});
   for i in $(seq 1 3500); do curl -s -m 5 -X POST -o /dev/null "http://$IP:8000/work?n=5000" & sleep 0.1; done; wait' &

bash $DEMO/calm-demo.sh -n 8 -i 30 -c $CLUSTER -s normal
```

Expected: the first slot PATCHes each service to its redundancy floor; later
slots report `applied=-` (target equals current — the loop is idempotent). Cross-
check with `kubectl get deploy -A` on the control machine.

## 6. Overload scenario (real traffic)

Drive a staged load — light, then heavy (larger per-request work at a higher
rate), then light — while the demo runs:

```bash
sshpass -p "$TB_PASS" ssh -p $CTRL_PORT $TB_USER@$TB_HOST '
IP=$(kubectl get svc -n mircoservicea mircoservicea -o jsonpath={.spec.clusterIP})
for i in $(seq 1 900);  do curl -s -m 5  -X POST -o /dev/null "http://$IP:8000/work?n=5000"  & sleep 0.1;   done; wait
for i in $(seq 1 4500); do curl -s -m 10 -X POST -o /dev/null "http://$IP:8000/work?n=80000" & sleep 0.033; done; wait
for i in $(seq 1 1500); do curl -s -m 5  -X POST -o /dev/null "http://$IP:8000/work?n=5000"  & sleep 0.1;   done; wait' &

bash $DEMO/calm-demo.sh -n 12 -i 30 -c $CLUSTER -s normal
```

Expected during the heavy phase: `violated`/`severe` flags, rewards dominated by
the severe term (around −2), real scale-out driven by DMR risk with each mode's
own scale step, mode switches under the severe guard bypass, and the recovery
state advancing to `soft`. After the load falls: scale-in stopping at the active
mode's redundancy floor. Note that the plan stage performs no capacity pre-check;
at the peak Kubernetes may hold excess replicas as Pending pods until scale-in.

## 7. Radio-degrade scenario (injection)

With a light load running (as in step 5):

```bash
bash $DEMO/calm-demo.sh -n 10 -i 30 -c $CLUSTER -s radio-degrade
```

From slot `ceil(n/3)` the driver writes synthetic radio values
(`mean_sinr`, `mean_prb_util`, `radio_masked:false`) into `calm-inject.json`,
which the telemetry stage folds into the context vector.

The learner state survives undeploy: if this run follows an earlier episode with
many switches, the stability guard's switch budget may keep blocking non-severe
switches. For a cold learner, restart the EFO first.

## 8. GUI

```bash
cd Orchestrator/efo/LCM_Enabler/frontend && npm run dev    # :5173, proxies /api to :8000
```

Open the Policy Designer (hard-reload after a backend change — the node schema is
cached per session). The palette lists `Timer` and the five CALM nodes; the
`calm-fourstage` service opens as the arranged four-branch graph; the RunPanel
shows per-node inputs and outputs, including the four arm scores, the guard
verdict and the reward breakdown.

## Troubleshooting

| Symptom | Cause / action |
|---|---|
| Jaeger endpoints answer HTTP 500 | Step 2 not applied (required after every reset) |
| Driver exits with "EFO binary predates the CALM nodes" | Rebuild and restart the EFO |
| Every slot shows the fallback mode with `reward=n/a` | Telemetry failed: wrong cluster name, control-plane or Jaeger unreachable; read `telemetry_reason` in the trace |
| Jaeger never lists the demo service | No traffic sent yet, or the OTEL service name differs from the application name |
| Mode never switches, guard always blocked | Learner state persisted from an earlier episode; restart the EFO for a cold start |
| Per-node detail needed | `curl ".../api/lcm/service/calm-fourstage/policy/calm-loop/run?since=0" \| jq` — the trace holds the latest run only, read it before the next tick |
| PATCHed pods stay Pending | Requested replicas exceed node capacity (no capacity pre-check); resolves on scale-in |

## References

- Node roles, cross-slot state, configuration, extension points:
  `Orchestrator/efo/Rule_Framework/README.md`, section "CALM four-stage loop"
- API surface and examples index: `Orchestrator/efo/LCM_Enabler/README.md`
