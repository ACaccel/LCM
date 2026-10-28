#!/usr/bin/env bash
# Remove LCM from this host. Data, configuration and toolchains stay unless
# --purge is given; the operator's token file is never touched.
#   sudo deploy/uninstall.sh [--purge] [--ts-port PORT]
# Docs: docs/operations.md#uninstall
set -Eeuo pipefail
export PATH=/usr/local/sbin:/usr/local/bin:/usr/sbin:/usr/bin:/sbin:/bin LC_ALL=C

purge=0
ts_port=9443
while (($#)); do
	case $1 in
	--purge) purge=1 && shift ;;
	--ts-port) (($# >= 2)) || exit 2; ts_port=$2 && shift 2 ;;
	*) echo 'Usage: sudo deploy/uninstall.sh [--purge] [--ts-port PORT]' >&2 && exit 2 ;;
	esac
done
log() { printf '==> %s\n' "$*"; }
die() { printf 'uninstall: %s\n' "$*" >&2; exit 1; }
trap 'die "failed at line $LINENO: $BASH_COMMAND"' ERR
((EUID == 0)) || die "run as root: sudo deploy/uninstall.sh"
[[ $ts_port =~ ^[0-9]{1,5}$ ]] || die "invalid --ts-port"

log "stopping lcm.service and pending self-updates"
systemctl stop 'lcm-self-update-*.timer' 'lcm-self-update-*.service' 2>/dev/null || true
systemctl reset-failed 'lcm-self-update-*' 2>/dev/null || true
systemctl disable --now lcm.service 2>/dev/null || true

log "tailscale serve on port $ts_port"
# Only the handler bootstrap.sh added is removed.
handler=$(tailscale serve status --json | jq -c --arg port "$ts_port" \
	'[.Web // {} | to_entries[] | select(.key | endswith(":" + $port)) | .value.Handlers] | first // null')
if [[ $handler == '{"/":{"Proxy":"http://127.0.0.1:7800"}}' ]]; then
	tailscale serve --https="$ts_port" off >/dev/null
	echo "    removed"
else
	echo "    left alone (not LCM's handler: $handler)"
fi

log "unit, sudoers rule, self-install and binaries"
rm -f /etc/systemd/system/lcm.service /etc/sudoers.d/lcm
rm -rf /usr/local/libexec/lcm
rm -f /usr/local/bin/lcm /usr/local/bin/lcm.prev /usr/local/bin/lcm.failed /usr/local/bin/lcm.new
rm -f /run/lcm-self-install.lock /run/lcm-self-install.pending
systemctl daemon-reload

if ((purge)); then
	log "purging data, configuration, toolchains and the lcm account"
	rm -rf /var/lib/lcm /etc/lcm /opt/lcm
	if id -u lcm >/dev/null 2>&1; then userdel lcm; fi
	getent group lcm >/dev/null && groupdel lcm || true
else
	echo "Kept /var/lib/lcm (database, secrets, checkout), /etc/lcm, /opt/lcm and the lcm account."
	echo "Run with --purge to remove them as well."
fi
