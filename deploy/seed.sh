#!/usr/bin/env bash
# Register the services and the self-deploy policy in a freshly bootstrapped
# LCM, and record its first deployment. Idempotent; run as the operator.
#   deploy/seed.sh
# Uses the admin token in LCM_TOKEN_FILE (default ~/.config/lcm/token, written
# by bootstrap.sh), the server LCM_SERVER (default http://127.0.0.1:7800) and
# the client LCM_BIN (default /usr/local/bin/lcm).
# Docs: docs/operations.md#procedure (step 3)
set -Eeuo pipefail -f
export LC_ALL=C

deploy_dir=$(cd -- "$(dirname -- "${BASH_SOURCE[0]}")" && pwd -P)
lcm=${LCM_BIN:-/usr/local/bin/lcm}
repo_url=https://github.com/ACaccel/LCM.git
export LCM_SERVER=${LCM_SERVER:-http://127.0.0.1:7800}
export LCM_TOKEN_FILE=${LCM_TOKEN_FILE:-$HOME/.config/lcm/token}

log() { printf '==> %s\n' "$*"; }
note() { printf '    %s\n' "$*"; }
die() { printf 'seed: %s\n' "$*" >&2; exit 1; }
trap 'die "failed at line $LINENO: $BASH_COMMAND"' ERR

for tool in "$lcm" git jq curl systemctl; do
	command -v "$tool" >/dev/null || die "missing required tool: $tool"
done
[[ -r $LCM_TOKEN_FILE ]] || die "no token file $LCM_TOKEN_FILE; run deploy/bootstrap.sh or set LCM_TOKEN_FILE"
curl -fsS --max-time 5 "$LCM_SERVER/api/v1/health" >/dev/null || die "LCM at $LCM_SERVER is not healthy"
work=$(mktemp -d)
trap 'rm -rf -- "$work"' EXIT

apply() { "$lcm" apply -f "$1" || die "lcm apply -f $1 failed"; }

# check_name UNIT: a check name derived from a unit name.
check_name() {
	local n=${1%.service}
	n=${n//@/-} n=${n//./-}
	printf '%s' "${n:0:63}"
}

# monitored NAME PATTERN: a monitoring-only service with a systemd check for
# every unit matching PATTERN. Long-running units are critical; a oneshot
# unit is represented by its timer, as a non-critical check.
monitored() {
	local name=$1 pattern=$2 unit type timer checks=''
	for unit in $units; do
		[[ $unit =~ $pattern ]] || continue
		[[ $(systemctl show -p LoadState --value "$unit") == loaded ]] || continue
		type=$(systemctl show -p Type --value "$unit")
		if [[ $type != oneshot ]]; then
			checks+=$'\n'"    - { name: $(check_name "$unit"), type: systemd, props: { unit: \"$unit\" } }"
			continue
		fi
		timer=${unit%.service}.timer
		if [[ $(systemctl show -p LoadState --value "$timer") == loaded ]]; then
			checks+=$'\n'"    - { name: $(check_name "$timer"), type: systemd, critical: false, props: { unit: \"$timer\" } }"
		else
			note "$name: skipped oneshot $unit (no timer)"
		fi
	done
	extra_checks "$name"
	if [[ -z $checks ]]; then
		note "$name: no units on this host"
		return
	fi
	service_doc "$name" "$checks"
}

# extra_checks NAME: http checks that answer right now, appended to checks.
extra_checks() {
	local url
	case $1 in
	card-together) url=http://127.0.0.1:3001/health ;;
	metube) url=http://127.0.0.1:8081/ ;;
	*) return 0 ;;
	esac
	if curl -fsS --max-time 3 -o /dev/null "$url"; then
		checks+=$'\n'"    - { name: http, type: http, props: { url: \"$url\" } }"
	else
		note "$1: $url does not answer; no http check"
	fi
}

service_doc() {
	cat >>"$work/monitored.yaml" <<EOF
---
# Monitoring only: runtime external, so LCM never acts on it.
apiVersion: lcm/v1
kind: Service
metadata:
  name: $1
spec:
  runtime:
    type: external
  checks:$2
EOF
	note "$1: $(grep -c '^    - ' <<<"$2") checks"
}

log "monitoring-only services"
units=$(systemctl list-units --type=service --all --no-legend --plain | awk '{print $1}')
: >"$work/monitored.yaml"
monitored card-together '^card-together(-.+)?\.service$'
monitored personal-quant-desk '^personal-quant-desk(-.+)?\.service$'
monitored pleiades '^pleiades@.+\.service$'
monitored mongodb-pleiades '^mongodb-pleiades\.service$'
checks=''
if docker inspect --format '{{.Name}}' metube >/dev/null 2>&1; then
	if id -nG lcm 2>/dev/null | grep -qw docker; then
		checks+=$'\n'"    - { name: container, type: docker, props: { container: metube } }"
	else
		# docker group membership is root-equivalent, so lcm does not get it.
		note "metube: lcm is not in the docker group; no docker check"
	fi
	extra_checks metube
	[[ -z $checks ]] || service_doc metube "$checks"
else
	note "metube: no such container"
fi
[[ ! -s $work/monitored.yaml ]] || apply "$work/monitored.yaml"

log "self-deploy service lcm"
apply "$deploy_dir/lcm-self.service.yaml"
deployed=$("$lcm" get services lcm -o json | jq -r '.deployedVersion // ""')
if [[ -n $deployed ]]; then
	note "already deployed at $deployed"
else
	sha=$(git ls-remote -- "$repo_url" refs/heads/main | cut -f1)
	[[ $sha =~ ^[0-9a-f]{40}$ ]] || die "cannot read the head of main from $repo_url"
	note "first deployment of main at $sha (builds the console and the binary; takes a few minutes)"
	"$lcm" action lcm deploy --version "$sha" --wait ||
		die "the first deployment failed; its log is on the service page of the web console"
	note "waiting for LCM to restart on the new binary"
	for _ in $(seq 120); do
		health=$("$lcm" get services lcm -o json 2>/dev/null | jq -r '.health // ""' || true)
		[[ $health == healthy ]] && break
		sleep 2
	done
	[[ $health == healthy ]] || die "service lcm is not healthy after the first deployment; see journalctl -u lcm and -u 'lcm-self-update-*'"
	note "healthy"
fi

log "auto-deploy policy"
apply "$deploy_dir/lcm-auto-deploy.yaml"

log "services"
"$lcm" get services
