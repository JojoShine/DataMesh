import importlib.util
import json
from pathlib import Path
import tempfile
import unittest

ROOT = Path(__file__).resolve().parents[2]
spec = importlib.util.spec_from_file_location('experience_env', ROOT / 'scripts/experience_env.py')
experience = importlib.util.module_from_spec(spec)
spec.loader.exec_module(experience)


class ExperienceEnvironmentTests(unittest.TestCase):
    def config(self, root):
        return experience.render_config(ROOT, root, experience.new_environment(), '172.30.239.0/24', mode='full')

    def test_every_service_network_volume_and_port_is_isolated(self):
        with tempfile.TemporaryDirectory() as folder:
            root = Path(folder)
            config = self.config(root)
            self.assertEqual(config['name'], 'datamesh-experience')
            self.assertNotIn('go-tools', config['services'])
            published = [(name, port) for name, service in config['services'].items() for port in service.get('ports', [])]
            self.assertEqual(published, [('ingress', '127.0.0.1:19443:8443')])
            for name, network in config['networks'].items():
                self.assertEqual(network['internal'], name != 'ingress')
                self.assertTrue(network['name'].startswith('datamesh-experience_'))
            for volume in config['volumes'].values():
                self.assertTrue(volume['name'].startswith('datamesh-experience_'))
            for name, service in config['services'].items():
                self.assertIn('mem_limit', service)
                self.assertIn('cpus', service)
                self.assertIn('pids_limit', service)
                for mount in service.get('volumes', []):
                    source = mount.split(':')[0]
                    if source.startswith('/'):
                        self.assertTrue(source.startswith(str(root)) or '/deploy/' in source or source.endswith('/migrations') or source == '/var/run/docker.sock', (name, source))
            server = config['services']['server']
            self.assertEqual(server['environment']['SESSION_COOKIE_NAME'], 'datamesh_experience_session')
            self.assertEqual(server['environment']['PUBLIC_TEST_ACCOUNTS_FILE'], '')
            self.assertEqual(server['environment']['ISOLATED_EXPERIENCE_GUARD'], 'true')
            self.assertNotIn('.local/smoke', json.dumps(config))
            self.assertNotIn('18443', json.dumps(config))
            self.assertEqual(config['services']['code-runner']['environment']['CODE_RUNNER_NAMESPACE'], 'datamesh-experience')
            self.assertEqual(config['services']['doris-fe']['environment']['DORIS_PRIORITY_NETWORKS'], '172.30.239.0/24')
            self.assertEqual(config['services']['server']['environment']['GOVERNANCE_DORIS_ADDRESS'], 'doris-fe:9030')
            self.assertEqual(config['services']['doris-be']['environment']['MASTER_FE_IP'], '172.30.239.10')

    def test_generated_secrets_and_reviewers_are_independent(self):
        first, second = experience.new_environment(), experience.new_environment()
        for key in ['CSRF_KEY', 'CREDENTIAL_KEY', 'POSTGRES_PASSWORD', 'SOURCE_READER_TOKEN', 'SECURITY_WATERMARK_KEYS']:
            self.assertNotEqual(first[key], second[key])
        accounts = experience.new_accounts()
        self.assertGreaterEqual(len(accounts), 8)
        self.assertEqual(len(set(a['username'] for a in accounts.values())), len(accounts))
        self.assertNotEqual(accounts['security_author']['username'], accounts['security_reviewer']['username'])
        self.assertNotEqual(accounts['provider']['username'], accounts['directory_reviewer']['username'])
        for name, account in accounts.items():
            if name in ['security_author', 'security_reviewer']:
                self.assertEqual(account['roles'], ['admin'])
                self.assertEqual(account['platforms'], ['security'])
            else:
                self.assertNotIn('admin', account['roles'])
            self.assertFalse(account.get('public_test', False))

    def test_preflight_reserves_full_suite_not_only_current_usage(self):
        with tempfile.TemporaryDirectory() as folder:
            config = self.config(Path(folder))
        required = experience.required_memory(config)
        self.assertGreater(required, 12 * 1024**3)
        with self.assertRaisesRegex(RuntimeError, 'Insufficient Docker memory'):
            experience.check_capacity(config, 8 * 1024**3, 4 * 1024**3)
        experience.check_capacity(config, 32 * 1024**3, 4 * 1024**3)

    def test_subnet_rejects_main_and_overlapping_existing_networks(self):
        with self.assertRaises(ValueError):
            experience.validate_subnet('172.29.238.0/24', [])
        with self.assertRaises(ValueError):
            experience.validate_subnet('172.30.239.0/24', ['172.30.0.0/16'])
        self.assertEqual(experience.validate_subnet('172.30.239.0/24', []), '172.30.239.0/24')

    def test_unknown_bind_mount_fails_closed(self):
        with tempfile.TemporaryDirectory() as folder:
            root = Path(folder)
            (root / 'compose.yaml').write_text(json.dumps({'services': {'bad': {'volumes': ['./.local/runtime.env:/secret:ro'], 'mem_limit': '1m'}}, 'networks': {}, 'volumes': {}}))
            with self.assertRaisesRegex(ValueError, 'Unapproved bind'):
                experience.render_config(root, root / '.local/experience', experience.new_environment(), '172.30.239.0/24')

class ExperienceBootstrapTests(unittest.TestCase):
    def test_stream_backend_gets_cold_start_grace_but_stops_on_real_failure(self):
        from unittest.mock import patch
        config = experience.render_config(ROOT, Path('/tmp/isolated'), experience.new_environment(), '172.30.239.0/24', features=['code', 'streaming'])
        self.assertEqual(config['services']['kafka']['healthcheck']['start_period'], '120s')
        self.assertEqual(config['services']['connect']['healthcheck']['start_period'], '480s')
        calls = []
        def compose(*args, **kwargs):
            calls.append(args)
            if args[0] == 'up':
                self.assertEqual(args[args.index('--wait-timeout') + 1], '600')
                self.assertIn('--wait', args)
                raise RuntimeError('actual health failure')
        with patch.object(experience.sys, 'argv', ['experience_env.py', 'up']), \
             patch.object(experience, 'preflight'), patch.object(experience, 'save_config', return_value=config), \
             patch.object(experience, 'compose', side_effect=compose):
            with self.assertRaisesRegex(RuntimeError, 'actual health failure'):
                experience.main()
        self.assertEqual([args[0] for args in calls], ['up', 'stop'])

    def test_docker_deadline_allows_compose_cold_start_wait(self):
        from types import SimpleNamespace
        from unittest.mock import patch
        deadlines = []
        def run(*args, **kwargs):
            deadlines.append(kwargs['timeout'])
            return SimpleNamespace(returncode=0, stdout='')
        with patch.object(experience.subprocess, 'run', side_effect=run):
            experience.docker('compose', 'up', '--wait', '--wait-timeout', '600')
            experience.docker('info')
        self.assertEqual(deadlines, [660, 300])

    def test_up_cannot_start_partial_suite_after_failed_preflight(self):
        from unittest.mock import patch
        with patch.object(experience.sys, 'argv', ['experience_env.py', 'up']), \
             patch.object(experience, 'preflight', side_effect=RuntimeError('Insufficient Docker memory')), \
             patch.object(experience, 'compose') as compose:
            with self.assertRaisesRegex(RuntimeError, 'Insufficient Docker memory'):
                experience.main()
            compose.assert_not_called()

    def test_fixture_package_is_synthetic_deterministic_and_bounded(self):
        import sys
        from unittest.mock import patch
        with tempfile.TemporaryDirectory() as folder, patch.object(experience, 'LOCAL', Path(folder)), \
             patch.object(sys, 'path', [str(ROOT / 'scripts'), *sys.path]):
            (Path(folder) / 'samples').mkdir()
            experience.prepare_business_files()
            first = (Path(folder) / 'samples/business-manifest.json').read_text()
            manifest = json.loads(first)
            self.assertTrue(manifest['synthetic'])
            self.assertEqual(len(manifest['tables']), 21)
            self.assertEqual(sum(t['rows'] for t in manifest['tables']), 75300)
            sql = (Path(folder) / 'samples/business-source.sql').read_text()
            self.assertIn('GRANT SELECT ON', sql)
            self.assertNotIn('PASSWORD', sql)
            self.assertEqual((Path(folder) / 'samples/business-source.sql').stat().st_mode & 0o777, 0o600)
            experience.prepare_business_files()
            self.assertEqual((Path(folder) / 'samples/business-manifest.json').read_text(), first)

    def test_verify_never_certifies_missing_real_bootstrap(self):
        from unittest.mock import patch
        with tempfile.TemporaryDirectory() as folder, patch.object(experience, 'LOCAL', Path(folder)):
            with self.assertRaises(FileNotFoundError):
                experience.verify()
            self.assertFalse((Path(folder) / 'verification.json').exists())

    def test_unknown_flink_submission_never_creates_job_binding(self):
        from unittest.mock import patch
        with tempfile.TemporaryDirectory() as folder, patch.object(experience, 'LOCAL', Path(folder)):
            local = Path(folder)
            for name in ['samples', 'bindings']:
                (local / name).mkdir()
            env = experience.new_environment()
            (local / 'credentials.json').write_text(json.dumps(env))
            (local / 'samples/business-manifest.json').write_text(json.dumps({'sha256': 'test', 'tables': []}))
            (local / 'business-source-installed.json').write_text(json.dumps({'sha256': 'test'}))
            def api(service, method, path, body=None):
                if path == '/connectors':
                    return ['datamesh-postgres', 'datamesh-mysql']
                if path.endswith('/config'):
                    kind = 'postgres' if 'postgres' in path else 'mysql'
                    config = json.loads((ROOT / f'configs/connectors/{kind}.json').read_text())
                    config['database.password'] = env['SOURCE_CDC_PASSWORD']
                    return config
                if path.endswith('/status'):
                    return {'connector': {'state': 'RUNNING'}, 'tasks': [{'state': 'RUNNING'}]}
                if path == '/jobs/overview':
                    return {'jobs': []}
                raise AssertionError(path)
            with patch.object(experience, 'component_api', side_effect=api), patch.object(experience, 'compose', return_value='unknown response'):
                with self.assertRaisesRegex(RuntimeError, 'No actual Flink job ID'):
                    experience.bootstrap_pipeline()
            self.assertFalse((local / 'bindings/flink.json').exists())
            self.assertEqual(json.loads((local / 'pipeline-intent.json').read_text())['status'], 'submitting')

    def test_prepare_preserves_private_credentials_and_primary_environment(self):
        import sys
        from unittest.mock import patch
        with tempfile.TemporaryDirectory() as folder:
            root = Path(folder)
            (root / 'compose.yaml').write_text((ROOT / 'compose.yaml').read_text())
            (root / '.local').mkdir()
            primary = root / '.local/runtime.env'
            primary.write_text('PRIMARY_SECRET=never-read-or-copy\n')
            (root / '.local/flink-jars').mkdir()
            (root / '.local/flink-jars/datamesh-stream-script.jar').write_bytes(b'PK-adapter-fixture')
            local = root / '.local/experience'
            with patch.object(experience, 'ROOT', root), patch.object(experience, 'LOCAL', local), \
                 patch.object(sys, 'path', [str(ROOT / 'scripts'), *sys.path]):
                experience.prepare('172.30.239.0/24')
                stream_token = (local / 'stream-script/token').read_text()
                self.assertRegex(stream_token, r'^[0-9a-f]{64}$')
                self.assertNotEqual(stream_token, (local / 'code-runner/token').read_text())
                self.assertEqual((local / 'stream-script/token').stat().st_mode & 0o777, 0o640)
                self.assertEqual((local / 'stream-script').stat().st_mode & 0o777, 0o700)
                self.assertEqual((local / 'flink-jars/datamesh-stream-script.jar').read_bytes(), b'PK-adapter-fixture')
                before = (local / 'credentials.json').read_text()
                account_passwords = {name: account['password'] for name, account in json.loads((local / 'accounts.json').read_text()).items()}
                initial_admin = (local / 'bootstrap-admin.json').read_text()
                deployment = json.loads((local / 'deployment.json').read_text())
                deployment['features'] = ['code']
                (local / 'deployment.json').write_text(json.dumps(deployment))
                experience.prepare('172.30.239.0/24')
                self.assertEqual((local / 'credentials.json').read_text(), before)
                self.assertEqual((local / 'stream-script/token').read_text(), stream_token)
                self.assertEqual({name: account['password'] for name, account in json.loads((local / 'accounts.json').read_text()).items()}, account_passwords)
                self.assertEqual((local / 'bootstrap-admin.json').read_text(), initial_admin)
                self.assertEqual(json.loads((local / 'deployment.json').read_text())['features'], ['code'])
                self.assertNotIn('profiles', json.loads((local / 'compose.json').read_text())['services']['code-runner'])
                for filename in ['credentials.json', 'accounts.json', 'account-guide.json', 'bootstrap-admin.json', 'compose.json', 'code-runner/token', 'certs/server.key']:
                    self.assertEqual((local / filename).stat().st_mode & 0o777, 0o600)
                self.assertEqual(primary.read_text(), 'PRIMARY_SECRET=never-read-or-copy\n')
                self.assertNotIn('never-read-or-copy', json.dumps(json.loads((local / 'compose.json').read_text())))


    def test_provision_rejects_undeclared_administrator_before_docker(self):
        from unittest.mock import patch
        with tempfile.TemporaryDirectory() as folder, patch.object(experience, 'LOCAL', Path(folder)):
            account = experience.new_accounts()['provider']
            account['roles'] = ['admin']
            (Path(folder) / 'accounts.json').write_text(json.dumps({'provider': account}))
            (Path(folder) / 'bootstrap-admin.json').write_text(json.dumps({'username': 'experience_bootstrap'}))
            with patch.object(experience, 'compose') as compose:
                with self.assertRaisesRegex(ValueError, 'Unexpected experience administrator'):
                    experience.provision()
                compose.assert_not_called()

    def test_existing_security_auditor_is_not_silently_promoted_to_admin(self):
        from unittest.mock import patch
        with tempfile.TemporaryDirectory() as folder, patch.object(experience, 'LOCAL', Path(folder)):
            account = experience.new_accounts()['security_reviewer']
            (Path(folder) / 'accounts.json').write_text(json.dumps({'security_reviewer': account}))
            (Path(folder) / 'bootstrap-admin.json').write_text(json.dumps({'username': 'experience_bootstrap'}))
            actual = {'department_id': account['department_id'], 'active': True, 'roles': ['auditor'], 'platforms': ['security']}
            with patch.object(experience, 'compose', side_effect=['1', json.dumps(actual)]) as compose:
                with self.assertRaisesRegex(RuntimeError, 'refusing silent privilege changes'):
                    experience.provision()
                self.assertEqual(compose.call_count, 2)
                self.assertTrue(all(call.args[0] == 'exec' for call in compose.call_args_list))


class LightweightExperienceTests(unittest.TestCase):
    def test_default_core_fits_without_cloning_compute_or_streaming(self):
        with tempfile.TemporaryDirectory() as folder:
            config = experience.render_config(ROOT, Path(folder), experience.new_environment(), '172.30.239.0/24')
        active = {name for name, service in config['services'].items() if not service.get('profiles')}
        self.assertEqual(active, {'postgres', 'rustfs', 'server', 'source-reader', 'ingress'})
        self.assertNotIn('ports', config['services']['server'])
        self.assertEqual(config['services']['ingress']['networks'], ['ingress', 'default'])
        self.assertNotIn('ingress', config['services']['server']['networks'])
        self.assertNotIn('ingress', config['services']['source-reader']['networks'])
        self.assertNotIn('APP_DSN', config['services']['ingress']['environment'])
        self.assertNotIn('doris-fe', config['services'])
        self.assertNotIn('doris-be', config['services'])
        self.assertNotIn('postgres-source', config['services'])
        self.assertEqual(experience.required_memory(config), int(1.9375 * 1024**3))
        experience.check_capacity(config, int(7.75 * 1024**3), int(4.26 * 1024**3))
        environment = config['services']['server']['environment']
        self.assertEqual(environment['DORIS_DATABASE_PREFIX'], 'experience_')
        self.assertEqual(environment['DORIS_READER_USER'], 'experience_reader')
        self.assertEqual(environment['DORIS_WRITER_USER'], 'experience_writer')
        self.assertEqual(environment['GOVERNANCE_DORIS_ADDRESS'], 'doris-fe:9030')
        self.assertTrue(config['networks']['compute']['external'])
        self.assertEqual(config['networks']['compute']['name'], 'datamesh-experience_compute')
        self.assertEqual(config['services']['postgres']['networks']['default']['aliases'], ['postgres-source'])

    def test_code_profile_requires_explicit_enable_and_reserves_one_bounded_job(self):
        with tempfile.TemporaryDirectory() as folder:
            config = experience.render_config(ROOT, Path(folder), experience.new_environment(), '172.30.239.0/24', features=['code'])
        self.assertNotIn('profiles', config['services']['code-runner'])
        self.assertEqual(experience.required_memory(config), int(2.3125 * 1024**3))
        self.assertEqual(config['services']['kafka']['profiles'], ['streaming'])


class DeliveryFixtureTests(unittest.TestCase):
    def test_new_fixture_uses_private_credential_and_sample_only(self):
        from unittest.mock import patch
        with tempfile.TemporaryDirectory() as folder, patch.object(experience, 'LOCAL', Path(folder)):
            local = Path(folder)
            (local / 'deployment.json').write_text(json.dumps({'mode': 'lightweight'}))
            (local / 'credentials.json').write_text(json.dumps(experience.new_environment()))
            calls = []
            def compose(*args, **kwargs):
                calls.append((args, kwargs))
                if kwargs.get('input', '').startswith('SELECT'):
                    return '0'
                self.assertTrue((local / 'delivery-fixture.json').exists())
                return ''
            with patch.object(experience, 'compose', side_effect=compose):
                experience.prepare_delivery_fixture()
            guide = json.loads((local / 'delivery-fixture.json').read_text())
            password = guide['credentials']['password']
            self.assertEqual(guide['status'], 'prepared')
            self.assertEqual(guide['database'], 'sample')
            self.assertEqual(guide['credentials']['username'], 'experience_etl')
            self.assertEqual((local / 'delivery-fixture.json').stat().st_mode & 0o777, 0o600)
            for args, kwargs in calls:
                self.assertNotIn(password, str(args))
                self.assertEqual(args[args.index('-d') + 1], 'sample')
            sql = calls[-1][1]['input']
            self.assertIn('GRANT SELECT, INSERT, UPDATE, DELETE', sql)
            self.assertIn('NOSUPERUSER NOCREATEDB NOCREATEROLE NOREPLICATION', sql)
            self.assertNotIn('TRUNCATE', sql)
            self.assertNotIn('DROP ', sql)

    def test_registration_reuses_stored_connection_and_stable_request_key(self):
        from unittest.mock import patch, MagicMock
        with tempfile.TemporaryDirectory() as folder, patch.object(experience, 'LOCAL', Path(folder)):
            local = Path(folder)
            (local / 'accounts.json').write_text(json.dumps(experience.new_accounts()))
            fixture = {'status': 'prepared', 'kind': 'postgres', 'host': 'postgres-source', 'port': 5432,
                'database': 'sample', 'credentials': {'username': 'experience_etl', 'password': 'private'}}
            provider = MagicMock()
            provider.principal = {'user_id': 'provider-id'}
            provider.call.return_value = {'source_id': 'registered-target'}
            with patch.object(experience, 'prepare_delivery_fixture', return_value=fixture), \
                 patch.object(experience, 'Client', return_value=provider):
                experience.register_delivery_fixture()
                first = provider.call.call_args
                self.assertEqual(first.args[:2], ('POST', '/api/v1/sources'))
                self.assertEqual(first.args[2]['owner_id'], 'provider-id')
                provider.reset_mock()
                experience.register_delivery_fixture()
                self.assertEqual(provider.call.call_args.args, ('GET', '/api/v1/sources/registered-target'))
                provider.close.assert_called_once()
            stored = json.loads((local / 'delivery-fixture.json').read_text())
            self.assertEqual(stored['source_id'], 'registered-target')

    def test_preexisting_target_without_ownership_is_not_adopted(self):
        from unittest.mock import patch
        with tempfile.TemporaryDirectory() as folder, patch.object(experience, 'LOCAL', Path(folder)):
            local = Path(folder)
            (local / 'deployment.json').write_text(json.dumps({'mode': 'lightweight'}))
            with patch.object(experience, 'compose', return_value='1') as compose:
                with self.assertRaisesRegex(RuntimeError, 'without our ownership record'):
                    experience.prepare_delivery_fixture()
                self.assertEqual(compose.call_count, 1)
            self.assertFalse((local / 'delivery-fixture.json').exists())


class MinimalCDCProfileTests(unittest.TestCase):
    def test_pinned_connect_uses_service_manifests_in_both_deployments(self):
        primary = json.loads((ROOT / 'compose.yaml').read_text())
        self.assertEqual(primary['services']['connect']['environment']['CONNECT_PLUGIN_DISCOVERY'], 'service_load')
        with tempfile.TemporaryDirectory() as folder:
            for mode in ['lightweight', 'full']:
                config = experience.render_config(ROOT, Path(folder), experience.new_environment(), '172.30.239.0/24', mode=mode, features=['streaming'])
                self.assertEqual(config['services']['connect']['environment']['CONNECT_PLUGIN_DISCOVERY'], 'service_load')

    def test_cdc_activates_capture_without_flink_or_extra_database(self):
        with tempfile.TemporaryDirectory() as folder:
            config = experience.render_config(ROOT, Path(folder), experience.new_environment(), '172.30.239.0/24', features=['code', 'cdc'])
        for name in ['kafka', 'connect']:
            self.assertNotIn('profiles', config['services'][name])
        for name in ['jobmanager', 'taskmanager', 'sql-gateway', 'flink-state-init']:
            self.assertEqual(config['services'][name]['profiles'], ['streaming'])
        self.assertNotIn('postgres-source', config['services'])
        self.assertEqual(experience.required_memory(config), int(3.6875 * 1024**3))
        with self.assertRaisesRegex(RuntimeError, 'Insufficient Docker memory'):
            experience.check_capacity(config, 8 * 1024**3, 4 * 1024**3)

    def test_streaming_still_includes_capture(self):
        with tempfile.TemporaryDirectory() as folder:
            config = experience.render_config(ROOT, Path(folder), experience.new_environment(), '172.30.239.0/24', features=['streaming'])
        for name in ['kafka', 'connect', 'jobmanager', 'taskmanager', 'sql-gateway', 'flink-state-init']:
            self.assertNotIn('profiles', config['services'][name])


class StreamScriptReadinessTests(unittest.TestCase):
    def test_only_selected_streaming_services_receive_private_bridge_access(self):
        with tempfile.TemporaryDirectory() as folder:
            local = Path(folder)
            for features in [[], ['code'], ['cdc'], ['code', 'streaming']]:
                config = experience.render_config(ROOT, local, experience.new_environment(), '172.30.239.0/24', features=features)
                enabled = 'streaming' in features
                for name in ['server', 'taskmanager', 'sql-gateway']:
                    service = config['services'][name]
                    token_mounts = [m for m in service.get('volumes', []) if 'stream-secrets' in m]
                    self.assertEqual(token_mounts, [str(local / 'stream-script/token') + ':/stream-secrets/token:ro'] if enabled else [])
                    self.assertEqual(service['environment'].get('GOVERNANCE_STREAM_SCRIPT_TOKEN_FILE', ''), '/stream-secrets/token' if enabled else '')
                    if enabled and name != 'server':
                        self.assertEqual(service['environment']['GOVERNANCE_STREAM_SCRIPT_ORIGIN'], 'https://server:8443')
                        self.assertEqual(service['environment']['GOVERNANCE_STREAM_SCRIPT_TLS_NAME'], 'localhost')
                        self.assertEqual(service['environment']['GOVERNANCE_STREAM_SCRIPT_CA_FILE'], '/certs/server.crt')
                        self.assertIn(str(local / 'certs/server.crt') + ':/certs/server.crt:ro', service['volumes'])
                for name in ['jobmanager', 'kafka', 'connect', 'code-runner', 'ingress']:
                    self.assertFalse(any('stream-secrets' in m for m in config['services'][name].get('volumes', [])))

    def test_readiness_rejects_missing_adapter_and_unsafe_private_key(self):
        import zipfile
        with tempfile.TemporaryDirectory() as folder:
            local = Path(folder)
            (local / 'stream-script').mkdir(mode=0o700)
            token = local / 'stream-script/token'
            token.write_text('a' * 64)
            token.chmod(0o640)
            (local / 'flink-jars').mkdir()
            with self.assertRaisesRegex(RuntimeError, 'adapter'):
                experience.validate_stream_script_artifacts(local)
            with zipfile.ZipFile(local / 'flink-jars/datamesh-stream-script.jar', 'w') as jar:
                jar.writestr('datamesh/governance/StreamScriptFunction.class', b'class-fixture')
            experience.validate_stream_script_artifacts(local)
            for invalid in ['short', 'g' * 64, 'a' * 64 + '\n']:
                token.write_text(invalid)
                with self.assertRaisesRegex(RuntimeError, 'token'):
                    experience.validate_stream_script_artifacts(local)
            token.write_text('a' * 64)
            token.chmod(0o644)
            with self.assertRaisesRegex(RuntimeError, 'token'):
                experience.validate_stream_script_artifacts(local)
            token.unlink()
            (local / 'other').write_text('a' * 64)
            token.symlink_to(local / 'other')
            with self.assertRaisesRegex(RuntimeError, 'token'):
                experience.validate_stream_script_artifacts(local)


class ComputeAttachmentTests(unittest.TestCase):
    def test_existing_namespace_without_ownership_record_cannot_be_adopted(self):
        from unittest.mock import patch
        with tempfile.TemporaryDirectory() as folder, patch.object(experience, 'LOCAL', Path(folder)):
            local = Path(folder)
            (local / 'deployment.json').write_text(json.dumps({'mode': 'lightweight'}))
            credentials = json.dumps(experience.new_environment())
            (local / 'credentials.json').write_text(credentials)
            def sql(container, username, password, query, allow_error=False):
                if query == 'SHOW DATABASES;':
                    return 'datamesh_dwd\nexperience_dwd\n', '', 0
                if query.startswith('SHOW GRANTS FOR'):
                    return '', 'User does not exist', 1
                raise AssertionError(query)
            with patch.dict(experience.os.environ, {'COMPUTE_ADMIN_PASSWORD': 'transient-main-admin-secret'}), \
                 patch.object(experience, 'docker', side_effect=['short-id', 'f' * 64]) as docker, \
                 patch.object(experience, 'compute_query', side_effect=sql):
                with self.assertRaisesRegex(RuntimeError, 'without our ownership record'):
                    experience.attach_compute()
                self.assertEqual([call.args[0] for call in docker.call_args_list], ['ps', 'inspect'])
            self.assertFalse((local / 'compute-intent.json').exists())
            self.assertFalse((local / 'compute-ready.json').exists())
            self.assertEqual((local / 'credentials.json').read_text(), credentials)

    def test_provisioning_intent_is_written_before_network_mutations(self):
        from unittest.mock import patch
        with tempfile.TemporaryDirectory() as folder, patch.object(experience, 'LOCAL', Path(folder)):
            local = Path(folder)
            (local / 'deployment.json').write_text(json.dumps({'mode': 'lightweight'}))
            (local / 'credentials.json').write_text(json.dumps(experience.new_environment()))
            def sql(container, username, password, query, allow_error=False):
                return ('datamesh_dwd\n', '', 0) if query == 'SHOW DATABASES;' else ('', 'User does not exist', 1)
            def docker(*args, **kwargs):
                if args[0] == 'ps': return 'short-id'
                if args[0] == 'inspect': return 'f' * 64
                if args[:2] == ('network', 'ls'): return ''
                if args[:2] == ('network', 'create'):
                    self.assertTrue((local / 'compute-intent.json').exists())
                    self.assertFalse((local / 'compute-ready.json').exists())
                    raise RuntimeError('Stopped after proving intent ordering')
                raise AssertionError(args)
            with patch.dict(experience.os.environ, {'COMPUTE_ADMIN_PASSWORD': 'transient-main-admin-secret'}), \
                 patch.object(experience, 'docker', side_effect=docker), \
                 patch.object(experience, 'workload_capabilities', return_value=({'max_concurrency': '2'}, ['normal'])), \
                 patch.object(experience, 'compute_query', side_effect=sql):
                with self.assertRaisesRegex(RuntimeError, 'proving intent ordering'):
                    experience.attach_compute()
            self.assertNotIn('transient-main-admin-secret', (local / 'compute-intent.json').read_text())
            self.assertFalse((local / 'compute-ready.json').exists())


class WorkloadGroupTests(unittest.TestCase):
    def test_missing_actual_queue_capabilities_fail_closed(self):
        from unittest.mock import patch
        with patch.object(experience, 'compute_query', return_value=('Name\tSTRING\n', '', 0)) as query:
            with self.assertRaisesRegex(RuntimeError, 'required workload-group queue controls'):
                experience.workload_capabilities('fe', 'root', 'private-password')
            self.assertEqual(query.call_count, 1)
            self.assertTrue(query.call_args.args[3].startswith('DESC '))

    def test_disabled_global_queue_is_not_enabled_automatically(self):
        from unittest.mock import patch
        columns = ['name', 'max_concurrency', 'max_queue_size', 'queue_timeout', 'running_query_num',
                   'waiting_query_num', 'min_memory_percent', 'max_memory_percent', 'min_cpu_percent', 'max_cpu_percent']
        responses = [('\n'.join(name + '\tSTRING' for name in columns), '', 0), ('enable_workload_group\tfalse', '', 0)]
        with patch.object(experience, 'compute_query', side_effect=responses) as query:
            with self.assertRaisesRegex(RuntimeError, 'global settings were not changed'):
                experience.workload_capabilities('fe', 'root', 'private-password')
            self.assertTrue(all(not call.args[3].startswith(('SET GLOBAL', 'CREATE', 'ALTER')) for call in query.call_args_list))

    def test_workload_metadata_tampering_cannot_be_certified(self):
        from unittest.mock import patch
        properties = {'max_concurrency': '2', 'max_queue_size': '20', 'queue_timeout': '5000'}
        with patch.object(experience, 'compute_query', return_value=('3\t20\t5000', '', 0)):
            with self.assertRaisesRegex(RuntimeError, 'limits differ'):
                experience.validate_workload_group('fe', 'experience_reader', 'private-password', properties)


    def test_doris4_show_counters_are_independent_of_metadata_schema(self):
        from unittest.mock import patch
        columns = ['id', 'name', 'max_concurrency', 'max_queue_size', 'queue_timeout',
                   'min_memory_percent', 'max_memory_percent', 'min_cpu_percent', 'max_cpu_percent']
        responses = [('\n'.join(name + '\tSTRING' for name in columns), '', 0), ('', '', 0), ('', '', 0),
                     ('Id\tName\trunning_query_num\twaiting_query_num\n1\tnormal\t0\t0\n', '', 0)]
        with patch.object(experience, 'compute_query', side_effect=responses) as query:
            properties, groups = experience.workload_capabilities('fe', 'root', 'private-password')
            self.assertEqual(properties['max_memory_percent'], '10%')
            self.assertEqual(properties['max_concurrency'], '2')
            self.assertEqual(groups, ['normal'])
            self.assertEqual(query.call_args.args[3], 'SHOW WORKLOAD GROUPS;')
            self.assertTrue(query.call_args.kwargs['headers'])

    def test_show_without_real_queue_counters_is_rejected(self):
        from unittest.mock import patch
        with patch.object(experience, 'compute_query', return_value=('Id\tName\n1\tnormal\n', '', 0)):
            with self.assertRaisesRegex(RuntimeError, 'queue counters are unsupported'):
                experience.workload_group_stats('fe', 'root', 'private-password')


class DockerBoundaryTests(unittest.TestCase):
    def test_preflight_accepts_docker_host_and_none_network_null_ipam(self):
        import zipfile
        from types import SimpleNamespace
        from unittest.mock import patch
        with tempfile.TemporaryDirectory() as folder, patch.object(experience, 'LOCAL', Path(folder)):
            local = Path(folder)
            (local / 'credentials.json').write_text(json.dumps(experience.new_environment()))
            (local / 'deployment.json').write_text(json.dumps({'mode': 'full', 'subnet': '172.30.239.0/24'}))
            for name in ['flink-jars', 'source-reader-drivers']:
                (local / name).mkdir()
                (local / name / 'fixture.jar').write_bytes(b'PK-test-artifact')
            with zipfile.ZipFile(local / 'flink-jars/datamesh-stream-script.jar', 'w') as jar:
                jar.writestr('datamesh/governance/StreamScriptFunction.class', b'class-fixture')
            (local / 'stream-script').mkdir(mode=0o700)
            (local / 'stream-script/token').write_text('a' * 64)
            (local / 'stream-script/token').chmod(0o640)
            def docker(*args, **kwargs):
                if args[0] == 'info': return json.dumps({'MemTotal': 32 * 1024**3})
                if args[0] == 'ps': return ''
                if args[:2] == ('network', 'ls'): return 'hostnet\nnonenet\n'
                if args[:2] == ('network', 'inspect'):
                    return json.dumps([{'Name': 'host', 'IPAM': {'Config': None}}, {'Name': 'none', 'IPAM': None}])
                if args[:2] == ('image', 'inspect'): return json.dumps([{'Id': 'sha256:' + 'a' * 64}])
                raise AssertionError(args)
            with patch.object(experience, 'docker', side_effect=docker), patch.object(experience, 'compose', return_value=''), \
                 patch.object(experience.shutil, 'disk_usage', return_value=SimpleNamespace(free=100 * 1024**3)), \
                 patch.object(experience.socket, 'socket'):
                experience.preflight()


class HTTPSReadinessTests(unittest.TestCase):
    def test_retries_transport_startup_without_retrying_mutations(self):
        from unittest.mock import patch, MagicMock
        from urllib.error import URLError
        import ssl
        response = MagicMock()
        response.__enter__.return_value.status = 200
        response.__enter__.return_value.read.return_value = b'{"status":"ready"}'
        opener = MagicMock()
        opener.open.side_effect = [URLError(ssl.SSLEOFError('startup')), response]
        with patch.object(experience, 'build_opener', return_value=opener), \
             patch.object(experience.ssl, 'create_default_context'), patch.object(experience.time, 'sleep'):
            experience.wait_http_ready()
        self.assertEqual(opener.open.call_count, 2)
        self.assertTrue(all(call.args[0].get_method() == 'GET' and call.args[0].full_url.endswith('/health/ready') for call in opener.open.call_args_list))

    def test_invalid_certificate_is_not_accepted_as_transient_readiness(self):
        from unittest.mock import patch, MagicMock
        from urllib.error import URLError
        import ssl
        opener = MagicMock()
        opener.open.side_effect = URLError(ssl.SSLCertVerificationError(1, 'untrusted certificate'))
        with patch.object(experience, 'build_opener', return_value=opener), \
             patch.object(experience.ssl, 'create_default_context'), patch.object(experience.time, 'sleep') as sleep:
            with self.assertRaisesRegex(RuntimeError, 'certificate'):
                experience.wait_http_ready()
            sleep.assert_not_called()

    def test_never_authenticates_after_readiness_deadline(self):
        from unittest.mock import patch, MagicMock
        from urllib.error import URLError
        opener = MagicMock()
        opener.open.side_effect = URLError('unavailable')
        with patch.object(experience, 'build_opener', return_value=opener), \
             patch.object(experience.ssl, 'create_default_context'), patch.object(experience.time, 'sleep'), \
             patch.object(experience.time, 'monotonic', side_effect=[0, 0, 46]):
            with self.assertRaisesRegex(RuntimeError, 'did not become ready'):
                experience.wait_http_ready()
        self.assertEqual(opener.open.call_count, 1)


class StartupOrderTests(unittest.TestCase):
    def test_ingress_is_started_after_migration_then_trusted_readiness_precedes_business(self):
        from unittest.mock import patch
        with tempfile.TemporaryDirectory() as folder:
            config = experience.render_config(ROOT, Path(folder), experience.new_environment(), '172.30.239.0/24')
        calls = []
        def compose(*args, **kwargs):
            calls.append(('compose', args))
        with patch.object(experience.sys, 'argv', ['experience_env.py', 'up']), \
             patch.object(experience, 'preflight'), patch.object(experience, 'save_config', return_value=config), \
             patch.object(experience, 'compose', side_effect=compose), \
             patch.object(experience, 'provision', side_effect=lambda: calls.append(('provision', ()))), \
             patch.object(experience, 'wait_http_ready', side_effect=lambda: calls.append(('ready', ()))), \
             patch.object(experience, 'bootstrap', side_effect=lambda: calls.append(('bootstrap', ()))), \
             patch.object(experience, 'verify', side_effect=lambda: calls.append(('verify', ()))), patch('builtins.print'):
            experience.main()
        backend = calls[0][1]
        self.assertNotIn('server', backend)
        self.assertNotIn('ingress', backend)
        self.assertEqual(calls[1][1][-1], 'migrate')
        self.assertEqual([call[0] for call in calls], ['compose', 'compose', 'provision', 'compose', 'ready', 'bootstrap', 'verify'])
        self.assertEqual(calls[3][1][-2:], ('server', 'ingress'))

    def test_failed_trusted_readiness_stops_only_own_project_before_bootstrap(self):
        from unittest.mock import patch
        with tempfile.TemporaryDirectory() as folder:
            config = experience.render_config(ROOT, Path(folder), experience.new_environment(), '172.30.239.0/24')
        with patch.object(experience.sys, 'argv', ['experience_env.py', 'up']), \
             patch.object(experience, 'preflight'), patch.object(experience, 'save_config', return_value=config), \
             patch.object(experience, 'compose') as compose, patch.object(experience, 'provision'), \
             patch.object(experience, 'wait_http_ready', side_effect=RuntimeError('not ready')), \
             patch.object(experience, 'bootstrap') as bootstrap:
            with self.assertRaisesRegex(RuntimeError, 'not ready'):
                experience.main()
            bootstrap.assert_not_called()
            self.assertEqual(compose.call_args.args, ('stop',))


class ProcessingAuthorizationTests(unittest.TestCase):
    def test_normal_governance_requires_independent_scoped_derived_sharing_grant(self):
        from types import SimpleNamespace
        from unittest.mock import MagicMock
        reviewer = MagicMock()
        reviewer.principal = {'user_id': 'independent-reviewer'}
        reviewer.call.side_effect = [{'items': []}, {'id': 'actual-grant'}]
        actor = SimpleNamespace(principal={'user_id': 'provider'})
        resource = {'resource_type': 'table_resource', 'resource_id': 'source-resource', 'contract_version': 1}
        experience.ensure_processing_authorization(reviewer, actor, resource, ['id', 'name'])
        method, path, payload = reviewer.call.call_args.args
        self.assertEqual((method, path), ('POST', '/api/v1/processing-authorizations'))
        self.assertEqual(payload['resource'], resource)
        self.assertEqual(payload['user_id'], 'provider')
        self.assertEqual(payload['fields'], ['id', 'name'])
        self.assertEqual(payload['purpose'], '数据治理')
        self.assertTrue(payload['allow_derived'])
        self.assertTrue(payload['allow_sharing'])

    def test_submitter_cannot_issue_own_processing_authorization(self):
        from unittest.mock import MagicMock
        actor = MagicMock()
        actor.principal = {'user_id': 'provider'}
        with self.assertRaisesRegex(ValueError, 'different'):
            experience.ensure_processing_authorization(actor, actor, {}, ['id'])
        actor.call.assert_not_called()


class ControlledDeliveryGrantTests(unittest.TestCase):
    def test_publisher_also_applies_and_uses_different_approver(self):
        from unittest.mock import MagicMock
        applicant, reviewer = MagicMock(), MagicMock()
        applicant.principal = {'user_id': 'provider', 'department_id': 'source-department'}
        reviewer.principal = {'user_id': 'reviewer'}
        applicant.call.side_effect = [{'application_id': 'application'}, None]
        resource = {'resource_type': 'governed_dataset', 'resource_id': 'actual-run', 'contract_version': 1}
        experience.approve_delivery_request(applicant, reviewer, resource, ['id', 'name', 'age', 'city'], '真实核验')
        self.assertEqual(applicant.call.call_args_list[0].args[:2], ('POST', '/api/v1/applications'))
        self.assertEqual(applicant.call.call_args_list[0].args[2]['resource'], resource)
        self.assertEqual(applicant.call.call_args_list[0].args[2]['fields'], ['id', 'name', 'age', 'city'])
        self.assertEqual(applicant.call.call_args.kwargs['expected'], 403)
        self.assertEqual(reviewer.call.call_args.args, ('POST', '/api/v1/applications/application/approve'))


class AcceptanceScopeTests(unittest.TestCase):
    def test_script_evidence_is_scoped_to_its_instance_and_does_not_certify_oracle(self):
        from unittest.mock import patch
        with tempfile.TemporaryDirectory() as folder, patch.object(experience, 'LOCAL', Path(folder)):
            proof = Path(folder) / 'script-stream-evidence.json'
            for evidence in [None, {'status': 'needs_app_restart', 'namespace': experience.PROJECT, 'synthetic': True},
                             {'status': 'passed', 'namespace': 'another-instance', 'synthetic': True}]:
                if evidence is not None:
                    proof.write_text(json.dumps(evidence))
                self.assertEqual(experience.acceptance_status()['postgresql_continuous_python_javascript'], 'not_verified')
            proof.write_text(json.dumps({'status': 'passed', 'namespace': experience.PROJECT, 'synthetic': True}))
            status = experience.acceptance_status()
            self.assertEqual(status['postgresql_continuous_python_javascript'], 'passed')
            self.assertEqual(status['optional_streaming_and_oracle_acceptance'], 'not_verified')
            self.assertEqual(status['deployed_security_policy_full_lifecycle'], 'not_end_to_end_verified')

    def test_code_and_browser_proofs_do_not_certify_uncovered_security_paths(self):
        from unittest.mock import patch
        with tempfile.TemporaryDirectory() as folder, patch.object(experience, 'LOCAL', Path(folder)):
            local = Path(folder)
            (local / 'code-verification.json').write_text(json.dumps({'result': 'passed', 'namespace': 'another-instance', 'remaining_own_jobs': 0}))
            self.assertEqual(experience.acceptance_status()['code_sandbox_integration'], 'not_verified')
            (local / 'code-verification.json').write_text(json.dumps({'result': 'passed', 'namespace': experience.PROJECT, 'remaining_own_jobs': 0}))
            (local / 'browser-verification.json').write_text(json.dumps({'realBackend': True, 'dualCookieIsolation': True, 'mainPublicBoundaryPreserved': True}))
            status = experience.acceptance_status()
            self.assertEqual(status['code_sandbox_integration'], 'passed')
            self.assertEqual(status['browser_dual_cookie_and_main_boundary'], 'passed')
            self.assertEqual(status['deployed_security_policy_full_lifecycle'], 'not_end_to_end_verified')
            self.assertEqual(status['external_api_and_uncovered_carriers'], 'not_verified')
            self.assertEqual(status['optional_streaming_and_oracle_acceptance'], 'not_verified')
            self.assertEqual(status['ordinary_two_script_governance'], 'not_verified')
            (local / 'two-script-verification.json').write_text(json.dumps({'state': 'succeeded', 'values_checked': True, 'max_jobs': 2, 'peak_sandbox_containers': 2}))
            self.assertEqual(experience.acceptance_status()['ordinary_two_script_governance'], 'not_verified')
            (local / 'two-script-verification.json').write_text(json.dumps({'state': 'succeeded', 'values_checked': True, 'max_jobs': 1, 'peak_sandbox_containers': 1}))
            self.assertEqual(experience.acceptance_status()['ordinary_two_script_governance'], 'passed')


class OptionalServiceCapacityTests(unittest.TestCase):
    def test_running_runner_cannot_be_omitted_from_the_selected_budget(self):
        from unittest.mock import patch
        with tempfile.TemporaryDirectory() as folder, patch.object(experience, 'LOCAL', Path(folder)):
            local = Path(folder)
            (local / 'credentials.json').write_text(json.dumps(experience.new_environment()))
            (local / 'deployment.json').write_text(json.dumps({'mode': 'lightweight', 'subnet': '172.30.239.0/24', 'features': []}))
            identity = 'a' * 64
            container = {'Id': identity, 'Memory': 128 * 1024**2, 'Labels': {
                'com.docker.compose.project': experience.PROJECT, 'com.docker.compose.service': 'code-runner',
                'datamesh.experience.mode': 'lightweight', 'com.docker.compose.oneoff': 'False'}}
            replies = [json.dumps({'MemTotal': 8 * 1024**3}), identity,
                       json.dumps(container), json.dumps({'ID': identity[:12], 'MemUsage': '32MiB / 128MiB'})]
            with patch.object(experience, 'docker', side_effect=replies):
                with self.assertRaisesRegex(RuntimeError, 'unselected optional experience service'):
                    experience.preflight()


if __name__ == '__main__':
    unittest.main()
