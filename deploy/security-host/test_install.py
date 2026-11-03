"""Installation failure tests use temporary destinations and fake subprocesses."""
import hashlib
import importlib.util
import json
from pathlib import Path
import tempfile
import types
import unittest
from unittest.mock import patch

SPEC = importlib.util.spec_from_file_location('security_host_installer', Path(__file__).with_name('install.py'))
I = importlib.util.module_from_spec(SPEC)
SPEC.loader.exec_module(I)


class InstallerTests(unittest.TestCase):
    def setUp(self):
        self.temporary = tempfile.TemporaryDirectory()
        self.addCleanup(self.temporary.cleanup)
        self.root = Path(self.temporary.name)
        for name in ('BASE', 'LIB', 'ETC', 'SUDOERS'):
            value = self.root / str(getattr(I, name)).lstrip('/')
            value.parent.mkdir(parents=True, exist_ok=True)
            p = patch.object(I, name, value)
            p.start()
            self.addCleanup(p.stop)
        fixed = tuple(str(self.root / name.lstrip('/')) for name in I.FIXED)
        p = patch.object(I, 'FIXED', fixed)
        p.start()
        self.addCleanup(p.stop)
        optional = tuple(str(self.root / name.lstrip('/')) for name in I.OPTIONAL)
        p = patch.object(I, 'OPTIONAL', optional)
        p.start()
        self.addCleanup(p.stop)
        for name in fixed:
            path = Path(name)
            path.parent.mkdir(parents=True, exist_ok=True)
            path.write_text('managed configuration\n')
        Path(fixed[0]).write_text('[Timer]\nOnCalendar=*-*-* 04:20:00\n')
        for name in fixed[3:5]:
            Path(name).write_text('limit_req_dry_run on;\nlimit_conn_dry_run on;\n')
        self.digest = hashlib.sha256(Path(__file__).with_name('helper.py').read_bytes()).hexdigest()
        self.patchers = [patch.object(I, 'trusted', lambda path: None),
                         patch.object(I.os, 'geteuid', return_value=0),
                         patch.object(I.pwd, 'getpwnam', return_value=types.SimpleNamespace(pw_uid=1234))]
        for p in self.patchers:
            p.start()
            self.addCleanup(p.stop)
        self.calls = []
        p = patch.object(I.subprocess, 'run', self.command)
        p.start()
        self.addCleanup(p.stop)
        self.fail_status = False

    def command(self, command, **kwargs):
        self.calls.append(command)
        if command[0] == '/usr/bin/python3':
            return types.SimpleNamespace(stdout=json.dumps({'version': 1, 'status': {'available': not self.fail_status}}).encode())
        return types.SimpleNamespace(stdout=b'')

    def test_digest_mismatch_writes_no_grant_or_policy(self):
        with self.assertRaises(ValueError):
            I.install('machine-a', '0' * 64)
        self.assertFalse(I.SUDOERS.exists())
        self.assertFalse((I.ETC / 'policy.json').exists())
        self.assertEqual(self.calls, [])

    def test_existing_policy_refuses_implicit_upgrade(self):
        I.ETC.mkdir()
        (I.ETC / 'policy.json').write_text('{"existing":true}')
        with self.assertRaises(ValueError):
            I.install('machine-a', self.digest)
        self.assertEqual((I.ETC / 'policy.json').read_text(), '{"existing":true}')
        self.assertFalse(I.SUDOERS.exists())

    def test_failed_status_removes_only_new_grant(self):
        self.fail_status = True
        with self.assertRaises(ValueError):
            I.install('machine-a', self.digest)
        self.assertFalse(I.SUDOERS.exists())
        self.assertTrue((I.ETC / 'policy.json').exists())
        self.assertTrue((I.LIB / 'helper.py').exists())

    def test_install_pins_identity_and_required_profiles(self):
        result = I.install('machine-a', self.digest)
        self.assertTrue(result['installed'])
        policy = json.loads((I.ETC / 'policy.json').read_text())
        self.assertEqual(policy['machineId'], 'machine-a')
        self.assertTrue(policy['profiles']['audit']['core']['required'])
        self.assertIn('NOSETENV', I.SUDOERS.read_text())
        self.assertNotIn('*', I.SUDOERS.read_text())
        self.assertTrue(all('systemctl' not in command for command in self.calls))


if __name__ == '__main__':
    unittest.main()
