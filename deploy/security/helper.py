#!/usr/bin/python3
"""Bounded JSON RPC for fixed workstation controls; installed root-owned only."""
from __future__ import annotations

import datetime as dt
import fcntl
import hashlib
import json
import os
from pathlib import Path
import pwd
import re
import resource
import signal
import stat
import subprocess
import sys
import tempfile

VERSION = 1
# Policy version 2 pins only files this component renders. Version 1 also
# pinned another project's Nginx snippet; install.py converts it on adoption.
POLICY_VERSION = 2
BASE = Path('/var/lib/lcm-security-host')
POLICY = Path('/etc/lcm/security-host/policy.json')
LIB = Path('/usr/local/lib/workstation-security')
REPORTS = Path('/var/lib/workstation-security/reports')
AIDE_TIMER = Path('/etc/systemd/system/workstation-security-aide.timer')
AIDE_DROPIN = Path('/etc/systemd/system/workstation-security-aide.timer.d/50-lcm-schedule.conf')
AIDE_CONFIG = Path('/etc/workstation-security/aide.conf')
BASELINE = Path('/var/lib/workstation-security/aide/aide.db')
AUDIT_OPTIONAL = Path('/etc/audit/rules.d/60-lcm-security-optional.rules')
AUDIT_CORE = Path('/etc/audit/rules.d/50-workstation-security.rules')
NGINX_CONF = Path('/etc/nginx/conf.d/20-workstation-security.conf')
HOOKS = Path('/etc/nginx/lcm/hooks')
NGINX_LOG = '/var/log/nginx/workstation-security.jsonl'
# The ingress component holds this lock while it changes Nginx; taking it too
# keeps either side from testing or reloading the other's half-written files.
NGINX_LOCK = Path('/run/lcm-ingress.lock')
CONTROL_LOCK = Path('/run/lcm-security.lock')
# A root operation of install.py waits for CONTROL_LOCK; while this file is
# recent, new requests are refused so that the lcm account cannot hold the
# lock away from root by starting the helper again and again.
PENDING = Path('/run/lcm-security.pending')
PENDING_SECONDS = 3600
MAX_DEPTH = 8
ACTIONS = ('aide.check', 'aide.schedule', 'nginx.configure', 'audit.profile', 'aide.profile', 'baseline.request')
# Categories with observation thresholds. Ingress names the category of every
# location in $lcm_category; health and static locations have no thresholds.
CATEGORIES = ('auth', 'api', 'upload', 'socket')
DEFAULT_LIMITS = (
    {'category': 'api', 'rate': '30r/s', 'burst': 600, 'connections': 256},
    {'category': 'auth', 'rate': '120r/m', 'burst': 120, 'connections': 64},
    {'category': 'socket', 'rate': '15r/s', 'burst': 300, 'connections': 256},
    {'category': 'upload', 'rate': '30r/m', 'burst': 120, 'connections': 64},
)
SERVICE = re.compile(r'[a-z][a-z0-9-]{0,62}')
MAX_SERVICES = 64
ENV ={'PATH': '/usr/sbin:/usr/bin:/sbin:/bin', 'LANG': 'C.UTF-8', 'TZ': 'UTC'}
MAX_JSON = 65536
# The operation journal holds at most OPERATION_CAP entries. When it is full,
# finished entries last updated more than FINISHED_RETENTION ago are removed
# first, so that old history cannot block new previews forever. Running,
# indeterminate and awaiting entries are never removed; a prepared entry is
# finished once its preview has expired.
OPERATION_CAP = 2000
FINISHED_RETENTION = dt.timedelta(days=90)
FINISHED_STATES = ('succeeded', 'failed', 'prepared')
# Time budget: one fixed command may take STEP_SECONDS, the request
# WORK_SECONDS, and a rollback gets a fresh ROLLBACK_SECONDS. The longest
# step sequences (four for nginx.configure, two for its rollback) fit inside
# their budgets, and both budgets together stay below the 45 second limit of
# the Go client (internal/securityhost/runner.go), so the helper always
# finishes and records its outcome before LCM gives up on it.
STEP_SECONDS = 5
WORK_SECONDS = 25
ROLLBACK_SECONDS = 12


class Failure(Exception):
    def __init__(self, code='unavailable'):
        self.code = code
        super().__init__(code)


class Deadline(Failure):
    """Raised by SIGALRM so that a stuck request unwinds through rollback."""


def on_alarm(signum, frame):
    raise Deadline()


def arm_deadline(seconds):
    # The default SIGALRM action kills the process, which would leave
    # root-owned files half changed with no rollback and no journal update.
    signal.signal(signal.SIGALRM, on_alarm)
    signal.alarm(seconds)


def stamp():
    return dt.datetime.now(dt.timezone.utc).isoformat().replace('+00:00', 'Z')


def canonical(value):
    return json.dumps(value, separators=(',', ':'), sort_keys=True).encode()


def digest(value):
    return hashlib.sha256(value).hexdigest()


def trusted(path, missing=False):
    """Every privileged source and destination has an immutable root ancestry."""
    if not path.is_absolute() or '..' in path.parts:
        raise Failure()
    for item in (*reversed(path.parents), path):
        try:
            info = item.lstat()
        except FileNotFoundError:
            if missing:
                continue
            raise Failure() from None
        if stat.S_ISLNK(info.st_mode) or info.st_uid != 0 or info.st_mode & 0o022:
            raise Failure()


def read(path, limit=MAX_JSON):
    trusted(path)
    fd = os.open(path, os.O_RDONLY | os.O_NOFOLLOW | os.O_NONBLOCK | os.O_CLOEXEC)
    with os.fdopen(fd, 'rb') as source:
        info = os.fstat(source.fileno())
        if not stat.S_ISREG(info.st_mode) or info.st_nlink != 1 or info.st_size > limit:
            raise Failure()
        value = source.read(limit + 1)
    if len(value) > limit:
        raise Failure()
    return value


def read_json(path):
    value = json.loads(read(path))
    if not isinstance(value, dict):
        raise Failure()
    return value


def atomic(path, value, mode=0o600):
    trusted(path.parent)
    trusted(path, missing=True)
    fd, temporary = tempfile.mkstemp(prefix='.lcm-security-', dir=path.parent)
    try:
        with os.fdopen(fd, 'wb') as target:
            os.fchmod(target.fileno(), mode)
            target.write(value)
            target.flush()
            os.fsync(target.fileno())
        os.replace(temporary, path)
        directory = os.open(path.parent, os.O_DIRECTORY | os.O_RDONLY)
        try:
            os.fsync(directory)
        finally:
            os.close(directory)
    finally:
        if os.path.exists(temporary):
            os.unlink(temporary)


def lock_file(path):
    """Take an exclusive lock on a root-only file in a root-owned directory."""
    trusted(path.parent)
    descriptor = os.open(path, os.O_WRONLY | os.O_CREAT | os.O_NOFOLLOW | os.O_CLOEXEC, 0o600)
    try:
        info = os.fstat(descriptor)
        if not stat.S_ISREG(info.st_mode) or info.st_uid != 0 or info.st_nlink != 1 or info.st_mode & 0o077:
            raise Failure()
        fcntl.flock(descriptor, fcntl.LOCK_EX | fcntl.LOCK_NB)
    except OSError:
        os.close(descriptor)
        raise Failure('busy') from None
    except BaseException:
        os.close(descriptor)
        raise
    return descriptor


def run(argv, timeout=STEP_SECONDS, account=None):
    kwargs = {}
    if account is not None:
        kwargs = {'user': account.pw_uid, 'group': account.pw_gid, 'extra_groups': ()}
    # A temporary regular output file bounds subprocess output without pipe deadlock.
    with tempfile.TemporaryFile() as output:
        def bounded_output():
            resource.setrlimit(resource.RLIMIT_FSIZE, (MAX_JSON, MAX_JSON))
        result = subprocess.run(argv, stdin=subprocess.DEVNULL, stdout=output,
                                stderr=subprocess.DEVNULL, timeout=timeout, check=False,
                                cwd='/', env=ENV, preexec_fn=bounded_output, **kwargs)
        output.seek(0)
        data = output.read(MAX_JSON + 1)
    if result.returncode != 0 or len(data) > MAX_JSON:
        raise Failure()
    return data.decode('utf-8')


def valid_id(value):
    return isinstance(value, str) and re.fullmatch(r'[A-Za-z0-9][A-Za-z0-9._:-]{0,99}', value) is not None


def parse_time(value):
    return dt.datetime.fromisoformat(value.replace('Z', '+00:00'))


def purge_finished(directory):
    """Remove finished journals last updated before FINISHED_RETENTION.

    Every update rewrites a journal, so a recent modification time skips it
    without reading. Unreadable journals stay for local review."""
    cutoff = dt.datetime.now(dt.timezone.utc) - FINISHED_RETENTION
    removed = False
    for path in sorted(directory.iterdir()):
        if path.suffix != '.json' or not valid_id(path.stem):
            continue
        try:
            if path.lstat().st_mtime >= cutoff.timestamp():
                continue
            journal = read_json(path)
            operation = journal['operation']
            finished = (operation['state'] in FINISHED_STATES and parse_time(operation['updatedAt']) < cutoff
                        and parse_time(journal['preview']['expiresAt']) < cutoff)
        except Deadline:
            raise
        except (Failure, OSError, ValueError, KeyError, TypeError, AttributeError):
            continue
        if finished:
            path.unlink()
            removed = True
    if removed:
        handle = os.open(directory, os.O_DIRECTORY | os.O_RDONLY)
        try:
            os.fsync(handle)
        finally:
            os.close(handle)


def printable(value):
    return isinstance(value, str) and 1 <= len(value.strip()) <= 500 and all(c.isprintable() for c in value)


def exact(value, keys):
    if not isinstance(value, dict) or set(value) != set(keys):
        raise Failure('invalid')


def integer(value, lower, upper):
    return type(value) is int and lower <= value <= upper


def valid_limits(limits):
    """Exactly one threshold set per category, in category order."""
    if not isinstance(limits, list) or len(limits) != len(CATEGORIES):
        raise Failure('invalid')
    seen = set()
    for item in limits:
        exact(item, ('category', 'rate', 'burst', 'connections'))
        category = item['category']
        if category not in CATEGORIES or category in seen or not isinstance(item['rate'], str):
            raise Failure('invalid')
        seen.add(category)
        if (not re.fullmatch(r'[1-9][0-9]{0,4}r/[ms]', item['rate']) or not integer(item['burst'], 1, 10000)
                or not integer(item['connections'], 1, 4096)):
            raise Failure('invalid')
    return sorted(({key: item[key] for key in ('category', 'rate', 'burst', 'connections')} for item in limits),
                  key=lambda v: v['category'])


def valid_services(services):
    if (not isinstance(services, list) or len(services) > MAX_SERVICES or services != sorted(set(services))
            or not all(isinstance(name, str) and SERVICE.fullmatch(name) for name in services)):
        raise Failure('invalid')
    return services


def valid_nginx(nginx):
    """The Nginx section of the policy: null when Nginx observation is off."""
    if nginx is None:
        return None
    exact(nginx, ('services', 'limits'))
    return {'services': valid_services(nginx['services']), 'limits': valid_limits(nginx['limits'])}


def render_nginx_conf(limits):
    """The http-level observation configuration. Nothing in it rejects traffic.

    The ingress component sets $lcm_service and $lcm_category in each of its
    locations; the maps give both an empty default elsewhere, so requests
    outside ingress locations are neither logged nor counted."""
    lines = ['# Managed by the LCM security component; observation only, nothing here rejects traffic.',
             'map $uri $lcm_service { default ""; }',
             'map $uri $lcm_category { default ""; }',
             'map $lcm_category $lcm_security_loggable {',
             '    "" 0;',
             '    health 0;',
             '    default 1;',
             '}']
    for item in valid_limits(limits):
        category = item['category']
        lines += ['map $lcm_category $lcm_security_{0}_key {{ default ""; {0} $binary_remote_addr; }}'.format(category),
                  'limit_req_zone $lcm_security_{0}_key zone=lcm_security_{0}:2m rate={1};'.format(category, item['rate']),
                  'limit_conn_zone $lcm_security_{0}_key zone=lcm_security_{0}_conn:2m;'.format(category)]
    lines += ['log_format lcm_security escape=json',
              '    \'{"time":"$time_iso8601","source_ip":"$remote_addr",\'',
              '    \'"service":"$lcm_service","category":"$lcm_category","method":"$request_method",\'',
              '    \'"status":$status,"request_time":$request_time,\'',
              '    \'"request_limit":"$limit_req_status","connection_limit":"$limit_conn_status",\'',
              '    \'"request_length":$request_length,"bytes_sent":$bytes_sent}\';',
              'access_log {} lcm_security if=$lcm_security_loggable;'.format(NGINX_LOG)]
    return ('\n'.join(lines) + '\n').encode()


def render_hook(item):
    category = item['category']
    return ('# Managed by the LCM security component; thresholds stay in observation (dry-run) mode.\n'
            'limit_req_dry_run on;\nlimit_conn_dry_run on;\nlimit_req_status 429;\nlimit_conn_status 429;\n'
            '# Dry-run notices contain raw requests; keep them below the default error-log level.\n'
            'limit_req_log_level info;\nlimit_conn_log_level info;\n'
            'limit_req zone=lcm_security_{0} burst={1} nodelay;\n'
            'limit_conn lcm_security_{0}_conn {2};\n').format(category, item['burst'], item['connections']).encode()


def hook_path(service, category):
    return HOOKS / service / (category + '-10-observation.conf')


def nginx_files(nginx):
    """Every Nginx file of the component, rendered from the policy section."""
    if nginx is None:
        return {}
    files = {NGINX_CONF: render_nginx_conf(nginx['limits'])}
    for service in nginx['services']:
        for item in nginx['limits']:
            files[hook_path(service, item['category'])] = render_hook(item)
    return files


def pinned_paths(policy):
    """The files whose digests the policy pins: all rendered by this component."""
    return {str(path) for path in (AIDE_TIMER, AIDE_CONFIG, AUDIT_CORE)} | {str(path) for path in nginx_files(policy.get('nginx'))}


def typed_settings(action, settings, policy):
    if action in ('aide.check', 'baseline.request'):
        exact(settings, ())
    elif action == 'aide.schedule':
        exact(settings, ('hour', 'minute'))
        if not integer(settings['hour'], 0, 23) or not integer(settings['minute'], 0, 59):
            raise Failure('invalid')
    elif action == 'nginx.configure':
        exact(settings, ('limits',))
        settings = {'limits': valid_limits(settings['limits'])}
    elif action in ('audit.profile', 'aide.profile'):
        exact(settings, ('profiles',))
        if not isinstance(settings['profiles'], list) or len(settings['profiles']) > 16:
            raise Failure('invalid')
        approved = policy['profiles'][action.split('.')[0]]
        seen = set()
        for item in settings['profiles']:
            exact(item, ('name', 'enabled'))
            name = item['name']
            if name not in approved or name in seen or type(item['enabled']) is not bool:
                raise Failure('invalid')
            if approved[name]['required'] and not item['enabled']:
                raise Failure('invalid')
            seen.add(name)
        if seen != set(approved):
            raise Failure('invalid')
        settings = {'profiles': sorted(settings['profiles'], key=lambda v: v['name'])}
    else:
        raise Failure('invalid')
    return settings



def optional_rules(content):
    rules = set()
    for line in content.decode().splitlines():
        if not line.strip() or line.lstrip().startswith('#'):
            continue
        if not re.fullmatch(r'-w /[A-Za-z0-9_./-]+ -p [rwxa]{1,4} -k wssec_optional_[a-z0-9_]{1,40}', line):
            raise Failure()
        parts = line.split()
        if parts[3] != ''.join(c for c in 'rwxa' if c in parts[3]) or any(part in ('.', '..') for part in parts[1].split('/')):
            raise Failure()
        rules.add(line)
    return rules


def sync_optional_audit(content, expected_foreign=None):
    """Modify only our exact optional watch keys; never flush kernel rules."""
    desired = optional_rules(content)
    lines = {line for line in run(['/usr/sbin/auditctl', '-l']).splitlines() if line and line != 'No rules'}
    current = {line for line in lines if 'wssec_optional_' in line}
    if optional_rules(('\n'.join(current) + '\n').encode()) != current:
        raise Failure()
    foreign = lines - current
    if expected_foreign is not None and foreign != set(expected_foreign):
        raise Failure()
    if not all(any(key in line for line in foreign) for key in ('wssec_accounts', 'wssec_ssh', 'wssec_sudoers')):
        raise Failure()
    for line in sorted(current - desired):
        parts = line.split()
        run(['/usr/sbin/auditctl', '-W', *parts[1:]])
    for line in sorted(desired - current):
        run(['/usr/sbin/auditctl', *line.split()])
    run(['/usr/sbin/augenrules'])
    after = {line for line in run(['/usr/sbin/auditctl', '-l']).splitlines() if line and line != 'No rules'}
    if after != foreign | desired:
        raise Failure()



def aide_targets(content):
    """Decode only the installed curated absolute-path SEC rule grammar."""
    targets = set()
    for line in content.decode('utf-8').splitlines():
        if len(line) > 1000:
            return None
        if line.startswith('!'):
            # An excluded subtree: validated, not a target.
            raw = line[1:-len('(/|$)')] if line.endswith('(/|$)') else ''
            target = re.sub(r'\\([.\-])', r'\1', raw)
            if (not re.fullmatch(r'/[A-Za-z0-9_./-]+', target) or re.escape(target) != raw
                    or any(part in ('.', '..') for part in target.split('/'))):
                return None
            continue
        if line.startswith(('/', '^', '=', '@@')):
            if not line.endswith('(/|$) SEC'):
                return None
            raw = line[:-len('(/|$) SEC')]
            target = re.sub(r'\\([.\-])', r'\1', raw)
            if not target.startswith('/') or len(target) > 500 or not re.fullmatch(r'/[A-Za-z0-9_./-]+', target) or re.escape(target) != raw:
                return None
            if any(part in ('.', '..') for part in target.split('/')):
                return None
            targets.add(target)
            if len(targets) > 256:
                return None
    return sorted(targets)


def nullable_time(value):
    try:
        if not isinstance(value, str) or len(value) > 60:
            return None
        parsed = dt.datetime.fromisoformat(value.replace('Z', '+00:00'))
        if parsed.tzinfo is None:
            return None
        return parsed.astimezone(dt.timezone.utc).isoformat().replace('+00:00', 'Z')
    except (ValueError, OverflowError):
        return None


def number(value, maximum=10**12):
    return value if integer(value, 0, maximum) else None


class Controls:
    def __init__(self):
        self.policy = read_json(POLICY)
        if self.policy.get('version') != POLICY_VERSION or not valid_id(self.policy.get('machineId')):
            raise Failure()
        self.nginx = valid_nginx(self.policy.get('nginx'))
        if not isinstance(self.policy.get('files'), dict) or set(self.policy['files']) != pinned_paths(self.policy):
            raise Failure()
        self.files = {name: read(Path(name)) for name in self.policy['files']}
        self.drift = any(digest(content) != self.policy['files'][name] for name, content in self.files.items())
        self.schedule = self.policy['schedule']
        self.profiles = self.policy['profiles']
        self.optional = {}
        for path in (AIDE_DROPIN, AUDIT_OPTIONAL):
            expected = self.policy.get('optionalFiles', {}).get(str(path))
            if path.exists() or path.is_symlink():
                content = read(path)
                self.optional[str(path)] = content
                self.drift = self.drift or expected is None or digest(content) != expected
            elif expected is not None:
                self.drift = True
        for kind in ('aide', 'audit'):
            for item in self.profiles[kind].values():
                if not isinstance(item, dict) or type(item.get('required')) is not bool or type(item.get('enabled')) is not bool:
                    raise Failure()
        self.effective = digest(canonical({'files': {name: digest(value) for name, value in self.files.items()},
                                          'optionalFiles': {name: digest(value) for name, value in self.optional.items()},
                                          'policy': self.policy}))

    def profile_list(self, kind):
        return [{'name': name, 'enabled': value['enabled'], 'required': value['required']}
                for name, value in sorted(self.profiles[kind].items())]

    def limits(self):
        return [dict(item) for item in self.nginx['limits']] if self.nginx else []

    def nginx_ready(self):
        # The pinned files are rendered from the policy, so without drift
        # they hold exactly its dry-run thresholds.
        return self.nginx is not None

    def status(self):
        aide = {'targets': aide_targets(self.files[str(AIDE_CONFIG)]), 'result': None, 'lastRunAt': None, 'nextRunAt': None, 'baselineCreatedAt': None,
                'baselineDigest': None, 'schedule': self.schedule, 'profiles': self.profile_list('aide'), 'reportSummary': None}
        audit = {'enabled': None, 'lost': None, 'backlog': None, 'rulesLoaded': None, 'profiles': self.profile_list('audit')}
        collector = {'observedAt': None, 'lastDelivery': None, 'queued': None, 'dropped': None,
                     'deliveryError': None, 'health': {}}
        try:
            report = read_json(REPORTS / 'aide-status.json')
            if report.get('result') in ('clean', 'changed', 'error', 'baseline-missing'):
                aide['result'] = report['result']
            aide['lastRunAt'] = nullable_time(report.get('observedAt'))
            aide['reportSummary'] = 'Integrity comparison: {}; added={}, removed={}, changed={}.'.format(
                aide['result'] or 'unknown', number(report.get('added')), number(report.get('removed')), number(report.get('changed')))
        except (OSError, ValueError, Failure):
            pass
        try:
            # Hashing a bounded immutable baseline does not expose its entries.
            aide['baselineDigest'] = digest(read(BASELINE, 64 * 1024 * 1024))
            aide['baselineCreatedAt'] = dt.datetime.fromtimestamp(BASELINE.stat().st_mtime, dt.timezone.utc).isoformat().replace('+00:00', 'Z')
        except (OSError, Failure):
            pass
        try:
            next_run = run(['/usr/bin/systemctl', 'show', 'workstation-security-aide.timer', '--property=NextElapseUSecRealtime', '--value']).strip()
            if next_run and next_run != 'n/a':
                parsed = dt.datetime.strptime(next_run, '%a %Y-%m-%d %H:%M:%S %Z').replace(tzinfo=dt.timezone.utc)
                aide['nextRunAt'] = parsed.isoformat().replace('+00:00', 'Z')
        except (ValueError, OSError, Failure, subprocess.SubprocessError):
            pass
        try:
            report = read_json(REPORTS / 'audit-status.json')
            for key in ('enabled', 'lost', 'backlog'):
                audit[key] = number(report.get(key))
            audit['rulesLoaded'] = report.get('rulesLoaded') if type(report.get('rulesLoaded')) is bool else None
        except (OSError, ValueError, Failure):
            pass
        try:
            trusted(LIB / 'runtime.py')
            account = pwd.getpwnam('workstation-security')
            if account.pw_uid == 0 or account.pw_gid == 0:
                raise Failure()
            value = json.loads(run(['/usr/bin/python3', '-I', str(LIB / 'runtime.py'), 'collector-status'], account=account))
            if not isinstance(value, dict) or set(value) != set(collector):
                raise Failure()
            collector['queued'] = number(value['queued'], 1000)
            collector['dropped'] = number(value['dropped'])
            for key in ('observedAt', 'lastDelivery'):
                collector[key] = nullable_time(value[key])
            if value['deliveryError'] in (None, 'token unavailable or invalid', 'ingest unavailable or rejected sanitized event', 'unrecognized status'):
                collector['deliveryError'] = value['deliveryError']
            else:
                collector['deliveryError'] = 'unrecognized status'
            if isinstance(value['health'], dict) and len(value['health']) <= 100:
                collector['health'] = {key: {'problem': entry['problem']} for key, entry in value['health'].items()
                                       if isinstance(key, str) and re.fullmatch(r'[a-z:-]{1,60}', key)
                                       and isinstance(entry, dict) and type(entry.get('problem')) is bool}
        except (KeyError, OSError, ValueError, Failure, subprocess.SubprocessError):
            pass
        nginx_ready = self.nginx_ready()
        capabilities = []
        for action in ACTIONS:
            enabled, reason = True, ''
            if self.drift and action not in ('aide.check', 'baseline.request'):
                enabled, reason = False, 'Managed configuration drift requires local review.'
            elif action == 'nginx.configure' and not nginx_ready:
                enabled, reason = False, 'Nginx traffic observation is not installed on this host.'
            elif action in ('audit.profile', 'aide.profile') and not any(not item['required'] for item in self.profiles[action.split('.')[0]].values()):
                enabled, reason = False, 'No optional profiles have been approved by the local administrator.'
            capabilities.append({'action': action, 'enabled': enabled, 'reason': reason})
        return {'available': True, 'reason': 'Managed configuration drift requires local review.' if self.drift else '',
                'machineId': self.policy['machineId'], 'effectiveDigest': self.effective, 'capabilities': capabilities,
                'aide': aide, 'audit': audit,
                'nginx': {'mode': 'dry-run' if nginx_ready else None, 'dryRunOnly': True, 'r11Approved': False, 'limits': self.limits()},
                'collector': collector}

    def journal_path(self, identity):
        if not valid_id(identity):
            raise Failure('invalid')
        return BASE / 'operations' / (identity + '.json')

    def journal(self, request):
        path = self.journal_path(request['requestId'])
        if not path.exists():
            raise Failure('not-found')
        return path, read_json(path)

    def preview(self, request):
        if not printable(request.get('reason')) or not printable(request.get('actor')):
            raise Failure('invalid')
        if request.get('action') not in ACTIONS:
            raise Failure('invalid')
        path = self.journal_path(request['requestId'])
        settings = typed_settings(request['action'], request.get('settings'), self.policy)
        intent = {key: request[key] for key in ('machineId', 'requestId', 'action', 'reason', 'actor', 'expectedDigest')}
        intent['settings'] = settings
        intent_digest = digest(canonical(intent))
        if path.exists():
            previous = read_json(path)
            if previous['intentDigest'] != intent_digest:
                raise Failure('conflict')
            return previous['preview']
        if request['expectedDigest'] != self.effective:
            raise Failure('conflict')
        capability = next(value for value in self.status()['capabilities'] if value['action'] == request['action'])
        if not capability['enabled']:
            raise Failure()
        operations = BASE / 'operations'
        if len(list(operations.iterdir())) >= OPERATION_CAP:
            purge_finished(operations)
            if len(list(operations.iterdir())) >= OPERATION_CAP:
                raise Failure()
        now = stamp()
        preview = {'requestId': request['requestId'], 'action': request['action'],
                   'previewHash': intent_digest,
                   'expiresAt': (dt.datetime.now(dt.timezone.utc) + dt.timedelta(minutes=10)).isoformat().replace('+00:00', 'Z'),
                   'changes': self.describe(request['action'], settings),
                   'requiresLocalApproval': request['action'] in ('baseline.request', 'aide.profile')}
        operation = {'id': request['requestId'], 'action': request['action'], 'state': 'prepared',
                     'reason': request['reason'], 'actor': request['actor'], 'createdAt': now, 'updatedAt': now,
                     'result': '', 'error': '', 'previewHash': intent_digest, 'effectiveDigest': self.effective}
        atomic(path, canonical({'intentDigest': intent_digest, 'intent': intent, 'preview': preview, 'operation': operation}))
        return preview

    def describe(self, action, settings):
        if action == 'aide.check':
            return ['Start the existing single-instance AIDE comparison; preserve the current baseline.']
        if action == 'aide.schedule':
            return ['Set the existing daily AIDE timer to {:02d}:{:02d} in the host timezone; retain ten-minute jitter.'.format(settings['hour'], settings['minute'])]
        if action == 'nginx.configure':
            return ['Update root-managed {} dry-run thresholds: rate={}, burst={}, connections={}.'.format(v['category'], v['rate'], v['burst'], v['connections']) for v in settings['limits']]
        if action == 'baseline.request':
            return ['Record a local baseline review request; no baseline contents or accepted state will change.']
        if action == 'aide.profile':
            return ['Stage the approved integrity profile selection for local baseline review; active scope remains unchanged.']
        return ['Change only root-approved optional audit profiles; required core rules remain unchanged.']

    def operation(self, request):
        path, journal = self.journal(request)
        operation = journal['operation']
        if operation['state'] == 'running':
            if operation['action'] == 'aide.check':
                try:
                    state = run(['/usr/bin/systemctl', 'show', 'workstation-security-aide.service', '--property=ActiveState', '--value']).strip()
                    if state not in ('active', 'activating', 'deactivating'):
                        report = read_json(REPORTS / 'aide-status.json')
                        if nullable_time(report.get('observedAt')) and dt.datetime.fromisoformat(report['observedAt'].replace('Z', '+00:00')) >= dt.datetime.fromisoformat(journal.get('startedAt', operation['createdAt']).replace('Z', '+00:00')) and report.get('result') in ('clean', 'changed'):
                            operation.update(state='succeeded', result='AIDE comparison completed: ' + report['result'] + '.')
                        elif nullable_time(report.get('observedAt')) and dt.datetime.fromisoformat(report['observedAt'].replace('Z', '+00:00')) >= dt.datetime.fromisoformat(journal.get('startedAt', operation['createdAt']).replace('Z', '+00:00')):
                            operation.update(state='failed', error='The AIDE comparison did not complete successfully; inspect the protected report locally.')
                        else:
                            operation.update(state='indeterminate', error='A new AIDE report could not be verified; local review is required.')
                except (OSError, ValueError, Failure, subprocess.SubprocessError):
                    pass
            else:
                # No mutation is replayed after a crash. Local review reconciles
                # actual files and command results against the protected journal.
                operation.update(state='indeterminate', error='The previous helper exited during a change; local review is required before retrying.')
            operation['updatedAt'] = stamp()
            atomic(path, canonical(journal))
        if operation['state'] == 'awaiting-local-approval':
            receipt_path = BASE / 'approvals' / (request['requestId'] + '.json')
            if receipt_path.exists():
                receipt = read_json(receipt_path)
                if set(receipt) == {'requestId', 'previewHash', 'approvedAt', 'baselineDigest'} and receipt['requestId'] == operation['id'] and receipt['previewHash'] == operation['previewHash'] and nullable_time(receipt['approvedAt']):
                    current = digest(read(BASELINE, 64 * 1024 * 1024))
                    if receipt['baselineDigest'] == current:
                        scope_ready = True
                        if operation['action'] == 'aide.profile':
                            current_controls = Controls()
                            selected = {(name, value['enabled']) for name, value in current_controls.profiles['aide'].items()}
                            requested = {(value['name'], value['enabled']) for value in journal['intent']['settings']['profiles']}
                            scope_ready = not current_controls.drift and selected == requested
                            if not scope_ready:
                                operation.update(result='The requested integrity profiles are not yet verified in the effective root policy; local scope review remains required.', updatedAt=stamp())
                        if scope_ready:
                            result = 'The local administrator recorded completion of the reviewed baseline request.'
                            if operation['action'] == 'aide.profile':
                                result = 'The requested integrity profiles and local baseline review were verified in the effective root policy.'
                                operation['effectiveDigest'] = current_controls.effective
                            operation.update(state='succeeded', result=result, updatedAt=stamp())
                        atomic(path, canonical(journal))
        return operation

    def apply(self, request):
        path, journal = self.journal(request)
        operation, intent = journal['operation'], journal['intent']
        if request.get('previewHash') != operation['previewHash'] or request.get('reason') != intent['reason'] or request.get('actor') != intent['actor']:
            raise Failure('conflict')
        if operation['state'] != 'prepared':
            return self.operation(request)
        if dt.datetime.now(dt.timezone.utc) >= dt.datetime.fromisoformat(journal['preview']['expiresAt'].replace('Z', '+00:00')) or intent['expectedDigest'] != self.effective:
            raise Failure('conflict')
        capability = next(v for v in self.status()['capabilities'] if v['action'] == operation['action'])
        if not capability['enabled']:
            raise Failure()
        operation.update(state='running', updatedAt=stamp())
        journal['startedAt'] = stamp()
        atomic(path, canonical(journal))
        try:
            action, settings = operation['action'], intent['settings']
            if action == 'aide.check':
                state = run(['/usr/bin/systemctl', 'show', 'workstation-security-aide.service', '--property=ActiveState', '--value']).strip()
                if state in ('active', 'activating', 'deactivating'):
                    raise Failure('busy')
                run(['/usr/bin/systemctl', 'start', '--no-block', 'workstation-security-aide.service'])
                operation['result'] = 'AIDE comparison was queued; completion requires a fresh protected report.'
            elif action == 'baseline.request' or action == 'aide.profile':
                operation.update(state='awaiting-local-approval', result='A local administrator must review and approve this request; no baseline or active integrity scope was changed.')
            else:
                self.change(action, settings, path, journal)
                operation.update(state='succeeded', result='The effective host configuration was read back and verified.')
                operation['effectiveDigest'] = Controls().effective
        except (Failure, OSError, ValueError, subprocess.SubprocessError):
            if operation['state'] != 'indeterminate':
                operation.update(state='failed', error='The fixed host operation failed; inspect the protected journal locally before retrying.')
        operation['updatedAt'] = stamp()
        atomic(path, canonical(journal))
        return operation

    def change(self, action, settings, journal_path, journal):
        if self.drift:
            raise Failure('conflict')
        targets = {}
        if action == 'aide.schedule':
            trusted(AIDE_DROPIN.parent)
            targets[AIDE_DROPIN] = ('[Timer]\nOnCalendar=\nOnCalendar=*-*-* {:02d}:{:02d}:00\nRandomizedDelaySec=10m\nPersistent=true\n'.format(settings['hour'], settings['minute'])).encode()
        elif action == 'nginx.configure':
            if not self.nginx_ready():
                raise Failure()
            targets = nginx_files({'services': self.nginx['services'], 'limits': settings['limits']})
            if set(targets) != set(nginx_files(self.nginx)):
                raise Failure()
        elif action == 'audit.profile':
            parts = []
            for profile in settings['profiles']:
                item = self.profiles['audit'][profile['name']]
                if not item['required'] and profile['enabled']:
                    content = item.get('content')
                    if not isinstance(content, str) or len(content) > 8192 or not content.endswith('\n'):
                        raise Failure()
                    for line in content.splitlines():
                        if not line.strip() or line.lstrip().startswith('#'):
                            continue
                        if not re.fullmatch(r'-w /[A-Za-z0-9_./-]+ -p [rwxa]{1,4} -k wssec_optional_[a-z0-9_]{1,40}', line):
                            raise Failure()
                    parts.append(content)
            targets[AUDIT_OPTIONAL] = ''.join(parts).encode()
        else:
            raise Failure('invalid')
        before = {str(path): self.files.get(str(path), self.optional.get(str(path))) for path in targets}
        # One digest over each set: the journal stays far below its read limit
        # whatever the number of services; the content to restore stays in
        # memory, and the files' own digests are pinned in the policy.
        journal['rollbackDigest'] = digest(canonical({name: digest(value) if value is not None else None
                                                      for name, value in before.items()}))
        journal['candidateDigest'] = digest(canonical({str(path): digest(value) for path, value in targets.items()}))
        policy_before = json.loads(canonical(self.policy))
        journal['policyBeforeDigest'] = digest(canonical(policy_before))
        if action == 'audit.profile':
            lines = {line for line in run(['/usr/sbin/auditctl', '-l']).splitlines() if line and line != 'No rules'}
            journal['auditForeignBefore'] = sorted(line for line in lines if 'wssec_optional_' not in line)
        atomic(journal_path, canonical(journal))
        nginx_fd = None
        written = []
        try:
            if action == 'nginx.configure':
                nginx_fd = lock_file(NGINX_LOCK)
                # Re-read under the lock the ingress component changes Nginx with.
                if Controls().effective != self.effective:
                    raise Failure('conflict')
                run(['/usr/sbin/nginx', '-t'])
            for path, value in targets.items():
                atomic(path, value, 0o644)
                written.append(path)
            if action == 'nginx.configure':
                run(['/usr/sbin/nginx', '-t'])
                run(['/usr/bin/systemctl', 'reload', 'nginx.service'])
                state = run(['/usr/bin/systemctl', 'show', 'nginx.service', '--property=ActiveState', '--value']).strip()
                if state != 'active':
                    raise Failure()
            elif action == 'aide.schedule':
                run(['/usr/bin/systemctl', 'daemon-reload'])
                run(['/usr/bin/systemctl', 'restart', 'workstation-security-aide.timer'])
                active = run(['/usr/bin/systemctl', 'show', 'workstation-security-aide.timer', '--property=ActiveState', '--value']).strip()
                timers = run(['/usr/bin/systemctl', 'show', 'workstation-security-aide.timer', '--property=TimersCalendar', '--value'])
                expected = '{:02d}:{:02d}:00'.format(settings['hour'], settings['minute'])
                if active != 'active' or expected not in timers:
                    raise Failure()
            elif action == 'audit.profile':
                sync_optional_audit(targets[AUDIT_OPTIONAL], journal['auditForeignBefore'])
            for path, value in targets.items():
                if read(path) != value:
                    raise Failure()
                key = 'files' if str(path) in self.policy['files'] else 'optionalFiles'
                self.policy.setdefault(key, {})[str(path)] = digest(value)
            if action == 'aide.schedule':
                self.policy['schedule'] = settings
            elif action == 'nginx.configure':
                self.policy['nginx']['limits'] = settings['limits']
            elif action == 'audit.profile':
                for profile in settings['profiles']:
                    self.policy['profiles']['audit'][profile['name']]['enabled'] = profile['enabled']
            atomic(POLICY, canonical(self.policy))
            if Controls().drift:
                raise Failure()
        except BaseException:
            # The failure may be the request deadline itself, so the rollback
            # gets its own bounded budget instead of whatever time is left.
            arm_deadline(ROLLBACK_SECONDS)
            rollback_ok = True
            try:
                for path in written:
                    if read(path) != targets[path]:
                        raise Failure()
                for path in written:
                    old = before[str(path)]
                    if old is None:
                        path.unlink()
                    else:
                        atomic(path, old, 0o644)
                atomic(POLICY, canonical(policy_before))
                if action == 'nginx.configure':
                    run(['/usr/sbin/nginx', '-t'])
                    run(['/usr/bin/systemctl', 'reload', 'nginx.service'])
                elif action == 'aide.schedule':
                    run(['/usr/bin/systemctl', 'daemon-reload'])
                    run(['/usr/bin/systemctl', 'restart', 'workstation-security-aide.timer'])
                elif action == 'audit.profile':
                    sync_optional_audit(before[str(AUDIT_OPTIONAL)] or b'', journal['auditForeignBefore'])
            except (OSError, Failure, subprocess.SubprocessError):
                rollback_ok = False
            signal.alarm(0)
            journal['rollbackVerified'] = rollback_ok
            atomic(journal_path, canonical(journal))
            if not rollback_ok:
                journal['operation'].update(state='indeterminate', error='Rollback could not be verified; local administrator review is required.')
            raise
        finally:
            if nginx_fd is not None:
                os.close(nginx_fd)

    def dispatch(self, request):
        if request.get('version') != VERSION or request.get('machineId') != self.policy['machineId']:
            raise Failure('invalid')
        command = request.get('command')
        allowed = {'version', 'command', 'machineId'}
        if command == 'preview':
            allowed |= {'requestId', 'action', 'reason', 'actor', 'expectedDigest', 'settings'}
        elif command == 'apply':
            allowed |= {'requestId', 'previewHash', 'reason', 'actor'}
        elif command == 'operation':
            allowed |= {'requestId'}
        if set(request) != allowed:
            raise Failure('invalid')
        if command == 'status':
            return {'version': VERSION, 'status': self.status()}
        if command == 'preview':
            return {'version': VERSION, 'preview': self.preview(request)}
        if command == 'apply':
            return {'version': VERSION, 'operation': self.apply(request)}
        if command == 'operation':
            return {'version': VERSION, 'operation': self.operation(request)}
        raise Failure('invalid')


def refuse_while_pending():
    try:
        info = PENDING.lstat()
    except FileNotFoundError:
        return
    # A stale file of an interrupted installer (it lives in /run) stops
    # counting after PENDING_SECONDS.
    if stat.S_ISREG(info.st_mode) and info.st_uid == 0 and dt.datetime.now().timestamp() - info.st_mtime < PENDING_SECONDS:
        raise Failure('busy')


def depth(raw):
    """The deepest nesting of arrays and objects, outside strings."""
    deepest = level = 0
    in_string = escaped = False
    for byte in raw:
        if in_string:
            if escaped:
                escaped = False
            elif byte == 0x5c:
                escaped = True
            elif byte == 0x22:
                in_string = False
        elif byte == 0x22:
            in_string = True
        elif byte in (0x5b, 0x7b):
            level += 1
            deepest = max(deepest, level)
        elif byte in (0x5d, 0x7d):
            level -= 1
    return deepest


def main():
    os.umask(0o077)
    resource.setrlimit(resource.RLIMIT_CORE, (0, 0))
    resource.setrlimit(resource.RLIMIT_AS, (256 * 1024 * 1024, 256 * 1024 * 1024))
    arm_deadline(WORK_SECONDS)
    if os.geteuid() != 0 or len(sys.argv) != 1:
        raise Failure()
    trusted(Path(__file__))
    trusted(BASE)
    # The installer holds the same lock while it changes the component.
    refuse_while_pending()
    lock = lock_file(CONTROL_LOCK)
    try:
        raw = sys.stdin.buffer.read(16385)
        if len(raw) > 16384:
            raise Failure('invalid')
        if depth(raw) > MAX_DEPTH:
            raise Failure('invalid')
        request = json.loads(raw)
        if not isinstance(request, dict):
            raise Failure('invalid')
        response = Controls().dispatch(request)
        encoded = canonical(response)
        if len(encoded) > MAX_JSON:
            raise Failure()
        print(encoded.decode())
    finally:
        os.close(lock)


if __name__ == '__main__':
    try:
        main()
    except Exception as error:  # noqa: BLE001 - every failure answers with the protocol's error
        print(json.dumps({'version': VERSION, 'error': error.code if isinstance(error, Failure) else 'unavailable'}))
