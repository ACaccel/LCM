#!/usr/bin/python3
"""Install reviewed fixed security RPC helper; root authorization is local."""
from __future__ import annotations

import argparse
import hashlib
import json
import os
from pathlib import Path
import pwd
import re
import stat
import subprocess
import tempfile

BASE = Path('/var/lib/lcm-security-host')
LIB = Path('/usr/local/libexec/lcm/security-host')
ETC = Path('/etc/lcm/security-host')
SUDOERS = Path('/etc/sudoers.d/lcm-security-host')
FIXED = ('/etc/systemd/system/workstation-security-aide.timer', '/etc/workstation-security/aide.conf',
         '/etc/nginx/conf.d/20-workstation-security.conf',
         '/etc/nginx/workstation-security/card-api-10-observation.conf',
         '/etc/nginx/workstation-security/card-socket-10-observation.conf',
         '/etc/nginx/snippets/card-together.conf',
         '/usr/local/lib/card-together-local-deploy/deploy/nginx/card-together.conf',
         '/etc/audit/rules.d/50-workstation-security.rules')
OPTIONAL = ('/etc/systemd/system/workstation-security-aide.timer.d/50-lcm-schedule.conf',
            '/etc/audit/rules.d/60-lcm-security-optional.rules')
ENV = {'PATH': '/usr/sbin:/usr/bin:/sbin:/bin', 'LANG': 'C.UTF-8'}


def trusted(path):
    for entry in (*reversed(path.parents), path):
        info = entry.lstat()
        if stat.S_ISLNK(info.st_mode) or info.st_uid != 0 or info.st_mode & 0o022:
            raise ValueError('An immutable root-owned installation path is required')


def snapshot(path):
    trusted(path)
    with path.open('rb') as source:
        data = source.read(65537)
    if len(data) > 65536:
        raise ValueError('Managed configuration exceeds its size bound')
    return data


def atomic(path, value, mode):
    trusted(path.parent)
    if path.exists() or path.is_symlink():
        trusted(path)
    fd, name = tempfile.mkstemp(prefix='.security-host-', dir=path.parent)
    try:
        with os.fdopen(fd, 'wb') as output:
            os.fchmod(output.fileno(), mode)
            output.write(value)
            output.flush()
            os.fsync(output.fileno())
        os.replace(name, path)
    finally:
        if os.path.exists(name):
            os.unlink(name)


def install(machine_id, expected):
    if os.geteuid() != 0:
        raise ValueError('Run this reviewed installer locally through sudo')
    if not re.fullmatch(r'[A-Za-z0-9][A-Za-z0-9._:-]{0,99}', machine_id):
        raise ValueError('A fixed immutable Machine id is required')
    if not re.fullmatch(r'[a-f0-9]{64}', expected):
        raise ValueError('A reviewed helper SHA-256 is required')
    source = Path(__file__).resolve().with_name('helper.py')
    descriptor = os.open(source, os.O_RDONLY | os.O_NOFOLLOW)
    with os.fdopen(descriptor, 'rb') as stream:
        info = os.fstat(stream.fileno())
        if not stat.S_ISREG(info.st_mode) or info.st_nlink != 1:
            raise ValueError('The reviewed helper must be a regular file')
        helper = stream.read(100001)
    if len(helper) > 100000 or hashlib.sha256(helper).hexdigest() != expected:
        raise ValueError('The reviewed helper digest does not match')
    pwd.getpwnam('lcm')
    if (ETC / 'policy.json').exists() or SUDOERS.exists():
        raise ValueError('An existing installation requires an explicit root-reviewed upgrade bundle')
    configs = {name: snapshot(Path(name)) for name in FIXED}
    if configs[FIXED[6]] != configs[FIXED[5]]:
        raise ValueError('The live and immutable Card policies must match before installation')
    for name in FIXED[3:5]:
        if not all(token in configs[name] for token in (b'limit_req_dry_run on;', b'limit_conn_dry_run on;')):
            raise ValueError('Only the reviewed dry-run installation is supported')
    schedule = re.findall(rb'^OnCalendar=\*-\*-\* ([0-9]{2}):([0-9]{2}):00$', configs[FIXED[0]], re.M)
    if len(schedule) != 1:
        raise ValueError('The existing daily AIDE timer requires local review')
    hour, minute = (int(value) for value in schedule[0])
    if hour > 23 or minute > 59:
        raise ValueError('The existing AIDE schedule is invalid')
    for name in OPTIONAL:
        if Path(name).exists() or Path(name).is_symlink():
            raise ValueError('Unexpected optional configuration requires local review')
    policy = {'version': 1, 'machineId': machine_id,
              'files': {name: hashlib.sha256(value).hexdigest() for name, value in configs.items()},
              'optionalFiles': {}, 'schedule': {'hour': hour, 'minute': minute},
              'profiles': {'aide': {'reviewed-baseline': {'enabled': True, 'required': True}},
                           'audit': {'core': {'enabled': True, 'required': True}}}}
    sudoers = b'lcm ALL=(root) NOPASSWD:NOSETENV: /usr/bin/python3 -I /usr/local/libexec/lcm/security-host/helper.py\n'
    with tempfile.NamedTemporaryFile(mode='wb') as candidate:
        candidate.write(sudoers)
        candidate.flush()
        subprocess.run(['/usr/sbin/visudo', '-cf', candidate.name], check=True, stdout=subprocess.DEVNULL, stderr=subprocess.DEVNULL, env=ENV)
    for directory in (BASE, BASE / 'operations', BASE / 'approvals', LIB, ETC, Path(OPTIONAL[0]).parent):
        if not directory.exists():
            trusted(directory.parent)
            directory.mkdir(mode=0o700 if directory in (BASE, BASE / 'operations', BASE / 'approvals', ETC) else 0o755)
        trusted(directory)
    atomic(LIB / 'helper.py', helper, 0o644)
    atomic(ETC / 'policy.json', json.dumps(policy, sort_keys=True, separators=(',', ':')).encode(), 0o600)
    atomic(SUDOERS, sudoers, 0o440)
    # Opening capability is the final step. Failure removes only this grant;
    # protected diagnostic state stays available for the administrator.
    try:
        subprocess.run(['/usr/sbin/visudo', '-cf', str(SUDOERS)], check=True, stdout=subprocess.DEVNULL, stderr=subprocess.DEVNULL, env=ENV)
        query = json.dumps({'version': 1, 'command': 'status', 'machineId': machine_id}).encode()
        result = subprocess.run(['/usr/bin/python3', '-I', str(LIB / 'helper.py')], input=query,
                                capture_output=True, timeout=45, check=True, env=ENV)
        reply = json.loads(result.stdout)
        if reply.get('version') != 1 or not reply.get('status', {}).get('available'):
            raise ValueError('Installed helper status was not verified')
    except BaseException:
        SUDOERS.unlink(missing_ok=True)
        raise
    return {'installed': True, 'machineId': machine_id, 'helperSha256': expected,
            'r11Enabled': False, 'baselineChanged': False, 'optionalProfiles': False}


def main():
    parser = argparse.ArgumentParser()
    parser.add_argument('--machine-id', required=True)
    parser.add_argument('--helper-sha256', required=True)
    args = parser.parse_args()
    os.umask(0o077)
    print(json.dumps(install(args.machine_id, args.helper_sha256), separators=(',', ':')))


if __name__ == '__main__':
    main()
