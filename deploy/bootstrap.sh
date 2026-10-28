#!/usr/bin/env bash
# Install LCM on this host, or bring an installation up to date. Idempotent.
#   sudo deploy/bootstrap.sh [options]        (--help lists them)
# Docs: docs/operations.md#install
set -Eeuo pipefail
umask 022
export PATH=/usr/local/sbin:/usr/local/bin:/usr/sbin:/usr/bin:/sbin:/bin LC_ALL=C

repo_dir=$(cd -- "$(dirname -- "${BASH_SOURCE[0]}")/.." && pwd -P)
binary=$repo_dir/bin/lcm
operator=${SUDO_USER:-}
go_root='' node_root=''
ts_host=acserver.tailcfcac2.ts.net
ts_port=9443
repo_url=https://github.com/ACaccel/LCM.git
replace_binary=0

usage() {
	cat <<EOF
Usage: sudo deploy/bootstrap.sh [options]
  --binary PATH      lcm binary to install when none is installed (default: bin/lcm, from 'make build')
  --replace-binary   install --binary even if /usr/local/bin/lcm exists (self-deploy keeps it current)
  --operator USER    account that gets the seed token and owns the default toolchains (default: \$SUDO_USER)
  --go-root DIR      Go toolchain to copy (default: ~OPERATOR/.local/go)
  --node-root DIR    Node.js toolchain to copy (default: ~OPERATOR/.local/node)
  --ts-host NAME     tailnet host name served by tailscale serve (default: $ts_host)
  --ts-port PORT     HTTPS port of tailscale serve (default: $ts_port)
  --repo URL         repository LCM deploys itself from (default: $repo_url)
EOF
}

log() { printf '==> %s\n' "$*"; }
note() { printf '    %s\n' "$*"; }
die() { printf 'bootstrap: %s\n' "$*" >&2; exit 1; }
trap 'die "failed at line $LINENO: $BASH_COMMAND"' ERR

while (($#)); do
	case $1 in
	--binary | --operator | --go-root | --node-root | --ts-host | --ts-port | --repo)
		(($# >= 2)) || die "$1 needs a value"
		case $1 in
		--binary) binary=$2 ;; --operator) operator=$2 ;; --go-root) go_root=$2 ;;
		--node-root) node_root=$2 ;; --ts-host) ts_host=$2 ;; --ts-port) ts_port=$2 ;; --repo) repo_url=$2 ;;
		esac
		shift 2
		;;
	--replace-binary) replace_binary=1 && shift ;;
	-h | --help) usage && exit 0 ;;
	*) usage >&2 && exit 2 ;;
	esac
done

# Validate everything before changing the host.
((EUID == 0)) || die "run as root: sudo deploy/bootstrap.sh"
for tool in useradd install visudo systemctl systemd-analyze systemd-run setpriv git curl jq tailscale flock make cmp diff; do
	command -v "$tool" >/dev/null || die "missing required tool: $tool"
done
[[ $operator =~ ^[a-z_][a-z0-9_-]*$ && $operator != root ]] || die "--operator must name a non-root account (got '${operator}')"
operator_home=$(getent passwd "$operator" | cut -d: -f6) || die "no such account: $operator"
operator_group=$(id -gn -- "$operator")
go_root=${go_root:-$operator_home/.local/go}
node_root=${node_root:-$operator_home/.local/node}
[[ -x $go_root/bin/go ]] || die "no Go toolchain at $go_root"
[[ -x $node_root/bin/node && -e $node_root/bin/npm ]] || die "no Node.js toolchain at $node_root"
[[ $ts_host =~ ^[a-z0-9.-]+$ ]] || die "invalid --ts-host"
[[ $ts_port =~ ^[0-9]{1,5}$ ]] && ((ts_port > 0 && ts_port < 65536)) || die "invalid --ts-port"
[[ $repo_url =~ ^https://[A-Za-z0-9./_-]+$ ]] || die "--repo must be an https URL without credentials"
[[ -f $binary && ! -L $binary ]] || die "no binary at $binary; run 'make build' first"
self_dns=$(tailscale status --json | jq -r '.Self.DNSName' | sed 's/\.$//')
[[ $self_dns == "$ts_host" ]] || die "this node is '$self_dns' on the tailnet, not --ts-host $ts_host"
serve_json=$(tailscale serve status --json)
serve_state=$(jq -r --arg hp "$ts_host:$ts_port" --arg port "$ts_port" '
	if (.TCP[$port] // null) == null then "free"
	elif .Web[$hp].Handlers == {"/": {"Proxy": "http://127.0.0.1:7800"}} and (.AllowFunnel[$hp] // false) == false then "ours"
	else "taken" end' <<<"$serve_json")
[[ $serve_state != taken ]] || die "tailscale serve already uses port $ts_port for something else; pick another --ts-port"

staging=$(mktemp -d)
trap 'rm -rf -- "$staging"' EXIT
chmod 0755 "$staging"
# Check the staged copy, run as nobody; the same copy is installed below.
install -m 0755 -- "$binary" "$staging/lcm"
(cd / && setpriv --reuid=nobody --regid=nogroup --clear-groups -- env -i "$staging/lcm" version) | grep -q '^lcm ' ||
	die "$binary does not look like an lcm binary"
install -m 0440 "$repo_dir/deploy/sudoers" "$staging/sudoers"
visudo -cf "$staging/sudoers" >/dev/null || die "deploy/sudoers does not pass visudo"

# changed DEST: whether installing a file over DEST changes it.
changed() { ! cmp -s -- "$1" "$2"; }

log "account lcm"
if id -u lcm >/dev/null 2>&1; then
	[[ $(getent passwd lcm | cut -d: -f6) == /var/lib/lcm ]] || die "account lcm exists with another home directory"
	note "exists"
else
	useradd --system --user-group --home-dir /var/lib/lcm --no-create-home --shell /usr/sbin/nologin lcm
	note "created"
fi
as_lcm() { (cd / && exec setpriv --reuid=lcm --regid=lcm --clear-groups -- env -i PATH="$PATH" HOME=/var/lib/lcm LC_ALL=C "$@"); }

log "directories and configuration"
fresh_db=0
[[ -e /var/lib/lcm/lcm.db ]] || fresh_db=1
install -d -o lcm -g lcm -m 0750 /var/lib/lcm /var/lib/lcm/work /var/lib/lcm/cache
install -d -o root -g root -m 0755 /etc/lcm /opt/lcm /opt/lcm/toolchain /usr/local/libexec/lcm
if [[ -e /etc/lcm/lcm.env ]]; then
	if changed "$repo_dir/deploy/lcm.env" /etc/lcm/lcm.env; then
		note "kept the existing /etc/lcm/lcm.env (differs from deploy/lcm.env)"
	fi
else
	install -o root -g lcm -m 0640 "$repo_dir/deploy/lcm.env" /etc/lcm/lcm.env
	note "wrote /etc/lcm/lcm.env"
fi

# copy_toolchain SRC NAME: refresh /opt/lcm/toolchain/NAME when it differs
# from SRC. Links must stay inside the copy, so that the operator cannot
# redirect what lcm executes. Nothing from SRC is executed as root.
copy_toolchain() {
	local src=$1 name=$2 dst=/opt/lcm/toolchain/$2 link
	if [[ -d $dst ]] && diff -rq --no-dereference -- "$src" "$dst" >/dev/null 2>&1; then
		note "$name: unchanged"
		return 0
	fi
	rm -rf -- "$dst.new" "$dst.old"
	cp -a --no-preserve=ownership -- "$src" "$dst.new"
	chown -R root:root -- "$dst.new"
	chmod -R go-w -- "$dst.new"
	while IFS= read -r -d '' link; do
		[[ $(realpath -m -- "$link") == "$dst.new"/* ]] || die "$src: link $link points outside the toolchain"
	done < <(find "$dst.new" -type l -print0)
	if [[ -e $dst ]]; then mv -- "$dst" "$dst.old"; fi
	mv -- "$dst.new" "$dst"
	rm -rf -- "$dst.old"
	note "$name: copied from $src"
}
log "toolchains in /opt/lcm/toolchain"
copy_toolchain "$go_root" go
copy_toolchain "$node_root" node
note "$(as_lcm /opt/lcm/toolchain/go/bin/go version), node $(as_lcm /opt/lcm/toolchain/node/bin/node --version)"

log "binary, self-install, sudoers rule and unit"
if [[ ! -e /usr/local/bin/lcm ]] || ((replace_binary)); then
	install -m 0755 -- "$staging/lcm" /usr/local/bin/lcm.new
	mv -f -- /usr/local/bin/lcm.new /usr/local/bin/lcm
	note "installed $(as_lcm /usr/local/bin/lcm version)"
else
	note "kept $(as_lcm /usr/local/bin/lcm version) (self-deploy updates it; --replace-binary forces $binary)"
fi
install -m 0755 "$repo_dir/deploy/self-install" /usr/local/libexec/lcm/self-install
install -m 0440 "$staging/sudoers" /etc/sudoers.d/lcm
visudo -c >/dev/null || { rm -f /etc/sudoers.d/lcm; die "sudoers no longer parses with /etc/sudoers.d/lcm; removed it"; }
unit_changed=0
if changed "$repo_dir/deploy/lcm.service" /etc/systemd/system/lcm.service; then
	install -m 0644 "$repo_dir/deploy/lcm.service" /etc/systemd/system/lcm.service
	unit_changed=1
fi
systemd-analyze verify /etc/systemd/system/lcm.service

log "self-deploy checkout /var/lib/lcm/work/lcm"
if [[ -d /var/lib/lcm/work/lcm/.git ]]; then
	origin=$(as_lcm git -C /var/lib/lcm/work/lcm remote get-url origin)
	[[ $origin == "$repo_url" ]] || die "/var/lib/lcm/work/lcm tracks $origin, not $repo_url"
	note "exists"
else
	as_lcm env GIT_TERMINAL_PROMPT=0 git clone --quiet --branch main -- "$repo_url" /var/lib/lcm/work/lcm
	note "cloned $repo_url"
fi

log "lcm.service"
systemctl daemon-reload
systemctl enable --quiet lcm.service
if ! systemctl is-active --quiet lcm.service; then
	systemctl start lcm.service
	note "started"
elif ((unit_changed)); then
	systemctl restart lcm.service
	note "restarted for the new unit file"
else
	note "running"
fi
for _ in $(seq 60); do
	curl -fsS --max-time 2 http://127.0.0.1:7800/api/v1/health >/dev/null 2>&1 && break
	sleep 1
done
curl -fsS --max-time 2 http://127.0.0.1:7800/api/v1/health >/dev/null || die "LCM is not healthy; see journalctl -u lcm"
note "healthy: $(curl -fsS http://127.0.0.1:7800/api/v1/health)"

log "admin account and seed token"
if ((fresh_db)) && [[ -t 0 ]]; then
	note "set the password of the web console's admin account (min 8 characters)"
	as_lcm /usr/local/bin/lcm admin set-password --data-dir /var/lib/lcm
else
	note "unchanged; to set the admin password: sudo -u lcm /usr/local/bin/lcm admin set-password --data-dir /var/lib/lcm"
fi
token_file=$operator_home/.config/lcm/token
if [[ -e $token_file ]]; then
	note "kept $token_file"
else
	# The operator's own process writes the file, so root never writes into
	# the operator's home directory.
	token=$(as_lcm /usr/local/bin/lcm token create --name "seed-$operator" --data-dir /var/lib/lcm)
	[[ -n $token ]] || die "lcm token create printed nothing"
	printf '%s\n' "$token" | (cd / && exec setpriv --reuid="$operator" --regid="$operator_group" --init-groups -- \
		env -i PATH="$PATH" sh -c 'umask 077 && mkdir -p -- "${1%/*}" && cat >"$1.tmp" && mv -f -- "$1.tmp" "$1"' sh "$token_file")
	unset token
	note "wrote an admin API token to $token_file (mode 0600)"
fi

log "tailscale serve https://$ts_host:$ts_port"
if [[ $serve_state == ours ]]; then
	note "already proxies to http://127.0.0.1:7800"
else
	tailscale serve --bg --yes --https="$ts_port" http://127.0.0.1:7800 >/dev/null
	note "proxies to http://127.0.0.1:7800 (tailnet only)"
fi

cat <<EOF

LCM is running at https://$ts_host:$ts_port (tailnet only).
Next, as $operator: deploy/seed.sh
It registers LCM's self-deploy service and policy, records the first
deployment of main and adds monitoring-only services for this host.
EOF
