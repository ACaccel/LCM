"""Installer tests: a temporary directory stands for the host's root, and
commands are recorded by a fake instead of run."""
import hashlib
import importlib.util
import json
import os
from pathlib import Path
import stat
import tempfile
import unittest
from unittest.mock import patch

HERE = Path(__file__).parent


def load(name, file):
    spec = importlib.util.spec_from_file_location(name, HERE / file)
    module = importlib.util.module_from_spec(spec)
    spec.loader.exec_module(module)
    return module


I = load('security_installer', 'install.py')
H = load('security_installer_helper', 'helper.py')
SOURCES = {name: (HERE / name).read_bytes() for name in ('runtime.py', 'install.py', 'helper.py')}
INPUT = {'version': 1, 'machineId': 'machine-a', 'endpoint': 'http://127.0.0.1:7800/api/v1/hooks/security',
         'services': ['web'], 'watch': {'deploy': ['/etc/web', '/opt/extra', '/opt/missing', '/opt/web']},
         'aideIgnore': ['/opt/web/releases']}
COLLECTOR_ID = 1234


class FakeSystem:
    """The commands the installer runs, with the state they change."""

    def __init__(self, root: Path):
        self.root = root
        self.calls = []
        self.units = {}
        self.vendor = {}
        self.rules = {'-a always,exit -F arch=b64 -S execve -k unrelated'}
        self.audit_enabled = 1
        self.fail = None
        self.accounts = {'workstation-security': (COLLECTOR_ID, COLLECTOR_ID, '/usr/sbin/nologin', ()),
                         'www-data': (33, 33, '/usr/sbin/nologin', ())}

    def mutating(self):
        readonly = ('is-enabled', 'is-active', 'show')
        out = []
        for argv in self.calls:
            if argv[0] == '/usr/bin/systemctl' and argv[1] in readonly:
                continue
            if argv[0] == '/usr/sbin/auditctl' and argv[1] in ('-s', '-l'):
                continue
            out.append(argv)
        return out

    def __call__(self, argv, timeout=120, stdin=None):
        self.calls.append(argv)
        if self.fail and self.fail(argv):
            return I.Result(1, '')
        program, args = argv[0], argv[1:]
        if program == '/usr/bin/systemctl':
            verb, unit = args[0], args[-1]
            state = self.units.setdefault(unit, {'enabled': False, 'active': False})
            if verb == 'is-enabled':
                return I.Result(0 if state['enabled'] else 1, '')
            if verb == 'is-active':
                return I.Result(0 if state['active'] else 3, '')
            if verb == 'show':
                return I.Result(0, 'loaded\n' if unit in self.vendor else 'not-found\n')
            if verb == 'enable':
                state.update(enabled=True, active=state['active'] or '--now' in args)
            elif verb == 'disable':
                state.update(enabled=False, active=False if '--now' in args else state['active'])
            elif verb in ('start', 'restart'):
                state['active'] = True
                if unit == I.AUDIT_STATUS:
                    put(self.root, str(I.BASE / 'reports' / 'audit-status.json'), '{}', 0o640)
            elif verb == 'stop':
                state['active'] = False
            return I.Result(0, '')
        if program == '/usr/sbin/auditctl':
            if args == ['-s']:
                return I.Result(0, 'enabled {}\nlost 0\n'.format(self.audit_enabled))
            if args == ['-l']:
                return I.Result(0, '\n'.join(sorted(self.rules)) + '\n')
            if args[0] == '-W':
                self.rules.discard('-w ' + ' '.join(args[1:]))
            elif args[0] == '-w':
                self.rules.add(' '.join(args))
            return I.Result(0, '')
        if program == '/usr/bin/apt-get':
            for tool in I.PACKAGE_TOOLS:
                put(self.root, tool, 'tool\n', 0o755)
            put(self.root, '/etc/audit/auditd.conf', 'log_file = /var/log/audit/audit.log\nnum_logs = 5\n', 0o640)
            return I.Result(0, '')
        if program == '/usr/sbin/groupadd':
            self.accounts['group:workstation-security'] = COLLECTOR_ID
            return I.Result(0, '')
        if program == '/usr/sbin/useradd':
            self.accounts['workstation-security'] = (COLLECTOR_ID, COLLECTOR_ID, '/usr/sbin/nologin', ())
            return I.Result(0, '')
        return I.Result(0, '')


def put(root: Path, path: str, content, mode=0o644):
    target = root / path.lstrip('/')
    target.parent.mkdir(parents=True, exist_ok=True)
    target.write_bytes(content if isinstance(content, bytes) else content.encode())
    os.chmod(target, mode)
    return target


def tree(root: Path) -> dict:
    """Every path below root with its type, mode and content digest."""
    out = {}
    for path in sorted(root.rglob('*')):
        info = path.lstat()
        value = (stat.S_IFMT(info.st_mode), stat.S_IMODE(info.st_mode))
        if stat.S_ISREG(info.st_mode):
            value += (hashlib.sha256(path.read_bytes()).hexdigest(),)
        out[str(path.relative_to(root))] = value
    return out


class InstallerTests(unittest.TestCase):
    def setUp(self):
        self.tmp = tempfile.TemporaryDirectory()
        self.addCleanup(self.tmp.cleanup)
        self.root = Path(self.tmp.name)
        for directory in ('/etc/systemd/system', '/etc/audit/rules.d', '/etc/ssh', '/etc/lcm', '/etc/web', '/opt/web',
                          '/opt/extra', '/etc/nginx/conf.d', '/var/log/nginx', '/var/lib', '/run', '/run/lock',
                          '/usr/local/libexec/lcm', '/home/alice/.ssh', '/etc/default', '/etc/cron.d'):
            (self.root / directory.lstrip('/')).mkdir(parents=True, exist_ok=True)
        for tool in I.PACKAGE_TOOLS + ('/usr/sbin/nginx', '/usr/bin/sudo', '/usr/local/bin/lcm'):
            put(self.root, tool, 'tool\n', 0o755)
        put(self.root, '/usr/bin/python3.12', 'python\n', 0o755)
        (self.root / 'usr/bin/python3').symlink_to('python3.12')
        put(self.root, '/etc/passwd', 'root:x:0:0:root:/root:/bin/bash\nalice:x:1000:1000::/home/alice:/bin/bash\n')
        put(self.root, '/etc/audit/auditd.conf', 'log_file = /var/log/audit/audit.log\nnum_logs = 5\n', 0o640)
        put(self.root, '/var/log/audit/audit.log', 'type=DAEMON_START\n', 0o600)
        os.chmod(self.root / 'var/log/audit', 0o700)
        self.system = FakeSystem(self.root)
        self.host = I.Host(self.root, self.system, self.system.accounts)
        patch.object(I.Component, 'verify', lambda component, plan: None).start()
        self.addCleanup(patch.stopall)

    def run_action(self, action, **fields):
        inputs = dict(INPUT, **fields)
        return I.run_action(self.host, H, action, I.validate_inputs(inputs, action), SOURCES, None)

    def read(self, path) -> bytes:
        return (self.root / str(path).lstrip('/')).read_bytes()

    def mode(self, path) -> int:
        return stat.S_IMODE((self.root / str(path).lstrip('/')).lstat().st_mode)

    def exists(self, path) -> bool:
        return os.path.lexists(self.root / str(path).lstrip('/'))

    def install(self):
        result = self.run_action('apply', token='lcm_example-token')
        self.assertEqual(result['manual'], [])
        return result

    # -- install and converge ---------------------------------------------------

    def test_plan_changes_nothing(self):
        before = tree(self.root)
        result = self.run_action('plan')
        self.assertTrue(result['changes'])
        self.assertTrue(result['tokenMissing'])
        self.assertEqual(tree(self.root), before)
        self.assertEqual(self.system.mutating(), [])

    def test_install_needs_a_token_and_converges(self):
        with self.assertRaises(I.Failure):
            self.run_action('apply')
        result = self.install()
        self.assertTrue(result['transaction'])
        self.assertEqual(self.read(I.TOKEN), b'lcm_example-token\n')
        self.assertEqual(self.mode(I.TOKEN), 0o600)
        again = self.run_action('plan')
        self.assertEqual((again['changes'], again['manual']), ([], []))
        self.system.calls.clear()
        self.assertEqual(self.run_action('apply')['changes'], [])
        self.assertEqual(self.system.mutating(), [])

    def test_collector_reads_the_token_as_a_credential(self):
        self.install()
        config = json.loads(self.read(I.COLLECTOR_CONFIG))
        self.assertEqual(config['tokenCredential'], 'ingest-token')
        self.assertNotIn('tokenFile', config)
        self.assertNotIn('lcm_example-token', json.dumps(config))
        unit = self.read(I.SYSTEMD / I.COLLECTOR).decode()
        self.assertIn('LoadCredential=ingest-token:/etc/workstation-security/ingest-token', unit)

    def test_watches_come_from_the_host_configuration(self):
        result = self.install()
        rules = self.read(H.AUDIT_CORE).decode()
        for path in ('/opt/web', '/etc/web', '/opt/extra', '/usr/local/libexec/lcm', '/usr/local/bin/lcm', '/etc/lcm'):
            self.assertIn('-w {} -p wa -k wssec_deploy'.format(path), rules)
        self.assertIn('-w /home/alice/.ssh -p wa -k wssec_ssh', rules)
        self.assertNotIn('/opt/missing', rules)
        self.assertIn('watch.deploy: /opt/missing does not exist and is not watched', result['notes'])
        aide = self.read(H.AIDE_CONFIG).decode()
        self.assertIn('/usr/bin/python3\\.12(/|$) SEC', aide)
        self.assertIn('/opt/web(/|$) SEC', aide)
        # Releases are recorded by LCM's deploys; audit still watches them.
        self.assertIn('\n!/opt/web/releases(/|$)\n', aide)
        self.assertIsNotNone(H.aide_targets(aide.encode()))
        self.assertNotIn('releases', rules)
        self.assertEqual(self.system.rules - {'-a always,exit -F arch=b64 -S execve -k unrelated'},
                         {line for line in rules.splitlines() if line.startswith('-w')})

    def test_nginx_observation_uses_ingress_variables(self):
        self.install()
        conf = self.read(H.NGINX_CONF).decode()
        self.assertIn('"service":"$lcm_service","category":"$lcm_category"', conf)
        self.assertIn('if=$lcm_security_loggable', conf)
        for category in H.CATEGORIES:
            hook = self.read(H.hook_path('web', category)).decode()
            self.assertIn('limit_req_dry_run on;', hook)
        self.assertNotIn('card', conf + ''.join(self.read(H.hook_path('web', c)).decode() for c in H.CATEGORIES))
        self.assertIn([str(I.NGINX), '-t'], self.system.calls)
        self.assertIn(['/usr/bin/systemctl', 'reload', 'nginx.service'], self.system.calls)
        self.assertEqual(self.mode(H.NGINX_LOG), 0o640)

    def test_policy_pins_exactly_the_rendered_files(self):
        self.install()
        policy = json.loads(self.read(H.POLICY))
        self.assertEqual(policy['version'], 2)
        self.assertEqual(set(policy['files']), H.pinned_paths(policy))
        for path, value in policy['files'].items():
            self.assertEqual(hashlib.sha256(self.read(path)).hexdigest(), value, path)
        self.assertEqual(policy['nginx']['services'], ['web'])

    def test_a_removed_grant_loses_its_hooks(self):
        self.run_action('apply', token='lcm_example-token', services=['site', 'web'])
        self.assertTrue(self.exists(H.hook_path('site', 'api')))
        put(self.root, str(H.HOOKS / 'site' / 'unrelated.conf'), '# not ours\n')
        result = self.run_action('apply', services=['web'])
        self.assertIn('remove ' + str(H.hook_path('site', 'api')), result['changes'])
        self.assertFalse(self.exists(H.hook_path('site', 'api')))
        self.assertTrue(self.exists(H.HOOKS / 'site' / 'unrelated.conf'))
        self.assertEqual(json.loads(self.read(H.POLICY))['nginx']['services'], ['web'])

    def test_vendor_timers_and_audit_permissions_are_recorded_then_changed(self):
        self.system.vendor = {'dailyaidecheck.timer'}
        self.system.units['dailyaidecheck.timer'] = {'enabled': True, 'active': True}
        self.install()
        self.assertEqual(json.loads(self.read(I.VENDOR_TIMERS_RECORD)), {'dailyaidecheck.timer': {'enabled': True, 'active': True}})
        self.assertFalse(self.system.units['dailyaidecheck.timer']['enabled'])
        self.assertEqual(json.loads(self.read(I.AUDIT_DIR_RECORD))['mode'], 0o700)
        self.assertEqual(self.mode(I.AUDIT_LOG_DIR), 0o750)
        self.assertEqual(self.mode('/var/log/audit/audit.log'), 0o640)
        auditd = self.read(I.AUDITD_CONF).decode()
        self.assertIn('num_logs = 8', auditd)
        self.assertNotIn('num_logs = 5', auditd)

    def test_missing_packages_and_account_come_first(self):
        for tool in I.PACKAGE_TOOLS:
            (self.root / tool.lstrip('/')).unlink()
        (self.root / 'etc/audit/auditd.conf').unlink()
        del self.system.accounts['workstation-security']
        plan = self.run_action('plan')
        self.assertIn('install the packages auditd aide with apt-get', plan['changes'])
        self.assertIn('create the system account workstation-security (no login shell)', plan['changes'])
        self.assertEqual(self.system.mutating(), [])
        result = self.install()
        self.assertEqual(result['changes'][:3], ['install the packages auditd aide with apt-get',
                                                'create the system group workstation-security',
                                                'create the system account workstation-security (no login shell)'])
        self.assertEqual(self.run_action('plan')['changes'], [])

    def test_check_refusals_change_nothing(self):
        cases = {
            'immutable audit': lambda: setattr(self.system, 'audit_enabled', 2),
            'login shell': lambda: self.system.accounts.update({'workstation-security': (COLLECTOR_ID, COLLECTOR_ID, '/bin/bash', ())}),
            'symbolic link': lambda: (self.root / 'etc/nginx/lcm').symlink_to(self.root / 'tmp'),
            'boot rule': lambda: put(self.root, '/etc/audit/rules.d/99-finalize.rules', '-e 2\n'),
        }
        for name, prepare in cases.items():
            with self.subTest(name=name):
                self.setUp()
                prepare()
                before = tree(self.root)
                result = self.run_action('apply', token='lcm_example-token')
                self.assertEqual(len(result['manual']), 1, result)
                self.assertEqual(result['changes'], [])
                self.assertEqual(tree(self.root), before)

    def test_failed_nginx_test_undoes_the_transaction(self):
        put(self.root, str(H.NGINX_CONF), '# hand-made\n')
        self.system.fail = lambda argv: argv == [str(I.NGINX), '-t']
        with self.assertRaises(I.Failure) as error:
            self.install()
        self.assertIn('were undone', str(error.exception))
        self.assertEqual(self.read(H.NGINX_CONF), b'# hand-made\n')
        self.assertFalse(self.exists(I.TOKEN))
        self.assertFalse(self.exists(H.POLICY))
        manifest = json.loads(self.read(I.TRANSACTIONS / I.transactions(self.host)[-1] / 'manifest.json'))
        self.assertEqual(manifest['state'], 'reverted')

    def test_failed_first_install_stops_what_it_started(self):
        # The helper check is the last step: everything before it ran.
        with patch.object(I.Component, 'verify', side_effect=I.Failure('helper unavailable')):
            with self.assertRaises(I.Failure):
                self.install()
        for unit in (I.COLLECTOR, I.AUDIT_TIMER, I.AIDE_TIMER, I.ROTATE_TIMER, 'auditd.service'):
            self.assertEqual(self.system.units[unit], {'enabled': False, 'active': False}, unit)
        self.assertFalse(any('wssec_' in rule for rule in self.system.rules))
        self.assertIn('-a always,exit -F arch=b64 -S execve -k unrelated', self.system.rules)
        self.assertFalse(self.exists(I.SYSTEMD / I.COLLECTOR))

    def test_rollback_restores_watches_and_restarts_the_collector(self):
        self.legacy()
        self.system.rules |= {'-w /etc/passwd -p wa -k wssec_accounts', '-w /opt/old-tool -p wa -k wssec_deploy'}
        put(self.root, str(H.AUDIT_CORE), '-w /etc/passwd -p wa -k wssec_accounts\n-w /opt/old-tool -p wa -k wssec_deploy\n', 0o600)
        before = set(self.system.rules)
        self.system.calls.clear()
        with patch.object(I.Component, 'verify', side_effect=I.Failure('helper unavailable')):
            with self.assertRaises(I.Failure):
                self.run_action('apply')
        self.assertEqual(self.system.rules, before)
        restarts = [argv for argv in self.system.calls if argv[1:] == ['try-restart', I.COLLECTOR]]
        self.assertEqual(len(restarts), 1)
        self.assertEqual(self.read(I.LIB / 'runtime.py') if self.exists(I.LIB / 'runtime.py') else None, None)

    def test_watches_change_without_a_gap(self):
        self.install()
        rules = self.read(H.AUDIT_CORE).decode()
        put(self.root, '/opt/new-tool/x', 'x')
        self.system.calls.clear()
        self.run_action('apply', watch=dict(INPUT['watch'], deploy=INPUT['watch']['deploy'] + ['/opt/new-tool']))
        auditctl = [argv[1:] for argv in self.system.calls if argv[0] == '/usr/sbin/auditctl' and argv[1] in ('-w', '-W')]
        self.assertEqual(auditctl, [['-w', '/opt/new-tool', '-p', 'wa', '-k', 'wssec_deploy']])
        self.assertNotEqual(rules, self.read(H.AUDIT_CORE).decode())

    def test_nginx_changes_and_rollback_hold_the_ingress_lock(self):
        events = []
        real_hold, real_release = I.hold, I.release
        def hold(host, paths, busy, wait=None):
            events.append(('hold', [str(path) for path in paths]))
            return real_hold(host, paths, busy, wait)
        def release(descriptors):
            events.append(('release', len(descriptors)))
            real_release(descriptors)
        self.legacy()
        put(self.root, str(I.LEGACY_HELPER_LOCK), '', 0o600)
        original = self.system.__call__
        def record(argv, timeout=120, stdin=None):
            if argv[:1] == [str(I.NGINX)]:
                events.append(('nginx', argv[1]))
            return original(argv, timeout, stdin)
        self.host.runner = record
        with patch.object(I, 'hold', hold), patch.object(I, 'release', release), \
                patch.object(I.Component, 'verify', side_effect=I.Failure('helper unavailable')), self.assertRaises(I.Failure):
            self.run_action('apply')
        self.assertEqual(events[0], ('hold', [str(I.LEGACY_HELPER_LOCK), str(H.NGINX_LOCK)]))
        # Both nginx tests, the change's and the rollback's, ran while held.
        self.assertEqual([e for e in events if e[0] == 'nginx'], [('nginx', '-t'), ('nginx', '-t')])
        self.assertEqual(events[-1], ('release', 2))
        # The lock files stay usable; nothing else holds them now.
        os.close(I.lock(self.host, H.NGINX_LOCK, 'busy'))

    def test_removal_and_revert_hold_the_ingress_lock(self):
        self.install()
        held = []
        real_hold = I.hold
        def hold(host, paths, busy, wait=None):
            held.append([str(path) for path in paths])
            return real_hold(host, paths, busy, wait)
        with patch.object(I, 'hold', hold):
            self.run_action('remove')
            self.run_action('revert')
        self.assertEqual(held, [[str(H.NGINX_LOCK)], [str(H.NGINX_LOCK)]])
        # The revert of a removal enables and starts the units again.
        for unit in (I.COLLECTOR, I.AUDIT_TIMER, I.AIDE_TIMER, I.ROTATE_TIMER):
            self.assertEqual(self.system.units[unit], {'enabled': True, 'active': True}, unit)
        self.assertTrue(self.exists(H.NGINX_CONF))

    def test_transactions_are_ordered_by_sequence_not_by_id(self):
        # Two transactions of one second differ only in random digits: make
        # the removal's ID sort first and check that revert still takes it.
        self.install()
        self.run_action('remove')
        first, second = I.transactions(self.host)
        for old, new in ((second, '20000101T000000Z-00000000'), (first, '20991231T235959Z-ffffffff')):
            directory = self.root / str(I.TRANSACTIONS).lstrip('/')
            (directory / old).rename(directory / new)
            manifest = json.loads((directory / new / 'manifest.json').read_text())
            manifest['id'] = new
            (directory / new / 'manifest.json').write_text(json.dumps(manifest))
        self.assertEqual(I.transactions(self.host), ['20991231T235959Z-ffffffff', '20000101T000000Z-00000000'])
        self.run_action('revert')
        self.assertTrue(self.exists(I.HELPER))
        self.assertEqual(json.loads(self.read(I.TRANSACTIONS / '20000101T000000Z-00000000' / 'manifest.json'))['state'], 'reverted')

    def test_busy_ingress_lock_is_waited_for_then_refused(self):
        self.install()
        descriptor = I.lock(self.host, H.NGINX_LOCK, 'busy')
        try:
            with patch.object(I, 'LOCK_WAIT', 0.6), self.assertRaises(I.Failure) as error:
                self.run_action('remove')
        finally:
            os.close(descriptor)
        self.assertIn('retry', str(error.exception))
        self.assertTrue(self.exists(I.HELPER))

    def test_root_operation_marks_itself_pending(self):
        self.assertEqual((I.PENDING, I.CONTROL_LOCK), (H.PENDING, H.CONTROL_LOCK))
        with I.root_operation(self.host):
            self.assertTrue(self.exists(I.PENDING))
        self.assertFalse(self.exists(I.PENDING))
        held = I.lock(self.host, I.CONTROL_LOCK, 'busy')
        try:
            with patch.object(I, 'LOCK_WAIT', 0.6), self.assertRaises(I.Failure):
                with I.root_operation(self.host):
                    pass
        finally:
            os.close(held)
        self.assertFalse(self.exists(I.PENDING))

    def test_invalid_policy_values_need_review_not_a_crash(self):
        self.install()
        policy = json.loads(self.read(H.POLICY))
        policy['nginx']['limits'][0]['rate'] = '1r/s; include /x'
        put(self.root, str(H.POLICY), json.dumps(policy), 0o600)
        result = self.run_action('plan')
        self.assertEqual(result['changes'], [])
        self.assertIn('values the helper refuses', result['manual'][0])

    def test_old_transactions_are_pruned(self):
        with patch.object(I, 'KEEP_TRANSACTIONS', 2):
            self.install()
            for _ in range(2):
                self.run_action('remove')
                self.run_action('apply', token='lcm_example-token')
        self.assertEqual(len(I.transactions(self.host)), 2)

    def test_aide_skips_ignored_service_trees(self):
        self.run_action('apply', token='lcm_example-token', aideIgnore=['/opt/web'])
        aide = self.read(H.AIDE_CONFIG).decode()
        self.assertNotIn('/opt/web(/|$) SEC', aide)
        self.assertIn('\n!/opt/web(/|$)\n', aide)
        self.assertIn('/etc/web(/|$) SEC', aide)
        self.assertIn('-w /opt/web -p wa -k wssec_deploy', self.read(H.AUDIT_CORE).decode())

    def test_review_reads_only_lcm_tools_without_following_links(self):
        (self.root / 'var/lib/workstation-security').mkdir(parents=True)
        put(self.root, '/usr/local/libexec/lcm/self-install', 'script', 0o755)
        put(self.root, '/usr/local/libexec/lcm-deploy', 'helper', 0o755)
        put(self.root, '/usr/local/libexec/other-tool', 'other', 0o755)
        put(self.root, '/opt/web/releases/1/app.js', 'service code')
        put(self.root, '/root-secret', 'SECRET')
        (self.root / 'usr/local/libexec/lcm/escape').symlink_to(self.root / 'root-secret')
        (self.root / 'usr/local/libexec/lcm/tree').symlink_to(self.root / 'opt/web')
        os.mkfifo(self.root / 'usr/local/libexec/lcm/fifo')
        with patch('builtins.print'):
            I.review_sources(self.host)
        report = json.loads(self.read(I.BASE / 'source-review.json'))
        paths = {tool['path']: tool for tool in report['tools']}
        self.assertIn('/usr/local/libexec/lcm/self-install', paths)
        self.assertIn('/usr/local/libexec/lcm-deploy', paths)
        self.assertEqual(paths['/usr/local/libexec/lcm/escape'], {'path': '/usr/local/libexec/lcm/escape', 'symlink': True})
        self.assertEqual(paths['/usr/local/libexec/lcm/tree'], {'path': '/usr/local/libexec/lcm/tree', 'symlink': True})
        self.assertEqual(paths['/usr/local/libexec/lcm/fifo']['type'], 'not a regular file')
        self.assertFalse(any('other-tool' in path or '/opt/' in path for path in paths))
        self.assertNotIn('SECRET', json.dumps(report))

    def test_inputs_are_validated(self):
        for field, value in (('aideIgnore', ['/opt/../etc']), ('aideIgnore', '/opt/web/releases'), ('machineId', '../x'), ('endpoint', 'http://192.0.2.10:7800/api/v1/hooks/security'),
                             ('services', ['web', 'Bad']), ('services', ['web', 'site']),
                             ('watch', {'probe': ['/opt/x']}), ('watch', {'deploy': ['/opt/../etc']}),
                             ('watch', {'deploy': ['relative']}), ('token', 'two words')):
            with self.subTest(field=field, value=value), self.assertRaises(I.Failure):
                I.validate_inputs(dict(INPUT, **{field: value}), 'apply')

    # -- adoption of the installation this component replaces ---------------------

    def legacy(self):
        """The layout the previous installers left, with example values."""
        files = {
            '/var/lib/workstation-security/aide/aide.db': b'reviewed baseline database',
            '/var/lib/workstation-security/collector/state.json': b'{"version":1}\n',
            '/var/lib/workstation-security/reports/aide-status.json': b'{"result":"clean"}\n',
            '/var/lib/lcm-security-host/operations/request-a.json': b'{"journal":true}\n',
            '/var/lib/lcm-security-host/approvals/request-a.json': b'{"receipt":true}\n',
            '/var/lib/workstation-security-nginx-20261101/logrotate.status': b'logrotate state - version 2\n',
            str(I.TOKEN): b'lcm_adopted-token\n',
            str(I.COLLECTOR_CONFIG): json.dumps({'machineId': 'machine-a', 'tokenFile': str(I.TOKEN)}).encode(),
            str(I.SYSTEMD / I.COLLECTOR): b'[Service]\nExecStart=old\n',
            str(I.SYSTEMD / I.ROTATE_SERVICE): b'ExecStart=/usr/sbin/logrotate --state /var/lib/workstation-security-nginx-20261101/logrotate.status\n',
            str(H.AIDE_DROPIN): b'[Timer]\nOnCalendar=\nOnCalendar=*-*-* 09:35:00\n',
            str(H.NGINX_CONF): ''.join(
                'limit_req_zone $ws_security_{0}_key zone=ws_security_{0}:2m rate={1};\n'.format(c, r)
                for c, r in (('auth', '60r/m'), ('api', '20r/s'), ('upload', '10r/m'), ('socket', '5r/s'))).encode(),
            str(I.LEGACY_HOOKS / 'card-api-10-observation.conf'): ''.join(
                'limit_req zone=ws_security_{0} burst=7 nodelay;\nlimit_conn ws_security_{0}_conn 8;\n'.format(c)
                for c in ('auth', 'api', 'upload')).encode(),
            str(I.LEGACY_HOOKS / 'card-socket-10-observation.conf'):
                b'limit_req zone=ws_security_socket burst=9 nodelay;\nlimit_conn ws_security_socket_conn 10;\n',
        }
        policy = {'version': 1, 'machineId': 'machine-a', 'files': {'/etc/nginx/snippets/other.conf': '0' * 64},
                  'optionalFiles': {str(H.AIDE_DROPIN): hashlib.sha256(files[str(H.AIDE_DROPIN)]).hexdigest()},
                  'schedule': {'hour': 9, 'minute': 35},
                  'profiles': {'aide': {'reviewed-baseline': {'enabled': True, 'required': True}},
                               'audit': {'core': {'enabled': True, 'required': True},
                                         'hosts': {'enabled': False, 'required': False,
                                                   'content': '-w /etc/hosts -p wa -k wssec_optional_hosts\n'}}}}
        files[str(H.POLICY)] = json.dumps(policy).encode()
        for path, content in files.items():
            put(self.root, path, content, 0o640 if path == str(I.TOKEN) else 0o600)
        os.chmod(self.root / str(I.LEGACY_HOOKS).lstrip('/'), 0o700)
        for unit in (I.COLLECTOR, I.AUDIT_TIMER, I.AIDE_TIMER, I.ROTATE_TIMER, 'auditd.service'):
            self.system.units[unit] = {'enabled': True, 'active': True}
        return files

    def test_adoption_keeps_baseline_state_token_and_approvals(self):
        files = self.legacy()
        result = self.run_action('apply')
        self.assertEqual(result['manual'], [])
        self.assertFalse(result['tokenMissing'])
        for path in ('/var/lib/workstation-security/aide/aide.db', '/var/lib/workstation-security/collector/state.json',
                     '/var/lib/workstation-security/reports/aide-status.json',
                     '/var/lib/lcm-security-host/operations/request-a.json', '/var/lib/lcm-security-host/approvals/request-a.json',
                     str(I.TOKEN), str(H.AIDE_DROPIN)):
            self.assertEqual(self.read(path), files[path], path)
        self.assertEqual(self.mode(I.TOKEN), 0o600)
        self.assertFalse(self.exists(I.LEGACY_HOOKS))
        self.assertEqual(self.read(I.LOGROTATE_DIR / 'status'), b'logrotate state - version 2\n')
        self.assertIn(b'/var/lib/workstation-security/logrotate/status', self.read(I.SYSTEMD / I.ROTATE_SERVICE))
        policy = json.loads(self.read(H.POLICY))
        self.assertEqual(policy['version'], 2)
        self.assertEqual(policy['schedule'], {'hour': 9, 'minute': 35})
        self.assertIn('hosts', policy['profiles']['audit'])
        self.assertEqual(policy['optionalFiles'], {str(H.AIDE_DROPIN): hashlib.sha256(files[str(H.AIDE_DROPIN)]).hexdigest()})
        self.assertEqual({item['category']: (item['rate'], item['burst'], item['connections']) for item in policy['nginx']['limits']},
                         {'auth': ('60r/m', 7, 8), 'api': ('20r/s', 7, 8), 'upload': ('10r/m', 7, 8), 'socket': ('5r/s', 9, 10)})
        self.assertIn('convert {} to version 2'.format(H.POLICY), result['changes'])
        # Every state between two Nginx steps passes nginx -t: the replaced
        # hooks go before the configuration that defined their zones, the new
        # configuration before the hooks that use its zones.
        manifest = json.loads(self.read(I.TRANSACTIONS / result['transaction'] / 'manifest.json'))
        order = [entry['path'] for entry in manifest['entries']]
        self.assertLess(order.index(str(I.LEGACY_HOOKS / 'card-api-10-observation.conf')), order.index(str(H.NGINX_CONF)))
        self.assertLess(order.index(str(H.NGINX_CONF)), order.index(str(H.hook_path('web', 'api'))))
        self.assertIn(['/usr/bin/systemctl', 'restart', I.COLLECTOR], self.system.calls)
        self.assertEqual(self.run_action('plan')['changes'], [])

    def test_adoption_is_reverted_from_its_manifest(self):
        files = self.legacy()
        before = tree(self.root)
        self.run_action('apply')
        plan = self.run_action('revert-plan')
        self.assertTrue(plan['changes'])
        self.assertEqual(self.run_action('revert')['manual'], [])
        after = tree(self.root)
        for path in files:
            key = path.lstrip('/')
            self.assertEqual(after.get(key), before[key], path)
        # Only records, data and lock files stay: the transaction, copied
        # rotation state, reports and recorded permissions.
        added = {path for path in set(after) - set(before)
                 if not path.startswith('var/lib/workstation-security/backups')}
        self.assertTrue(all(path.startswith(('var/lib/workstation-security/', 'var/log/nginx/', 'run/')) for path in added), added)
        self.assertEqual(self.run_action('revert-plan')['changes'], [])

    def test_revert_refuses_files_changed_since(self):
        self.legacy()
        self.run_action('apply')
        put(self.root, str(H.NGINX_CONF), '# edited later\n')
        plan = self.run_action('revert')
        self.assertEqual(plan['manual'], ['{} changed after transaction {}; review it by hand'.format(
            H.NGINX_CONF, I.transactions(self.host)[-1])])
        self.assertEqual(self.read(H.NGINX_CONF), b'# edited later\n')

    def test_adoption_refuses_another_machine(self):
        self.legacy()
        policy = json.loads(self.read(H.POLICY))
        policy['machineId'] = 'machine-b'
        put(self.root, str(H.POLICY), json.dumps(policy), 0o600)
        before = tree(self.root)
        result = self.run_action('apply')
        self.assertIn("Machine 'machine-b'", result['manual'][0])
        self.assertEqual(tree(self.root), before)

    def test_adoption_refuses_unknown_legacy_hooks(self):
        self.legacy()
        put(self.root, str(I.LEGACY_HOOKS / 'other.conf'), '# by hand\n')
        result = self.run_action('apply')
        self.assertIn('other.conf', result['manual'][0])

    # -- removal and cleanup ------------------------------------------------------------

    def test_removal_keeps_baseline_reports_and_journal(self):
        self.system.vendor = {'dailyaidecheck.timer'}
        self.system.units['dailyaidecheck.timer'] = {'enabled': True, 'active': True}
        self.install()
        put(self.root, '/var/lib/workstation-security/aide/aide.db', 'baseline')
        put(self.root, '/var/lib/lcm-security-host/operations/request-a.json', '{}')
        plan = self.run_action('remove-plan')
        self.assertIn('remove ' + str(I.HELPER), plan['changes'])
        result = self.run_action('remove')
        for path in (I.HELPER, I.LIB / 'runtime.py', I.TOKEN, H.AUDIT_CORE, H.NGINX_CONF, H.hook_path('web', 'api'),
                     H.POLICY, I.SYSTEMD / I.COLLECTOR):
            self.assertFalse(self.exists(path), path)
        for path in ('/var/lib/workstation-security/aide/aide.db', '/var/lib/lcm-security-host/operations/request-a.json',
                     str(I.BASE / 'backups' / 'policy-before-removal.json')):
            self.assertTrue(self.exists(path), path)
        self.assertTrue(self.system.units['dailyaidecheck.timer']['enabled'])
        self.assertFalse(any('wssec_' in rule for rule in self.system.rules))
        self.assertIn('-a always,exit -F arch=b64 -S execve -k unrelated', self.system.rules)
        self.assertEqual(self.mode(I.AUDIT_LOG_DIR), 0o700)
        self.assertTrue(result['notes'])
        self.assertEqual(self.run_action('remove-plan')['changes'], [])

    def leftovers(self):
        for directory in I.LEFTOVER_DIRS:
            put(self.root, directory + '/state.json', '{}')
        for unit in I.LEFTOVER_UNITS:
            put(self.root, str(I.SYSTEMD / unit), 'ExecStart=/var/lib/workstation-security-ssh-auth-20261029/runner.py\n')
        put(self.root, '/var/lib/lcm-security-other/x', 'x')

    def test_cleanup_removes_only_the_known_leftovers(self):
        self.leftovers()
        put(self.root, str(I.SYSTEMD / I.ROTATE_SERVICE), 'ReadWritePaths=/var/lib/workstation-security-nginx-20261101\n')
        before = tree(self.root)
        plan = self.run_action('cleanup-plan')
        self.assertEqual(tree(self.root), before)
        self.assertEqual(plan['manual'], ['/var/lib/workstation-security-nginx-20261101 is still named by {}; '
                                          'run "lcm host apply" first'.format(I.SYSTEMD / I.ROTATE_SERVICE)])
        self.assertIn('/var/lib/lcm-security-other is not a known leftover and was left alone; review it by hand',
                      plan['notes'])
        self.assertEqual(self.run_action('cleanup')['changes'], [])
        (self.root / str(I.SYSTEMD / I.ROTATE_SERVICE).lstrip('/')).unlink()
        result = self.run_action('cleanup')
        self.assertEqual(result['manual'], [])
        for directory in I.LEFTOVER_DIRS:
            self.assertFalse(self.exists(directory), directory)
        for unit in I.LEFTOVER_UNITS:
            self.assertFalse(self.exists(I.SYSTEMD / unit))
        self.assertTrue(self.exists('/var/lib/lcm-security-other/x'))
        self.assertEqual(self.run_action('cleanup-plan')['changes'], [])

    def test_cleanup_never_follows_a_link(self):
        outside = put(self.root, '/srv/keep/file', 'keep')
        (self.root / 'var/lib/workstation-security-20261101').symlink_to(outside.parent)
        plan = self.run_action('cleanup')
        self.assertIn('not a root-owned directory', plan['manual'][0])
        self.assertTrue(outside.exists())
        os.unlink(self.root / 'var/lib/workstation-security-20261101')
        put(self.root, '/var/lib/workstation-security-20261101/link-target', 'x')
        (self.root / 'var/lib/workstation-security-20261101/escape').symlink_to(outside.parent)
        self.run_action('cleanup')
        self.assertTrue(outside.exists())


if __name__ == '__main__':
    unittest.main()
