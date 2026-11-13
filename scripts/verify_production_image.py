"""Verify one combined application image against an owned PostgreSQL instance."""
import argparse
import hashlib
import http.cookiejar
import json
import os
from pathlib import Path
import re
import secrets
import ssl
import subprocess
import tempfile
import time
from urllib.error import HTTPError, URLError
from urllib.request import HTTPSHandler, HTTPCookieProcessor, Request, build_opener
import uuid

ROOT = Path(__file__).resolve().parents[1]


def verify(image, postgres_image, architecture, output):
    suffix = uuid.uuid4().hex[:10]
    network = 'datamesh-ci-' + suffix
    postgres = network + '-postgres'
    app = network + '-app'
    volume = network + '-pg'
    containers = []
    network_created = volume_created = False
    environment = os.environ.copy()
    admin = secrets.token_hex(24)
    owner = secrets.token_hex(24)
    application = secrets.token_hex(24)
    password = secrets.token_urlsafe(24)
    key = secrets.token_hex(32)

    def run(args, **kwargs):
        result = subprocess.run(args, cwd=ROOT, env=environment, capture_output=True, **kwargs)
        if result.returncode:
            detail = result.stdout.decode(errors='replace') + result.stderr.decode(errors='replace')
            for secret in [admin, owner, application, password, key, environment.get('CREDENTIAL_KEY', '')]:
                if secret: detail = detail.replace(secret, '[redacted]')
            output.parent.mkdir(parents=True, exist_ok=True)
            diagnostic_file = output.parent / 'image-task-diagnostics-private.log'
            diagnostic_file.write_text(detail)
            diagnostic_file.chmod(0o600)
            raise RuntimeError('Production image check command failed: ' + args[0])
        return result.stdout

    def inspect(reference):
        return json.loads(run(['docker', 'image', 'inspect', reference]))[0]

    def sql(statement):
        return run(['docker', 'exec', '-i', postgres, 'psql', '-v', 'ON_ERROR_STOP=1', '-U', 'datamesh_bootstrap', '-d', 'datamesh', '-At'], input=statement.encode()).decode().strip()

    with tempfile.TemporaryDirectory(prefix='datamesh-ci-') as temporary:
        directory = Path(temporary)
        tls = directory / 'tls'
        tls.mkdir()
        for name, value in [('postgres-admin-password', admin), ('migration-db-password', owner), ('app-db-password', application)]:
            (directory / name).write_text(value)
            (directory / name).chmod(0o600)
        bootstrap = directory / 'bootstrap-admin.json'
        bootstrap.write_text(json.dumps({'username': 'ci_admin', 'password': password, 'department_id': 'ci-private'}))
        bootstrap.chmod(0o600)
        run(['openssl', 'req', '-x509', '-newkey', 'rsa:2048', '-noenc', '-days', '1', '-subj', '/CN=localhost', '-addext', 'subjectAltName=DNS:localhost,IP:127.0.0.1', '-keyout', str(tls / 'server.key'), '-out', str(tls / 'server.crt')])
        (tls / 'server.key').chmod(0o600)
        (tls / 'server.crt').chmod(0o644)
        try:
            metadata = inspect(image)
            host_arch = run(['docker', 'info', '--format', '{{.Architecture}}']).decode().strip()
            host_arch = {'aarch64': 'arm64', 'x86_64': 'amd64'}.get(host_arch, host_arch)
            if metadata['Os'] != 'linux' or metadata['Architecture'] != architecture or host_arch != architecture:
                raise RuntimeError('Native image architecture does not match the verification host')
            if metadata['Config'].get('User') != '10001:10001':
                raise RuntimeError('Application must run as the expected non-root identity')
            run(['docker', 'network', 'create', network]); network_created = True
            run(['docker', 'volume', 'create', volume]); volume_created = True
            # Bind mounts keep secrets scoped to their owning task and Unix user.
            postgres_uid = run(['docker', 'run', '--rm', '--network', 'none', '--entrypoint', 'id', postgres_image, '-u', 'postgres']).decode().strip()
            postgres_gid = run(['docker', 'run', '--rm', '--network', 'none', '--entrypoint', 'id', postgres_image, '-g', 'postgres']).decode().strip()
            if not postgres_uid.isdecimal() or not postgres_gid.isdecimal(): raise RuntimeError('Invalid database image identity')
            run(['docker', 'run', '--rm', '--network', 'none', '-v', str(directory) + ':/owned', '--entrypoint', 'sh', postgres_image, '-c', 'chown ' + postgres_uid + ':' + postgres_gid + ' /owned/*-password; chown 10001:10001 /owned/bootstrap-admin.json /owned/tls/server.key'])

            def start_postgres():
                args = ['docker', 'run', '-d', '--name', postgres, '--network', network, '--memory', '384m', '-v', volume + ':/var/lib/postgresql/data', '-v', str(ROOT / 'deploy/production/postgres/01-roles.sh') + ':/docker-entrypoint-initdb.d/01-roles.sh:ro']
                for name in ['postgres-admin-password', 'migration-db-password', 'app-db-password']:
                    args += ['-v', str(directory / name) + ':/run/secrets/' + name + ':ro']
                args += ['-e', 'POSTGRES_USER=datamesh_bootstrap', '-e', 'POSTGRES_DB=datamesh', '-e', 'POSTGRES_PASSWORD_FILE=/run/secrets/postgres-admin-password', '-e', 'MIGRATION_DB_PASSWORD_FILE=/run/secrets/migration-db-password', '-e', 'APP_DB_PASSWORD_FILE=/run/secrets/app-db-password', postgres_image]
                run(args)
                if postgres not in containers: containers.append(postgres)
                for _ in range(90):
                    result = subprocess.run(['docker', 'exec', postgres, 'pg_isready', '-h', '127.0.0.1', '-U', 'datamesh_bootstrap', '-d', 'datamesh'], capture_output=True)
                    if result.returncode == 0: return
                    if run(['docker', 'inspect', '--format', '{{.State.Running}}', postgres]).decode().strip() != 'true':
                        raise RuntimeError('Owned PostgreSQL exited during initialization')
                    time.sleep(1)
                raise RuntimeError('Owned PostgreSQL did not become ready')

            start_postgres()
            if sql("SELECT count(*) FROM pg_roles WHERE rolname IN ('datamesh_owner','datamesh_app') AND NOT rolsuper AND NOT rolcreatedb AND NOT rolcreaterole") != '2':
                raise RuntimeError('Database role separation failed')
            environment.update(MIGRATION_DATABASE_URL=f'postgres://datamesh_owner:{owner}@{postgres}:5432/datamesh?sslmode=disable', DATABASE_URL=f'postgres://datamesh_app:{application}@{postgres}:5432/datamesh?sslmode=disable', CSRF_KEY=key)
            for _ in range(2):
                run(['docker', 'run', '--rm', '--network', network, '-e', 'MIGRATION_DATABASE_URL', image, '/app/bin/migrate'])
            for _ in range(2):
                run(['docker', 'run', '--rm', '--network', network, '-e', 'DATABASE_URL', '-e', 'CSRF_KEY', '-e', 'BOOTSTRAP_ADMIN_FILE=/run/secrets/bootstrap-admin.json', '-v', str(bootstrap) + ':/run/secrets/bootstrap-admin.json:ro', image, '/app/bin/bootstrap'])
            if sql("SELECT count(*) FROM users WHERE username='ci_admin'") != '1':
                raise RuntimeError('Bootstrap was not idempotent')
            initial = sql("SELECT md5(password_hash) FROM users WHERE username='ci_admin'")
            port = 31443
            origin = f'https://localhost:{port}'
            environment.update(APP_DSN=environment['DATABASE_URL'], APP_ORIGIN=origin, CREDENTIAL_KEY=secrets.token_hex(32), BASE_PATH='/datamesh', PORT='8080', TLS_CERT='/certs/server.crt', TLS_KEY='/certs/server.key', WEB_DIR='/app/web')
            args = ['docker', 'run', '-d', '--name', app, '--network', network, '--memory', '512m', '--read-only', '--tmpfs', '/tmp:size=128m,mode=1777', '--cap-drop', 'ALL', '--security-opt', 'no-new-privileges:true', '-p', f'127.0.0.1:{port}:8080', '-v', str(tls) + ':/certs:ro']
            for name in ['APP_DSN', 'APP_ORIGIN', 'CSRF_KEY', 'CREDENTIAL_KEY', 'BASE_PATH', 'PORT', 'TLS_CERT', 'TLS_KEY', 'WEB_DIR']:
                args += ['-e', name]
            run(args + [image]); containers.append(app)
            context = ssl.create_default_context(cafile=str(tls / 'server.crt'))
            opener = build_opener(HTTPSHandler(context=context), HTTPCookieProcessor(http.cookiejar.CookieJar()))

            def request(path, body=None, csrf=None):
                headers = {'Origin': origin}
                if body is not None: headers['Content-Type'] = 'application/json'
                if csrf: headers['X-CSRF-Token'] = csrf
                return opener.open(Request(origin + path, data=json.dumps(body).encode() if body is not None else None, headers=headers), timeout=10)

            def ready():
                for _ in range(90):
                    try:
                        with request('/datamesh/healthz') as response:
                            if json.load(response) == {'status': 'ready'}: return
                    except (OSError, HTTPError, URLError): pass
                    time.sleep(1)
                raise RuntimeError('Application database readiness failed')

            ready()
            run(['docker', 'exec', app, '/app/bin/healthcheck'])
            with request('/datamesh/') as response: index = response.read()
            assets = re.findall(rb'(?:src|href)="(/datamesh/static/[^"?]+)"', index)
            if len(assets) < 3: raise RuntimeError('Frontend static prefix was not built correctly')
            for asset in assets:
                with request(asset.decode()) as response:
                    if response.status != 200 or response.read() == index: raise RuntimeError('Static resource or SPA routing failed')
            for path in ['/datamesh/platform/governance/sandbox', '/datamesh/guides/governance']:
                with request(path) as response:
                    if response.read() != index: raise RuntimeError('SPA deep link failed')
            for path, status in [('/datamesh/api/v1/not-a-route', 404), ('/api/v1/me', 404), ('/datamesh/api/v1/me', 401)]:
                try:
                    with request(path): raise RuntimeError('Unexpected API access')
                except HTTPError as error:
                    if error.code != status or error.read() == index: raise RuntimeError('API error was masked by frontend')
            with request('/datamesh/auth/login', {'username': 'ci_admin', 'password': password}) as response:
                principal = json.load(response)
                cookie = response.headers.get('Set-Cookie', '')
                if 'Path=/datamesh' not in cookie or 'Secure' not in cookie or 'HttpOnly' not in cookie: raise RuntimeError('Session cookie scope failed')
            with request('/datamesh/api/v1/me') as response:
                if json.load(response)['roles'] != ['admin']: raise RuntimeError('Authenticated API failed')
            # Recreate the database container against the same owned volume.
            run(['docker', 'rm', '-f', postgres]); containers.remove(postgres)
            start_postgres(); ready()
            if sql("SELECT md5(password_hash) FROM users WHERE username='ci_admin'") != initial: raise RuntimeError('Existing-volume restart changed credentials')
            with request('/datamesh/api/v1/me') as response:
                if json.load(response)['username'] != 'ci_admin': raise RuntimeError('Existing-volume restart lost identity/session')
            with request('/datamesh/auth/logout', {}, principal['csrf_token']) as response:
                if response.status != 204: raise RuntimeError('Logout failed')
            report = {'state': 'verified', 'architecture': architecture, 'native': True, 'database_mode': 'bundled', 'web_mode': 'combined', 'base_path': '/datamesh', 'image_id': metadata['Id'], 'size_bytes': metadata['Size'], 'repeat_migrate': True, 'repeat_bootstrap': True, 'restricted_roles': True, 'persistent_volume_restart': True, 'tls_healthcheck': True, 'static_and_api_deep_links': True, 'authenticated_api_and_cookie_scope': True, 'index_sha256': hashlib.sha256(index).hexdigest()}
            output.parent.mkdir(parents=True, exist_ok=True)
            output.write_text(json.dumps(report, indent=2) + '\n')
            print(json.dumps(report))
        except Exception:
            diagnostics = {}
            for name in containers:
                result = subprocess.run(['docker', 'logs', '--tail', '100', name], capture_output=True, text=True)
                text = result.stdout + result.stderr
                for secret in [admin, owner, application, password, key, environment.get('CREDENTIAL_KEY', '')]:
                    if secret: text = text.replace(secret, '[redacted]')
                diagnostics[name] = text
            output.parent.mkdir(parents=True, exist_ok=True)
            diagnostic_file = output.parent / 'image-verification-diagnostics-private.json'
            diagnostic_file.write_text(json.dumps(diagnostics, indent=2) + '\n')
            diagnostic_file.chmod(0o600)
            raise
        finally:
            for name in reversed(containers): subprocess.run(['docker', 'rm', '-f', name], capture_output=True)
            if volume_created: subprocess.run(['docker', 'volume', 'rm', volume], capture_output=True)
            if network_created: subprocess.run(['docker', 'network', 'rm', network], capture_output=True)
            # Files were deliberately assigned to container identities above.
            subprocess.run(['docker', 'run', '--rm', '--network', 'none', '-v', str(directory) + ':/owned', '--entrypoint', 'chown', postgres_image, '-R', str(os.getuid()) + ':' + str(os.getgid()), '/owned'], capture_output=True)


if __name__ == '__main__':
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument('--image', required=True)
    parser.add_argument('--postgres-image', required=True)
    parser.add_argument('--architecture', choices=['amd64', 'arm64'], default='amd64')
    parser.add_argument('--output', type=Path, required=True)
    arguments = parser.parse_args()
    try: verify(arguments.image, arguments.postgres_image, arguments.architecture, arguments.output)
    except (OSError, ValueError, RuntimeError, subprocess.SubprocessError) as error:
        print(str(error) if isinstance(error, RuntimeError) else 'Production image verification failed; no credentials emitted.')
        raise SystemExit(1)
