import json
from pathlib import Path
import re
import unittest

ROOT = Path(__file__).resolve().parents[2]


class ProductionConfigurationTests(unittest.TestCase):
    def setUp(self):
        self.compose = json.loads((ROOT / 'compose.prod.yaml').read_text())

    def test_database_application_and_ops_are_separate_and_same_digest(self):
        services = self.compose['services']
        self.assertEqual(services['app']['image'], services['migrate']['image'])
        self.assertEqual(services['app']['image'], services['bootstrap']['image'])
        self.assertEqual(services['migrate']['command'], ['/app/bin/migrate'])
        self.assertEqual(services['bootstrap']['command'], ['/app/bin/bootstrap'])
        self.assertEqual(services['migrate']['profiles'], ['ops'])
        self.assertIn('MIGRATION_DATABASE_URL', services['migrate']['environment'])
        for key in ['MIGRATION_DATABASE_URL', 'POSTGRES_PASSWORD', 'DORIS_ROOT_PASSWORD', 'BOOTSTRAP_ADMIN_FILE']:
            self.assertNotIn(key, services['app']['environment'])
        self.assertNotIn('env_file', services['app'])

    def test_only_web_is_published_and_all_runtime_services_recover(self):
        for name, service in self.compose['services'].items():
            self.assertNotIn('build', service)
            self.assertNotIn('container_name', service)
            if name == 'app':
                self.assertEqual(service['ports'], ['127.0.0.1:${APP_PORT:?application port required}:8080'])
            else:
                self.assertNotIn('ports', service)
            if name not in ['migrate', 'bootstrap', 'flink-state-init']:
                self.assertEqual(service['restart'], 'unless-stopped')
        self.assertEqual(self.compose['services']['flink-state-init']['restart'], 'no')

    def test_prefix_cookie_and_internal_bridge_boundaries(self):
        app = self.compose['services']['app']
        self.assertEqual(app['environment']['PORT'], '8080')
        self.assertIn('BASE_PATH', app['environment'])
        self.assertIn('TRUSTED_PROXY_IPS', app['environment'])
        nginx = (ROOT / 'nginx/business.conf.example').read_text()
        self.assertIn('location ^~ /datamesh/internal/', nginx)
        self.assertIn('proxy_ssl_verify on;', nginx)
        self.assertNotRegex(nginx, r'^\s*(?:server|listen|server_name)\s', 'Business snippet must not replace shared servers')
        for name in ['taskmanager', 'sql-gateway']:
            self.assertEqual(self.compose['services'][name]['environment']['GOVERNANCE_STREAM_SCRIPT_ORIGIN'], 'https://app:8080')

    def test_new_roles_are_restricted_and_passwords_are_not_cli_arguments(self):
        script = ROOT / 'deploy/production/postgres/01-roles.sh'
        self.assertTrue(script.stat().st_mode & 0o111)
        text = script.read_text()
        self.assertEqual(text.count('NOSUPERUSER NOCREATEDB NOCREATEROLE'), 2)
        self.assertNotIn('--set=app_password', text)
        self.assertIn('\\getenv application_password PG_APP_PASSWORD', text)

    def test_external_actions_use_full_commit_pins(self):
        actions = []
        for file in (ROOT / '.github/workflows').glob('*.yml'):
            for reference in re.findall(r'uses:\s*([^\s]+)', file.read_text()):
                if not reference.startswith('./'):
                    self.assertRegex(reference, r'^[^@]+@[a-f0-9]{40}$')
                    actions.append(reference)
        self.assertGreaterEqual(len(actions), 3)
        self.assertNotIn('docker build', (ROOT / '.github/workflows/release.yml').read_text())


if __name__ == '__main__':
    unittest.main()
