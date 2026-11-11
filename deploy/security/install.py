#!/usr/bin/python3
"""Root installer of LCM's optional security component.

"lcm host apply" runs this file with python3 -I from a root-only staging
directory, next to the runtime.py and helper.py of the same build, and passes
the wanted state as one JSON object on stdin. Every action prints one JSON
object on stdout:

  plan, apply              converge the component (install, adopt, upgrade)
  remove-plan, remove      remove the active parts, keep baseline and reports
  cleanup-plan, cleanup    remove the known leftovers of earlier installers
  revert-plan, revert      undo the last recorded transaction

The *-plan actions change nothing. Every change of apply, remove and revert is
recorded first in a transaction manifest with a copy of each file it
replaces, so that "revert" can put the previous state back.

The installed copy in /usr/local/lib/workstation-security serves the local
operator actions that never run from LCM: baseline, verify, review, status.
"""

from __future__ import annotations

import argparse
import contextlib
import datetime as dt
import fcntl
import grp
import hashlib
import importlib.util
import json
import os
from pathlib import Path
import pwd
import re
import shutil
import stat
import subprocess
import sys
import tempfile
import time
import urllib.parse

BASE = Path('/var/lib/workstation-security')
LIB = Path('/usr/local/lib/workstation-security')
ETC = Path('/etc/workstation-security')
USER = 'workstation-security'
TOKEN = ETC / 'ingest-token'
CREDENTIAL = 'ingest-token'
COLLECTOR_CONFIG = ETC / 'collector.json'
TRANSACTIONS = BASE / 'backups' / 'transactions'
LOGROTATE_DIR = BASE / 'logrotate'
LOGROTATE_CONF = Path('/etc/logrotate-workstation-nginx-security.conf')
HELPER = Path('/usr/local/libexec/lcm/security-host/helper.py')
HELPER_STATE = Path('/var/lib/lcm-security-host')
SYSTEMD = Path('/etc/systemd/system')
AUDITD_CONF = Path('/etc/audit/auditd.conf')
AUDIT_RULES = Path('/etc/audit/rules.d')
AIDE_DEFAULTS = Path('/etc/default/aide')
AUDIT_LOG_DIR = Path('/var/log/audit')
NGINX = Path('/usr/sbin/nginx')
VENDOR_TIMERS_RECORD = BASE / 'vendor-timers.json'
AUDIT_DIR_RECORD = BASE / 'audit-dir.json'
COLLECTOR = 'workstation-security-collector.service'
AUDIT_STATUS = 'workstation-security-audit-status.service'
AUDIT_TIMER = 'workstation-security-audit-status.timer'
AIDE_SERVICE = 'workstation-security-aide.service'
AIDE_TIMER = 'workstation-security-aide.timer'
ROTATE_SERVICE = 'workstation-nginx-security-logrotate.service'
ROTATE_TIMER = 'workstation-nginx-security-logrotate.timer'
DEFAULT_TIMERS = ('aidecheck.timer', 'aide.timer', 'dailyaidecheck.timer', 'aide-daily.timer')
PACKAGES = ('auditd', 'aide')
PACKAGE_TOOLS = ('/usr/sbin/auditd', '/usr/sbin/auditctl', '/usr/sbin/augenrules', '/usr/sbin/ausearch', '/usr/bin/aide')
ENDPOINT_PATH = '/api/v1/hooks/security'
# Root operations hold CONTROL_LOCK; PENDING tells the helper, which the lcm
# account can start through sudo at any time, to refuse new requests until
# the root operation has the lock and is done.
CONTROL_LOCK = Path('/run/lcm-security.lock')
PENDING = Path('/run/lcm-security.pending')
# The lock of the helper this component replaces; adoption holds it so the
# old helper cannot change files meanwhile.
LEGACY_HELPER_LOCK = HELPER_STATE / 'control.lock'
LOCK_WAIT = 60
# Finished transactions kept for review and revert; older ones are removed,
# with the file copies (including a token) they hold.
KEEP_TRANSACTIONS = 10
# LCM's own root tools, the only trees "review" reads.
TOOL_PATHS = ('/usr/local/bin/lcm', str(LIB))
TOOL_DIRS = ('/usr/local/libexec',)
# Files of the installers this component replaces. Adoption converts or
# removes them in its first recorded transaction.
LEGACY_HOOKS = Path('/etc/nginx/workstation-security')
LEGACY_HOOK_FILES = ('card-api-10-observation.conf', 'card-socket-10-observation.conf')
LEGACY_LOGROTATE_STATE = Path('/var/lib/workstation-security-nginx-20261101/logrotate.status')
# The exact leftovers "cleanup" removes; anything else is only reported.
LEFTOVER_UNITS = ('workstation-security-ssh-auth-rollback.service', 'workstation-security-ssh-auth-rollback.timer')
LEFTOVER_DIRS = tuple('/var/lib/workstation-security-' + suffix for suffix in (
    '20261101', 'deploy-20261029', 'nginx-20261101', 'lcm-20261101', 'lcm-repair-20261101',
    'repair-20261101', 'completion-20261102', 'ssh-auth-20261029')) + tuple('/var/lib/lcm-security-' + suffix for suffix in (
    'console-20261102', 'console-release-20261102'))
LEFTOVER_FAMILIES = ('workstation-security-', 'lcm-security-')
OWN_STATE_DIRS = ('/var/lib/workstation-security', str(HELPER_STATE))

WATCH_CATEGORIES = ('accounts', 'ssh', 'sudoers', 'cron', 'systemd', 'deploy', 'nginx')
WATCHES = {
    'accounts': ('/etc/passwd', '/etc/group', '/etc/shadow', '/etc/gshadow'),
    'ssh': ('/etc/ssh',),
    'sudoers': ('/etc/sudoers', '/etc/sudoers.d'),
    'cron': ('/etc/crontab', '/etc/cron.d', '/etc/cron.daily', '/etc/cron.hourly',
             '/etc/cron.weekly', '/etc/cron.monthly', '/var/spool/cron/crontabs'),
    'systemd': ('/etc/systemd/system', '/etc/systemd/user'),
    'deploy': ('/usr/local/bin/lcm', '/etc/lcm', '/usr/local/libexec/lcm'),
    'nginx': ('/etc/nginx',),
    'probe': (str(BASE / 'audit-probe'),),
}
# Binaries whose integrity matters beyond the watched directories.
CRITICAL = ('/usr/bin/sudo', '/usr/sbin/sshd', '/usr/sbin/nginx', str(LIB))
AUDIT_OPTIONS = {'max_log_file': '32', 'num_logs': '8', 'max_log_file_action': 'ROTATE',
                 'space_left': '512', 'space_left_action': 'SYSLOG', 'admin_space_left': '128',
                 'admin_space_left_action': 'SYSLOG', 'disk_full_action': 'SYSLOG',
                 'disk_error_action': 'SYSLOG', 'log_group': USER}
SAFE_PATH = re.compile(r'/[A-Za-z0-9_./-]+')
MACHINE_ID = re.compile(r'[A-Za-z0-9][A-Za-z0-9._:-]{0,99}')
SERVICE = re.compile(r'[a-z][a-z0-9-]{0,62}')
TRANSACTION_ID = re.compile(r'[0-9]{8}T[0-9]{6}Z-[0-9a-f]{8}')
MAX_FILE = 1024 * 1024
# Host configuration allows 32 extra paths per category; deploy also holds
# the directories of every granted service.
MAX_WATCH = 256
ENV = {'PATH': '/usr/sbin:/usr/bin:/sbin:/bin', 'LANG': 'C.UTF-8', 'DEBIAN_FRONTEND': 'noninteractive',
       'NEEDRESTART_MODE': 'l'}


class Failure(Exception):
    """A refusal with a message for the administrator."""


class Blocked(Failure):
    """A difference that needs the administrator; nothing was changed."""


def digest(value: bytes) -> str:
    return hashlib.sha256(value).hexdigest()


def stamp() -> str:
    return dt.datetime.now(dt.timezone.utc).isoformat().replace('+00:00', 'Z')


def canonical(value: object) -> bytes:
    return json.dumps(value, separators=(',', ':'), sort_keys=True).encode()


def load_helper(directory: Path):
    """Load helper.py of the same build: it renders the Nginx files and
    defines the policy that both programs share."""
    path = directory / 'helper.py'
    info = path.lstat()
    if not stat.S_ISREG(info.st_mode) or info.st_size > MAX_FILE:
        raise Failure('helper.py is missing next to install.py')
    spec = importlib.util.spec_from_file_location('lcm_security_helper', path)
    module = importlib.util.module_from_spec(spec)
    spec.loader.exec_module(module)
    return module


# ---------------------------------------------------------------------------
# Host access. Paths are host paths; a test root maps them below a directory.


class Host:
    def __init__(self, root: Path = Path('/'), runner=None, accounts=None):
        self.root = root
        self.real = root == Path('/')
        self.runner = runner
        # Tests describe accounts as {name: (uid, gid, shell, groups)}.
        self.accounts = accounts

    def p(self, path) -> Path:
        return self.root / str(path).lstrip('/')

    def run(self, argv: list[str], timeout: int = 120, check: bool = True, stdin: bytes | None = None):
        if self.runner is not None:
            result = self.runner(argv, timeout=timeout, stdin=stdin)
        else:
            # Children never inherit the installer's stdin, which carried its input.
            result = subprocess.run(argv, input=stdin if stdin is not None else b'', capture_output=True,
                                    timeout=timeout, check=False, env=ENV, cwd='/')
            result = Result(result.returncode, result.stdout.decode('utf-8', 'replace'),
                            result.stderr.decode('utf-8', 'replace'))
        if check and result.code != 0:
            message = '{} failed with status {}'.format(' '.join(argv[:3]), result.code)
            tail = excerpt(result.err) or excerpt(result.out)
            raise Failure('{}: {}'.format(message, tail) if tail else message)
        return result

    def info(self, path):
        try:
            return os.lstat(self.p(path))
        except FileNotFoundError:
            return None

    def exists(self, path) -> bool:
        return self.info(path) is not None

    def owner_ok(self, info, uid: int, gid: int) -> bool:
        # A test root belongs to the test user; ownership is only checked on
        # the host itself.
        return not self.real or (info.st_uid == uid and info.st_gid == gid)

    def trusted(self, path, missing: bool = False) -> None:
        """Refuse a path with a symbolic link, or, on the host, an ancestor
        that someone other than root could change."""
        path = Path(path)
        if not path.is_absolute() or any(part in ('.', '..') for part in path.parts):
            raise Failure('{} is not an absolute, clean path'.format(path))
        absent = False
        for item in (*reversed(path.parents), path):
            info = self.info(item)
            if info is None:
                if missing:
                    absent = True
                    continue
                raise Failure('{} does not exist'.format(item))
            if absent:
                raise Failure('{} appeared below a missing directory'.format(item))
            if stat.S_ISLNK(info.st_mode):
                raise Failure('{} is a symbolic link; host files and their directories must not be'.format(item))
            if self.real and item != path and (info.st_uid != 0 or info.st_mode & 0o022):
                raise Failure('{} could be changed by an account other than root; review it'.format(item))

    def read(self, path, limit: int = MAX_FILE) -> bytes:
        self.trusted(Path(path).parent)
        descriptor = os.open(self.p(path), os.O_RDONLY | os.O_NOFOLLOW | os.O_NONBLOCK | os.O_CLOEXEC)
        with os.fdopen(descriptor, 'rb') as stream:
            info = os.fstat(stream.fileno())
            if not stat.S_ISREG(info.st_mode) or info.st_size > limit:
                raise Failure('{} is not a regular file of at most {} bytes'.format(path, limit))
            data = stream.read(limit + 1)
        if len(data) > limit:
            raise Failure('{} is larger than {} bytes'.format(path, limit))
        return data

    def read_optional(self, path, limit: int = MAX_FILE) -> bytes | None:
        return self.read(path, limit) if self.exists(path) else None

    def write(self, path, data: bytes, mode: int, uid: int = 0, gid: int = 0) -> None:
        path = Path(path)
        self.trusted(path.parent)
        info = self.info(path)
        if info is not None and not stat.S_ISREG(info.st_mode):
            raise Failure('{} is not a regular file'.format(path))
        descriptor, temporary = tempfile.mkstemp(prefix='.lcm-security-', dir=self.p(path.parent))
        try:
            with os.fdopen(descriptor, 'wb') as stream:
                os.fchmod(stream.fileno(), mode)
                if self.real:
                    os.fchown(stream.fileno(), uid, gid)
                stream.write(data)
                stream.flush()
                os.fsync(stream.fileno())
            os.replace(temporary, self.p(path))
            self.sync_dir(path.parent)
        finally:
            if os.path.lexists(temporary):
                os.unlink(temporary)

    def sync_dir(self, path) -> None:
        descriptor = os.open(self.p(path), os.O_RDONLY | os.O_DIRECTORY | os.O_CLOEXEC)
        try:
            os.fsync(descriptor)
        finally:
            os.close(descriptor)

    def mkdir(self, path, mode: int, uid: int = 0, gid: int = 0) -> None:
        self.trusted(Path(path).parent)
        os.mkdir(self.p(path), mode)
        self.set_meta(path, mode, uid, gid)

    def set_meta(self, path, mode: int, uid: int, gid: int) -> None:
        """Change owner and mode through a descriptor that refuses links."""
        self.trusted(Path(path).parent)
        before = self.info(path)
        flags = os.O_RDONLY | os.O_NOFOLLOW | os.O_CLOEXEC | os.O_NONBLOCK
        if before is not None and stat.S_ISDIR(before.st_mode):
            flags |= os.O_DIRECTORY
        descriptor = os.open(self.p(path), flags)
        try:
            info = os.fstat(descriptor)
            if before is None or (info.st_dev, info.st_ino) != (before.st_dev, before.st_ino):
                raise Failure('{} changed while it was being updated'.format(path))
            if self.real:
                os.fchown(descriptor, uid, gid)
            os.fchmod(descriptor, mode)
        finally:
            os.close(descriptor)

    def remove(self, path) -> None:
        self.trusted(Path(path).parent)
        info = self.info(path)
        if info is None:
            return
        if stat.S_ISDIR(info.st_mode):
            os.rmdir(self.p(path))
        else:
            os.unlink(self.p(path))
        self.sync_dir(Path(path).parent)

    def listdir(self, path) -> list[str]:
        return sorted(os.listdir(self.p(path))) if self.exists(path) else []

    def account(self, name: str):
        """(uid, gid, shell, supplementary group IDs) or None."""
        if self.accounts is not None:
            return self.accounts.get(name)
        try:
            entry = pwd.getpwnam(name)
        except KeyError:
            return None
        groups = tuple(g.gr_gid for g in grp.getgrall() if name in g.gr_mem)
        return entry.pw_uid, entry.pw_gid, entry.pw_shell, groups

    def group(self, name: str):
        if self.accounts is not None:
            entry = self.accounts.get(name)
            return entry[1] if entry else self.accounts.get('group:' + name)
        try:
            return grp.getgrnam(name).gr_gid
        except KeyError:
            return None


class Result:
    def __init__(self, code: int, out: str, err: str = ''):
        self.code = code
        self.out = out
        self.err = err


EXCERPT_LINES = 6
EXCERPT_CHARS = 600


def excerpt(text: str) -> str:
    """The last lines of a failed command's output on one printable line:
    tools such as apt and nginx print the cause before their summary."""
    lines = [line.strip() for line in text.replace('\r', '\n').replace('\t', ' ').split('\n')]
    lines = [line for line in lines if line][-EXCERPT_LINES:]
    flat = ' | '.join(lines)
    flat = ''.join(c if c.isprintable() else '?' for c in flat)
    return flat[-EXCERPT_CHARS:]


def lock(host: Host, path: Path, busy: str, wait: float = 0) -> int:
    """Take an exclusive lock, waiting at most wait seconds for it."""
    host.trusted(path.parent)
    descriptor = os.open(host.p(path), os.O_WRONLY | os.O_CREAT | os.O_NOFOLLOW | os.O_CLOEXEC, 0o600)
    try:
        info = os.fstat(descriptor)
        if not stat.S_ISREG(info.st_mode) or info.st_nlink != 1 or (host.real and (info.st_uid != 0 or info.st_mode & 0o077)):
            raise Failure('{} is not a root-only lock file'.format(path))
        deadline = time.monotonic() + wait
        while True:
            try:
                fcntl.flock(descriptor, fcntl.LOCK_EX | fcntl.LOCK_NB)
                break
            except BlockingIOError:
                if time.monotonic() >= deadline:
                    raise Failure(busy) from None
                time.sleep(0.5)
    except OSError:
        os.close(descriptor)
        raise Failure(busy) from None
    except BaseException:
        os.close(descriptor)
        raise
    return descriptor


def hold(host: Host, paths, busy: str, wait: float | None = None) -> list[int]:
    wait = LOCK_WAIT if wait is None else wait
    descriptors = []
    try:
        for path in paths:
            descriptors.append(lock(host, Path(path), busy, wait))
    except BaseException:
        release(descriptors)
        raise
    return descriptors


def release(descriptors: list[int]) -> None:
    for descriptor in reversed(descriptors):
        os.close(descriptor)


# ---------------------------------------------------------------------------
# Rendering.


def safe_config(existing: str, settings: dict[str, str], delimiter: str = ' = ') -> str:
    """Set keys in a KEY=VALUE file, keep every other line."""
    remaining = dict(settings)
    lines = []
    for line in existing.splitlines():
        match = re.match(r'^\s*([A-Za-z_][A-Za-z0-9_]*)\s*=', line)
        if match and match[1] in settings:
            if match[1] in remaining:
                lines.append(match[1] + delimiter + remaining.pop(match[1]))
            continue
        lines.append(line)
    lines.extend(key + delimiter + value for key, value in remaining.items())
    return '\n'.join(lines) + '\n'


def audit_rules(paths: dict[str, list[str]]) -> str:
    lines = ['# Managed by the LCM security component; preserve all other audit rule files.']
    for category, targets in sorted(paths.items()):
        if category not in WATCHES:
            raise Failure('unknown audit category ' + category)
        for target in sorted(set(targets)):
            if not SAFE_PATH.fullmatch(target) or any(part in ('.', '..') for part in target.split('/')):
                raise Failure('unsafe audit path ' + target)
            lines.append(f'-w {target} -p wa -k wssec_{category}')
    return '\n'.join(lines) + '\n'


def aide_config(paths: list[str], ignore: list[str] = ()) -> str:
    header = """# Managed by the LCM security component: a curated baseline without the vendor's broad defaults.
database_in=file:/var/lib/workstation-security/aide/aide.db
database_out=file:/var/lib/workstation-security/aide/aide.db.new
gzip_dbout=no
report_level=summary
SEC = p+ftype+i+l+n+u+g+s+m+c+sha256
"""
    lines = [header.rstrip()]
    for path in sorted(set(paths)):
        lines.append(re.escape(path) + '(/|$) SEC')
    # Excluded below a selected path, such as the releases LCM deploys and
    # records itself.
    for path in sorted(set(ignore)):
        lines.append('!' + re.escape(path) + '(/|$)')
    return '\n'.join(lines) + '\n'


SANDBOX = """NoNewPrivileges=yes
PrivateTmp=yes
ProtectSystem=strict
ProtectHome=yes
ProtectKernelTunables=yes
ProtectKernelModules=yes
ProtectControlGroups=yes
RestrictSUIDSGID=yes
LockPersonality=yes
RestrictRealtime=yes
UMask=0077
"""


def unit_files(nginx: bool) -> dict[str, str]:
    units = {
        COLLECTOR: """[Unit]
Description=Sanitized workstation security collector for LCM
After=network.target auditd.service lcm.service

[Service]
Type=simple
User=workstation-security
Group=workstation-security
ExecStart=/usr/bin/python3 -I /usr/local/lib/workstation-security/runtime.py collect
LoadCredential=ingest-token:/etc/workstation-security/ingest-token
Restart=on-failure
RestartSec=15s
TimeoutStopSec=20s
MemoryMax=128M
TasksMax=16
CPUQuota=20%
ReadWritePaths=/var/lib/workstation-security/collector
ReadOnlyPaths=/etc/workstation-security /var/lib/workstation-security/reports
InaccessiblePaths=/var/lib/workstation-security/aide /var/lib/workstation-security/backups
RestrictAddressFamilies=AF_INET AF_INET6 AF_UNIX
IPAddressDeny=any
IPAddressAllow=localhost
CapabilityBoundingSet=
""" + SANDBOX + '\n[Install]\nWantedBy=multi-user.target\n',
        AUDIT_STATUS: """[Unit]
Description=Read kernel audit health into sanitized local report
After=auditd.service

[Service]
Type=oneshot
ExecStart=/usr/bin/python3 -I /usr/local/lib/workstation-security/runtime.py audit-status
ReadWritePaths=/var/lib/workstation-security/reports
RestrictAddressFamilies=AF_UNIX AF_NETLINK
CapabilityBoundingSet=CAP_AUDIT_CONTROL CAP_AUDIT_READ
MemoryMax=64M
TimeoutStartSec=30s
""" + SANDBOX,
        AUDIT_TIMER: """[Unit]
Description=Observe audit loss and backlog every minute

[Timer]
OnBootSec=30s
OnUnitActiveSec=60s
AccuracySec=5s
Unit=workstation-security-audit-status.service

[Install]
WantedBy=timers.target
""",
        AIDE_SERVICE: """[Unit]
Description=Check reviewed workstation integrity baseline

[Service]
Type=oneshot
ExecStart=/usr/bin/python3 -I /usr/local/lib/workstation-security/runtime.py aide-check
ReadWritePaths=/var/lib/workstation-security/reports
ReadOnlyPaths=/var/lib/workstation-security/aide
Nice=19
IOSchedulingClass=idle
MemoryMax=512M
TimeoutStartSec=3700s
RestrictAddressFamilies=AF_UNIX
""" + SANDBOX.replace('ProtectHome=yes', 'ProtectHome=read-only'),
        AIDE_TIMER: """[Unit]
Description=Daily off-peak workstation integrity check

[Timer]
OnCalendar=*-*-* 04:20:00
RandomizedDelaySec=10m
Persistent=true
Unit=workstation-security-aide.service

[Install]
WantedBy=timers.target
""",
    }
    if nginx:
        units[ROTATE_SERVICE] = """[Unit]
Description=Rotate the dedicated Nginx security observation log
After=nginx.service
ConditionPathExists=/var/log/nginx/workstation-security.jsonl

[Service]
Type=oneshot
ExecStart=/usr/sbin/logrotate --state /var/lib/workstation-security/logrotate/status /etc/logrotate-workstation-nginx-security.conf
UMask=0027
NoNewPrivileges=true
PrivateTmp=true
ProtectSystem=strict
ProtectHome=true
ReadWritePaths=/var/log/nginx /var/lib/workstation-security/logrotate
ProtectKernelTunables=true
ProtectKernelModules=true
ProtectControlGroups=true
RestrictAddressFamilies=AF_UNIX
Nice=15
IOSchedulingClass=idle
"""
        units[ROTATE_TIMER] = """[Unit]
Description=Check Nginx security log size every five minutes

[Timer]
OnBootSec=5min
OnUnitActiveSec=5min
AccuracySec=15s
RandomizedDelaySec=15s
Unit=workstation-nginx-security-logrotate.service

[Install]
WantedBy=timers.target
"""
    return units


LOGROTATE = """/var/log/nginx/workstation-security.jsonl {
    daily
    maxsize 20M
    rotate 14
    missingok
    notifempty
    compress
    delaycompress
    create 0640 root workstation-security
    sharedscripts
    postrotate
        /usr/bin/systemctl kill --kill-whom=main --signal=USR1 nginx.service
    endscript
}
"""

ENABLED_UNITS = (COLLECTOR, AUDIT_TIMER, AIDE_TIMER)


def validate_inputs(value: object, action: str) -> dict:
    """Every input is checked again here: the installer runs as root."""
    if not isinstance(value, dict) or value.get('version') != 1:
        raise Failure('unsupported installer input')
    allowed = {'version', 'machineId', 'endpoint', 'services', 'watch', 'aideIgnore', 'token'}
    if set(value) - allowed:
        raise Failure('unexpected installer input fields')
    if action in ('plan', 'apply'):
        if not isinstance(value.get('machineId'), str) or not MACHINE_ID.fullmatch(value['machineId']):
            raise Failure('machineId must be the immutable Machine ID')
        endpoint = value.get('endpoint')
        parsed = urllib.parse.urlsplit(endpoint) if isinstance(endpoint, str) else None
        if (parsed is None or parsed.scheme != 'http' or parsed.hostname not in ('127.0.0.1', '::1') or parsed.port is None
                or parsed.path != ENDPOINT_PATH or parsed.username or parsed.password or parsed.query or parsed.fragment):
            raise Failure('endpoint must be the loopback LCM security ingest URL with a port')
    services = value.get('services', [])
    if (not isinstance(services, list) or len(services) > 64 or services != sorted(set(services))
            or not all(isinstance(name, str) and SERVICE.fullmatch(name) for name in services)):
        raise Failure('services must be a sorted list of service names')
    watch = value.get('watch', {})
    if not isinstance(watch, dict):
        raise Failure('watch must be an object')
    for category, paths in watch.items():
        if category not in WATCH_CATEGORIES:
            raise Failure('watch.{} is not a watched category'.format(category))
        if not isinstance(paths, list) or len(paths) > MAX_WATCH:
            raise Failure('watch.{} must be a list of at most {} paths'.format(category, MAX_WATCH))
        for path in paths:
            if (not isinstance(path, str) or not SAFE_PATH.fullmatch(path) or path == '/' or path.endswith('/')
                    or '//' in path or any(part in ('.', '..') for part in path.split('/'))):
                raise Failure('watch.{} must hold absolute, clean paths (got {!r})'.format(category, path))
    ignore = value.get('aideIgnore', [])
    if not isinstance(ignore, list) or len(ignore) > MAX_WATCH or not all(
            isinstance(path, str) and SAFE_PATH.fullmatch(path) and not path.endswith('/') and '//' not in path
            and not any(part in ('.', '..') for part in path.split('/')) for path in ignore):
        raise Failure('aideIgnore must hold absolute, clean paths')
    token = value.get('token')
    if token is not None and (not isinstance(token, str) or not re.fullmatch(r'[!-~]{1,4096}', token)):
        raise Failure('token must be printable ASCII without spaces')
    return value


# ---------------------------------------------------------------------------
# Transactions: every change is recorded before it is made.


def describe(host: Host, path, kind: str) -> dict:
    """What a revert compares: existence, type, mode and, for a file entry,
    the content digest. A meta entry only changed owner and mode."""
    info = host.info(path)
    if info is None:
        return {'exists': False}
    if stat.S_ISDIR(info.st_mode):
        return {'exists': True, 'dir': True, 'mode': stat.S_IMODE(info.st_mode)}
    if kind == 'meta':
        return {'exists': True, 'mode': stat.S_IMODE(info.st_mode)}
    return {'exists': True, 'sha256': digest(host.read(path, 64 * MAX_FILE)), 'mode': stat.S_IMODE(info.st_mode)}


class Transaction:
    """The manifest of one run: for each path the state before (with a copy
    of a replaced file) and after the change."""

    def __init__(self, host: Host, kind: str):
        self.host = host
        self.id = dt.datetime.now(dt.timezone.utc).strftime('%Y%m%dT%H%M%SZ') + '-' + os.urandom(4).hex()
        self.dir = TRANSACTIONS / self.id
        # IDs of one second differ only in random digits; the sequence orders
        # transactions for revert and pruning.
        sequence = 1 + max((sequence_of(host, name) for name in transactions(host)), default=0)
        self.manifest = {'version': 1, 'id': self.id, 'sequence': sequence, 'kind': kind, 'createdAt': stamp(),
                         'state': 'running', 'entries': []}
        for path, mode in ((BASE, 0o755), (BASE / 'backups', 0o700), (TRANSACTIONS, 0o700), (self.dir, 0o700)):
            if not host.exists(path):
                host.mkdir(path, mode)
        self.save()

    def save(self) -> None:
        self.host.write(self.dir / 'manifest.json', json.dumps(self.manifest, indent=1, sort_keys=True).encode(), 0o600)

    def before(self, path, kind: str) -> dict:
        state = describe(self.host, path, kind)
        info = self.host.info(path)
        if info is not None:
            state.update(uid=info.st_uid, gid=info.st_gid)
        if kind == 'file' and state['exists'] and not state.get('dir'):
            backup = '{:04d}'.format(len(self.manifest['entries']))
            self.host.write(self.dir / backup, self.host.read(path, 64 * MAX_FILE), 0o600)
            state['backup'] = backup
        entry = {'path': str(path), 'kind': kind, 'before': state}
        self.manifest['entries'].append(entry)
        self.save()
        return entry

    def after(self, entry: dict) -> None:
        entry['after'] = describe(self.host, entry['path'], entry['kind'])
        self.save()

    def finish(self, state: str) -> None:
        self.manifest['state'] = state
        self.manifest['finishedAt'] = stamp()
        self.save()


def change_file(tx: Transaction, path, data: bytes, mode: int, uid: int = 0, gid: int = 0) -> None:
    entry = tx.before(path, 'file')
    tx.host.write(path, data, mode, uid, gid)
    tx.after(entry)


def change_meta(tx: Transaction, path, mode: int, uid: int, gid: int) -> None:
    entry = tx.before(path, 'meta')
    tx.host.set_meta(path, mode, uid, gid)
    tx.after(entry)


def make_dir(tx: Transaction, path, mode: int, uid: int = 0, gid: int = 0) -> None:
    entry = tx.before(path, 'dir')
    if tx.host.exists(path):
        tx.host.set_meta(path, mode, uid, gid)
    else:
        tx.host.mkdir(path, mode, uid, gid)
    tx.after(entry)


def remove_path(tx: Transaction, path) -> None:
    info = tx.host.info(path)
    entry = tx.before(path, 'dir' if info is not None and stat.S_ISDIR(info.st_mode) else 'file')
    tx.host.remove(path)
    tx.after(entry)


def unit_now(host: Host, unit: str) -> dict:
    enabled = host.run(['/usr/bin/systemctl', 'is-enabled', '--quiet', unit], check=False).code == 0
    active = host.run(['/usr/bin/systemctl', 'is-active', '--quiet', unit], check=False).code == 0
    return {'enabled': enabled, 'active': active}


def put_unit(host: Host, unit: str, want: dict, now: dict) -> None:
    if want['enabled'] != now['enabled']:
        host.run(['/usr/bin/systemctl', 'enable' if want['enabled'] else 'disable', unit])
    if want['active'] != now['active']:
        host.run(['/usr/bin/systemctl', 'start' if want['active'] else 'stop', unit])


def set_unit(tx: Transaction, unit: str, enabled: bool, active: bool) -> None:
    """Enable, disable, start or stop a unit as a recorded entry, so that a
    rollback or revert puts back the state it found."""
    before = unit_now(tx.host, unit)
    entry = {'path': 'unit:' + unit, 'kind': 'unit', 'unit': unit, 'before': before}
    tx.manifest['entries'].append(entry)
    tx.save()
    put_unit(tx.host, unit, {'enabled': enabled, 'active': active}, before)
    entry['after'] = {'enabled': enabled, 'active': active}
    tx.save()


def write_data(host: Host, path, data: bytes, mode: int, uid: int = 0, gid: int = 0) -> None:
    """Write a file that the component's programs change later (state and
    records). It is not part of a transaction: a revert would always find it
    changed."""
    host.write(path, data, mode, uid, gid)


def transactions(host: Host) -> list[str]:
    """Transaction IDs, oldest first."""
    names = [name for name in host.listdir(TRANSACTIONS) if TRANSACTION_ID.fullmatch(name)]
    return sorted(names, key=lambda name: (sequence_of(host, name), name))


def sequence_of(host: Host, name: str) -> int:
    try:
        value = load_manifest(host, name).get('sequence', 0)
    except (Failure, OSError, ValueError):
        return 0
    return value if type(value) is int and value >= 0 else 0


def load_manifest(host: Host, identity: str) -> dict:
    if not TRANSACTION_ID.fullmatch(identity):
        raise Failure('unknown transaction ' + identity)
    manifest = json.loads(host.read(TRANSACTIONS / identity / 'manifest.json'))
    if not isinstance(manifest, dict) or manifest.get('id') != identity or not isinstance(manifest.get('entries'), list):
        raise Failure('the manifest of transaction {} is invalid'.format(identity))
    return manifest


def helper_digests(host: Host, helper) -> dict[str, str]:
    """The digests the helper's policy pins for the files it wrote."""
    try:
        policy = json.loads(host.read_optional(helper.POLICY, 65536) or b'{}')
    except ValueError:
        return {}
    pinned = {}
    for key in ('files', 'optionalFiles'):
        value = policy.get(key) if isinstance(policy, dict) else None
        if isinstance(value, dict):
            pinned.update({path: item for path, item in value.items() if isinstance(item, str)})
    return pinned


def restore_entries(host: Host, manifest: dict, dry: bool, helper=None) -> tuple[list[str], list[str]]:
    """Undo a transaction's entries in reverse order. An entry whose path no
    longer holds what the transaction left is a conflict and stays, except
    (when helper is given) for the helper's policy and the files whose
    current content the policy pins: the helper rewrote those on purpose,
    so revert still puts back or removes them. A hand edit does not match
    the pin and stays a conflict."""
    done, conflicts = [], []
    removed = set()
    directory = TRANSACTIONS / manifest['id']
    keys = ('exists', 'dir', 'sha256', 'mode')
    pinned = helper_digests(host, helper) if helper is not None else {}
    for entry in reversed(manifest['entries']):
        path, kind, before, after = Path(entry['path']), entry.get('kind'), entry['before'], entry.get('after')
        if kind == 'unit':
            unit = entry.get('unit', '')
            if not re.fullmatch(r'[A-Za-z0-9@._-]+\.(service|timer)', unit):
                raise Failure('transaction {} names an invalid unit'.format(manifest['id']))
            now = unit_now(host, unit)
            want = {'enabled': bool(before.get('enabled')), 'active': bool(before.get('active'))}
            if now == want:
                continue
            done.append('restore {} ({}, {})'.format(unit, 'enabled' if want['enabled'] else 'disabled',
                                                    'active' if want['active'] else 'inactive'))
            if not dry:
                # Its unit file may have been restored just before.
                host.run(['/usr/bin/systemctl', 'daemon-reload'], check=False)
                put_unit(host, unit, want, now)
            continue
        if kind not in ('file', 'meta', 'dir'):
            raise Failure('transaction {} has an entry this installer cannot undo'.format(manifest['id']))
        now = describe(host, path, kind)
        if now == {key: before[key] for key in keys if key in before}:
            continue
        # An entry without "after" was interrupted: the path holds either state.
        drifted = after is not None and now != after
        if drifted and helper is not None and kind == 'file' and now.get('sha256') and (
                path == helper.POLICY or pinned.get(str(path)) == now['sha256']):
            drifted = False
        if drifted:
            conflicts.append(str(path))
            continue
        if not before['exists'] and now.get('dir') and [name for name in host.listdir(path) if str(path / name) not in removed]:
            # A directory the transaction created now holds data written
            # since, such as reports; it stays.
            continue
        if not before['exists']:
            removed.add(str(path))
        done.append(('restore ' if before['exists'] else 'remove ') + str(path))
        if dry:
            continue
        if not before['exists']:
            host.remove(path)
        elif kind == 'meta' or before.get('dir'):
            if not now['exists']:
                if not before.get('dir'):
                    conflicts.append(str(path))
                    continue
                host.mkdir(path, before['mode'], before['uid'], before['gid'])
            else:
                host.set_meta(path, before['mode'], before['uid'], before['gid'])
        else:
            data = host.read(directory / before['backup'], 64 * MAX_FILE)
            if digest(data) != before['sha256']:
                raise Failure('the backup of {} in transaction {} changed'.format(path, manifest['id']))
            host.write(path, data, before['mode'], before['uid'], before['gid'])
    return done, conflicts


# ---------------------------------------------------------------------------
# The plan: an ordered list of steps, each a description and an action.


class Step:
    def __init__(self, what: str, action):
        self.what = what
        self.action = action


class Plan:
    def __init__(self):
        # Locks execute() holds from the first step to the end of a rollback.
        self.locks: list[Path] = []
        self.helper = None
        self.steps: list[Step] = []
        self.manual: list[str] = []
        self.notes: list[str] = []
        self.token_missing = False
        # Set by a revert that takes the component away: the caller then
        # removes what it installed around it (the sudo grant, the settings).
        self.removes_component = False

    def add(self, what: str, action) -> None:
        self.steps.append(Step(what, action))

    def result(self) -> dict:
        return {'ok': True, 'changes': [step.what for step in self.steps], 'manual': self.manual,
                'notes': self.notes, 'tokenMissing': self.token_missing, 'componentRemoved': self.removes_component}


def runtime_rules(host: Host) -> set[str]:
    """The managed watches loaded in the kernel; optional ones are the helper's."""
    if not host.exists('/usr/sbin/auditctl'):
        return set()
    out = host.run(['/usr/sbin/auditctl', '-l'], check=False).out
    return {line for line in out.splitlines() if re.fullmatch(r'-w /[A-Za-z0-9_./-]+ -p [rwxa]+ -k wssec_[a-z]+', line)}


def load_rules(host: Host, helper) -> None:
    """Make the kernel's managed watches those of the rules file (none when it
    is gone): add the missing ones first, then remove the extra ones, so that
    coverage never drops in between. Unrelated rules are never flushed."""
    if not host.exists('/usr/sbin/auditctl'):
        return
    wanted = set()
    for line in (host.read_optional(helper.AUDIT_CORE) or b'').decode().splitlines():
        if not line or line.startswith('#'):
            continue
        if not re.fullmatch(r'-w /[A-Za-z0-9_./-]+ -p wa -k wssec_[a-z]+', line):
            raise Failure('unexpected managed audit rule')
        wanted.add(line)
    current = runtime_rules(host)
    for line in sorted(wanted - current):
        host.run(['/usr/sbin/auditctl', *line.split()])
    for line in sorted(current - wanted):
        parts = line.split()
        host.run(['/usr/sbin/auditctl', '-W', parts[1], '-p', parts[3], '-k', parts[5]])
    # Generate the boot rules without --load, which could flush unrelated rules.
    host.run(['/usr/sbin/augenrules'])


COLLECTOR_FILES = (str(LIB / 'runtime.py'), str(COLLECTOR_CONFIG), str(SYSTEMD / COLLECTOR), str(TOKEN))


def after_restore(host: Host, helper, manifest: dict, strict: bool) -> None:
    """Bring running state in line with restored files: units, Nginx, the
    kernel's watches and the collector."""
    paths = {entry['path'] for entry in manifest['entries']}
    host.run(['/usr/bin/systemctl', 'daemon-reload'], check=strict)
    if manifest.get('nginx') and host.exists(NGINX):
        if host.run([str(NGINX), '-t'], check=strict).code == 0:
            host.run(['/usr/bin/systemctl', 'reload', 'nginx.service'], check=strict)
    if str(helper.AUDIT_CORE) in paths:
        load_rules(host, helper)
    if paths & set(COLLECTOR_FILES) and host.exists(SYSTEMD / COLLECTOR):
        host.run(['/usr/bin/systemctl', 'try-restart', COLLECTOR], check=False)


class Component:
    """The wanted state of the component and the steps that reach it."""

    def __init__(self, host: Host, helper, inputs: dict, sources: dict[str, bytes]):
        self.host = host
        self.H = helper
        self.inputs = inputs
        self.sources = sources
        self.nginx = host.exists(NGINX)

    # -- survey ------------------------------------------------------------

    def read_json(self, path, limit: int = 65536):
        raw = self.host.read_optional(path, limit)
        if raw is None:
            return None
        try:
            value = json.loads(raw)
        except ValueError:
            raise Blocked('{} is not valid JSON; review it'.format(path)) from None
        if not isinstance(value, dict):
            raise Blocked('{} is not a JSON object; review it'.format(path))
        return value

    def check_audit(self) -> None:
        if self.host.exists('/usr/sbin/auditctl'):
            state = self.host.run(['/usr/sbin/auditctl', '-s'], check=False)
            if re.search(r'^enabled\s+2\s*$', state.out, re.M):
                raise Blocked('the kernel audit configuration is immutable (enabled 2); it cannot change without a reboot')
        conf = self.host.read_optional(AUDITD_CONF)
        if conf is not None:
            configured = re.findall(r'^\s*log_file\s*=\s*(\S+)\s*$', conf.decode('utf-8', 'replace'), re.M)
            if configured and configured[-1] != '/var/log/audit/audit.log':
                raise Blocked('auditd.conf names a custom log_file; review reader access before applying')
        for name in self.host.listdir(AUDIT_RULES):
            if name.endswith('.rules'):
                text = self.host.read(AUDIT_RULES / name).decode('utf-8', 'replace')
                if re.search(r'^\s*-e\s*2\s*(?:#.*)?$', text, re.M):
                    raise Blocked('{} makes the audit rules immutable; review it'.format(AUDIT_RULES / name))

    def collector_account(self, plan: Plan):
        """The collector's (uid, gid), adding account steps when missing."""
        gid = self.host.group(USER)
        account = self.host.account(USER)
        if account is not None:
            uid, primary, shell, groups = account
            if shell not in ('/usr/sbin/nologin', '/sbin/nologin') or uid == 0:
                raise Blocked('the account {} exists with a login shell or user ID 0; review it'.format(USER))
            if primary != gid or any(group != gid for group in groups):
                raise Blocked('the account {} has unexpected group memberships; review it'.format(USER))
            return uid, gid
        if gid is None:
            plan.add('create the system group ' + USER, lambda tx: self.host.run(['/usr/sbin/groupadd', '--system', USER]))
        plan.add('create the system account {} (no login shell)'.format(USER), lambda tx: self.host.run([
            '/usr/sbin/useradd', '--system', '--gid', USER, '--home-dir', str(BASE / 'collector'),
            '--no-create-home', '--shell', '/usr/sbin/nologin', USER]))
        return None

    def previous_policy(self) -> dict | None:
        policy = self.read_json(self.H.POLICY)
        if policy is None:
            return None
        if policy.get('machineId') != self.inputs['machineId']:
            raise Blocked('the installed helper policy is for Machine {!r}, the configuration names {!r}; '
                          'review which Machine this host is'.format(policy.get('machineId'), self.inputs['machineId']))
        if policy.get('version') not in (1, self.H.POLICY_VERSION):
            raise Blocked('{} has an unknown version; review it'.format(self.H.POLICY))
        return policy

    def legacy_limits(self, plan: Plan) -> list[dict]:
        """Thresholds of the replaced Nginx observation files, so that
        adoption keeps the reviewed values."""
        try:
            conf = self.host.read(self.H.NGINX_CONF).decode()
            hooks = {name: self.host.read(LEGACY_HOOKS / name).decode() for name in LEGACY_HOOK_FILES}
            limits = []
            for category in self.H.CATEGORIES:
                hook = hooks['card-socket-10-observation.conf' if category == 'socket' else 'card-api-10-observation.conf']
                rate = re.findall(r'limit_req_zone \$ws_security_' + category + r'_key zone=ws_security_' + category
                                  + r':2m rate=([1-9][0-9]*r/[ms]);', conf)
                burst = re.findall(r'limit_req zone=ws_security_' + category + r' burst=([0-9]+) nodelay;', hook)
                connections = re.findall(r'limit_conn ws_security_' + category + r'_conn ([0-9]+);', hook)
                if len(rate) != 1 or len(burst) != 1 or len(connections) != 1:
                    raise ValueError(category)
                limits.append({'category': category, 'rate': rate[0], 'burst': int(burst[0]),
                               'connections': int(connections[0])})
            return self.H.valid_limits(limits)
        except (Failure, OSError, ValueError, self.H.Failure):
            plan.notes.append('the replaced Nginx thresholds could not be read; the defaults apply')
            return [dict(item) for item in self.H.DEFAULT_LIMITS]

    def policy(self, plan: Plan, previous: dict | None, files: dict) -> dict:
        nginx = None
        if self.nginx:
            limits = None
            if previous is not None and previous.get('version') == self.H.POLICY_VERSION and previous.get('nginx'):
                limits = self.H.valid_limits(previous['nginx'].get('limits'))
            elif self.host.exists(LEGACY_HOOKS):
                limits = self.legacy_limits(plan)
            nginx = {'services': list(self.inputs.get('services', [])),
                     'limits': limits or [dict(item) for item in self.H.DEFAULT_LIMITS]}
        policy = {'version': self.H.POLICY_VERSION, 'machineId': self.inputs['machineId'],
                  'optionalFiles': {}, 'schedule': {'hour': 4, 'minute': 20},
                  'profiles': {'aide': {'reviewed-baseline': {'enabled': True, 'required': True}},
                               'audit': {'core': {'enabled': True, 'required': True}}},
                  'nginx': nginx}
        if previous is not None:
            for key in ('optionalFiles', 'schedule', 'profiles'):
                if key in previous:
                    policy[key] = previous[key]
            if not isinstance(policy['optionalFiles'], dict) or set(policy['optionalFiles']) - {
                    str(self.H.AIDE_DROPIN), str(self.H.AUDIT_OPTIONAL)}:
                raise Blocked('{} pins unexpected optional files; review it'.format(self.H.POLICY))
        else:
            for path in (self.H.AIDE_DROPIN, self.H.AUDIT_OPTIONAL):
                if self.host.exists(path):
                    raise Blocked('{} exists without a helper policy that pins it; review it'.format(path))
        policy['files'] = {str(path): digest(files[path][0]) for path in
                           (self.H.AIDE_TIMER, self.H.AIDE_CONFIG, self.H.AUDIT_CORE)}
        for path, data in self.H.nginx_files(nginx).items():
            policy['files'][str(path)] = digest(data)
        return policy

    def watches(self, plan: Plan) -> dict[str, list[str]]:
        def present(path: str, directory: bool = False) -> bool:
            info = self.host.info(path)
            return info is not None and (stat.S_ISDIR(info.st_mode) if directory else True)
        # Paths this component creates count as present, so that the first
        # run renders what later runs do.
        own = {str(BASE / 'audit-probe'), '/usr/local/libexec/lcm'}
        paths = {key: [target for target in targets if target in own or present(target)] for key, targets in WATCHES.items()}
        paths['deploy'] += ['/usr/local/libexec/' + name for name in self.host.listdir('/usr/local/libexec')
                            if name.startswith('lcm') and SAFE_PATH.fullmatch('/usr/local/libexec/' + name)]
        for category, targets in sorted(self.inputs.get('watch', {}).items()):
            for target in targets:
                if present(target):
                    paths[category].append(target)
                else:
                    plan.notes.append('watch.{}: {} does not exist and is not watched'.format(category, target))
        # Read account metadata only; never password hashes or key contents.
        passwd = self.host.read_optional('/etc/passwd')
        for line in (passwd or b'').decode('utf-8', 'replace').splitlines():
            fields = line.split(':')
            if len(fields) != 7:
                continue
            home = fields[5]
            if home == '/root' or home.startswith('/home/'):
                for category, candidate in (('ssh', home + '/.ssh'), ('systemd', home + '/.config/systemd/user')):
                    if SAFE_PATH.fullmatch(candidate) and '..' not in candidate.split('/') and present(candidate, True):
                        paths[category].append(candidate)
        return {key: sorted(set(value)) for key, value in paths.items()}

    def critical(self, watches: dict[str, list[str]]) -> list[str]:
        paths = [path for values in watches.values() for path in values]
        paths += [path for path in CRITICAL if path == str(LIB) or self.host.exists(path)]
        python = self.python_binary()
        if python:
            paths.append(python)
        paths.append(str(ETC / 'aide.conf'))
        return paths

    def python_binary(self) -> str | None:
        """The file behind /usr/bin/python3, which runs every root program here."""
        path = '/usr/bin/python3'
        for _ in range(8):
            info = self.host.info(path)
            if info is None:
                return None
            if not stat.S_ISLNK(info.st_mode):
                return path if SAFE_PATH.fullmatch(path) else None
            target = os.readlink(self.host.p(path))
            path = os.path.normpath(os.path.join(os.path.dirname(path), target))
        return None

    # -- the plan -------------------------------------------------------------

    def wanted_files(self, ids, watches) -> dict:
        uid, gid = ids
        config = {'machineId': self.inputs['machineId'], 'endpoint': self.inputs['endpoint'],
                  'tokenCredential': CREDENTIAL, 'nginxLog': self.H.NGINX_LOG,
                  'auditLog': '/var/log/audit/audit.log', 'reportsDir': str(BASE / 'reports'),
                  'stateDir': str(BASE / 'collector')}
        files = {
            LIB / 'runtime.py': (self.sources['runtime.py'], 0o644, 0, 0),
            LIB / 'install.py': (self.sources['install.py'], 0o644, 0, 0),
            HELPER: (self.sources['helper.py'], 0o644, 0, 0),
            COLLECTOR_CONFIG: (json.dumps(config, indent=2).encode() + b'\n', 0o640, 0, gid),
            self.H.AIDE_CONFIG: (aide_config([path for path in self.critical(watches) if path not in self.inputs.get('aideIgnore', [])],
                                             self.inputs.get('aideIgnore', [])).encode(), 0o600, 0, 0),
            self.H.AUDIT_CORE: (audit_rules(watches).encode(), 0o600, 0, 0),
        }
        for name, content in unit_files(self.nginx).items():
            files[SYSTEMD / name] = (content.encode(), 0o644, 0, 0)
        if self.nginx:
            files[LOGROTATE_CONF] = (LOGROTATE.encode(), 0o644, 0, 0)
        return files

    def wanted_dirs(self, ids) -> list[tuple]:
        uid, gid = ids
        dirs = [(BASE, 0o755, 0, 0), (BASE / 'reports', 0o2750, 0, gid), (BASE / 'aide', 0o700, 0, 0),
                (BASE / 'collector', 0o700, uid, gid), (BASE / 'audit-probe', 0o700, 0, 0),
                (BASE / 'backups', 0o700, 0, 0), (ETC, 0o750, 0, gid), (LIB, 0o755, 0, 0),
                (HELPER.parent.parent, 0o755, 0, 0), (HELPER.parent, 0o700, 0, 0), (self.H.POLICY.parent, 0o700, 0, 0),
                (HELPER_STATE, 0o700, 0, 0), (HELPER_STATE / 'operations', 0o700, 0, 0),
                (HELPER_STATE / 'approvals', 0o700, 0, 0), (self.H.AIDE_DROPIN.parent, 0o700, 0, 0)]
        if self.nginx:
            dirs.append((LOGROTATE_DIR, 0o700, 0, 0))
        return dirs

    def dir_step(self, plan: Plan, path, mode, uid, gid, collector_uid=None, create_only=False) -> None:
        info = self.host.info(path)
        if info is None:
            for parent in reversed(Path(path).parents):
                if not self.host.exists(parent) and not any(step.what.startswith('create {} '.format(parent)) for step in plan.steps):
                    plan.add('create {} (0755)'.format(parent), lambda tx, parent=parent: make_dir(tx, parent, 0o755))
            plan.add('create {} ({:04o})'.format(path, mode), lambda tx: make_dir(tx, path, mode, uid, gid))
            return
        if not stat.S_ISDIR(info.st_mode):
            raise Blocked('{} exists but is not a directory; move it away'.format(path))
        if create_only:
            self.host.trusted(path)
            return
        if self.host.real and info.st_uid not in (0, collector_uid if collector_uid is not None else 0):
            raise Blocked('{} is owned by user {}; review its content before root takes it over'.format(path, info.st_uid))
        if stat.S_IMODE(info.st_mode) != mode or not self.host.owner_ok(info, uid, gid):
            plan.add('set {} to {}:{} {:04o}'.format(path, uid, gid, mode), lambda tx: change_meta(tx, path, mode, uid, gid))

    def file_step(self, plan: Plan, path, data: bytes, mode: int, uid: int, gid: int) -> bool:
        """Add a step when the file differs; report whether it does."""
        self.host.trusted(Path(path).parent, missing=True)
        info = self.host.info(path)
        if info is not None and not stat.S_ISREG(info.st_mode):
            raise Blocked('{} exists but is not a regular file; move it away'.format(path))
        if info is not None and self.host.read(path, 64 * MAX_FILE) == data:
            if stat.S_IMODE(info.st_mode) == mode and self.host.owner_ok(info, uid, gid):
                return False
            plan.add('set {} to {}:{} {:04o}'.format(path, uid, gid, mode), lambda tx: change_meta(tx, path, mode, uid, gid))
            return True
        verb = 'install' if info is None else 'update'
        plan.add('{} {}'.format(verb, path), lambda tx: change_file(tx, path, data, mode, uid, gid))
        return True

    def unit_state(self, unit: str) -> tuple[bool, bool]:
        enabled = self.host.run(['/usr/bin/systemctl', 'is-enabled', '--quiet', unit], check=False).code == 0
        active = self.host.run(['/usr/bin/systemctl', 'is-active', '--quiet', unit], check=False).code == 0
        return enabled, active

    def runtime_rules(self) -> set[str]:
        return runtime_rules(self.host)

    def plan(self) -> Plan:
        plan = Plan()
        try:
            self.check_audit()
            if not all(self.host.exists(tool) for tool in PACKAGE_TOOLS):
                plan.add('install the packages ' + ' '.join(PACKAGES) + ' with apt-get', self.install_packages)
            ids = self.collector_account(plan)
            if ids is None:
                # The rest depends on the new account; apply plans it again.
                # The caller still learns now that a token is needed, since
                # it supplies one only when the plan asks for it.
                plan.token_missing = self.host.info(TOKEN) is None and self.inputs.get('token') is None
                plan.notes.append('the remaining steps are planned after the account exists')
                return plan
            self.converge(plan, ids)
        except Failure as error:
            # Nothing is changed while a difference needs the administrator.
            plan.manual.append(str(error))
            plan.steps = []
        except self.H.Failure:
            plan.manual.append('{} holds values the helper refuses (such as Nginx thresholds); review it'.format(self.H.POLICY))
            plan.steps = []
        return plan

    def install_packages(self, tx) -> None:
        self.host.run(['/usr/bin/apt-get', 'install', '--yes', '--no-install-recommends', *PACKAGES], timeout=1200)

    def converge(self, plan: Plan, ids) -> None:
        uid, gid = ids
        previous = self.previous_policy()
        collector = self.read_json(COLLECTOR_CONFIG)
        if collector is not None and collector.get('machineId') != self.inputs['machineId']:
            raise Blocked('the installed collector reports for Machine {!r}, the configuration names {!r}; '
                          'review which Machine this host is'.format(collector.get('machineId'), self.inputs['machineId']))
        legacy_hooks = []
        if self.host.exists(LEGACY_HOOKS):
            legacy_hooks = self.host.listdir(LEGACY_HOOKS)
            unknown = sorted(set(legacy_hooks) - set(LEGACY_HOOK_FILES))
            if unknown:
                raise Blocked('{} holds files no installer made ({}); review and move them'.format(LEGACY_HOOKS, ', '.join(unknown)))
        watches = self.watches(plan)
        files = self.wanted_files(ids, watches)
        policy = self.policy(plan, previous, files)

        for path, mode, owner, group in self.wanted_dirs(ids):
            self.dir_step(plan, path, mode, owner, group, collector_uid=uid if path == BASE / 'collector' else None)
        collector_changed = self.token_steps(plan)
        changed = {}
        for path, (data, mode, owner, group) in files.items():
            changed[path] = self.file_step(plan, path, data, mode, owner, group)
        collector_changed |= any(changed[path] for path in (LIB / 'runtime.py', COLLECTOR_CONFIG, SYSTEMD / COLLECTOR))
        units_changed = any(changed[SYSTEMD / name] for name in unit_files(self.nginx))
        for path, settings, delimiter, mode in ((AUDITD_CONF, AUDIT_OPTIONS, ' = ', 0o640),
                                                (AIDE_DEFAULTS, {'CRON_DAILY_RUN': 'no'}, '=', 0o644)):
            existing = self.host.read_optional(path)
            if existing is None and path == AUDITD_CONF and self.host.exists('/usr/sbin/auditd'):
                raise Blocked('{} is missing although auditd is installed; review the package'.format(path))
            merged = safe_config((existing or b'').decode('utf-8', 'replace'), settings, delimiter).encode()
            if existing != merged:
                plan.add('set {} in {}'.format(', '.join(sorted(settings)), path),
                         lambda tx, path=path, merged=merged, mode=mode: change_file(tx, path, merged, mode))
        if self.nginx and not self.host.exists(LOGROTATE_DIR / 'status') and self.host.exists(LEGACY_LOGROTATE_STATE):
            state = self.host.read(LEGACY_LOGROTATE_STATE, 65536)
            plan.add('copy the log rotation state {} to {}'.format(LEGACY_LOGROTATE_STATE, LOGROTATE_DIR / 'status'),
                     lambda tx: write_data(self.host, LOGROTATE_DIR / 'status', state, 0o600))
        self.vendor_timer_steps(plan)
        self.audit_log_steps(plan, gid)
        if units_changed:
            plan.add('reload the systemd configuration', lambda tx: self.host.run(['/usr/bin/systemctl', 'daemon-reload']))
        enabled, active = self.unit_state('auditd.service')
        if not (enabled and active):
            plan.add('enable and start auditd.service', lambda tx: (
                set_unit(tx, 'auditd.service', True, True),
                self.host.run(['/usr/sbin/auditctl', '--signal', 'reload'], check=False)))
        wanted_rules = {line for line in files[self.H.AUDIT_CORE][0].decode().splitlines() if line and not line.startswith('#')}
        if changed[self.H.AUDIT_CORE] or self.runtime_rules() != wanted_rules:
            plan.add('load the managed audit watches (unrelated rules are kept)', lambda tx: self.load_rules())
        if changed[self.H.AIDE_CONFIG]:
            plan.notes.append('the integrity scope changed: the next AIDE check reports the difference until the '
                              'baseline is reviewed and accepted locally')
            plan.add('check the AIDE configuration', lambda tx: self.host.run(
                ['/usr/bin/aide', '--config', str(self.H.AIDE_CONFIG), '--config-check']))
        if self.nginx:
            self.nginx_steps(plan, policy, legacy_hooks)
        elif self.host.exists(LEGACY_HOOKS):
            raise Blocked('{} exists but Nginx is not installed; review it'.format(LEGACY_HOOKS))
        else:
            plan.notes.append('Nginx is not installed; traffic observation is off')
        policy_bytes = canonical(policy)
        current = self.host.read_optional(self.H.POLICY, 65536)
        if current is None or json.loads(current) != policy:
            what = 'convert {} to version {}'.format(self.H.POLICY, self.H.POLICY_VERSION) if previous and previous.get('version') == 1 \
                else ('write ' if current is None else 'update ') + str(self.H.POLICY)
            plan.add(what, lambda tx: change_file(tx, self.H.POLICY, policy_bytes, 0o600))
        elif self.host.info(self.H.POLICY).st_mode & 0o077:
            plan.add('set {} to 0600'.format(self.H.POLICY), lambda tx: change_meta(tx, self.H.POLICY, 0o600, 0, 0))
        units = list(ENABLED_UNITS) + ([ROTATE_TIMER] if self.nginx else [])
        started = set()
        for unit in units:
            enabled, active = self.unit_state(unit)
            if not (enabled and active):
                started.add(unit)
                plan.add('enable and start ' + unit, lambda tx, unit=unit: set_unit(tx, unit, True, True))
        if not self.host.exists(BASE / 'reports' / 'audit-status.json'):
            plan.add('write a first audit status report', lambda tx: self.host.run(
                ['/usr/bin/systemctl', 'start', AUDIT_STATUS]))
        if collector_changed and COLLECTOR not in started:
            plan.add('restart {} for its new files'.format(COLLECTOR), lambda tx: self.host.run(
                ['/usr/bin/systemctl', 'restart', COLLECTOR]))
        if not self.host.exists(self.H.BASELINE):
            plan.notes.append('no AIDE baseline yet: review the host, then run the local baseline procedure')

    def token_steps(self, plan: Plan) -> bool:
        info = self.host.info(TOKEN)
        if info is None:
            token = self.inputs.get('token')
            plan.token_missing = token is None
            if token is None:
                def refuse(tx):
                    raise Failure('no ingest token was supplied')
                plan.add('write a new ingest API token to {} (root 0600)'.format(TOKEN), refuse)
            else:
                data = (token + '\n').encode()
                plan.add('write the new ingest API token to {} (root 0600)'.format(TOKEN),
                         lambda tx: change_file(tx, TOKEN, data, 0o600))
            return True
        if not stat.S_ISREG(info.st_mode):
            raise Blocked('{} is not a regular file; review it'.format(TOKEN))
        value = self.host.read(TOKEN, 4097).strip()
        if not re.fullmatch(rb'[!-~]{1,4096}', value):
            raise Blocked('{} does not hold a token; replace it or remove it to get a new one'.format(TOKEN))
        if stat.S_IMODE(info.st_mode) != 0o600 or not self.host.owner_ok(info, 0, 0):
            plan.add('restrict {} to root 0600 (the collector reads it as a systemd credential)'.format(TOKEN),
                     lambda tx: change_meta(tx, TOKEN, 0o600, 0, 0))
            return True
        return False

    def vendor_timer_steps(self, plan: Plan) -> None:
        states = {}
        for unit in DEFAULT_TIMERS:
            load = self.host.run(['/usr/bin/systemctl', 'show', '--property=LoadState', '--value', unit], check=False).out.strip()
            if load in ('', 'not-found'):
                continue
            enabled, active = self.unit_state(unit)
            states[unit] = {'enabled': enabled, 'active': active}
        if not self.host.exists(VENDOR_TIMERS_RECORD):
            record = json.dumps(states, sort_keys=True).encode()
            plan.add('record the vendor AIDE timers in ' + str(VENDOR_TIMERS_RECORD),
                     lambda tx: write_data(self.host, VENDOR_TIMERS_RECORD, record, 0o600))
        for unit, state in states.items():
            if state['enabled'] or state['active']:
                plan.add('disable the vendor timer ' + unit, lambda tx, unit=unit: set_unit(tx, unit, False, False))

    def audit_log_steps(self, plan: Plan, gid: int) -> None:
        info = self.host.info(AUDIT_LOG_DIR)
        if info is None:
            return
        if stat.S_ISLNK(info.st_mode) or not stat.S_ISDIR(info.st_mode):
            raise Blocked('{} is not a directory; review it'.format(AUDIT_LOG_DIR))
        logs = [name for name in self.host.listdir(AUDIT_LOG_DIR) if name.startswith('audit.log')]
        stale = [name for name in logs if stat.S_ISREG(self.host.info(AUDIT_LOG_DIR / name).st_mode)
                 and (stat.S_IMODE(self.host.info(AUDIT_LOG_DIR / name).st_mode) != 0o640
                      or not self.host.owner_ok(self.host.info(AUDIT_LOG_DIR / name), 0, gid))]
        if stat.S_IMODE(info.st_mode) == 0o750 and self.host.owner_ok(info, 0, gid) and not stale:
            return
        if not self.host.exists(AUDIT_DIR_RECORD):
            files = {}
            for name in logs:
                entry = self.host.info(AUDIT_LOG_DIR / name)
                if stat.S_ISREG(entry.st_mode):
                    files[str(entry.st_ino)] = {'uid': entry.st_uid, 'gid': entry.st_gid, 'mode': stat.S_IMODE(entry.st_mode)}
            record = json.dumps({'mode': stat.S_IMODE(info.st_mode), 'uid': info.st_uid, 'gid': info.st_gid,
                                 'files': files}).encode()
            plan.add('record the audit log permissions in ' + str(AUDIT_DIR_RECORD),
                     lambda tx: write_data(self.host, AUDIT_DIR_RECORD, record, 0o600))

        def grant(tx):
            change_meta(tx, AUDIT_LOG_DIR, 0o750, 0, gid)
            for name in stale:
                change_meta(tx, AUDIT_LOG_DIR / name, 0o640, 0, gid)
        plan.add('let the group {} read {} (root:{} 0750, logs 0640)'.format(USER, AUDIT_LOG_DIR, USER), grant)

    def load_rules(self) -> None:
        load_rules(self.host, self.H)

    def nginx_steps(self, plan: Plan, policy: dict, legacy_hooks: list[str]) -> None:
        """The Nginx files, ordered so that every state between two steps
        passes nginx -t: replaced hooks go before the configuration that
        defined their zones, the configuration comes before the hooks that
        use its zones."""
        wanted = self.H.nginx_files(policy['nginx'])
        sub = Plan()
        for name in legacy_hooks:
            path = LEGACY_HOOKS / name
            sub.add('remove the replaced hook ' + str(path), lambda tx, path=path: remove_path(tx, path))
        if self.host.exists(LEGACY_HOOKS):
            sub.add('remove the empty directory ' + str(LEGACY_HOOKS), lambda tx: remove_path(tx, LEGACY_HOOKS))
        # Hook files of services no longer granted; only our own file names.
        names = {category + '-10-observation.conf' for category in self.H.CATEGORIES}
        for service in self.host.listdir(self.H.HOOKS):
            if service in policy['nginx']['services'] or not SERVICE.fullmatch(service):
                continue
            for name in self.host.listdir(self.H.HOOKS / service):
                if name in names:
                    path = self.H.HOOKS / service / name
                    sub.add('remove ' + str(path), lambda tx, path=path: remove_path(tx, path))
        # The ingress component shares /etc/nginx/lcm: create it when
        # missing, never change it. The per-service hook directories are ours.
        for path in (Path('/etc/nginx/lcm'), self.H.HOOKS):
            self.dir_step(sub, path, 0o755, 0, 0, create_only=True)
        for name in policy['nginx']['services']:
            self.dir_step(sub, self.H.HOOKS / name, 0o755, 0, 0)
        for path, data in wanted.items():
            self.file_step(sub, path, data, 0o644, 0, 0)
        log_step = self.log_step()
        if log_step is not None:
            sub.add(*log_step)
        if not sub.steps:
            return
        steps = list(sub.steps)
        for step in steps:
            plan.add(step.what, None)

        # execute() holds the ingress lock across these steps and any rollback.
        plan.locks.append(self.H.NGINX_LOCK)

        def apply_nginx(tx):
            tx.manifest['nginx'] = True
            tx.save()
            for step in steps:
                step.action(tx)
            self.host.run([str(NGINX), '-t'])
            self.host.run(['/usr/bin/systemctl', 'reload', 'nginx.service'])
        plan.add('validate (nginx -t) and reload nginx.service', apply_nginx)

    def log_step(self):
        log = Path(self.H.NGINX_LOG)
        gid = self.host.group(USER)
        info = self.host.info(log)
        if info is None:
            # The log is data, like the reports: created once, never recorded.
            def create(tx):
                self.host.trusted(log.parent)
                descriptor = os.open(self.host.p(log), os.O_WRONLY | os.O_CREAT | os.O_EXCL | os.O_NOFOLLOW, 0o640)
                try:
                    if self.host.real:
                        os.fchown(descriptor, 0, gid)
                    os.fchmod(descriptor, 0o640)
                finally:
                    os.close(descriptor)
            return ('create the observation log {} (root:{} 0640)'.format(log, USER), create)
        nginx_user = self.host.account('www-data')
        owners = {0} | ({nginx_user[0]} if nginx_user else set())
        if (not stat.S_ISREG(info.st_mode) or stat.S_IMODE(info.st_mode) != 0o640
                or (self.host.real and (info.st_uid not in owners or info.st_gid != gid))):
            raise Blocked('{} has unexpected ownership or permissions; review it'.format(log))
        return None

    # -- verification -----------------------------------------------------------

    def verify(self, plan: Plan) -> None:
        """The installed helper answers a status request for this Machine.
        Drift it reports (a hand-edited optional file) is shown, not undone."""
        try:
            reply = self.H.Controls().dispatch({'version': 1, 'command': 'status', 'machineId': self.inputs['machineId']})
        except self.H.Failure:
            raise Failure('the installed helper does not answer a status request') from None
        status = reply.get('status', {})
        if not status.get('available'):
            raise Failure('the installed helper reports that it is unavailable')
        if status.get('reason'):
            plan.notes.append('the helper reports: ' + status['reason'])


# ---------------------------------------------------------------------------
# Removal, cleanup and revert.


def removal_plan(host: Host, helper) -> Plan:
    plan = Plan()
    units = [name for name in (*ENABLED_UNITS, ROTATE_TIMER, AUDIT_STATUS, AIDE_SERVICE, ROTATE_SERVICE)
             if host.exists(SYSTEMD / name)]
    for unit in units:
        plan.add('stop and disable ' + unit, lambda tx, unit=unit: set_unit(tx, unit, False, False))
    policy = None
    if host.exists(helper.POLICY):
        try:
            policy = json.loads(host.read(helper.POLICY, 65536))
        except ValueError:
            policy = None
        backup = BASE / 'backups' / 'policy-before-removal.json'
        data = host.read(helper.POLICY, 65536)
        plan.add('keep a copy of {} in {}'.format(helper.POLICY, backup), lambda tx: change_file(tx, backup, data, 0o600))
    files = [SYSTEMD / name for name in unit_files(True)] + [
        helper.AIDE_DROPIN, LIB / 'runtime.py', LIB / 'install.py', HELPER, COLLECTOR_CONFIG, TOKEN,
        helper.AIDE_CONFIG, helper.AUDIT_CORE, helper.AUDIT_OPTIONAL, LOGROTATE_CONF, helper.POLICY]
    # Hooks go before the configuration that defines their zones.
    nginx = []
    for service in host.listdir(helper.HOOKS):
        for category in helper.CATEGORIES:
            path = helper.hook_path(service, category)
            if SERVICE.fullmatch(service) and host.exists(path):
                nginx.append(path)
    if host.exists(helper.NGINX_CONF):
        nginx.append(helper.NGINX_CONF)
    for path in files + nginx:
        if host.exists(path):
            plan.add('remove ' + str(path), lambda tx, path=path: remove_path(tx, path))
    if host.exists(helper.AIDE_DROPIN.parent) and not [n for n in host.listdir(helper.AIDE_DROPIN.parent)
                                                      if n != helper.AIDE_DROPIN.name]:
        plan.add('remove ' + str(helper.AIDE_DROPIN.parent), lambda tx: remove_path(tx, helper.AIDE_DROPIN.parent))
    if not plan.steps:
        return plan
    plan.add('reload the systemd configuration', lambda tx: host.run(['/usr/bin/systemctl', 'daemon-reload']))
    if host.exists('/usr/sbin/auditctl'):
        def unload(tx):
            # The rules files are gone: managed watches go; optional ones too.
            load_rules(host, helper)
            unload_optional_audit(host)
        plan.add('remove the managed audit watches from the kernel (unrelated rules are kept)', unload)
    if nginx:
        plan.locks.append(helper.NGINX_LOCK)

        def reload_nginx(tx):
            tx.manifest['nginx'] = True
            tx.save()
            host.run([str(NGINX), '-t'])
            host.run(['/usr/bin/systemctl', 'reload', 'nginx.service'])
        plan.add('validate (nginx -t) and reload nginx.service', reload_nginx)
    if host.exists(VENDOR_TIMERS_RECORD):
        try:
            vendor = json.loads(host.read(VENDOR_TIMERS_RECORD, 65536))
        except ValueError:
            vendor = {}
        for unit, state in sorted(vendor.items()) if isinstance(vendor, dict) else ():
            if unit in DEFAULT_TIMERS and isinstance(state, dict) and state.get('enabled'):
                plan.add('enable the vendor timer ' + unit + ' again',
                         lambda tx, unit=unit, state=state: set_unit(tx, unit, True, bool(state.get('active'))))
    if host.exists(AUDIT_DIR_RECORD):
        try:
            record = json.loads(host.read(AUDIT_DIR_RECORD, 65536))
            mode, uid, gid = int(record['mode']), int(record['uid']), int(record['gid'])
        except (ValueError, KeyError, TypeError):
            record = None
        if record is not None:
            plan.add('restore the permissions of {} ({}:{} {:04o})'.format(AUDIT_LOG_DIR, uid, gid, mode),
                     lambda tx: change_meta(tx, AUDIT_LOG_DIR, mode & 0o7777, uid, gid))
    plan.notes.append('kept: the AIDE baseline, reports and collector state in {}, the helper journal in {}, '
                      'the packages, the account {} and the auditd settings; revoke the ingest token in LCM'.format(
                          BASE, HELPER_STATE, USER))
    return plan


def unload_optional_audit(host: Host) -> None:
    """Remove the optional watches from the kernel and regenerate the boot
    rules; unrelated rules are kept."""
    if not host.exists('/usr/sbin/auditctl'):
        return
    out = host.run(['/usr/sbin/auditctl', '-l'], check=False).out
    for line in sorted(out.splitlines()):
        match = re.fullmatch(r'-w (/[A-Za-z0-9_./-]+) -p ([rwxa]+) -k (wssec_optional_[a-z0-9_]+)', line)
        if match:
            host.run(['/usr/sbin/auditctl', '-W', match[1], '-p', match[2], '-k', match[3]])
    host.run(['/usr/sbin/augenrules'])


def references(host: Host, needle: str) -> list[str]:
    """Configuration files that name a path, so that cleanup never removes
    something an installed unit or job still uses."""
    found = []
    roots = [SYSTEMD, Path('/etc/logrotate.d'), Path('/etc/cron.d')]
    candidates = [Path('/etc') / name for name in host.listdir('/etc') if name.startswith('logrotate')]
    for root in roots:
        for name in host.listdir(root):
            path = root / name
            info = host.info(path)
            if info is None or stat.S_ISLNK(info.st_mode):
                continue
            if stat.S_ISDIR(info.st_mode):
                candidates += [path / child for child in host.listdir(path)]
            else:
                candidates.append(path)
    for path in candidates:
        info = host.info(path)
        if info is None or not stat.S_ISREG(info.st_mode) or info.st_size > MAX_FILE:
            continue
        if Path(path).name in LEFTOVER_UNITS:
            continue
        try:
            named = needle.encode() in host.read(path)
        except (Failure, OSError):
            # What cannot be read safely might name the path.
            named = True
        if named:
            found.append(str(path))
    return found


def cleanup_plan(host: Host) -> Plan:
    plan = Plan()
    for unit in LEFTOVER_UNITS:
        path = SYSTEMD / unit
        if not host.exists(path):
            continue
        enabled, active = (host.run(['/usr/bin/systemctl', command, '--quiet', unit], check=False).code == 0
                           for command in ('is-enabled', 'is-active'))
        if enabled or active:
            plan.add('stop and disable ' + unit, lambda tx, unit=unit: set_unit(tx, unit, False, False))
        plan.add('remove ' + str(path), lambda tx, path=path: remove_path(tx, path))
    if plan.steps:
        plan.add('reload the systemd configuration', lambda tx: host.run(['/usr/bin/systemctl', 'daemon-reload']))
    for directory in LEFTOVER_DIRS:
        info = host.info(directory)
        if info is None:
            continue
        if stat.S_ISLNK(info.st_mode) or not stat.S_ISDIR(info.st_mode) or (host.real and info.st_uid != 0):
            plan.manual.append('{} is not a root-owned directory; review it by hand'.format(directory))
            continue
        users = references(host, directory)
        if users:
            plan.manual.append('{} is still named by {}; run "lcm host apply" first'.format(directory, ', '.join(users)))
            continue
        plan.add('remove {} (with its content)'.format(directory), lambda tx, directory=directory: remove_tree(tx, directory))
    for name in host.listdir('/var/lib'):
        path = '/var/lib/' + name
        if name.startswith(LEFTOVER_FAMILIES) and path not in LEFTOVER_DIRS and path not in OWN_STATE_DIRS:
            plan.notes.append('{} is not a known leftover and was left alone; review it by hand'.format(path))
    return plan


def remove_tree(tx: Transaction, directory: str) -> None:
    """Remove a leftover tree. rmtree works on descriptors and never follows a
    link inside the tree; the removal is recorded, not backed up."""
    tx.host.trusted(Path(directory).parent)
    tx.manifest['entries'].append({'path': directory, 'kind': 'tree', 'before': {'exists': True}, 'after': {'exists': False}})
    tx.save()
    if not shutil.rmtree.avoids_symlink_attacks:
        raise Failure('this Python cannot remove a tree without following links')
    shutil.rmtree(tx.host.p(directory))


def revert_plan(host: Host, helper, identity: str | None) -> Plan:
    plan = Plan()
    candidates = [name for name in transactions(host)
                  if load_manifest(host, name).get('state') == 'applied' and load_manifest(host, name).get('kind') in ('apply', 'remove')]
    if identity is None:
        if not candidates:
            plan.notes.append('no recorded transaction to revert')
            return plan
        identity = candidates[-1]
    manifest = load_manifest(host, identity)
    if manifest.get('state') == 'reverted':
        plan.notes.append('transaction {} is already reverted'.format(identity))
        return plan
    if any(entry.get('kind') == 'tree' for entry in manifest['entries']):
        plan.manual.append('transaction {} removed leftover trees; they cannot be restored'.format(identity))
        return plan
    done, conflicts = restore_entries(host, manifest, dry=True, helper=helper)
    for path in conflicts:
        plan.manual.append('{} changed after transaction {}; review it by hand'.format(path, identity))
    if conflicts:
        return plan
    for what in done:
        plan.add(what, None)
    if not done:
        return plan

    # Reverting the install itself removes the component. The optional files
    # the helper created since, which no entry records, go with it, unless a
    # policy of an earlier installation existed (an adoption), which owns them.
    created = {entry['path'] for entry in manifest['entries'] if not entry['before'].get('exists')}
    recorded = {entry['path'] for entry in manifest['entries']}
    plan.removes_component = host.exists(HELPER) and str(HELPER) in created
    extra = []
    if plan.removes_component and str(helper.POLICY) in created:
        pinned = helper_digests(host, helper)
        for path in (helper.AUDIT_OPTIONAL, helper.AIDE_DROPIN):
            if str(path) not in recorded and str(path) in pinned and host.exists(path):
                extra.append(path)
                plan.add('remove ' + str(path), None)

    if manifest.get('nginx'):
        plan.locks.append(helper.NGINX_LOCK)

    def revert(tx):
        restore_entries(host, manifest, dry=False, helper=helper)
        for path in extra:
            host.remove(path)
        parent = helper.AIDE_DROPIN.parent
        if extra and host.exists(parent) and not host.listdir(parent):
            host.remove(parent)
        if helper.AUDIT_OPTIONAL in extra:
            unload_optional_audit(host)
        after_restore(host, helper, manifest, strict=True)
        manifest['state'] = 'reverted'
        manifest['revertedAt'] = stamp()
        host.write(TRANSACTIONS / identity / 'manifest.json', json.dumps(manifest, indent=1, sort_keys=True).encode(), 0o600)
    plan.steps[-1].action = revert
    plan.add('reload systemd, audit watches and Nginx for the restored files', lambda tx: None)
    return plan


def execute(host: Host, plan: Plan, kind: str, after=None) -> dict:
    """Run a plan's steps in one recorded transaction; on a failure undo
    what the transaction changed."""
    result = plan.result()
    if plan.manual:
        result['changes'] = []
    if plan.manual or not plan.steps:
        return result
    descriptors = hold(host, plan.locks, 'the ingress component or the previous helper is changing the host; retry')
    try:
        tx = Transaction(host, kind)
        result['transaction'] = tx.id
        try:
            for step in plan.steps:
                if step.action is not None:
                    step.action(tx)
            if after is not None:
                after()
            tx.finish('applied')
        except BaseException as error:
            tx.finish('failed')
            message = str(error) if isinstance(error, (Failure, OSError)) else type(error).__name__
            try:
                _, conflicts = restore_entries(host, tx.manifest, dry=False)
                after_restore(host, plan.helper, tx.manifest, strict=False)
                tx.finish('reverted' if not conflicts else 'indeterminate')
            except Exception as revert_error:  # noqa: BLE001 - reported with the transaction to review
                tx.finish('indeterminate')
                raise Failure('{}; undoing it failed too ({}): review transaction {}'.format(message, revert_error, tx.id)) from None
            raise Failure('{}; the changes of transaction {} were undone'.format(message, tx.id)) from None
    finally:
        release(descriptors)
    prune(host)
    return result


def prune(host: Host) -> None:
    """Keep the newest KEEP_TRANSACTIONS transactions."""
    names = transactions(host)
    for name in names[:-KEEP_TRANSACTIONS]:
        host.trusted(TRANSACTIONS)
        shutil.rmtree(host.p(TRANSACTIONS / name))


# ---------------------------------------------------------------------------
# Local operator actions of the installed copy.


def baseline(host: Host, args) -> None:
    if not args.reviewed_source:
        raise Failure('Source/package review required; pass --reviewed-source only after reviewing findings')
    database = BASE / 'aide/aide.db'
    if host.exists(database) and not args.replace_reviewed:
        raise Failure('Baseline exists; use --replace-reviewed only after reviewing every difference')
    for path in (BASE / 'reports/aide.lock', ETC / 'aide.conf', BASE / 'aide/aide.db.new', database):
        host.trusted(path, missing=True)
        info = host.info(path)
        if info is not None and host.real and info.st_uid != 0:
            raise Failure('Untrusted baseline/lock file')
    descriptor = lock(host, BASE / 'reports/aide.lock', 'an AIDE check is running; retry')
    try:
        # A separate AIDE output; a scheduled check difference is never accepted automatically.
        result = host.run(['/usr/bin/aide', '--config', str(ETC / 'aide.conf'), '--init'], timeout=3600, check=False)
        if result.code != 0:
            raise Failure('AIDE initialization failed; inspect protected configuration and run manually for details')
        candidate = BASE / 'aide/aide.db.new'
        info = host.info(candidate)
        if info is None or not stat.S_ISREG(info.st_mode) or info.st_size > 128 * 1024 * 1024:
            raise Failure('Unexpected baseline output')
        if host.exists(database):
            previous = database.with_suffix('.db.previous')
            shutil.copyfile(host.p(database), host.p(previous))
            os.chmod(host.p(previous), 0o600)
        os.chmod(host.p(candidate), 0o600)
        os.replace(host.p(candidate), host.p(database))
    finally:
        os.close(descriptor)
    host.run(['/usr/bin/systemctl', 'start', AIDE_SERVICE], timeout=3700)
    print(json.dumps({'baselineCreated': True, 'historicalCompromiseExcluded': False,
                      'baselineDigest': digest(host.read(database, 128 * 1024 * 1024))}))


def tool_paths(host: Host) -> list[str]:
    """LCM's own root tools: the binary, /usr/local/libexec/lcm* and the
    component's programs. Service directories are never read."""
    paths = [path for path in TOOL_PATHS]
    for directory in TOOL_DIRS:
        paths += [directory + '/' + name for name in host.listdir(directory) if name.startswith('lcm')]
    return sorted(set(paths))


def digest_file(host: Host, path: str, limit: int) -> dict:
    """Hash a regular file opened without following a link."""
    descriptor = os.open(host.p(path), os.O_RDONLY | os.O_NOFOLLOW | os.O_NONBLOCK | os.O_CLOEXEC)
    with os.fdopen(descriptor, 'rb') as stream:
        info = os.fstat(stream.fileno())
        if not stat.S_ISREG(info.st_mode):
            return {'path': path, 'type': 'not a regular file'}
        if info.st_size > limit:
            raise Failure('Root-tool review exceeds file size budget')
        hashed, read = hashlib.sha256(), 0
        while chunk := stream.read(1 << 20):
            read += len(chunk)
            if read > limit:
                raise Failure('Root-tool review exceeds file size budget')
            hashed.update(chunk)
    return {'path': path, 'uid': info.st_uid, 'mode': oct(stat.S_IMODE(info.st_mode)), 'sha256': hashed.hexdigest()}


def review_sources(host: Host) -> None:
    report = {'observedAt': stamp(), 'packages': {}, 'tools': []}
    for package in ('sudo', 'openssh-server', 'nginx', 'python3-minimal'):
        result = host.run(['/usr/bin/dpkg', '--verify', package], check=False)
        # Verification can flag legitimate conffile edits; only the operator accepts them.
        report['packages'][package] = {'exitCode': result.code, 'findings': result.out.splitlines()[:200]}
    for top in tool_paths(host):
        info = host.info(top)
        if info is None:
            continue
        if not stat.S_ISDIR(info.st_mode):
            entries = [top]
        else:
            entries = []
            for directory, dirs, files in os.walk(host.p(top), followlinks=False):
                relative = '/' + os.path.relpath(directory, host.root) if host.root != Path('/') else directory
                entries += [relative.rstrip('/') + '/' + name for name in sorted(files + [d for d in dirs if os.path.islink(os.path.join(directory, d))])]
        for path in entries:
            if len(report['tools']) >= 3000:
                raise Failure('Root-tool source review exceeds file budget')
            if stat.S_ISLNK(host.info(path).st_mode):
                report['tools'].append({'path': path, 'symlink': True})
            else:
                report['tools'].append(digest_file(host, path, 128 * 1024 * 1024))
    target = BASE / 'source-review.json'
    host.write(target, json.dumps(report, indent=2).encode(), 0o600)
    print(json.dumps({'report': str(target), 'reviewRequired': True,
                      'limitation': 'Local package metadata and current root tools do not prove past integrity'}))


def verify_probes(host: Host) -> None:
    """Exercise disposable targets, never the real baseline or account files."""
    probe = BASE / 'audit-probe' / ('acceptance-' + str(time.time_ns()))
    host.write(probe, b'synthetic security acceptance probe\n', 0o640)
    host.remove(probe)
    time.sleep(2)
    audit = host.run(['/usr/sbin/ausearch', '-k', 'wssec_probe', '-ts', 'recent', '--raw'], check=False)
    audit_ok = audit.code == 0 and probe.name in audit.out
    with tempfile.TemporaryDirectory(prefix='acceptance-', dir=host.p(BASE / 'aide')) as temporary:
        directory = Path(temporary)
        targets = directory / 'targets'
        targets.mkdir()
        content, permission, removed = targets / 'content', targets / 'permission', targets / 'removed'
        for path in (content, permission, removed):
            path.write_text('original\n')
            path.chmod(0o600)
        config = directory / 'aide.conf'
        value = aide_config([str(targets)]).replace(str(BASE / 'aide/aide.db.new'), str(directory / 'db.new'))
        config.write_text(value.replace(str(BASE / 'aide/aide.db'), str(directory / 'db')))
        if host.run(['/usr/bin/aide', '--config', str(config), '--init'], timeout=120, check=False).code != 0:
            raise Failure('Disposable AIDE acceptance baseline failed')
        (directory / 'db.new').rename(directory / 'db')
        content.write_text('modified content\n')
        permission.chmod(0o644)
        removed.unlink()
        (targets / 'added').write_text('new\n')
        checked = host.run(['/usr/bin/aide', '--config', str(config), '--check'], timeout=120, check=False)
    report = {'observedAt': stamp(), 'auditProbeObserved': audit_ok,
              'aideAddedRemovedChangedDetected': checked.code == 7, 'aideExitCode': checked.code,
              'actualBaselineUnchanged': True}
    host.write(BASE / 'acceptance.json', json.dumps(report).encode(), 0o600)
    print(json.dumps(report))
    if not (audit_ok and checked.code == 7):
        raise Failure('Host acceptance probe failed; inspect protected local reports before marking complete')


def local_status(host: Host) -> dict:
    result = {'installed': host.exists(LIB / 'runtime.py'), 'baselineExists': False}
    if os.geteuid() != 0:
        result['privateStatus'] = 'requires root operator command'
        return result
    result['baselineExists'] = host.exists(BASE / 'aide/aide.db')
    result['transactions'] = transactions(host)[-5:]
    return result


# ---------------------------------------------------------------------------


def read_sources(directory: Path) -> dict[str, bytes]:
    sources = {}
    for name in ('runtime.py', 'install.py', 'helper.py'):
        path = directory / name
        descriptor = os.open(path, os.O_RDONLY | os.O_NOFOLLOW | os.O_CLOEXEC)
        with os.fdopen(descriptor, 'rb') as stream:
            info = os.fstat(stream.fileno())
            if not stat.S_ISREG(info.st_mode) or info.st_size > MAX_FILE:
                raise Failure('{} is not a regular file next to install.py'.format(name))
            sources[name] = stream.read(MAX_FILE + 1)
    return sources


def run_action(host: Host, helper, action: str, inputs: dict, sources: dict[str, bytes], transaction: str | None) -> dict:
    if action in ('plan', 'apply'):
        component = Component(host, helper, inputs, sources)
        plan = component.plan()
        plan.helper = helper
        if action == 'plan':
            return plan.result()
        if plan.token_missing:
            raise Failure('no ingest token was supplied')
        if any(step.action == component.install_packages for step in plan.steps) or host.account(USER) is None:
            # Packages and the account come first; everything else depends on them.
            first = Plan()
            first.steps = [step for step in plan.steps if step.what.startswith(('install the packages', 'create the system'))]
            first.helper = helper
            result = execute(host, first, 'prepare')
            plan = component.plan()
            plan.helper = helper
            plan.steps = [step for step in plan.steps if not step.what.startswith(('install the packages', 'create the system'))]
            plan.notes = result['notes'] + plan.notes
            prepared = result['changes']
        else:
            prepared = []
        if host.exists(LEGACY_HELPER_LOCK):
            plan.locks.insert(0, LEGACY_HELPER_LOCK)
        result = execute(host, plan, 'apply', after=(lambda: component.verify(plan)) if plan.steps else None)
        result['changes'] = prepared + result['changes']
        return result
    if action in ('remove-plan', 'remove'):
        plan = removal_plan(host, helper)
        plan.helper = helper
        return plan.result() if action == 'remove-plan' else execute(host, plan, 'remove')
    if action in ('cleanup-plan', 'cleanup'):
        plan = cleanup_plan(host)
        plan.helper = helper
        return plan.result() if action == 'cleanup-plan' else execute(host, plan, 'cleanup')
    if action in ('revert-plan', 'revert'):
        plan = revert_plan(host, helper, transaction)
        if action == 'revert-plan' or plan.manual or not plan.steps:
            return plan.result()
        # Restoring is the step itself; it is not recorded again.
        result = plan.result()
        descriptors = hold(host, plan.locks, 'the ingress component is changing Nginx; retry')
        try:
            for step in plan.steps:
                if step.action is not None:
                    step.action(None)
        finally:
            release(descriptors)
        return result
    raise Failure('unknown action ' + action)


@contextlib.contextmanager
def root_operation(host: Host):
    """Hold the component's lock for a root operation. The pending file makes
    the helper, which the lcm account can start through sudo at any time,
    refuse new requests, so that the lock is free within the wait."""
    host.write(PENDING, '{} {}\n'.format(os.getpid(), int(time.time())).encode(), 0o600)
    try:
        descriptor = lock(host, CONTROL_LOCK, 'another security component change is running; retry', LOCK_WAIT)
    except BaseException:
        host.remove(PENDING)
        raise
    try:
        yield
    finally:
        os.close(descriptor)
        host.remove(PENDING)


ACTIONS = ('plan', 'apply', 'remove-plan', 'remove', 'cleanup-plan', 'cleanup', 'revert-plan', 'revert',
           'baseline', 'review', 'verify', 'status')


def main() -> int:
    parser = argparse.ArgumentParser(description='Install, adopt, upgrade or remove the LCM security component.')
    parser.add_argument('action', choices=ACTIONS)
    parser.add_argument('--transaction', help='revert this transaction instead of the last one')
    parser.add_argument('--reviewed-source', action='store_true')
    parser.add_argument('--replace-reviewed', action='store_true')
    args = parser.parse_args()
    os.umask(0o077)
    if os.geteuid() != 0 and args.action != 'status':
        parser.error('this action requires root')
    host = Host()
    try:
        if args.action == 'status':
            print(json.dumps(local_status(host)))
            return 0
        with root_operation(host):
            if args.action == 'baseline':
                baseline(host, args)
                return 0
            if args.action == 'review':
                review_sources(host)
                return 0
            if args.action == 'verify':
                verify_probes(host)
                return 0
            raw = sys.stdin.buffer.read(65537)
            if len(raw) > 65536:
                raise Failure('installer input is too large')
            inputs = validate_inputs(json.loads(raw or b'{"version":1}'), args.action)
            directory = Path(__file__).resolve().parent
            sources = read_sources(directory) if args.action in ('plan', 'apply') else {}
            helper = load_helper(directory) if args.action != 'status' else None
            if args.action in ('remove-plan', 'remove', 'cleanup-plan', 'cleanup', 'revert-plan', 'revert') and helper is None:
                raise Failure('helper.py is missing next to install.py')
            print(json.dumps(run_action(host, helper, args.action, inputs, sources, args.transaction)))
        return 0
    except Exception as error:  # noqa: BLE001 - every failure is reported as JSON
        message = str(error) if isinstance(error, (Failure, OSError, ValueError)) else type(error).__name__
        print(json.dumps({'ok': False, 'error': message[:1000]}))
        return 1


if __name__ == '__main__':
    sys.exit(main())
