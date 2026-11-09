#!/usr/bin/env bash
# Install LCM on this host, or bring an installation up to date. Idempotent.
#   sudo deploy/bootstrap.sh [options]        (--help lists them)
# A wrapper around "lcm host init" (first run only) and "lcm host apply",
# which do the work. Docs: docs/operations.md#install
set -Eeuo pipefail
umask 022
export PATH=/usr/local/sbin:/usr/local/bin:/usr/sbin:/usr/bin:/sbin:/bin LC_ALL=C

repo_dir=$(cd -- "$(dirname -- "${BASH_SOURCE[0]}")/.." && pwd -P)
binary=$repo_dir/bin/lcm
config=/etc/lcm/host.json
operator=${SUDO_USER:-}
ts_host=''
init_args=() apply_args=() given=()

usage() {
	cat <<EOF
Usage: sudo deploy/bootstrap.sh [options]
  --binary PATH      reviewed lcm build that performs the installation (default: bin/lcm, from 'make build')
  --replace-binary   install --binary even if /usr/local/bin/lcm exists (self-deploy keeps it current)
  --check            report what would change and change nothing (exit status 3 when something would)
  --ts-host NAME     refuse to run unless this node's tailnet name is NAME (no default)
First run only, when $config does not exist yet (afterwards edit that file):
  --operator USER    account that gets the seed token and owns the default toolchains (default: \$SUDO_USER)
  --go-root DIR      Go toolchain to copy (default: ~OPERATOR/.local/go)
  --node-root DIR    Node.js toolchain to copy (default: ~OPERATOR/.local/node)
  --ts-port PORT     HTTPS port of tailscale serve (default: 9443)
  --repo URL         repository LCM deploys itself from (default: https://github.com/ACaccel/LCM.git)
EOF
}

die() { printf 'bootstrap: %s\n' "$*" >&2; exit 1; }

while (($#)); do
	case $1 in
	--binary | --operator | --go-root | --node-root | --ts-host | --ts-port | --repo)
		(($# >= 2)) || die "$1 needs a value"
		case $1 in
		--binary) binary=$2 ;;
		--ts-host) ts_host=$2 ;;
		--operator) operator=$2 && given+=("$1") ;;
		--go-root) init_args+=(--go-root "$2") && given+=("$1") ;;
		--node-root) init_args+=(--node-root "$2") && given+=("$1") ;;
		--ts-port) init_args+=(--ts-port "$2") && given+=("$1") ;;
		--repo) init_args+=(--repository "$2") && given+=("$1") ;;
		esac
		shift 2
		;;
	--replace-binary | --check) apply_args+=("$1") && shift ;;
	-h | --help) usage && exit 0 ;;
	*) usage >&2 && exit 2 ;;
	esac
done

# Only a build of a reviewed checkout may run as root. lcm host refuses the
# self-deployed binary as well, but that check runs inside the binary it
# judges, so it is made here before the binary runs.
real=$(realpath -m -- "$binary")
case $real in
/usr/local/bin/lcm | /usr/local/bin/lcm.* | /var/lib/lcm/*)
	die "refusing $binary ($real): it is the self-deployed binary or a copy of it; run 'make build' in a reviewed checkout"
	;;
esac
for installed in /usr/local/bin/lcm /usr/local/bin/lcm.prev /usr/local/bin/lcm.failed; do
	[[ ! $real -ef $installed ]] || die "refusing $binary: it is the same file as $installed; run 'make build' in a reviewed checkout"
done
lcm_uid=$(id -u lcm 2>/dev/null || true)
if [[ -n $lcm_uid && -e $real && $(stat -c %u -- "$real") == "$lcm_uid" ]]; then
	die "refusing $binary: it is owned by lcm, which self-deploy controls; run 'make build' in a reviewed checkout"
fi
((EUID == 0)) || die "run as root: sudo deploy/bootstrap.sh"
[[ -f $binary && ! -L $binary && -x $binary ]] || die "no binary at $binary; run 'make build' first"
if [[ -n $ts_host ]]; then
	command -v tailscale >/dev/null && command -v jq >/dev/null || die "--ts-host needs tailscale and jq"
	self_dns=$(tailscale status --json | jq -r '.Self.DNSName // ""' | sed 's/\.$//')
	[[ $self_dns == "$ts_host" ]] || die "this node is '$self_dns' on the tailnet, not --ts-host $ts_host"
fi

if [[ -e $config || -L $config ]]; then
	((${#given[@]} == 0)) || die "$config exists, so ${given[*]} would have no effect; edit $config instead"
else
	[[ -n $operator ]] || die "--operator is required when sudo does not set SUDO_USER"
	if [[ ${apply_args[*]} == *--check* ]]; then
		printf 'bootstrap: %s does not exist; without --check it would be created with:\n  %s host init --operator %s %s\n' \
			"$config" "$binary" "$operator" "${init_args[*]}"
		exit 3
	fi
	"$binary" host init --config "$config" --operator "$operator" "${init_args[@]}"
fi
exec "$binary" host apply --config "$config" "${apply_args[@]}"
