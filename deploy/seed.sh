#!/usr/bin/env bash
# Register the self-deploy service and policy (and, optionally, monitoring-only
# services) in a freshly bootstrapped LCM, and record its first deployment.
# Idempotent; run as the operator.
#   deploy/seed.sh
# Uses the admin token in LCM_TOKEN_FILE (default ~/.config/lcm/token, written
# by bootstrap.sh), the server LCM_SERVER (default http://127.0.0.1:7800) and
# the client LCM_BIN (default /usr/local/bin/lcm).
# Optional inputs:
#   LCM_SEED_INVENTORY    local inventory of monitoring-only services (default
#                         ~/.config/lcm/seed-inventory; see deploy/seed-inventory.example)
#   LCM_SEED_NOTIFY_URL, LCM_SEED_NOTIFY_TOKEN_SECRET
#                         konata route and the name of the secret holding its key;
#                         deployment notifications are off unless both are set
# Docs: docs/operations.md#procedure (step 3)
set -Eeuo pipefail -f
export LC_ALL=C
shopt -u patsub_replacement 2>/dev/null || true

deploy_dir=$(cd -- "$(dirname -- "${BASH_SOURCE[0]}")" && pwd -P)
lcm=${LCM_BIN:-/usr/local/bin/lcm}
repo_url=https://github.com/ACaccel/LCM.git
export LCM_SERVER=${LCM_SERVER:-http://127.0.0.1:7800}
export LCM_TOKEN_FILE=${LCM_TOKEN_FILE:-$HOME/.config/lcm/token}
inventory=${LCM_SEED_INVENTORY:-$HOME/.config/lcm/seed-inventory}

log() { printf '==> %s\n' "$*"; }
note() { printf '    %s\n' "$*"; }
die() { printf 'seed: %s\n' "$*" >&2; exit 1; }
trap 'die "failed at line $LINENO: $BASH_COMMAND"' ERR

apply() { "$lcm" apply -f "$1" || die "lcm apply -f $1 failed"; }

# check_name UNIT: a check name derived from a unit name.
check_name() {
	local n=${1%.service}
	n=${n//@/-} n=${n//./-}
	printf '%s' "${n:0:63}"
}

# valid_url URL: an http(s) URL without credentials and with only characters
# that are safe to write into a YAML string unescaped.
valid_url() {
	[[ $1 =~ ^https?://[A-Za-z0-9._:-]+(/[A-Za-z0-9._~%/+=?:,-]*)?$ ]]
}

# Parsed inventory entries, filled by parse_inventory: kind, name, target
# (unit regex or container name) and optional URL, index-aligned.
inv_kind=() inv_name=() inv_target=() inv_url=()

# parse_inventory FILE: read and validate the local inventory of
# monitoring-only services; exits on the first invalid line. One entry per
# line:
#   service NAME UNIT_REGEX [HTTP_URL]
#   container NAME CONTAINER [HTTP_URL]
# Blank lines and # comments are ignored.
parse_inventory() {
	local file=$1 line fields kind name target url rc n=0 i
	inv_kind=() inv_name=() inv_target=() inv_url=()
	[[ -r $file ]] || die "cannot read inventory $file"
	while IFS= read -r line || [[ -n $line ]]; do
		n=$((n + 1))
		line=${line%%[[:space:]]#*}
		[[ $line =~ ^[[:space:]]*(#.*)?$ ]] && continue
		read -ra fields <<<"$line"
		kind=${fields[0]}
		[[ $kind == service || $kind == container ]] || die "$file:$n: unknown kind '$kind' (want service or container)"
		if ((${#fields[@]} < 3 || ${#fields[@]} > 4)); then
			die "$file:$n: want '$kind NAME TARGET [HTTP_URL]'"
		fi
		name=${fields[1]} target=${fields[2]} url=${fields[3]:-}
		[[ $name =~ ^[a-z0-9-]{1,63}$ ]] || die "$file:$n: invalid name '$name' (want ^[a-z0-9-]{1,63}\$)"
		# lcm is the self-deploy service and must not be shadowed.
		[[ $name != lcm ]] || die "$file:$n: the name lcm is reserved for the self-deploy service"
		for ((i = 0; i < ${#inv_name[@]}; i++)); do
			[[ ${inv_name[i]} != "$name" ]] || die "$file:$n: duplicate name '$name'"
		done
		if [[ $kind == service ]]; then
			rc=0
			[[ "" =~ $target ]] || rc=$?
			((rc != 2)) || die "$file:$n: invalid unit regex '$target'"
		else
			[[ $target =~ ^[A-Za-z0-9][A-Za-z0-9_.-]{0,127}$ ]] || die "$file:$n: invalid container name '$target'"
		fi
		if [[ -n $url ]]; then
			valid_url "$url" || die "$file:$n: invalid URL '$url' (want http(s):// without credentials)"
		fi
		inv_kind+=("$kind") inv_name+=("$name") inv_target+=("$target") inv_url+=("$url")
	done <"$file"
}

# http_check NAME URL: print an http check line when the URL answers right now.
http_check() {
	if curl -fsS --max-time 3 -o /dev/null "$2"; then
		printf '\n    - { name: http, type: http, props: { url: "%s" } }' "$2"
	else
		note "$1: $2 does not answer; no http check" >&2
	fi
}

# service_doc NAME CHECKS: append the monitoring-only Resource document.
service_doc() {
	cat >>"$work/monitored.yaml" <<DOC
---
# Monitoring only: adapter external, so LCM never acts on it.
apiVersion: lcm/v2
kind: Resource
metadata:
  name: $1
spec:
  type: service
  adapter: external
  checks:$2
DOC
	note "$1: $(grep -c '^    - ' <<<"$2") checks"
}

# monitored_service NAME PATTERN [URL]: a monitoring-only service with a
# systemd check for every unit matching PATTERN. Long-running units are
# critical; a oneshot unit is represented by its timer, as a non-critical
# check.
monitored_service() {
	local name=$1 pattern=$2 url=${3:-} unit type timer checks=''
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
	[[ -z $url ]] || checks+=$(http_check "$name" "$url")
	if [[ -z $checks ]]; then
		note "$name: no units on this host"
		return
	fi
	service_doc "$name" "$checks"
}

# monitored_container NAME CONTAINER [URL]: a monitoring-only service for a
# docker container.
monitored_container() {
	local name=$1 container=$2 url=${3:-} checks=''
	if ! docker inspect --format '{{.Name}}' -- "$container" >/dev/null 2>&1; then
		note "$name: no such container"
		return
	fi
	if id -nG lcm 2>/dev/null | grep -qw docker; then
		checks+=$'\n'"    - { name: container, type: docker, props: { container: \"$container\" } }"
	else
		# docker group membership is root-equivalent, so lcm does not get it.
		note "$name: lcm is not in the docker group; no docker check"
	fi
	[[ -z $url ]] || checks+=$(http_check "$name" "$url")
	[[ -z $checks ]] || service_doc "$name" "$checks"
}

# render_policy SRC DEST: copy the auto-deploy policy and, when both
# LCM_SEED_NOTIFY_URL and LCM_SEED_NOTIFY_TOKEN_SECRET are set, point its
# notifications at that konata route. The tracked file stays unchanged.
render_policy() {
	local src=$1 dest=$2 url=${LCM_SEED_NOTIFY_URL:-} secret=${LCM_SEED_NOTIFY_TOKEN_SECRET:-} text
	if [[ -z $url || -z $secret ]]; then
		[[ -z $url && -z $secret ]] || note "notifications stay off: set both LCM_SEED_NOTIFY_URL and LCM_SEED_NOTIFY_TOKEN_SECRET"
		cp -- "$src" "$dest"
		return
	fi
	valid_url "$url" || die "LCM_SEED_NOTIFY_URL is not an http(s) URL without credentials"
	[[ $secret =~ ^[A-Za-z0-9][A-Za-z0-9._-]{0,63}$ ]] || die "LCM_SEED_NOTIFY_TOKEN_SECRET is not a valid secret name"
	text=$(<"$src")
	[[ $text == *$'\n    notifyUrl: ""\n'* && $text == *$'\n    notifyTokenSecret: ""\n'* ]] ||
		die "$src no longer has empty notifyUrl and notifyTokenSecret parameters"
	text=${text/$'\n    notifyUrl: ""\n'/$'\n    notifyUrl: '"$url"$'\n'}
	text=${text/$'\n    notifyTokenSecret: ""\n'/$'\n    notifyTokenSecret: '"$secret"$'\n'}
	printf '%s\n' "$text" >"$dest"
}

main() {
	local tool i policy deployed sha health
	for tool in "$lcm" git jq curl systemctl; do
		command -v "$tool" >/dev/null || die "missing required tool: $tool"
	done
	[[ -r $LCM_TOKEN_FILE ]] || die "no token file $LCM_TOKEN_FILE; run deploy/bootstrap.sh or set LCM_TOKEN_FILE"
	curl -fsS --max-time 5 "$LCM_SERVER/api/v1/health" >/dev/null || die "LCM at $LCM_SERVER is not healthy"
	work=$(mktemp -d)
	trap 'rm -rf -- "$work"' EXIT

	log "monitoring-only services"
	if [[ -e $inventory ]]; then
		parse_inventory "$inventory"
		units=$(systemctl list-units --type=service --all --no-legend --plain | awk '{print $1}')
		: >"$work/monitored.yaml"
		for i in "${!inv_name[@]}"; do
			case ${inv_kind[i]} in
			service) monitored_service "${inv_name[i]}" "${inv_target[i]}" "${inv_url[i]}" ;;
			container) monitored_container "${inv_name[i]}" "${inv_target[i]}" "${inv_url[i]}" ;;
			esac
		done
		[[ ! -s $work/monitored.yaml ]] || apply "$work/monitored.yaml"
	else
		note "none (no inventory at $inventory; see deploy/seed-inventory.example)"
	fi

	log "self-deploy service lcm"
	# The first deployment below reads the head of repo_url; LCM clones and
	# fetches source.url, so both must name the same repository.
	grep -qxF "      url: $repo_url" "$deploy_dir/lcm-self.service.yaml" ||
		die "deploy/lcm-self.service.yaml does not set source.url: $repo_url"
	apply "$deploy_dir/lcm-self.service.yaml"
	deployed=$("$lcm" get resources lcm -o json | jq -r '.service.deployedVersion // ""')
	if [[ -n $deployed ]]; then
		note "already deployed at $deployed"
	else
		sha=$(git ls-remote -- "$repo_url" refs/heads/main | cut -f1)
		[[ $sha =~ ^[0-9a-f]{40}$ ]] || die "cannot read the head of main from $repo_url"
		note "first deployment of main at $sha (builds the console and the binary; takes a few minutes)"
		"$lcm" resource action lcm deploy --version "$sha" --wait ||
			die "the first deployment failed; its log is on the service page of the web console"
		note "waiting for LCM to restart on the new binary"
		for _ in $(seq 120); do
			health=$("$lcm" get resources lcm -o json 2>/dev/null | jq -r '.health.status // ""' || true)
			[[ $health == healthy ]] && break
			sleep 2
		done
		[[ $health == healthy ]] || die "service lcm is not healthy after the first deployment; see journalctl -u lcm and -u 'lcm-self-update-*'"
		note "healthy"
	fi

	log "auto-deploy policy"
	policy=$work/lcm-auto-deploy.yaml
	render_policy "$deploy_dir/lcm-auto-deploy.yaml" "$policy"
	apply "$policy"

	log "resources"
	"$lcm" get resources
}

if [[ ${BASH_SOURCE[0]} == "$0" ]]; then
	main "$@"
fi
