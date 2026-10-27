#!/usr/bin/env bash
#
# Complete LCM functionality test harness (manual; not committed).
# Default flow uses config-logic.yaml (webhook + If + Module) and needs only the
# EFO running on :8000 (no control node). The IEEE1935 action-node pipeline is
# gated behind RUN_IEEE1935=1 (needs a live control node + app store).
#
# Usage:
#   bash Orchestrator/efo/LCM_Enabler/examples/lcm-test/run.sh                 # core logic flow, PASS/FAIL only
#   bash Orchestrator/efo/LCM_Enabler/examples/lcm-test/run.sh -v              # also print request payload + response body
#   VERBOSE=1 bash Orchestrator/efo/LCM_Enabler/examples/lcm-test/run.sh       # same as -v
#   HOST=1.2.3.4 PORT=8000 bash ...      # override target
#   RUN_IEEE1935=1 bash Orchestrator/efo/LCM_Enabler/examples/lcm-test/run.sh  # also run the action-node pipeline
#   LOGFILE=/tmp/lcm.log bash ...        # tee the whole transcript to a file
#
# Response bodies are pretty-printed with jq when it is available.
set -uo pipefail

VERBOSE="${VERBOSE:-0}"
case "${1:-}" in
  -v|--verbose) VERBOSE=1 ;;
  -h|--help) awk 'NR>1 && /^#/ {print; next} NR>1 {exit}' "$0"; exit 0 ;;
esac

HOST="${HOST:-localhost}"
PORT="${PORT:-8000}"
BASE="http://${HOST}:${PORT}/api/lcm"
DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
RESP="$(mktemp)"
trap 'rm -f "$RESP"' EXIT

# Tee the transcript when LOGFILE is set, so a verbose run can be reviewed later.
if [ -n "${LOGFILE:-}" ]; then
  exec > >(tee "$LOGFILE") 2>&1
fi

HAVE_JQ=0
command -v jq >/dev/null 2>&1 && HAVE_JQ=1

PASS=0
FAIL=0

# show_body : print the last response body, pretty-printed when it is JSON
show_body() {
  local body
  body="$(cat "$RESP")"
  if [ -z "$body" ]; then
    printf '      res : <empty>\n'
    return
  fi
  if [ "$HAVE_JQ" = 1 ] && printf '%s' "$body" | jq -e . >/dev/null 2>&1; then
    printf '%s' "$body" | jq . | sed 's/^/      res | /'
  else
    printf '      res | %s\n' "$body"
  fi
}

# req <expected_http_status> <method> <path> [extra curl args...]
req() {
  local expect="$1" method="$2" path="$3"
  shift 3
  local code elapsed
  read -r code elapsed < <(curl -sS -o "$RESP" -w '%{http_code} %{time_total}' \
    -X "$method" "$@" "${BASE}${path}" 2>/dev/null || echo "000 0")
  if [ "$code" = "$expect" ]; then
    PASS=$((PASS + 1))
    printf 'PASS  %-6s %-46s -> %s  %ss\n' "$method" "$path" "$code" "$elapsed"
  else
    FAIL=$((FAIL + 1))
    printf 'FAIL  %-6s %-46s -> %s (want %s)\n' "$method" "$path" "$code" "$expect"
  fi
  if [ "$VERBOSE" = 1 ]; then
    [ "$#" -gt 0 ] && printf '      req : %s\n' "$*"
    show_body
  elif [ "$code" != "$expect" ]; then
    show_body
  fi
}

# wait_for <substring> <path> : poll a GET until the body matches, then count a PASS
# The webhook POST returns as soon as the event is dispatched, so the run trace
# fills in asynchronously and has to be polled the way the GUI polls it.
wait_for() {
  local want="$1" path="$2"
  local tries=0
  while [ "$tries" -lt 40 ]; do
    curl -sS -o "$RESP" "${BASE}${path}" >/dev/null 2>&1
    if grep -q -- "$want" "$RESP"; then
      PASS=$((PASS + 1))
      printf 'PASS  wait   %-46s (found %s)\n' "$path" "$want"
      [ "$VERBOSE" = 1 ] && show_body
      return
    fi
    sleep 0.2
    tries=$((tries + 1))
  done
  FAIL=$((FAIL + 1))
  printf 'FAIL  wait   %-46s want %s (timed out)\n' "$path" "$want"
  show_body
}

# assert_body <substring> <label> : check the last response body
assert_body() {
  # Fixed-string match: every caller asserts a literal JSON fragment, and JSON
  # brackets are regex metacharacters that made grep error out as a false FAIL.
  if grep -qF -- "$1" "$RESP"; then
    PASS=$((PASS + 1))
    printf 'PASS  assert  %-46s (found %s)\n' "$2" "$1"
  else
    FAIL=$((FAIL + 1))
    printf 'FAIL  assert  %-46s want %s\n' "$2" "$1"
    show_body
  fi
}

json='-H'
CT='Content-Type: application/json'

echo "== LCM functionality test against ${BASE} (config-logic.yaml, verbose=${VERBOSE}) =="

# --- register + inspect ---
req 200 POST /service/file -F "file=@${DIR}/config-logic.yaml"
# The upload response names what was created so the GUI can open it directly.
assert_body '"service_name":"svc-logic"' "upload response names the service"
assert_body '"policies":["policy-1"' "upload response lists policies in document order"
# Registering the same name again must refuse; the GUI's overwrite-confirm
# flow keys off this message.
req 400 POST /service/file -F "file=@${DIR}/config-logic.yaml"
assert_body 'already exist' "duplicate register names the conflict"
req 200 GET  /service
req 200 GET  /nodetypes
req 200 GET  /service/svc-logic
req 200 GET  /service/svc-logic/policy/policy-1

# --- reconfigure (allowed: no deployed policies) ---
req 200 PUT  /service/file -F "file=@${DIR}/reconfig.yaml"
# PUT replaces the whole service config, so restore the full one: the deploy
# section below expects every policy from config-logic.yaml to exist.
req 200 PUT  /service/file -F "file=@${DIR}/config-logic.yaml"
assert_body '"service_name":"svc-logic"' "reconfigure response names the service"

# --- upload error handling ---
# No file part at all: used to nil-deref and take the whole EFO down.
req 400 POST /service/file
req 400 PUT  /service/file
# A parse failure must keep its prefix and carry the yaml line detail.
BAD_YAML="$(mktemp)"
printf 'service_name: [broken\npolicy: {' > "$BAD_YAML"
req 400 POST /service/file -F "file=@${BAD_YAML};filename=bad.yaml"
assert_body 'failed to parse YAML file' "parse error keeps its prefix"
assert_body 'line' "parse error carries the yaml line detail"
printf 'policy: []\n' > "$BAD_YAML"
req 400 POST /service/file -F "file=@${BAD_YAML};filename=noname.yaml"
assert_body 'service_name' "missing service_name is named in the error"
rm -f "$BAD_YAML"

# --- upload example config (the GUI teaching file stays valid) ---
req 200 POST /service/file -F "file=@${DIR}/config-upload-example.yaml"
assert_body '"service_name":"svc-upload-example"' "example config registers"
assert_body '"policies":["threshold-alert"]' "example config lists its policy"
req 200 DELETE /service/svc-upload-example

# --- policy CRUD ---
req 200 POST /service/svc-logic/policy "$json" "$CT" \
  -d '{"name":"policy-2","trigger":[{"name":"wh-2","type":"Webhook","property":{"event_type":"other"}}],"node":[],"path":[]}'
req 200 PUT  /service/svc-logic/policy/policy-2 "$json" "$CT" \
  -d '{"name":"policy-2","trigger":[{"name":"wh-2","type":"Webhook","property":{"event_type":"changed"}}],"node":[],"path":[]}'
req 200 GET    /service/svc-logic/policy/policy-2
req 200 DELETE /service/svc-logic/policy/policy-2

# --- module CRUD ---
req 200 GET /service/svc-logic/module
assert_body 'branch-mod' "the uploaded module is listed"
req 200 GET /service/svc-logic/module/branch-mod
assert_body '"numOutPort":2' "module arity is reported with lowercase wire keys"

mod_ok='{"name":"pass-mod","numInPort":1,"numOutPort":1,"inPort":[{"modulePort":0,"nodeName":"inner","nodePort":0}],"outPort":[{"modulePort":0,"nodeName":"inner","nodePort":0}],"node":[{"name":"inner","type":"Wait","property":{"seconds":0}}],"path":[]}'
req 200 POST /service/svc-logic/module "$json" "$CT" -d "$mod_ok"
req 400 POST /service/svc-logic/module "$json" "$CT" -d "$mod_ok"   # duplicate
req 200 PUT  /service/svc-logic/module/pass-mod "$json" "$CT" -d "$mod_ok"
req 200 DELETE /service/svc-logic/module/pass-mod

# A boundary port left unwired used to nil-deref inside a walker goroutine and
# take the whole EFO process down; it must be refused at save time now.
req 400 POST /service/svc-logic/module "$json" "$CT" \
  -d '{"name":"broken","numInPort":1,"numOutPort":2,"inPort":[{"modulePort":0,"nodeName":"inner","nodePort":0}],"outPort":[{"modulePort":0,"nodeName":"inner","nodePort":0}],"node":[{"name":"inner","type":"Wait","property":{"seconds":0}}],"path":[]}'
# A module containing itself used to overflow the goroutine stack, which recover
# cannot catch.
req 400 POST /service/svc-logic/module "$json" "$CT" \
  -d '{"name":"loop","numInPort":1,"numOutPort":1,"inPort":[{"modulePort":0,"nodeName":"self","nodePort":0}],"outPort":[{"modulePort":0,"nodeName":"self","nodePort":0}],"node":[{"name":"self","type":"Module","property":{"module":"loop"}}],"path":[]}'
# Renaming through PUT would orphan every reference.
req 400 PUT /service/svc-logic/module/branch-mod "$json" "$CT" \
  -d '{"name":"renamed","numInPort":1,"numOutPort":2,"inPort":[{"modulePort":0,"nodeName":"m-if","nodePort":0}],"outPort":[{"modulePort":0,"nodeName":"m-if","nodePort":0},{"modulePort":1,"nodeName":"m-if","nodePort":1}],"node":[{"name":"m-if","type":"If","property":{"combinator":"AND","conditions":[{"field":"Value","operator":"<=","value":10}]}}],"path":[]}'
# Still referenced by policy-1, policy-legacy and reconfig's instance.
req 400 DELETE /service/svc-logic/module/branch-mod

# --- deploy ---
req 200 POST /service/policy/deploy "$json" "$CT" -d '{"SvcName":"svc-logic","Policies":["policy-1","policy-rejoin","policy-context","policy-compare","policy-crossrun"]}'
req 200 GET  /service/svc-logic/policy/policy-1
assert_body '"Status":"DEPLOYED"' "policy-1 deployed"

# --- webhook: match (dispatched>=1) / non-match (0) / unknown service (400, R3) ---
req 200 POST /webhook "$json" "$CT" -d '{"SvcName":"svc-logic","EventType":"metric","Content":{"Value":8}}'
assert_body '"dispatched":1' "webhook match dispatched=1"
# --- execution trace: the run must appear and settle, with the taken branch ---
wait_for '"status":"complete"' "/service/svc-logic/policy/policy-1/run"
assert_body '"hasRun":true' "run recorded"
assert_body '"p-if-ge"' "If node appears in the trace"
assert_body '"inputRaw"' "raw upstream payload is reported"
req 200 GET "/service/svc-logic/policy/policy-1/run?since=0"
req 200 GET "/service/svc-logic/policy/policy-1/run?since=9999"
assert_body '"events":[]' "since past the end returns no events"
req 400 GET "/service/svc-logic/policy/policy-1/run?since=abc"

# --- branch and rejoin: Merge mode:any continues on whichever side of the If ran ---
req 200 POST /webhook "$json" "$CT" -d '{"SvcName":"svc-logic","EventType":"rejoin","Content":{"Value":8}}'
assert_body '"dispatched":1' "rejoin webhook dispatched=1"
wait_for '"status":"complete"' "/service/svc-logic/policy/policy-rejoin/run"
assert_body '"r-join"' "Merge mode:any ran after a one-sided branch"
assert_body '"r-http"' "HTTPRequest ran downstream of the join"
assert_body '"Status_code"' "HTTPRequest reported a status code"
assert_body '"waiting":[]' "nothing parked; the untaken branch did not stall the run"

# --- namespaced payloads: context survives nodes that replace the payload ---
req 200 POST /webhook "$json" "$CT" -d '{"SvcName":"svc-logic","EventType":"context","Content":{"cluster":"edge-1","Value":8}}'
assert_body '"dispatched":1' "context webhook dispatched=1"
wait_for '"status":"complete"' "/service/svc-logic/policy/policy-context/run"
assert_body '"ctx-wh"' "the trigger namespace is on the wire, not just its fields"
assert_body 'edge-1' "the trigger's field survived nodes that produce their own"
assert_body '"c-drop"' "a qualified condition field (ctx-wh.Value) took the true branch"

# --- Compare: both sides fed in one run through the tee-mod fan-out ---
req 200 POST /webhook "$json" "$CT" -d '{"SvcName":"svc-logic","EventType":"compare","Content":{"Value":8,"Threshold":5}}'
wait_for '"k-over"' "/service/svc-logic/policy/policy-compare/run"
assert_body '"k-over"' "Compare took the true branch: 8 >= 5"
req 200 POST /webhook "$json" "$CT" -d '{"SvcName":"svc-logic","EventType":"compare","Content":{"Value":3,"Threshold":5}}'
wait_for '"k-under"' "/service/svc-logic/policy/policy-compare/run"
assert_body '"k-under"' "Compare took the false branch: 3 < 5"

# --- crossrun: two trigger chains cannot join across runs ---
# Each event starts its own run and a run resets the policy's packets
# (policy/Policy.go), so the limit-side token is gone by the time the measure
# event runs: Compare must end up blocked waiting on port 1.
req 200 POST /webhook "$json" "$CT" -d '{"SvcName":"svc-logic","EventType":"limit","Content":{"Threshold":5}}'
req 200 POST /webhook "$json" "$CT" -d '{"SvcName":"svc-logic","EventType":"measure","Content":{"Value":8}}'
wait_for '"status":"complete"' "/service/svc-logic/policy/policy-crossrun/run"
assert_body '"blocked":1' "the run completed with Compare still blocked"
assert_body '"node":"k-cmp","scope":"","port":1' "Compare is the node waiting, on the limit-side port"

req 200 POST /webhook "$json" "$CT" -d '{"SvcName":"svc-logic","EventType":"nope","Content":{"Value":8}}'
assert_body '"dispatched":0' "webhook non-match dispatched=0"
req 400 POST /webhook "$json" "$CT" -d '{"SvcName":"ghost","EventType":"metric","Content":{}}'

# --- undeploy + delete ---
req 200 POST   /service/policy/undeploy "$json" "$CT" -d '{"SvcName":"svc-logic","Policies":["policy-1","policy-rejoin","policy-context","policy-compare","policy-crossrun"]}'
req 400 GET    "/service/svc-logic/policy/policy-1/run"

req 200 DELETE /service/svc-logic

# --- optional: IEEE1935 action-node pipeline (needs a real control node + app store) ---
if [ "${RUN_IEEE1935:-0}" = "1" ]; then
  echo "== IEEE1935 pipeline (config-ieee1935.yaml) =="
  req 200 POST   /service/file -F "file=@${DIR}/config-ieee1935.yaml"
  req 200 POST   /service/policy/deploy "$json" "$CT" -d '{"SvcName":"svc-app","Policies":["deploy-pipeline"]}'
  req 200 POST   /webhook "$json" "$CT" -d '{"SvcName":"svc-app","EventType":"deploy","Content":{}}'
  wait_for '"status":"complete"' "/service/svc-app/policy/deploy-pipeline/run"
  req 200 POST   /service/policy/undeploy "$json" "$CT" -d '{"SvcName":"svc-app","Policies":["deploy-pipeline"]}'
  req 200 DELETE /service/svc-app
fi

echo "== done: PASS=${PASS} FAIL=${FAIL} =="
[ "$FAIL" -eq 0 ]
