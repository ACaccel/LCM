#!/usr/bin/env bash
#
# CALM four-stage loop demo driver.
#
# The policy carries its own clock: the Timer trigger fires one slot every
# interval_seconds and the graph runs telemetry -> selector -> mode -> apply ->
# wait -> post-verify by itself. This script only deploys the policy, injects the
# demo scenario, watches the execution trace, and cleans up afterwards.
#
# Usage:
#   bash Orchestrator/efo/LCM_Enabler/examples/calm-fourstage/calm-demo.sh [-n slots] [-i interval_s] [-c cluster]
#                                   [-s scenario] [--dry-run] [-H host]
#
#   -n  number of slots to run                       (default 10)
#   -i  slot period in seconds                       (default 30)
#   -c  target cluster; discovered when exactly one cluster exists
#   -s  scenario: normal | radio-degrade | overload  (default normal)
#   -H  EFO base URL                                 (default http://127.0.0.1:8000)
#   --dry-run  compute and report every placement change without PATCHing
#
# Requires curl and jq. The EFO must already be running with a build that
# contains the CALM nodes; this script never restarts it.
set -uo pipefail

DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
SOURCE_YAML="${DIR}/config-calm.yaml"
INJECT_FILE="${DIR}/calm-inject.json"

SVC="calm-fourstage"
POLICY="calm-loop"

SLOTS=10
INTERVAL=30
CLUSTER=""
SCENARIO="normal"
DRY_RUN="false"
HOST="http://127.0.0.1:8000"

usage() { awk 'NR>1 && /^#/ {sub(/^# ?/, ""); print; next} NR>1 {exit}' "$0"; }

while [ "$#" -gt 0 ]; do
  case "$1" in
    -n) SLOTS="${2:-}"; shift 2 ;;
    -i) INTERVAL="${2:-}"; shift 2 ;;
    -c) CLUSTER="${2:-}"; shift 2 ;;
    -s) SCENARIO="${2:-}"; shift 2 ;;
    -H) HOST="${2:-}"; shift 2 ;;
    --dry-run) DRY_RUN="true"; shift ;;
    -h|--help) usage; exit 0 ;;
    *) printf 'unknown argument: %s\n' "$1" >&2; usage >&2; exit 1 ;;
  esac
done

case "$SCENARIO" in
  normal|radio-degrade|overload) ;;
  *) printf 'unknown scenario %s (normal, radio-degrade, overload)\n' "$SCENARIO" >&2; exit 1 ;;
esac
case "$SLOTS" in ''|*[!0-9]*) printf -- '-n must be a positive integer\n' >&2; exit 1 ;; esac
case "$INTERVAL" in ''|*[!0-9]*) printf -- '-i must be a positive integer\n' >&2; exit 1 ;; esac
[ "$SLOTS" -ge 1 ] || { printf -- '-n must be at least 1\n' >&2; exit 1; }
# The Timer node refuses anything below one second at deploy time.
[ "$INTERVAL" -ge 1 ] || { printf -- '-i must be at least 1\n' >&2; exit 1; }

for tool in curl jq; do
  command -v "$tool" >/dev/null 2>&1 || {
    printf '%s is required but not installed\n' "$tool" >&2
    exit 1
  }
done
[ -f "$SOURCE_YAML" ] || { printf 'missing %s\n' "$SOURCE_YAML" >&2; exit 1; }

LCM="${HOST}/api/lcm"
RESP="$(mktemp)"
TEMP_YAML="$(mktemp -t calm-fourstage.XXXXXX.yaml)"
DEPLOYED=0

# api <method> <url> [curl args...] : run a request, leave the body in $RESP and
# echo the HTTP status. Mirrors run.sh's req() without its PASS/FAIL accounting.
api() {
  local method="$1" url="$2"
  shift 2
  curl -sS -o "$RESP" -w '%{http_code}' -X "$method" "$@" "$url" 2>/dev/null || echo "000"
}

body() { cat "$RESP"; }

cleanup() {
  local status=$?
  trap - EXIT INT TERM
  if [ "$DEPLOYED" = 1 ]; then
    printf '\nundeploying %s/%s\n' "$SVC" "$POLICY"
    api POST "${LCM}/service/policy/undeploy" \
      -H 'Content-Type: application/json' \
      -d "{\"SvcName\":\"${SVC}\",\"Policies\":[\"${POLICY}\"]}" >/dev/null
    api DELETE "${LCM}/service/${SVC}" >/dev/null
  fi
  rm -f "$TEMP_YAML" "$RESP"
  exit "$status"
}
trap cleanup EXIT INT TERM

# --- preflight: the EFO must know the CALM node types -------------------------

code="$(api GET "${LCM}/nodetypes")"
if [ "$code" != "200" ]; then
  printf 'cannot reach the EFO at %s (GET /api/lcm/nodetypes -> %s)\n' "$HOST" "$code" >&2
  exit 1
fi
if ! grep -q 'CALM_Telemetry' "$RESP"; then
  printf 'EFO binary predates the CALM nodes; ask the maintainer to restart with the new build\n' >&2
  exit 2
fi

# --- target cluster -----------------------------------------------------------

if [ -z "$CLUSTER" ]; then
  code="$(api GET "${HOST}/api/clusters")"
  if [ "$code" != "200" ]; then
    printf 'cannot list clusters (GET /api/clusters -> %s); pass -c <cluster>\n' "$code" >&2
    exit 1
  fi
  mapfile -t names < <(body | jq -r '.[]?.Cluster_Name // empty')
  if [ "${#names[@]}" -eq 1 ]; then
    CLUSTER="${names[0]}"
    printf 'discovered target cluster: %s\n' "$CLUSTER"
  else
    printf 'found %d clusters; pass -c <cluster>\n' "${#names[@]}" >&2
    exit 1
  fi
fi

# --- scenario injection -------------------------------------------------------
#
# The inject file overwrites state-vector fields after CALM_Telemetry has
# measured them, so a scenario needs no synthetic traffic. Only the domain-wide
# StateVector fields exist; there is no per-service key to inject, which is why
# the overload scenario drives the autoscaler through max_dmr rather than
# through a request rate.
#
# The scenario starts at slot SWITCH_SLOT so the demo shows the transition, not
# just the end state.
SWITCH_SLOT=$(( (SLOTS + 2) / 3 ))

# Radio degradation. The four radio dimensions are masked by default and the
# context builder substitutes the neutral 0.5 for each; clearing radio_masked is
# what makes the injected values reach the bandit.
#   sinr_unit  = clip((SINR + 20) / 40, 0, 1) = (-15 + 20) / 40 = 0.125 (vs 0.5)
#   prb        = clip(0.95, 0, 1)             = 0.95              (vs 0.5)
#   prb^2      = 0.95^2                       = 0.9025            (vs 0.25)
RADIO_INJECT='{"mean_sinr": -15.0, "mean_prb_util": 0.95, "radio_masked": false}'

# Overload. calm/config.go DefaultService: dmr_slo (eps) = 0.10, dmr_crit = 0.28,
# service_rate_per_replica (mu) = 200; RefReqRate = 200.
#   dmr_risk = max_dmr / eps = 0.35 / 0.10 = 3.5
#   risk     = max(dmr_risk, lambda / (max(replicas,1) * mu)) >= 3.5
# 3.5 clears every mode's scale_out_threshold (latency 0.55, resilient 0.65,
# balanced and energy_saving 0.70), so whichever arm the bandit picks scales out
# by its own scale_step. 0.35 is also above dmr_crit, so the injected state reads
# as a severe-level miss ratio.
#   req_rate feature = clip(400 / 200, 0, 1) = 1.0 (saturated)
# Note that total_req_rate moves the bandit context only: the autoscaler's
# load_ratio is per service and reads svc_<name>_rate, which no inject file can
# reach.
OVERLOAD_INJECT='{"total_req_rate": 400.0, "max_dmr": 0.35}'

# write_inject <json> : replace the inject file the telemetry node reads.
write_inject() {
  printf '%s\n' "$1" > "$INJECT_FILE"
}

# scenario_for_slot <completed_slots> : the inject payload that should be in
# place while the next slot runs.
scenario_for_slot() {
  local done_slots="$1"
  if [ "$SCENARIO" = "normal" ] || [ "$done_slots" -lt "$SWITCH_SLOT" ]; then
    printf '{}'
    return
  fi
  case "$SCENARIO" in
    radio-degrade) printf '%s' "$RADIO_INJECT" ;;
    overload)      printf '%s' "$OVERLOAD_INJECT" ;;
  esac
}

write_inject '{}'

# --- prepare the policy document ---------------------------------------------

sed \
  -e "s|^\( *interval_seconds: \).*\(# DEMO_INTERVAL_SECONDS\)|\1${INTERVAL} \2|" \
  -e "s|^\( *max_ticks: \)[^#]*\(# DEMO_MAX_TICKS\)|\1${SLOTS} \2|" \
  -e "s|^\( *target_cluster: \)DEMO_TARGET_CLUSTER *$|\1\"${CLUSTER}\"|" \
  -e "s|^\( *inject_file: \).*\(# DEMO_INJECT_FILE\)|\1\"${INJECT_FILE}\" \2|" \
  -e "s|^\( *dry_run: \)[^#]*\(# DEMO_DRY_RUN\)|\1${DRY_RUN} \2|" \
  "$SOURCE_YAML" > "$TEMP_YAML"

for marker in DEMO_TARGET_CLUSTER; do
  if grep -q "^ *target_cluster: ${marker}" "$TEMP_YAML"; then
    printf 'placeholder %s was not substituted; %s may have been edited\n' \
      "$marker" "$SOURCE_YAML" >&2
    exit 1
  fi
done

printf '== CALM four-stage demo ==\n'
printf 'host      : %s\n' "$HOST"
printf 'cluster   : %s\n' "$CLUSTER"
printf 'slots     : %s at %ss\n' "$SLOTS" "$INTERVAL"
printf 'scenario  : %s' "$SCENARIO"
[ "$SCENARIO" = "normal" ] || printf ' (from slot %s)' "$SWITCH_SLOT"
printf '\n'
printf 'dry run   : %s\n' "$DRY_RUN"
printf 'inject    : %s\n\n' "$INJECT_FILE"

# --- register + deploy --------------------------------------------------------

code="$(api POST "${LCM}/service/file" -F "file=@${TEMP_YAML};filename=config-calm.yaml")"
if [ "$code" != "200" ]; then
  # A leftover registration from an interrupted run is the usual cause. Undeploy
  # first: a service with deployed policies refuses to be replaced or deleted.
  api POST "${LCM}/service/policy/undeploy" \
    -H 'Content-Type: application/json' \
    -d "{\"SvcName\":\"${SVC}\",\"Policies\":[\"${POLICY}\"]}" >/dev/null
  api DELETE "${LCM}/service/${SVC}" >/dev/null
  code="$(api POST "${LCM}/service/file" -F "file=@${TEMP_YAML};filename=config-calm.yaml")"
fi
if [ "$code" != "200" ]; then
  printf 'upload failed (%s): %s\n' "$code" "$(body)" >&2
  exit 1
fi
printf 'uploaded  : %s\n' "$(body)"

code="$(api POST "${LCM}/service/policy/deploy" \
  -H 'Content-Type: application/json' \
  -d "{\"SvcName\":\"${SVC}\",\"Policies\":[\"${POLICY}\"]}")"
if [ "$code" != "200" ]; then
  printf 'deploy failed (%s): %s\n' "$code" "$(body)" >&2
  api DELETE "${LCM}/service/${SVC}" >/dev/null
  exit 1
fi
DEPLOYED=1
printf 'deployed  : %s/%s\n\n' "$SVC" "$POLICY"

# --- observation loop ---------------------------------------------------------
#
# The recorder keeps only the latest run, so each slot has to be read before the
# next Timer tick replaces it. A changed runId is therefore the slot boundary,
# and the run is harvested as soon as verify-0 -- the last node of the graph --
# has reported.

# summarise : reduce one RunTrace to a tab-separated row.
#   mode  reward  recovery  switched  guard  applied  verify_ok  note
SUMMARISE='
  def out($n): [.events[]? | select(.node == $n)] | last | (.output[0]? // {});
  out("selector-0") as $s
  | out("verify-0") as $v
  | out("apply-0")  as $a
  | [ ($s.mode // "-"),
      (if $v.verify_ok == true then (($v.reward // 0) * 1000 | round / 1000 | tostring) else "n/a" end),
      ($v.recovery_state // $s.recovery_state // "-"),
      (if $s.switched == true then "yes" else "no" end),
      (if $s.allowed == false then "blocked" else "ok" end),
      ([ $a | to_entries[] | select(.key | startswith("applied_"))
              | ((.key | ltrimstr("applied_")) + ":" + (.value | tostring)) ]
       | if length == 0 then "-" else join(",") end),
      (if $v.verify_ok == true then "yes" else "no" end),
      ([ ($v.verify_reason // empty),
         (if ($v.violated // false) then "violated" else empty end),
         (if ($v.severe // false) then "severe" else empty end),
         ($a.errors // [] | .[]),
         ($a.unapplied_actions // [] | .[]) ]
       | join(" | "))
    ] | @tsv'

RUN_URL="${LCM}/service/${SVC}/policy/${POLICY}/run?since=0"
last_run_id=""
seen=0
switch_count=0
guard_blocks=0
reward_sum=0
reward_n=0
deadline=$(( $(date +%s) + SLOTS * (INTERVAL + 30) ))

printf 'watching %s\n\n' "$RUN_URL"

while [ "$seen" -lt "$SLOTS" ]; do
  if [ "$(date +%s)" -ge "$deadline" ]; then
    printf '\ntimed out after %s slots of %s\n' "$seen" "$SLOTS"
    break
  fi

  code="$(api GET "$RUN_URL")"
  if [ "$code" = "200" ]; then
    run_id="$(body | jq -r '.runId // ""')"
    finished="$(body | jq -r 'if ([.events[]? | select(.node == "verify-0")] | length) > 0 or .status == "complete" then "yes" else "no" end')"

    if [ -n "$run_id" ] && [ "$run_id" != "$last_run_id" ] && [ "$finished" = "yes" ]; then
      last_run_id="$run_id"
      seen=$(( seen + 1 ))

      IFS=$'\t' read -r mode reward recovery switched guard applied verify_ok note \
        < <(body | jq -r "$SUMMARISE")

      printf 'slot %-3s mode=%-16s reward=%-8s recovery=%-7s switched=%-4s guard=%-8s applied=%s\n' \
        "$seen" "$mode" "$reward" "$recovery" "$switched" "$guard" "$applied"
      [ -n "$note" ] && printf '         note: %s\n' "$note"

      [ "$switched" = "yes" ] && switch_count=$(( switch_count + 1 ))
      [ "$guard" = "blocked" ] && guard_blocks=$(( guard_blocks + 1 ))
      if [ "$verify_ok" = "yes" ]; then
        reward_sum="$(awk -v a="$reward_sum" -v b="$reward" 'BEGIN{printf "%.6f", a + b}')"
        reward_n=$(( reward_n + 1 ))
      fi

      # The scenario for the next slot is written now, between runs, so the
      # telemetry node picks it up on its next read rather than mid-slot.
      want="$(scenario_for_slot "$seen")"
      if [ "$want" != "$(cat "$INJECT_FILE" 2>/dev/null)" ]; then
        write_inject "$want"
        printf '         inject -> %s\n' "$want"
      fi
    fi
  fi

  sleep 2
done

# --- summary ------------------------------------------------------------------

printf '\n== summary ==\n'
printf 'slots observed : %s of %s\n' "$seen" "$SLOTS"
printf 'mode switches  : %s\n' "$switch_count"
printf 'guard blocks   : %s\n' "$guard_blocks"
if [ "$reward_n" -gt 0 ]; then
  printf 'mean reward    : %s (over %s verified slots)\n' \
    "$(awk -v s="$reward_sum" -v n="$reward_n" 'BEGIN{printf "%.4f", s / n}')" "$reward_n"
else
  printf 'mean reward    : n/a (no slot was verified)\n'
fi

printf '\ndeployments on %s\n' "$CLUSTER"
code="$(api GET "${HOST}/api/clusters/${CLUSTER}/deployments")"
if [ "$code" = "200" ]; then
  body | jq -r '
    (["APPLICATION", "REPLICAS", "CPU", "MEMORY"] | @tsv),
    (.[]? | [(.Application_Name // "-"), (.Replicas // "-"),
             (.Allocated_cpu // "-"), (.Alocated_memory // "-")] | @tsv)' |
    column -t -s $'\t' 2>/dev/null || body
else
  printf 'could not read deployments (%s)\n' "$code"
fi

# cleanup() undeploys, deletes the service and removes the temp document.
