import importlib.util
import json
from pathlib import Path
import tempfile
import unittest
from unittest.mock import patch

ROOT = Path(__file__).resolve().parents[2]
spec = importlib.util.spec_from_file_location('dev_env', ROOT / 'scripts/dev_env.py')
dev = importlib.util.module_from_spec(spec)
spec.loader.exec_module(dev)


class MainStreamConfigurationTests(unittest.TestCase):
    def test_main_bridge_reaches_only_trusted_services(self):
        services = json.loads((ROOT / 'compose.yaml').read_text())['services']
        for name, service in services.items():
            mounts = service.get('volumes', [])
            tokens = [mount for mount in mounts if '/stream-secrets/' in mount]
            if name in ['server', 'taskmanager', 'sql-gateway']:
                self.assertEqual(tokens, ['./.local/stream-script/token:/stream-secrets/token:ro'])
                self.assertEqual(service['environment']['GOVERNANCE_STREAM_SCRIPT_TOKEN_FILE'], '/stream-secrets/token')
                if name != 'server':
                    self.assertEqual(service['environment']['GOVERNANCE_STREAM_SCRIPT_ORIGIN'], 'https://server:8443')
                    self.assertIn('./.local/certs/server.crt:/certs/server.crt:ro', mounts)
                    self.assertIn('${LOCAL_GID}', service['group_add'])
            else:
                self.assertEqual(tokens, [])

    def test_main_token_is_private_stable_and_invalid_existing_key_is_rejected(self):
        with tempfile.TemporaryDirectory() as folder, patch.object(dev, 'LOCAL', Path(folder)):
            dev.prepare_stream_script_token()
            token = Path(folder) / 'stream-script/token'
            first = token.read_text()
            self.assertRegex(first, r'^[0-9a-f]{64}$')
            self.assertEqual(token.stat().st_mode & 0o777, 0o640)
            dev.prepare_stream_script_token()
            self.assertEqual(token.read_text(), first)
            token.write_text('invalid')
            with self.assertRaisesRegex(RuntimeError, 'stream script token'):
                dev.prepare_stream_script_token()
            self.assertEqual(token.read_text(), 'invalid')

    def test_main_token_cannot_be_a_symlink(self):
        with tempfile.TemporaryDirectory() as folder, patch.object(dev, 'LOCAL', Path(folder)):
            local = Path(folder)
            (local / 'stream-script').mkdir()
            target = local / 'untouched'
            target.write_text('private')
            (local / 'stream-script/token').symlink_to(target)
            with self.assertRaisesRegex(RuntimeError, 'stream script token'):
                dev.prepare_stream_script_token()
            self.assertEqual(target.read_text(), 'private')
