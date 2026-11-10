"""Privilege-boundary and recovery tests use only temporary files and fake OS calls."""
import copy
import datetime as dt
import hashlib
import importlib.util
import json
import os
from pathlib import Path
import tempfile
import stat
import types
import unittest
from unittest.mock import patch

SPEC = importlib.util.spec_from_file_location('security_host_helper', Path(__file__).with_name('helper.py'))
H = importlib.util.module_from_spec(SPEC)
SPEC.loader.exec_module(H)
ORIGINAL_TRUSTED = H.trusted


class HelperTests(unittest.TestCase):
    def setUp(self):
        self.temporary = tempfile.TemporaryDirectory()
        self.addCleanup(self.temporary.cleanup)
        self.root = Path(self.temporary.name)
        names = ('BASE', 'POLICY', 'LIB', 'REPORTS', 'AIDE_TIMER', 'AIDE_DROPIN', 'AIDE_CONFIG', 'BASELINE', 'AUDIT_OPTIONAL',
                 'AUDIT_CORE', 'NGINX_CONF', 'HOOKS', 'NGINX_LOCK', 'CONTROL_LOCK')
        self.patchers = []
        for name in names:
            value = self.root / str(getattr(H, name)).lstrip('/')
            value.parent.mkdir(parents=True, exist_ok=True)
            self.patchers.append(patch.object(H, name, value))
        for p in self.patchers:
            p.start()
            self.addCleanup(p.stop)
        H.BASE.mkdir(exist_ok=True)
        H.REPORTS.mkdir(exist_ok=True)
        (H.BASE / 'operations').mkdir()
        (H.BASE / 'approvals').mkdir()
        self.addCleanup(patch.stopall)
        patch.object(H, 'trusted', lambda path, missing=False: None).start()
        self.locks = []
        patch.object(H, 'lock_file', self.fake_lock).start()
        self.commands = []
        patch.object(H, 'run', self.fake_run).start()
        nginx = {'services': ['site', 'web'], 'limits': [dict(item) for item in H.DEFAULT_LIMITS]}
        data = {H.AIDE_TIMER: b'[Timer]\nOnCalendar=*-*-* 04:20:00\n', H.AIDE_CONFIG: b'reviewed aide baseline\n',
                H.AUDIT_CORE: b'-w /etc/passwd -p wa -k wssec_accounts\n'}
        data.update(H.nginx_files(nginx))
        for path, value in data.items():
            path.parent.mkdir(parents=True, exist_ok=True)
            path.write_bytes(value)
        self.policy = {'version': 2, 'machineId': 'machine-a', 'files': {str(p): H.digest(v) for p, v in data.items()},
                       'optionalFiles': {}, 'schedule': {'hour': 4, 'minute': 20}, 'nginx': nginx,
                       'profiles': {'aide': {'baseline': {'enabled': True, 'required': True}}, 'audit': {'core': {'enabled': True, 'required': True}}}}
        H.POLICY.write_bytes(H.canonical(self.policy))
        self.active = 'inactive'
        self.fail_command = None
        self.audit_rules = {'-w /etc/passwd -p wa -k wssec_accounts', '-w /etc/ssh -p wa -k wssec_ssh', '-w /etc/sudoers -p wa -k wssec_sudoers', '-a always,exit -F arch=b64 -S execve -k unrelated'}

    def fake_lock(self, path):
        self.locks.append(path)
        return os.open(os.devnull, os.O_RDONLY)

    def fake_run(self, command, timeout=20, account=None):
        self.commands.append((command, account))
        if self.fail_command and self.fail_command(command):
            raise H.Failure()
        if command == ['/usr/sbin/auditctl', '-l']:
            return '\n'.join(sorted(self.audit_rules)) + '\n'
        if command[:2] == ['/usr/sbin/auditctl', '-W']:
            self.audit_rules.discard('-w ' + ' '.join(command[2:]))
            return ''
        if command[:2] == ['/usr/sbin/auditctl', '-w']:
            self.audit_rules.add(' '.join(command[1:]))
            return ''
        if 'ActiveState' in ' '.join(command):
            return 'active\n' if command[2] != 'workstation-security-aide.service' else self.active + '\n'
        if 'TimersCalendar' in ' '.join(command):
            return 'OnCalendar=*-*-* 09:35:00\n'
        if 'collector-status' in command:
            return H.canonical({'queued': 0, 'dropped': 0, 'observedAt': None, 'lastDelivery': None, 'deliveryError': None, 'health': {}}).decode()
        return ''

    def preview(self, action='aide.schedule', settings=None, identity='request-a'):
        controls = H.Controls()
        request = {'version': 1, 'command': 'preview', 'machineId': 'machine-a', 'requestId': identity,
                   'action': action, 'reason': 'Scheduled security maintenance', 'actor': 'user:admin',
                   'expectedDigest': controls.effective, 'settings': settings if settings is not None else {'hour': 9, 'minute': 35}}
        return request, controls.dispatch(request)['preview']

    def apply(self, request, preview):
        return H.Controls().dispatch({'version': 1, 'command': 'apply', 'machineId': 'machine-a',
                                      'requestId': request['requestId'], 'previewHash': preview['previewHash'],
                                      'reason': request['reason'], 'actor': request['actor']})['operation']

    def limits(self):
        return {'limits': [{'category': c, 'rate': '45r/s', 'burst': 333, 'connections': 99} for c in H.CATEGORIES]}

    def nginx_paths(self):
        return [H.NGINX_CONF] + [H.hook_path(s, c) for s in ('site', 'web') for c in H.CATEGORIES]

    def test_status_unknown_reports_remain_null(self):
        status = H.Controls().status()
        self.assertTrue(status['available'])
        self.assertIsNone(status['aide']['result'])
        self.assertIsNone(status['audit']['lost'])
        self.assertFalse(status['nginx']['r11Approved'])
        self.assertTrue(status['nginx']['dryRunOnly'])

    def test_extra_fields_paths_and_actions_are_rejected(self):
        controls = H.Controls()
        with self.assertRaises(H.Failure):
            controls.dispatch({'version': 1, 'command': 'status', 'machineId': 'machine-a', 'path': '/etc/shadow'})
        for action, settings in (('baseline.accept', {}), ('aide.schedule', {'hour': 4, 'minute': 20, 'unit': 'sshd'}),
                                 ('nginx.configure', {'limits': []}), ('aide.schedule', {'hour': True, 'minute': 0})):
            with self.subTest(action=action, settings=settings), self.assertRaises(H.Failure):
                H.typed_settings(action, settings, self.policy)

    def test_profile_required_coverage_cannot_be_removed(self):
        with self.assertRaises(H.Failure):
            H.typed_settings('audit.profile', {'profiles': [{'name': 'core', 'enabled': False}]}, self.policy)
        status = H.Controls().status()
        capability = next(v for v in status['capabilities'] if v['action'] == 'audit.profile')
        self.assertFalse(capability['enabled'])

    def test_same_request_replay_is_stable_and_conflicts_on_changed_intent(self):
        request, preview = self.preview()
        self.assertEqual(H.Controls().dispatch(request)['preview'], preview)
        request['reason'] = 'A different reason'
        with self.assertRaises(H.Failure) as error:
            H.Controls().dispatch(request)
        self.assertEqual(error.exception.code, 'conflict')

    def age_journal(self, identity, state, days):
        path = H.BASE / 'operations' / (identity + '.json')
        journal = json.loads(path.read_bytes())
        then = dt.datetime.now(dt.timezone.utc) - dt.timedelta(days=days)
        journal['operation'].update(state=state, updatedAt=then.isoformat().replace('+00:00', 'Z'))
        journal['preview']['expiresAt'] = (then + dt.timedelta(minutes=10)).isoformat().replace('+00:00', 'Z')
        path.write_bytes(H.canonical(journal))
        os.utime(path, (then.timestamp(), then.timestamp()))

    def test_full_journal_purges_only_old_finished_operations(self):
        kept = {'indeterminate': 200, 'running': 200, 'awaiting-local-approval': 200, 'succeeded': 30}
        purged = {'succeeded': 91, 'failed': 120, 'prepared': 100}
        names = []
        for prefix, states in (('kept-', kept), ('purged-', purged)):
            for state, days in states.items():
                identity = prefix + state
                self.preview(identity=identity)
                self.age_journal(identity, state, days)
                names.append(identity)
        with patch.object(H, 'OPERATION_CAP', len(names)):
            self.preview(identity='request-new')
            remaining = sorted(path.stem for path in (H.BASE / 'operations').iterdir())
            self.assertEqual(remaining, sorted(['kept-' + s for s in kept] + ['request-new']))
        with patch.object(H, 'OPERATION_CAP', len(remaining)), self.assertRaises(H.Failure):
            self.preview(identity='request-blocked')

    def test_preview_requires_current_digest(self):
        request, _ = self.preview()
        request['requestId'] = 'request-b'
        request['expectedDigest'] = '0' * 64
        with self.assertRaises(H.Failure) as error:
            H.Controls().dispatch(request)
        self.assertEqual(error.exception.code, 'conflict')

    def test_apply_rechecks_effective_digest(self):
        request, preview = self.preview()
        H.NGINX_CONF.write_bytes(H.NGINX_CONF.read_bytes() + b'# external edit\n')
        with self.assertRaises(H.Failure) as error:
            self.apply(request, preview)
        self.assertEqual(error.exception.code, 'conflict')
        self.assertFalse(H.AIDE_DROPIN.exists())

    def test_baseline_request_preserves_active_files(self):
        H.BASELINE.write_bytes(b'baseline content')
        before = H.BASELINE.read_bytes()
        request, preview = self.preview('baseline.request', {})
        self.assertTrue(preview['requiresLocalApproval'])
        operation = self.apply(request, preview)
        self.assertEqual(operation['state'], 'awaiting-local-approval')
        self.assertEqual(H.BASELINE.read_bytes(), before)
        self.assertEqual(self.commands.count((['/usr/bin/systemctl', 'start', '--no-block', 'workstation-security-aide.service'], None)), 0)

    def test_schedule_updates_one_timer_and_verifies_readback(self):
        request, preview = self.preview()
        old = H.AIDE_TIMER.read_bytes()
        operation = self.apply(request, preview)
        self.assertEqual(operation['state'], 'succeeded')
        self.assertIn(b'OnCalendar=*-*-* 09:35:00', H.AIDE_DROPIN.read_bytes())
        self.assertEqual(H.AIDE_TIMER.read_bytes(), old)
        self.assertEqual(H.Controls().status()['aide']['schedule'], {'hour': 9, 'minute': 35})

    def test_nginx_rerenders_every_service_hook_in_dry_run(self):
        request, preview = self.preview('nginx.configure', self.limits())
        operation = self.apply(request, preview)
        self.assertEqual(operation['state'], 'succeeded')
        self.assertFalse(H.Controls().drift)
        self.assertEqual(self.locks, [H.NGINX_LOCK])
        for service in ('site', 'web'):
            hook = H.hook_path(service, 'api').read_bytes()
            self.assertIn(b'limit_req_dry_run on;', hook)
            self.assertIn(b'limit_req zone=lcm_security_api burst=333 nodelay;', hook)
        self.assertIn(b'zone=lcm_security_auth:2m rate=45r/s;', H.NGINX_CONF.read_bytes())
        self.assertTrue(all(v['rate'] == '45r/s' for v in H.Controls().limits()))
        self.assertEqual(H.read_json(H.POLICY)['nginx']['limits'], H.valid_limits(self.limits()['limits']))

    def test_ingress_files_never_count_as_drift(self):
        site = H.HOOKS.parent / 'ingress-site.conf'
        site.write_text('server { set $lcm_service web; set $lcm_category api; }\n')
        (H.HOOKS / 'web' / 'notes.txt').write_text('unrelated\n')
        self.assertFalse(H.Controls().drift)

    def test_policy_must_pin_exactly_the_rendered_files(self):
        policy = H.read_json(H.POLICY)
        policy['files']['/etc/nginx/snippets/other.conf'] = '0' * 64
        H.POLICY.write_bytes(H.canonical(policy))
        with self.assertRaises(H.Failure):
            H.Controls()
        policy = copy.deepcopy(self.policy)
        policy['version'] = 1
        H.POLICY.write_bytes(H.canonical(policy))
        with self.assertRaises(H.Failure):
            H.Controls()

    def test_without_nginx_observation_thresholds_are_unavailable(self):
        policy = copy.deepcopy(self.policy)
        policy['nginx'] = None
        policy['files'] = {name: value for name, value in policy['files'].items() if name in H.pinned_paths(policy)}
        H.POLICY.write_bytes(H.canonical(policy))
        status = H.Controls().status()
        self.assertEqual(status['nginx']['limits'], [])
        self.assertIsNone(status['nginx']['mode'])
        capability = next(v for v in status['capabilities'] if v['action'] == 'nginx.configure')
        self.assertFalse(capability['enabled'])

    def test_rendered_nginx_text_takes_only_validated_values(self):
        for bad in ({'category': 'api', 'rate': '1r/s; include /x', 'burst': 1, 'connections': 1},
                    {'category': 'health', 'rate': '1r/s', 'burst': 1, 'connections': 1}):
            limits = [dict(item) for item in H.DEFAULT_LIMITS if item['category'] != bad['category']] + [bad]
            with self.subTest(bad=bad), self.assertRaises(H.Failure):
                H.render_nginx_conf(limits)
        with self.assertRaises(H.Failure):
            H.valid_services(['web', '../etc'])

    def test_nginx_validation_failure_restores_files_and_policy(self):
        before = {p: p.read_bytes() for p in self.nginx_paths() + [H.POLICY]}
        request, preview = self.preview('nginx.configure', self.limits())
        count = [0]
        def fail(command):
            if command == ['/usr/sbin/nginx', '-t']:
                count[0] += 1
                return count[0] == 2
            return False
        self.fail_command = fail
        operation = self.apply(request, preview)
        self.assertEqual(operation['state'], 'failed')
        for path, value in before.items():
            self.assertEqual(path.read_bytes(), value)
        self.assertFalse(H.Controls().drift)

    def test_failed_rollback_is_indeterminate_and_never_replayed(self):
        request, preview = self.preview('nginx.configure', self.limits())
        self.fail_command = lambda command: command == ['/usr/bin/systemctl', 'reload', 'nginx.service']
        operation = self.apply(request, preview)
        self.assertEqual(operation['state'], 'indeterminate')
        before = len(self.commands)
        self.assertEqual(self.apply(request, preview)['state'], 'indeterminate')
        self.assertFalse(any(command[0] == '/usr/sbin/nginx' for command, account in self.commands[before:]))

    def test_deadline_during_step_rolls_back(self):
        alarms = []
        patch.object(H.signal, 'alarm', alarms.append).start()
        previous = H.signal.getsignal(H.signal.SIGALRM)
        self.addCleanup(H.signal.signal, H.signal.SIGALRM, previous)
        H.arm_deadline(H.WORK_SECONDS)
        before = {p: p.read_bytes() for p in self.nginx_paths() + [H.POLICY]}
        request, preview = self.preview('nginx.configure', self.limits())
        fired = [False]
        def expire(command):
            # The first reload stands for a step that is still running when
            # the request deadline passes.
            if command == ['/usr/bin/systemctl', 'reload', 'nginx.service'] and not fired[0]:
                fired[0] = True
                H.signal.getsignal(H.signal.SIGALRM)(H.signal.SIGALRM, None)
            return False
        self.fail_command = expire
        operation = self.apply(request, preview)
        self.assertTrue(fired[0])
        self.assertEqual(operation['state'], 'failed')
        for path, value in before.items():
            self.assertEqual(path.read_bytes(), value)
        self.assertEqual(alarms, [H.WORK_SECONDS, H.ROLLBACK_SECONDS, 0])

    def test_step_timeouts_fit_the_deadlines(self):
        # nginx.configure runs at most four fixed commands and its rollback two.
        self.assertLess(4 * H.STEP_SECONDS, H.WORK_SECONDS)
        self.assertLess(2 * H.STEP_SECONDS, H.ROLLBACK_SECONDS)
        # internal/securityhost/runner.go gives the helper 45 seconds.
        self.assertLess(H.WORK_SECONDS + H.ROLLBACK_SECONDS, 45)

    def test_interrupted_noncheck_mutation_requires_local_review(self):
        request, preview = self.preview()
        path = H.Controls().journal_path(request['requestId'])
        journal = H.read_json(path)
        journal['operation']['state'] = 'running'
        H.atomic(path, H.canonical(journal))
        operation = H.Controls().operation({'requestId': request['requestId']})
        self.assertEqual(operation['state'], 'indeterminate')

    def test_aide_start_is_not_duplicated(self):
        request, preview = self.preview('aide.check', {})
        operation = self.apply(request, preview)
        self.assertEqual(operation['state'], 'running')
        self.active = 'active'
        self.apply(request, preview)
        starts = [command for command, account in self.commands if command[:3] == ['/usr/bin/systemctl', 'start', '--no-block']]
        self.assertEqual(len(starts), 1)

    def test_drift_disables_configuration_without_disabling_baseline_request(self):
        H.NGINX_CONF.write_bytes(H.NGINX_CONF.read_bytes() + b'# drift\n')
        caps = {v['action']: v for v in H.Controls().status()['capabilities']}
        self.assertFalse(caps['nginx.configure']['enabled'])
        self.assertFalse(caps['aide.schedule']['enabled'])
        self.assertTrue(caps['baseline.request']['enabled'])


    def test_aide_completion_requires_report_after_execution_start(self):
        request, preview = self.preview('aide.check', {})
        operation = self.apply(request, preview)
        self.assertEqual(operation['state'], 'running')
        old = {'observedAt': '2020-01-01T00:00:00Z', 'result': 'clean'}
        (H.REPORTS / 'aide-status.json').write_bytes(H.canonical(old))
        operation = H.Controls().operation({'requestId': request['requestId']})
        self.assertEqual(operation['state'], 'indeterminate')

    def test_aide_comparison_changed_is_a_successful_check(self):
        request, preview = self.preview('aide.check', {})
        self.apply(request, preview)
        future = (dt.datetime.now(dt.timezone.utc) + dt.timedelta(seconds=1)).isoformat().replace('+00:00', 'Z')
        (H.REPORTS / 'aide-status.json').write_bytes(H.canonical({'observedAt': future, 'result': 'changed'}))
        operation = H.Controls().operation({'requestId': request['requestId']})
        self.assertEqual(operation['state'], 'succeeded')
        self.assertIn('changed', operation['result'])

    def test_web_cannot_supply_baseline_approval_receipt(self):
        request, preview = self.preview('baseline.request', {})
        self.apply(request, preview)
        with self.assertRaises(H.Failure):
            H.Controls().dispatch({'version': 1, 'command': 'operation', 'machineId': 'machine-a',
                                   'requestId': request['requestId'], 'approval': {'approved': True}})

    def test_root_receipt_tracks_local_baseline_completion(self):
        H.BASELINE.write_bytes(b'locally reviewed baseline')
        request, preview = self.preview('baseline.request', {})
        self.apply(request, preview)
        receipt = {'requestId': request['requestId'], 'previewHash': preview['previewHash'],
                   'approvedAt': H.stamp(), 'baselineDigest': H.digest(H.BASELINE.read_bytes())}
        (H.BASE / 'approvals' / (request['requestId'] + '.json')).write_bytes(H.canonical(receipt))
        operation = H.Controls().operation({'requestId': request['requestId']})
        self.assertEqual(operation['state'], 'succeeded')

    def aide_profile_request(self):
        self.policy['profiles']['aide']['optional'] = {'required': False, 'enabled': False}
        H.POLICY.write_bytes(H.canonical(self.policy))
        H.BASELINE.write_bytes(b'locally reviewed baseline')
        settings = {'profiles': [{'name': 'baseline', 'enabled': True}, {'name': 'optional', 'enabled': True}]}
        request, preview = self.preview('aide.profile', settings)
        self.apply(request, preview)
        receipt = {'requestId': request['requestId'], 'previewHash': preview['previewHash'],
                   'approvedAt': H.stamp(), 'baselineDigest': H.digest(H.BASELINE.read_bytes())}
        (H.BASE / 'approvals' / (request['requestId'] + '.json')).write_bytes(H.canonical(receipt))
        return request

    def test_profile_receipt_without_applied_scope_cannot_complete(self):
        request = self.aide_profile_request()
        operation = H.Controls().operation({'requestId': request['requestId']})
        self.assertEqual(operation['state'], 'awaiting-local-approval')
        self.assertIn('scope review remains required', operation['result'])
        self.assertFalse(H.Controls().profiles['aide']['optional']['enabled'])

    def test_profile_receipt_requires_drift_free_effective_root_policy(self):
        request = self.aide_profile_request()
        self.policy['profiles']['aide']['optional']['enabled'] = True
        H.POLICY.write_bytes(H.canonical(self.policy))
        H.AIDE_CONFIG.write_bytes(H.AIDE_CONFIG.read_bytes() + b'/optional(/|$) SEC\n')
        operation = H.Controls().operation({'requestId': request['requestId']})
        self.assertEqual(operation['state'], 'awaiting-local-approval')
        self.assertTrue(H.Controls().drift)

    def test_profile_receipt_completes_only_after_matching_scope_and_no_drift(self):
        request = self.aide_profile_request()
        self.policy['profiles']['aide']['optional']['enabled'] = True
        H.AIDE_CONFIG.write_bytes(H.AIDE_CONFIG.read_bytes() + b'/optional(/|$) SEC\n')
        self.policy['files'][str(H.AIDE_CONFIG)] = H.digest(H.AIDE_CONFIG.read_bytes())
        H.POLICY.write_bytes(H.canonical(self.policy))
        operation = H.Controls().operation({'requestId': request['requestId']})
        self.assertEqual(operation['state'], 'succeeded')
        self.assertFalse(H.Controls().drift)
        self.assertEqual(operation['effectiveDigest'], H.Controls().effective)

    def test_audit_optional_template_cannot_delete_required_rules(self):
        self.policy['profiles']['audit']['unsafe'] = {'required': False, 'enabled': False, 'content': '-D\n'}
        H.POLICY.write_bytes(H.canonical(self.policy))
        settings = {'profiles': [{'name': 'core', 'enabled': True}, {'name': 'unsafe', 'enabled': True}]}
        request, preview = self.preview('audit.profile', settings)
        operation = self.apply(request, preview)
        self.assertEqual(operation['state'], 'failed')
        self.assertFalse(H.AUDIT_OPTIONAL.exists())


    def test_optional_audit_change_preserves_unrelated_runtime_rules(self):
        self.policy['profiles']['audit']['extra'] = {'required': False, 'enabled': False, 'content': '-w /etc/hosts -p wa -k wssec_optional_hosts\n'}
        H.POLICY.write_bytes(H.canonical(self.policy))
        before = copy.deepcopy(self.audit_rules)
        settings = {'profiles': [{'name': 'core', 'enabled': True}, {'name': 'extra', 'enabled': True}]}
        request, preview = self.preview('audit.profile', settings)
        operation = self.apply(request, preview)
        self.assertEqual(operation['state'], 'succeeded')
        self.assertTrue(before.issubset(self.audit_rules))
        self.assertIn('-w /etc/hosts -p wa -k wssec_optional_hosts', self.audit_rules)
        self.assertFalse(any('--load' in command or '-D' in command for command, _ in self.commands))


    def test_optional_watch_permissions_and_paths_must_be_canonical(self):
        for line in (b'-w /etc/hosts -p aw -k wssec_optional_hosts\n',
                     b'-w /etc/hosts -p wwa -k wssec_optional_hosts\n',
                     b'-w /etc/../etc/hosts -p wa -k wssec_optional_hosts\n'):
            with self.subTest(line=line), self.assertRaises(H.Failure):
                H.optional_rules(line)

    def test_collector_status_refuses_root_identity_drift(self):
        with patch.object(H.pwd, 'getpwnam', return_value=types.SimpleNamespace(pw_uid=0, pw_gid=1234)):
            status = H.Controls().status()
        self.assertFalse(any('collector-status' in command for command, _ in self.commands))
        self.assertIsNone(status['collector']['queued'])


    def test_unrelated_runtime_drift_cannot_claim_verified_rollback(self):
        self.policy['profiles']['audit']['extra'] = {'required': False, 'enabled': False, 'content': '-w /etc/hosts -p wa -k wssec_optional_hosts\n'}
        H.POLICY.write_bytes(H.canonical(self.policy))
        settings = {'profiles': [{'name': 'core', 'enabled': True}, {'name': 'extra', 'enabled': True}]}
        request, preview = self.preview('audit.profile', settings)
        original_run = self.fake_run
        def drift(command, timeout=20, account=None):
            result = original_run(command, timeout, account)
            if command[:2] == ['/usr/sbin/auditctl', '-w']:
                self.audit_rules.discard('-a always,exit -F arch=b64 -S execve -k unrelated')
            return result
        with patch.object(H, 'run', drift):
            operation = self.apply(request, preview)
        self.assertEqual(operation['state'], 'indeterminate')
        journal = H.read_json(H.Controls().journal_path(request['requestId']))
        self.assertFalse(journal['rollbackVerified'])
        self.assertIn('-a always,exit -F arch=b64 -S execve -k unrelated', journal['auditForeignBefore'])


    def test_aide_targets_exposes_only_curated_bounded_paths(self):
        paths = ['/etc/ssh', '/usr/local/libexec/lcm/security-host/helper.py', '/etc/workstation-security/aide.conf']
        config = 'database_in=file:/private/db\nSEC = p+sha256\n' + ''.join(H.re.escape(path) + '(/|$) SEC\n' for path in paths)
        self.assertEqual(H.aide_targets(config.encode()), sorted(paths))
        self.assertIsNone(H.aide_targets(b'/etc/.* SEC\n'))
        self.assertIsNone(H.aide_targets(b'@@include /etc/private.conf\n'))
        self.assertIsNone(H.aide_targets((''.join('/path' + str(i) + '(/|$) SEC\n' for i in range(257))).encode()))

    def test_collector_status_drops_identity(self):
        account = types.SimpleNamespace(pw_uid=1234, pw_gid=1234)
        with patch.object(H.pwd, 'getpwnam', return_value=account):
            status = H.Controls().status()
        reads = [(command, who) for command, who in self.commands if 'collector-status' in command]
        self.assertEqual(reads[-1][1], account)
        self.assertEqual(status['collector']['queued'], 0)



class PrivilegedPathTests(unittest.TestCase):
    def metadata(self, uid=0, mode=stat.S_IFDIR | 0o755, links=1):
        return types.SimpleNamespace(st_uid=uid, st_mode=mode, st_nlink=links, st_size=0)

    def test_ancestor_symlink_writable_and_nonroot_are_rejected(self):
        for bad in (self.metadata(mode=stat.S_IFLNK | 0o777), self.metadata(mode=stat.S_IFDIR | 0o777), self.metadata(uid=1000)):
            with self.subTest(bad=bad):
                def lstat(path):
                    return bad if str(path) == '/etc' else self.metadata()
                with patch.object(H.Path, 'lstat', lstat), self.assertRaises(H.Failure):
                    ORIGINAL_TRUSTED(Path('/etc/lcm/security-host/policy.json'))

    def test_relative_and_parent_paths_are_rejected(self):
        for path in (Path('relative.json'), Path('/etc/../tmp/policy.json')):
            with self.assertRaises(H.Failure):
                ORIGINAL_TRUSTED(path)

    def test_privileged_reader_rejects_nonregular_and_hardlinked_files(self):
        with tempfile.TemporaryDirectory() as temporary:
            path = Path(temporary) / 'file'
            path.write_text('{}')
            for info in (self.metadata(mode=stat.S_IFIFO | 0o600), self.metadata(mode=stat.S_IFREG | 0o600, links=2)):
                with patch.object(H, 'trusted', lambda p: None), patch.object(H.os, 'fstat', return_value=info), self.assertRaises(H.Failure):
                    H.read(path)


if __name__ == '__main__':
    unittest.main()
