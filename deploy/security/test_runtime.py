"""Collector tests: temporary files only, no host writes or network."""
import importlib.util
import json
import os
from pathlib import Path
import tempfile
import time
from types import SimpleNamespace
import unittest
from unittest import mock

SPEC = importlib.util.spec_from_file_location('security_runtime', Path(__file__).with_name('runtime.py'))
runtime = importlib.util.module_from_spec(SPEC)
SPEC.loader.exec_module(runtime)


def syscall(when, serial, key, auid=1000, comm='vi', exe='/usr/bin/vim.basic', enriched=''):
    """A SYSCALL record in the field order auditd writes."""
    tail = 'key="{}"'.format(key) if key else 'key=(null)'
    line = ('type=SYSCALL msg=audit({}:{}): arch=c000003e syscall=257 success=yes exit=3 a0=ffffff9c items=2 '
            'ppid=1 pid=2 auid={} uid=0 gid=0 euid=0 suid=0 fsuid=0 egid=0 sgid=0 fsgid=0 tty=pts0 ses=3 '
            'comm="{}" exe="{}" subj=unconfined {}').format(when, serial, auid, comm, exe, tail)
    if enriched:
        line += '\x1d' + enriched
    return (line + '\n').encode()


class CollectorTests(unittest.TestCase):
    def setUp(self):
        self.tmp = tempfile.TemporaryDirectory()
        self.addCleanup(self.tmp.cleanup)
        self.root = Path(self.tmp.name)
        self.config = {'machineId': 'machine-a', 'tokenFile': str(self.root / 'token'),
                       'endpoint': 'http://127.0.0.1:8080/api/v1/hooks/security'}
        self.collector = runtime.Collector(self.config, self.root / 'state.json')

    def nginx_row(self, when, **fields):
        row = {'time': runtime.stamp(when), 'status': 200, 'request_time': 0.125, 'request_length': 120,
               'bytes_sent': 300, 'source_ip': 'SECRET-IP'}
        row.update(fields)
        return json.dumps(row).encode()

    def traffic(self):
        return [row['traffic'] for row in self.collector.state['queue'] if row.get('kind') == 'traffic']

    def test_initial_cursor_does_not_replay_history(self):
        log = self.root / 'events.log'
        log.write_bytes(b'old\n')
        self.assertEqual(self.collector.lines('test', log), [])
        with log.open('ab') as out:
            out.write(b'new\n')
        self.assertEqual(self.collector.lines('test', log), [b'new\n'])

    def test_rotation_drains_predecessor_then_successor(self):
        log = self.root / 'events.log'
        log.write_bytes(b'old\n')
        self.collector.lines('test', log)
        with log.open('ab') as out:
            out.write(b'tail\n')
        log.rename(self.root / 'events.log.1')
        log.write_bytes(b'new\n')
        self.assertEqual(self.collector.lines('test', log), [b'tail\n', b'new\n'])
        self.assertEqual(self.collector.lines('test', log), [])

    def test_missing_predecessor_reports_gap(self):
        log = self.root / 'events.log'
        log.write_bytes(b'old\n')
        self.collector.lines('test', log)
        # Keep the old inode alive so inode reuse cannot hide rotation in this fixture.
        with log.open('rb'):
            log.unlink()
            log.write_bytes(b'new\n')
            self.assertEqual(self.collector.lines('test', log), [b'new\n'])
        self.assertTrue(self.collector.state['health']['rotation:test']['problem'])

    def test_partial_line_waits_until_complete(self):
        log = self.root / 'events.log'
        log.touch()
        self.collector.lines('test', log)
        log.write_bytes(b'partial')
        self.assertEqual(self.collector.lines('test', log), [])
        with log.open('ab') as out:
            out.write(b'done\n')
        self.assertEqual(self.collector.lines('test', log), [b'partialdone\n'])

    def test_oversized_line_dropped_not_reparsed(self):
        log = self.root / 'events.log'
        log.touch()
        self.collector.lines('test', log)
        log.write_bytes(b'secret' * 4000 + b'\nclean\n')
        self.assertEqual(self.collector.lines('test', log), [b'clean\n'])
        self.assertNotIn('secret', json.dumps(self.collector.state))

    def test_truncation_reports_gap(self):
        log = self.root / 'events.log'
        log.write_bytes(b'old' * 100 + b'\n')
        self.collector.lines('test', log)
        log.write_bytes(b'new\n')
        self.assertEqual(self.collector.lines('test', log), [b'new\n'])
        self.assertTrue(self.collector.state['health']['truncate:test']['problem'])

    def test_nginx_summary_names_service_and_category_only(self):
        row = self.nginx_row(time.time() - 100, service='web', category='auth', request_limit='REJECTED_DRY_RUN',
                             connection_limit='PASSED', query='SECRET-TOKEN', request='SECRET-URI')
        self.collector.nginx([row])
        events = [r['event'] for r in self.collector.state['queue'] if 'event' in r]
        self.assertEqual(len(events), 1)
        self.assertEqual(events[0]['fingerprint'], 'limit-observation:web:auth')
        self.assertIn('service=web, category=auth', events[0]['summary'])
        self.assertIn('not proof of attack', events[0]['summary'])
        self.assertNotIn('SECRET', json.dumps(self.collector.state['queue']))

    def test_traffic_is_kept_per_service_and_category(self):
        self.collector.state['health']['input:nginx'] = {'problem': False}
        when = time.time() - 300
        lines = [self.nginx_row(when, service=service, category=category)
                 for service in ('web', 'site') for category in ('auth', 'api', 'upload', 'socket', 'static')]
        lines.append(self.nginx_row(when, service='web', category='unknown-category'))
        self.collector.nginx(lines)
        traffic = self.traffic()
        self.assertEqual(len(traffic), 11)
        self.assertEqual({(row['service'], row['category']) for row in traffic},
                         {(s, c) for s in ('web', 'site') for c in ('auth', 'api', 'upload', 'socket', 'static', 'other')} - {('site', 'other')})
        self.assertTrue(all(row['requests'] == 1 and row['requestTimeSumMs'] == 125 for row in traffic))
        self.assertNotIn('SECRET', json.dumps(traffic))

    def test_health_and_unlabelled_requests_are_not_counted(self):
        self.collector.state['health']['input:nginx'] = {'problem': False}
        when = time.time() - 300
        self.collector.nginx([self.nginx_row(when, service='web', category='health'),
                              self.nginx_row(when, service='', category=''),
                              self.nginx_row(when, category='api', service='Bad Name;')])
        traffic = self.traffic()
        self.assertEqual(len(traffic), 1)
        self.assertNotIn('service', traffic[0])
        self.assertEqual(traffic[0]['category'], 'api')

    def test_traffic_window_does_not_finalize_backlog_or_mutate_late(self):
        self.collector.state['health']['input:nginx'] = {'problem': False}
        self.collector.state['inputRemaining'] = {'nginx': 100}
        row = self.nginx_row(time.time() - 300, service='web', category='static')
        self.collector.nginx([row])
        self.assertEqual(self.traffic(), [])
        self.collector.state['inputRemaining']['nginx'] = 0
        self.collector.nginx([])
        self.collector.nginx([row])
        self.assertEqual(len(self.traffic()), 1)
        self.assertEqual(self.traffic()[0]['requests'], 1)
        self.assertTrue(self.collector.state['health']['traffic:late']['problem'])

    def test_state_of_the_previous_version_is_still_read(self):
        # The adopted collector state keys windows by category alone.
        self.collector.state['health']['input:nginx'] = {'problem': False}
        bucket = int((time.time() - 600) // 60) * 60
        self.collector.state['traffic'] = {f'{bucket}:api': {
            'machineId': 'machine-a', 'windowStart': runtime.stamp(bucket), 'windowEnd': runtime.stamp(bucket + 60),
            'category': 'api', 'requests': 2, 'status429': 0, 'status5xx': 0, 'dryRunHits': 0,
            'requestTimeSumMs': 2.0, 'requestTimeMaxMs': 1.0, 'responseBytes': 1, 'requestBytes': 1}}
        self.collector.state['trafficFinalizedUntil'] = {'api': bucket}
        self.collector.nginx([])
        self.assertEqual([row['category'] for row in self.traffic()], ['api'])

    def audit_record(self, serial, key, when=1793500000.123, auid=1000):
        return [syscall(when, serial, key, auid=auid),
                f'type=PATH msg=audit({when}:{serial}): name="SECRET-PATH"\n'.encode(),
                f'type=EOE msg=audit({when}:{serial}):\n'.encode()]

    def test_audit_records_are_summarized_per_minute_without_details(self):
        lines = []
        for serial in range(2000):
            lines += self.audit_record(serial, 'wssec_deploy')
        lines += self.audit_record(9999, 'wssec_ssh')
        self.collector.audit(lines)
        self.assertEqual(self.collector.state['queue'], [])
        self.collector.now += 60
        self.collector.audit([])
        events = [row['event'] for row in self.collector.state['queue']]
        self.assertEqual(sorted(e['fingerprint'] for e in events), ['change:deploy', 'change:ssh'])
        deploy = next(e for e in events if e['fingerprint'] == 'change:deploy')
        self.assertIn('records=2000 in one minute', deploy['summary'])
        self.assertIn('loginUid=1000', deploy['summary'])
        self.assertTrue(deploy['historical'])
        self.assertNotIn('SECRET', json.dumps(self.collector.state))

    def test_forged_command_names_cannot_change_the_category_or_identity(self):
        lines = []
        # The real key is ssh; comm and exe name a key and fields of their own.
        lines.append(syscall(1793500000.1, 1, 'wssec_ssh', comm='key=wssec_zz auid=0', exe='/tmp/key="wssec_probe" euid=0'))
        lines.append(f'type=EOE msg=audit(1793500000.1:1):\n'.encode())
        # No key at all, with a forged one in comm: not a watched change.
        lines.append(syscall(1793500000.2, 2, None, comm='key="wssec_accounts"'))
        lines.append(f'type=EOE msg=audit(1793500000.2:2):\n'.encode())
        # The enriched format appends interpreted fields after \x1d.
        lines.append(syscall(1793500000.3, 3, 'wssec_cron', enriched=' key="wssec_sudoers" AUID="alice"'))
        lines.append(f'type=EOE msg=audit(1793500000.3:3):\n'.encode())
        self.collector.audit(lines)
        self.collector.now += 60
        self.collector.audit([])
        summaries = sorted(row['event']['summary'] for row in self.collector.state['queue'])
        self.assertEqual(len(summaries), 2)
        self.assertIn('category=cron, loginUid=1000', summaries[0])
        self.assertIn('category=ssh, loginUid=1000, effectiveUid=0', summaries[1])
        self.assertNotIn('zz', json.dumps(self.collector.state))

    def test_hex_encoded_key_is_decoded(self):
        fields, _ = runtime.syscall_fields('type=SYSCALL msg=audit(1.0:1): arch=c000003e syscall=257 auid=1 comm="x" key=(null)')
        self.assertEqual(fields['auid'], '1')
        self.assertIsNone(runtime.syscall_fields('type=SYSCALL msg=audit(1.0:1): comm="x" key=(null)')[1])
        self.assertEqual(runtime.syscall_fields('type=SYSCALL msg=audit(1.0:1): comm="x" key=' + b'wssec_ssh'.hex().upper())[1], 'wssec_ssh')

    def test_any_malformed_nginx_line_is_counted_not_fatal(self):
        when = time.time() - 100
        good = self.nginx_row(when, service='web', category='api', status=500)
        lines = [b'[1]\n', b'"x"\n', b'null\n', b'[' * 5000 + b']' * 5000 + b'\n', b'{"time": 5}\n',
                 json.dumps({'time': {'a': 1}, 'category': 'api'}).encode(), b'\xff\xfe\n',
                 b'{' * 3000 + b'\n', b'x' * (runtime.MAX_LINE + 10) + b'\n', good]
        self.collector.nginx(lines)
        self.assertEqual(self.collector.state['nginxDiscarded'], 9)
        self.assertTrue(self.collector.state['health']['parse:nginx']['problem'])
        events = [row['event'] for row in self.collector.state['queue'] if row.get('event', {}).get('source') == 'nginx']
        self.assertEqual([e['fingerprint'] for e in events], ['server-error:web:api'])

    def test_a_bad_nginx_batch_is_checkpointed(self):
        log = self.root / 'nginx.jsonl'
        log.write_bytes(b'')
        reports = self.root / 'reports'
        reports.mkdir()
        config = dict(self.config, nginxLog=str(log), auditLog=str(self.root / 'audit.log'), reportsDir=str(reports))
        (self.root / 'audit.log').write_bytes(b'')
        collector = runtime.Collector(config, self.root / 'poll.json')
        with mock.patch.object(runtime.Collector, 'delivery'):
            collector.poll()
            log.write_bytes(b'[' * 5000 + b'\n' + b'[1]\n')
            collector = runtime.Collector(config, self.root / 'poll.json')
            collector.poll()
        saved = json.loads((self.root / 'poll.json').read_text())
        self.assertEqual(saved['cursors']['nginx']['offset'], log.stat().st_size)

    def test_distinct_services_are_bounded(self):
        self.collector.state['health']['input:nginx'] = {'problem': False}
        when = time.time() - 300
        lines = [self.nginx_row(when, service='svc-{}'.format(i), category='api') for i in range(300)]
        self.collector.nginx(lines)
        services = {row['traffic'].get('service', '') for row in self.collector.state['queue'] if row.get('kind') == 'traffic'}
        self.assertLessEqual(len(services), runtime.MAX_SERVICES + 2)
        self.assertIn('', services)
        self.assertTrue(self.collector.state['health']['traffic:capacity']['problem'])

    def test_nginx_flood_never_evicts_audit_events(self):
        self.collector.enqueue(runtime.event(self.config, 'auditd', 'change:ssh', 'Sensitive change', identity='audit-1'))
        with mock.patch.object(runtime, 'MAX_QUEUE', 20), mock.patch.object(runtime, 'MAX_LOW_QUEUE', 12):
            for i in range(50):
                self.collector.enqueue_traffic({'category': 'api', 'requests': i})
                self.collector.enqueue(runtime.event(self.config, 'nginx', 'server-error:api', 'x', identity='n{}'.format(i)))
            self.collector.enqueue(runtime.event(self.config, 'aide', 'integrity', 'AIDE', identity='aide-1'))
        sources = [row['event']['source'] for row in self.collector.state['queue'] if 'event' in row]
        self.assertIn('auditd', sources)
        self.assertIn('aide', sources)
        self.assertLessEqual(sum(runtime.is_low(row) for row in self.collector.state['queue']), 12)
        self.assertGreater(self.collector.state['dropped'], 0)
        # High priority events are delivered first.
        sent = []
        def opened(request, timeout):
            sent.append(json.loads(request.data).get('source'))
            response = mock.MagicMock()
            response.__enter__.return_value = SimpleNamespace(status=202, read=lambda limit: b'{"accepted":true}')
            return response
        (self.root / 'token').write_text('token')
        opener = mock.Mock()
        opener.open.side_effect = opened
        with mock.patch.object(runtime.urllib.request, 'build_opener', return_value=opener):
            self.collector.delivery()
        self.assertEqual(sent[:2], ['auditd', 'aide'])

    def test_audit_summary_identity_is_stable(self):
        self.collector.audit(self.audit_record(1, 'wssec_accounts'))
        self.collector.now += 60
        self.collector.audit([])
        first = self.collector.state['queue'][0]['event']['eventId']
        again = runtime.Collector(self.config, self.root / 'other.json')
        again.now = self.collector.now - 60
        again.audit(self.audit_record(1, 'wssec_accounts'))
        again.now += 60
        again.audit([])
        self.assertEqual(again.state['queue'][0]['event']['eventId'], first)

    def test_audit_group_capacity_is_bounded(self):
        with mock.patch.object(runtime, 'MAX_AUDIT_GROUPS', 2):
            for uid in range(5):
                self.collector.finish_audit(str(uid), {'category': 'deploy', 'when': 1793500000.0, 'auid': uid})
        self.assertEqual(len(self.collector.state['auditGroups']), 2)
        self.assertTrue(self.collector.state['health']['capacity:audit']['problem'])

    def test_audit_missing_lost_and_backlog_reports(self):
        reports = self.root / 'reports'
        reports.mkdir()
        runtime.atomic_json(reports / 'audit-status.json', {'observedAt': runtime.stamp(), 'enabled': 1,
                            'rulesLoaded': True, 'lost': 8, 'backlog': 90, 'backlogLimit': 100})
        self.collector.reports(reports)
        health = self.collector.state['health']
        self.assertTrue(health['audit:loss']['problem'])
        self.assertTrue(health['audit:backlog']['problem'])
        self.assertTrue(health['missing:aide-status']['problem'])

    def test_aide_failure_and_overdue_are_reported_once(self):
        reports = self.root / 'reports'
        reports.mkdir()
        runtime.atomic_json(reports / 'aide-status.json', {'observedAt': runtime.stamp(time.time() - 40 * 3600),
                            'runId': 'run1', 'result': 'error', 'added': 0, 'removed': 0, 'changed': 0})
        self.collector.reports(reports)
        self.collector.reports(reports)
        aide_rows = [row for row in self.collector.state['queue'] if row['event']['source'] == 'aide']
        self.assertEqual(len(aide_rows), 1)
        self.assertTrue(self.collector.state['health']['stale:aide-status']['problem'])

    def test_queue_is_bounded_and_loss_observable(self):
        with mock.patch.object(runtime, 'MAX_QUEUE', 2):
            for number in range(4):
                self.collector.enqueue(runtime.event(self.config, 'collector', 'test', 'Test summary', identity=str(number)))
        self.assertEqual(len(self.collector.state['queue']), 2)
        self.assertEqual(self.collector.state['dropped'], 2)

    def test_retry_persists_id_and_backoff_without_secret(self):
        (self.root / 'token').write_text('SECRET-INGEST-KEY')
        self.collector.enqueue(runtime.event(self.config, 'collector', 'test', 'Test summary', identity='id'))
        event_id = self.collector.state['queue'][0]['event']['eventId']
        opener = mock.Mock()
        opener.open.side_effect = OSError('connection unavailable')
        with mock.patch.object(runtime.urllib.request, 'build_opener', return_value=opener):
            self.collector.delivery()
        row = self.collector.state['queue'][0]
        self.assertEqual(row['event']['eventId'], event_id)
        self.assertGreater(row['retryAt'], self.collector.now)
        self.assertNotIn('SECRET', json.dumps(self.collector.state))

    def test_permanent_rejection_is_counted_lost_without_blocking_the_queue(self):
        (self.root / 'token').write_text('token')
        for identity in ('refused', 'accepted'):
            self.collector.enqueue(runtime.event(self.config, 'collector', 'test', 'Test summary', identity=identity))
        accepted = mock.MagicMock()
        accepted.__enter__.return_value = SimpleNamespace(status=202, read=lambda limit: b'{"accepted":true}')
        refused = runtime.urllib.error.HTTPError('http://127.0.0.1:8080', 400, 'Bad Request', {}, None)
        opener = mock.Mock()
        opener.open.side_effect = [refused, accepted]
        with mock.patch.object(runtime.urllib.request, 'build_opener', return_value=opener):
            self.collector.delivery()
        self.assertEqual(self.collector.state['queue'], [])
        self.assertEqual(self.collector.state['dropped'], 1)
        self.assertIsNotNone(self.collector.state['lastDelivery'])

    def test_unavailable_ingest_keeps_the_item_for_a_retry(self):
        (self.root / 'token').write_text('token')
        self.collector.enqueue(runtime.event(self.config, 'collector', 'test', 'Test summary', identity='id'))
        opener = mock.Mock()
        opener.open.side_effect = runtime.urllib.error.HTTPError('http://127.0.0.1:8080', 503, 'Unavailable', {}, None)
        with mock.patch.object(runtime.urllib.request, 'build_opener', return_value=opener):
            self.collector.delivery()
        self.assertEqual(len(self.collector.state['queue']), 1)
        self.assertEqual(self.collector.state['dropped'], 0)

    def test_token_comes_from_the_unit_credential(self):
        credentials = self.root / 'credentials'
        credentials.mkdir()
        (credentials / 'ingest-token').write_text('credential-token\n')
        config = dict(self.config, tokenCredential='ingest-token')
        del config['tokenFile']
        with mock.patch.dict(os.environ, {'CREDENTIALS_DIRECTORY': str(credentials)}):
            self.assertEqual(runtime.token_path(config), credentials / 'ingest-token')
        for environment, name in (({}, 'ingest-token'), ({'CREDENTIALS_DIRECTORY': str(credentials)}, '../token')):
            with self.subTest(name=name), mock.patch.dict(os.environ, environment, clear=True), self.assertRaises(OSError):
                runtime.token_path(dict(config, tokenCredential=name))

    def test_missing_credential_is_a_delivery_error(self):
        config = dict(self.config, tokenCredential='ingest-token')
        del config['tokenFile']
        collector = runtime.Collector(config, self.root / 'state2.json')
        with mock.patch.dict(os.environ, {}, clear=True):
            collector.delivery()
        self.assertEqual(collector.state['deliveryError'], 'token unavailable or invalid')

    def test_disallows_nonloopback_delivery(self):
        (self.root / 'token').write_text('token')
        self.config['endpoint'] = 'https://example.org/api/v1/hooks/security'
        with self.assertRaises(ValueError):
            self.collector.delivery()


class RootHelperTests(unittest.TestCase):
    def test_privileged_helpers_reject_collector_owned_ancestor(self):
        def metadata(path):
            return SimpleNamespace(st_mode=0o40700 if path.name == 'collector' else 0o40755,
                                   st_uid=1000 if path.name == 'collector' else 0)
        with mock.patch.object(Path, 'lstat', autospec=True, side_effect=metadata):
            with self.assertRaises(ValueError):
                runtime.trusted_root_path(Path('/var/lib/workstation-security/collector/forged'))

    def test_root_output_rejects_symlink_without_truncating_target(self):
        with tempfile.TemporaryDirectory() as temporary:
            target = Path(temporary) / 'target'
            target.write_bytes(b'protected original')
            output = Path(temporary) / 'output'
            output.symlink_to(target)
            with mock.patch.object(runtime, 'trusted_root_path'):
                with self.assertRaises(OSError):
                    runtime.protected_root_file(output)
            self.assertEqual(target.read_bytes(), b'protected original')

    def test_atomic_json_replaces_symlink_without_following_it(self):
        with tempfile.TemporaryDirectory() as temporary:
            target = Path(temporary) / 'target'
            target.write_bytes(b'protected original')
            output = Path(temporary) / 'output'
            output.symlink_to(target)
            runtime.atomic_json(output, {'sanitized': True})
            self.assertFalse(output.is_symlink())
            self.assertEqual(target.read_bytes(), b'protected original')

    def test_collector_status_only_exports_fixed_fields(self):
        raw = {'queued': 4, 'dropped': 0, 'observedAt': runtime.stamp(), 'lastDelivery': None,
               'deliveryError': 'SECRET-TOKEN', 'auths': {'registry': {'auth': 'SECRET-DOCKER'}},
               'health': {'input:nginx': {'problem': False, 'detail': 'SECRET-LOG'},
                          'SECRET-KEY': {'problem': True}}}
        safe = runtime.sanitized_status(raw)
        self.assertEqual(set(safe), {'queued', 'dropped', 'observedAt', 'lastDelivery', 'deliveryError', 'health'})
        self.assertNotIn('SECRET', json.dumps(safe))
        self.assertEqual(safe['health'], {'input:nginx': {'problem': False}})

    def test_collector_status_rejects_final_symlink(self):
        with tempfile.TemporaryDirectory() as temporary:
            state = Path(temporary) / 'collector'
            state.mkdir(mode=0o700)
            secret = Path(temporary) / 'root-only.json'
            secret.write_text('{"auths":{"registry":{"auth":"SECRET"}}}')
            (state / 'status.json').symlink_to(secret)
            with mock.patch.object(runtime, 'trusted_root_path'):
                with self.assertRaises(OSError):
                    runtime.collector_status(state)

    def test_collector_status_rejects_fifo_without_blocking(self):
        with tempfile.TemporaryDirectory() as temporary:
            state = Path(temporary) / 'collector'
            state.mkdir(mode=0o700)
            os.mkfifo(state / 'status.json', 0o600)
            with mock.patch.object(runtime, 'trusted_root_path'):
                with self.assertRaises(ValueError):
                    runtime.collector_status(state)


if __name__ == '__main__':
    unittest.main()
