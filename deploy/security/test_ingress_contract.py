"""The security component's Nginx files against the sites the ingress
component renders: internal/ingress/testdata holds its golden files, which
its own tests keep equal to what it renders."""
import fnmatch
import importlib.util
from pathlib import Path
import re
import shutil
import socket
import subprocess
import tempfile
import unittest
from unittest.mock import patch

HERE = Path(__file__).parent
SITES = sorted((HERE.parent.parent / 'internal' / 'ingress' / 'testdata').glob('*.conf'))


def load(name, file):
    spec = importlib.util.spec_from_file_location(name, HERE / file)
    module = importlib.util.module_from_spec(spec)
    spec.loader.exec_module(module)
    return module


H = load('contract_helper', 'helper.py')
R = load('contract_runtime', 'runtime.py')
INCLUDE = re.compile(r'include "([^"]+)";')
NGINX = Path('/usr/sbin/nginx')


def hooks_of(site: str) -> list[tuple[str, str, str]]:
    """(directory, service, category glob) of every hook include."""
    out = []
    for path in INCLUDE.findall(site):
        match = re.fullmatch(r'(/[a-z/]+)/([a-z][a-z0-9-]*)/([a-z]+)-\*\.conf', path)
        if match is None:
            raise AssertionError('unexpected include ' + path)
        out.append(match.groups())
    return out


def free_port() -> int:
    with socket.socket() as probe:
        probe.bind(('127.0.0.1', 0))
        return probe.getsockname()[1]


def listen_on_loopback(site: str) -> str:
    """nginx -t binds the listen sockets: move them to free loopback ports,
    one listen per server and port."""
    ports = {'80': free_port(), '443': free_port()}
    out, seen = [], set()
    for line in site.splitlines():
        match = re.fullmatch(r'(\s*)listen \S*?(80|443)( ssl)?;', line)
        if match is None:
            if line.strip() == 'server {':
                seen = set()
            out.append(line)
            continue
        if match[2] not in seen:
            seen.add(match[2])
            out.append('{}listen 127.0.0.1:{}{};'.format(match[1], ports[match[2]], match[3] or ''))
    return '\n'.join(out) + '\n'


class IngressContractTests(unittest.TestCase):
    def test_golden_sites_exist(self):
        self.assertTrue(SITES, 'no ingress golden files found')

    def test_hook_includes_match_the_rendered_hook_files(self):
        for site_path in SITES:
            site = site_path.read_text()
            with self.subTest(site=site_path.name):
                for directory, service, category in hooks_of(site):
                    self.assertEqual(directory, str(H.HOOKS))
                    self.assertIn(category, H.CATEGORIES + ('health', 'static'))
                    if category in H.CATEGORIES:
                        name = H.hook_path(service, category).name
                        self.assertTrue(fnmatch.fnmatch(name, category + '-*.conf'), name)

    def test_every_server_and_hooked_location_sets_both_variables(self):
        for site_path in SITES:
            site = site_path.read_text()
            with self.subTest(site=site_path.name):
                servers = site.count('server {')
                self.assertGreater(servers, 0)
                self.assertEqual(site.count('set $lcm_service ""'), servers)
                self.assertEqual(site.count('set $lcm_category ""'), servers)
                for block in re.findall(r'location [^{]*\{[^}]*\}', site):
                    if 'include "' in block:
                        self.assertIn('set $lcm_service "', block)
                        self.assertIn('set $lcm_category "', block)
                categories = set(re.findall(r'set \$lcm_category "([a-z]*)";', site)) - {''}
                # Every category the collector receives is one LCM stores.
                self.assertLessEqual(categories - {'health'}, R.TRAFFIC)

    @unittest.skipUnless(NGINX.exists() and shutil.which('openssl'), 'nginx and openssl are needed')
    def test_sites_and_security_files_pass_nginx_together(self):
        with tempfile.TemporaryDirectory(prefix='lcm-security-contract-') as temporary:
            root = Path(temporary)
            subprocess.run(['openssl', 'req', '-x509', '-newkey', 'ec', '-pkeyopt', 'ec_paramgen_curve:prime256v1',
                            '-nodes', '-keyout', str(root / 'key.pem'), '-out', str(root / 'cert.pem'), '-days', '1',
                            '-subj', '/CN=example.org'], check=True, capture_output=True)
            (root / 'logs').mkdir()
            for site_path in SITES:
                site = site_path.read_text()
                with self.subTest(site=site_path.name):
                    hooks = root / site_path.stem / 'hooks'
                    services = sorted({service for _, service, _ in hooks_of(site)})
                    with patch.object(H, 'HOOKS', hooks):
                        files = H.nginx_files({'services': services, 'limits': [dict(i) for i in H.DEFAULT_LIMITS]})
                    for path, data in files.items():
                        if path == H.NGINX_CONF:
                            data = data.replace(H.NGINX_LOG.encode(), str(root / 'logs' / 'security.jsonl').encode())
                            path = root / site_path.stem / 'security.conf'
                        path.parent.mkdir(parents=True, exist_ok=True)
                        path.write_bytes(data)
                    site = site.replace(str(H.HOOKS), str(hooks))
                    site = re.sub(r'ssl_certificate "[^"]+";', 'ssl_certificate "{}";'.format(root / 'cert.pem'), site)
                    site = re.sub(r'ssl_certificate_key "[^"]+";', 'ssl_certificate_key "{}";'.format(root / 'key.pem'), site)
                    site = listen_on_loopback(site)
                    (root / site_path.stem / 'site.conf').write_text(site)
                    conf = root / site_path.stem / 'nginx.conf'
                    temp = ' '.join('{}_temp_path {};'.format(kind, root / kind) for kind in
                                    ('client_body', 'proxy', 'fastcgi', 'uwsgi', 'scgi'))
                    conf.write_text('pid {0}/nginx.pid;\nerror_log {0}/logs/error.log;\nevents {{}}\nhttp {{\n'
                                    '{1}\naccess_log off;\ninclude {2}/security.conf;\ninclude {2}/site.conf;\n}}\n'
                                    .format(root, temp, root / site_path.stem))
                    result = subprocess.run([str(NGINX), '-t', '-p', str(root), '-c', str(conf), '-e',
                                             str(root / 'logs' / 'error.log')], capture_output=True, text=True)
                    self.assertEqual(result.returncode, 0, result.stderr)


if __name__ == '__main__':
    unittest.main()
