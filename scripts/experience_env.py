#!/usr/bin/env python3
"""Independent full-suite deployment; never read the primary runtime environment."""
import argparse
import copy
import ipaddress
import hashlib
import http.cookiejar
import json
import os
from pathlib import Path
import re
import secrets
import shutil
import socket
import ssl
import time
import uuid
import zipfile
from datetime import datetime, timedelta, timezone
from decimal import Decimal
from urllib.error import HTTPError, URLError
from urllib.request import build_opener, HTTPSHandler, HTTPCookieProcessor, ProxyHandler, Request
import subprocess
import sys

ROOT = Path(__file__).resolve().parents[1]
LOCAL = ROOT / '.local/experience'
PROJECT = 'datamesh-experience'
GIB = 1024**3
EXPRESSION = re.compile(r'\$\{([A-Z_][A-Z_0-9]*)(?:(:\?|:-)([^}]*))?\}')


def private_write(path, value):
    if path.is_symlink():
        raise ValueError('Refusing symlink in experience configuration')
    fd = os.open(path, os.O_WRONLY | os.O_CREAT | os.O_TRUNC, 0o600)
    os.fchmod(fd, 0o600)
    with os.fdopen(fd, 'w') as stream:
        json.dump(value, stream, ensure_ascii=False, indent=2)
        stream.write('\n')


def new_environment():
    keys = ['POSTGRES_PASSWORD', 'APP_DB_PASSWORD', 'RUSTFS_SECRET_KEY', 'MYSQL_ROOT_PASSWORD',
            'SOURCE_DB_PASSWORD', 'SOURCE_CDC_PASSWORD', 'ORACLE_PASSWORD', 'DORIS_ROOT_PASSWORD',
            'DORIS_WRITER_PASSWORD', 'DORIS_READER_PASSWORD']
    env = {key: secrets.token_hex(24) for key in keys}
    env.update({key: secrets.token_hex(32) for key in ['CSRF_KEY', 'CREDENTIAL_KEY', 'SOURCE_READER_TOKEN']})
    env.update(APP_DSN=f"postgres://datamesh_app:{env['APP_DB_PASSWORD']}@postgres:5432/datamesh?sslmode=disable",
               MIGRATION_DSN=f"postgres://datamesh_owner:{env['POSTGRES_PASSWORD']}@postgres:5432/datamesh?sslmode=disable",
               RUSTFS_ACCESS_KEY='experience' + secrets.token_hex(10), APP_ORIGIN='https://localhost:19443',
               LOCAL_UID=str(os.getuid()), LOCAL_GID=str(os.getgid()), DIRECTORY_TEST_MODE='0',
               SECURITY_WATERMARK_KEY_ID='experience-v1',
               SECURITY_WATERMARK_KEYS=json.dumps({'experience-v1': secrets.token_hex(32)}),
               SOURCE_ALLOWED_TARGETS=json.dumps([
                   {'kind': 'postgres', 'host': 'postgres-source', 'port': 5432, 'database': 'sample'},
                   {'kind': 'mysql', 'host': 'mysql-source', 'port': 3306, 'database': 'sample'},
                   {'kind': 'oracle', 'host': 'oracle-source', 'port': 1521, 'database': 'FREEPDB1'}]),
               API_ALLOWED_TARGETS='[]', CODE_PYTHON_IMAGE='', CODE_JAVASCRIPT_IMAGE='')
    return env


def new_accounts():
    definitions = {
        'provider': ('experience-provider', ['exchange', 'governance', 'assets'], ['provider', 'operator', 'governor']),
        'directory_reviewer': ('experience-provider', ['exchange', 'assets'], ['provider']),
        'operator': ('experience-provider', ['exchange'], ['operator']),
        'governor': ('experience-provider', ['governance'], ['governor']),
        'consumer': ('experience-consumer', ['exchange', 'assets'], ['consumer']),
        'auditor': ('experience-security', ['security'], ['auditor']),
        'security_author': ('experience-security', ['security'], ['admin']),
        'security_reviewer': ('experience-security', ['security'], ['admin']),
    }
    return {name: dict(username='experience_' + name, password=secrets.token_urlsafe(24),
                       department_id=department, platforms=platforms, roles=roles)
            for name, (department, platforms, roles) in definitions.items()}


def validate_subnet(value, existing):
    subnet = ipaddress.ip_network(value, strict=True)
    if subnet.version != 4 or subnet.prefixlen != 24 or not subnet.is_private:
        raise ValueError('Experience subnet must be a private IPv4 /24')
    for other in ['172.29.238.0/24', *existing]:
        network = ipaddress.ip_network(other)
        if network.version == 4 and subnet.overlaps(network):
            raise ValueError('Experience subnet overlaps an existing or primary network')
    return str(subnet)


def render_config(root, local, env, subnet, mode='lightweight', features=()):
    if mode not in ['lightweight', 'full'] or set(features) - {'code', 'cdc', 'streaming', 'mysql', 'oracle'}:
        raise ValueError('Unknown experience mode or feature')
    validate_subnet(subnet, [])
    config = copy.deepcopy(json.loads((root / 'compose.yaml').read_text()))
    config['name'] = PROJECT
    config['services'].pop('go-tools', None)  # Full repository mount includes primary secrets.
    def resolve(value):
        if isinstance(value, dict):
            return {key: resolve(item) for key, item in value.items()}
        if isinstance(value, list):
            return [resolve(item) for item in value]
        if not isinstance(value, str):
            return value
        def replace(match):
            key, mode, default = match.groups()
            result = env.get(key, '')
            if not result and mode == ':?':
                raise ValueError('Missing isolated configuration: ' + key)
            return result or (default if mode == ':-' else '')
        return EXPRESSION.sub(replace, value)
    config = resolve(config)
    local_binds = {
        './.local/certs': 'certs', './.local/bindings': 'bindings',
        './.local/certs/server.crt': 'certs/server.crt',
        './.local/code-runner/token': 'code-runner/token', './.local/smoke': 'samples',
        './.local/stream-script/token': 'stream-script/token',
        './.local/flink-jars': 'flink-jars', './.local/source-reader-drivers': 'source-reader-drivers',
    }
    for name, service in config['services'].items():
        service.pop('ports', None)
        service.pop('build', None)
        service.pop('profiles', None)
        service.setdefault('labels', {})['datamesh.experience.mode'] = mode
        service['cpus'] = service.get('cpus', 2)
        service['pids_limit'] = service.get('pids_limit', 512)
        service['memswap_limit'] = service['mem_limit']
        service['logging'] = {'driver': 'json-file', 'options': {'max-size': '10m', 'max-file': '3'}}
        if name == 'migrate':
            service['profiles'] = ['tools']
        mounts = []
        for mount in service.get('volumes', []):
            source, destination = mount.split(':', 1)
            if source == './.local/public-test-accounts':
                continue
            if source in local_binds:
                source = str(local / local_binds[source])
            elif source.startswith('./deploy/') or source == './migrations':
                source = str(root / source[2:])
            elif source == '/var/run/docker.sock':
                if name != 'code-runner':
                    raise ValueError('Unapproved Docker socket mount')
            elif source.startswith(('.', '/')) or source not in config['volumes']:
                raise ValueError('Unapproved bind mount: ' + source)
            mounts.append(source + ':' + destination)
        service['volumes'] = mounts
    for name in config['volumes']:
        config['volumes'][name] = {'name': PROJECT + '_' + name}
    config['networks'] = {
        'default': {'name': PROJECT + '_network', 'internal': True},
        'doris': {'name': PROJECT + '_doris', 'internal': True, 'ipam': {'config': [{'subnet': subnet}]}},
        'code_control': {'name': PROJECT + '_code_control', 'internal': True},
    }
    prefix = subnet.rsplit('.', 1)[0]
    fe, be = config['services']['doris-fe'], config['services']['doris-be']
    fe['environment'].update(FE_SERVERS=f'fe1:{prefix}.10:9010', DORIS_PRIORITY_NETWORKS=subnet)
    fe['networks']['doris']['ipv4_address'] = prefix + '.10'
    be['environment'].update(MASTER_FE_IP=prefix + '.10', CURRENT_BE_IP=prefix + '.11', PRIORITY_NETWORKS=subnet)
    be['networks']['doris']['ipv4_address'] = prefix + '.11'
    config['services']['server']['environment'].update(SESSION_COOKIE_NAME='datamesh_experience_session', PUBLIC_TEST_ACCOUNTS_FILE='', ISOLATED_EXPERIENCE_GUARD='true', GOVERNANCE_DORIS_ADDRESS='doris-fe:9030')
    config['services']['code-runner']['environment']['CODE_RUNNER_NAMESPACE'] = PROJECT
    # Bundled pinned connectors expose SourceConnector/JSON service manifests.
    config['services']['connect']['environment']['CONNECT_PLUGIN_DISCOVERY'] = 'service_load'
    # Primary stream bindings must never leak into an independent deployment.
    for name in ['server', 'taskmanager', 'sql-gateway']:
        service = config['services'][name]
        service['volumes'] = [mount for mount in service['volumes']
                              if '/stream-secrets/' not in mount and not mount.endswith(':/certs/server.crt:ro')]
        for key in ['GOVERNANCE_STREAM_SCRIPT_TOKEN_FILE', 'GOVERNANCE_STREAM_SCRIPT_ORIGIN',
                    'GOVERNANCE_STREAM_SCRIPT_CA_FILE', 'GOVERNANCE_STREAM_SCRIPT_TLS_NAME']:
            service['environment'].pop(key, None)
        service.pop('group_add', None)
    if mode == 'lightweight':
        for name in ['doris-fe', 'doris-be', 'postgres-source']:
            del config['services'][name]
        config['networks'].pop('doris')
        # Root provisions this internal bridge and explicitly attaches only the shared FE.
        config['networks']['compute'] = {'name': PROJECT + '_compute', 'external': True}
        for name, service in config['services'].items():
            networks = service.get('networks')
            if isinstance(networks, list):
                service['networks'] = ['compute' if item == 'doris' else item for item in networks]
            if name in ['server', 'source-reader']:
                service['environment'].update(DORIS_DATABASE_PREFIX='experience_', DORIS_READER_USER='experience_reader',
                    DORIS_WRITER_USER='experience_writer', GOVERNANCE_DORIS_ADDRESS='doris-fe:9030',
                    DORIS_QUERY_MEMORY_BYTES='134217728', DORIS_QUERY_TIMEOUT_SECONDS='30', DORIS_WORKLOAD_GROUP='experience_compute')
            feature = ('code' if name == 'code-runner' else 'mysql' if name == 'mysql-source' else
                       'oracle' if name == 'oracle-source' else 'streaming' if name in
                       ['kafka', 'connect', 'jobmanager', 'taskmanager', 'sql-gateway', 'flink-state-init'] else None)
            if feature and feature not in features:
                if name not in ['kafka', 'connect'] or 'cdc' not in features:
                    service['profiles'] = [feature]
        config['services']['code-runner']['environment']['CODE_RUNNER_MAX_JOBS'] = '1'
        config['services']['postgres']['mem_limit'] = '256m'
        config['services']['postgres']['memswap_limit'] = '256m'
        config['services']['postgres']['environment']['SOURCE_DB_PASSWORD'] = env['SOURCE_DB_PASSWORD']
        config['services']['postgres']['networks'] = {'default': {'aliases': ['postgres-source']}}
        # Source samples use a separate database/role on this private PG, never the metadata DB.
        config['services']['postgres']['command'] = ['postgres', '-c', 'wal_level=logical']
        config['services']['server']['environment']['SOURCE_ALLOWED_TARGETS'] = json.dumps([
            target for target in json.loads(env['SOURCE_ALLOWED_TARGETS'])
            if target['kind'] == 'postgres' or target['kind'] in features])
    if mode == 'full' or 'streaming' in features:
        for name in ['server', 'taskmanager', 'sql-gateway']:
            service = config['services'][name]
            service['environment']['GOVERNANCE_STREAM_SCRIPT_TOKEN_FILE'] = '/stream-secrets/token'
            service['volumes'].append(str(local / 'stream-script/token') + ':/stream-secrets/token:ro')
            if name != 'server':
                service['environment'].update(GOVERNANCE_STREAM_SCRIPT_ORIGIN='https://server:8443',
                    GOVERNANCE_STREAM_SCRIPT_CA_FILE='/certs/server.crt', GOVERNANCE_STREAM_SCRIPT_TLS_NAME='localhost')
                service['volumes'].append(str(local / 'certs/server.crt') + ':/certs/server.crt:ro')
                # Flink's UID differs from the server's; grant only the private token's group.
                service.setdefault('group_add', []).append(env['LOCAL_GID'])
    # Docker Desktop does not publish ports from an all-internal endpoint. Only this
    # fixed-upstream TLS proxy joins a normal bridge; application and data services stay internal.
    config['networks']['ingress'] = {'name': PROJECT + '_ingress', 'internal': False}
    config['services']['ingress'] = {
        'image': config['services']['server']['image'], 'entrypoint': ['/app/experience-ingress'],
        'environment': {'TLS_CERT': '/certs/server.crt', 'TLS_KEY': '/certs/server.key',
                        'INGRESS_CA': '/certs/server.crt', 'GOMEMLIMIT': '48MiB'},
        'volumes': [str(local / 'certs') + ':/certs:ro'], 'ports': ['127.0.0.1:19443:8443'],
        'networks': ['ingress', 'default'], 'depends_on': {'server': {'condition': 'service_started'}},
        'user': env['LOCAL_UID'] + ':' + env['LOCAL_GID'], 'read_only': True, 'cap_drop': ['ALL'],
        'security_opt': ['no-new-privileges:true'], 'mem_limit': '64m', 'memswap_limit': '64m',
        'cpus': .25, 'pids_limit': 32, 'labels': {'datamesh.experience.mode': mode},
        'logging': {'driver': 'json-file', 'options': {'max-size': '10m', 'max-file': '3'}},
    }
    return config


def bytes_value(value):
    if isinstance(value, int):
        return value
    match = re.fullmatch(r'([0-9.]+)\s*([kKmMgGtT]?(?:i?[bB])?)?', value)
    if not match:
        raise ValueError('Invalid Docker memory size')
    unit = (match[2] or '').lower().rstrip('b').rstrip('i')
    return int(float(match[1]) * 1024 ** {'': 0, 'k': 1, 'm': 2, 'g': 3, 't': 4}[unit])


def required_memory(config):
    # Active services plus migration; optional runner has two concurrent 256MiB jobs.
    active = {name: service for name, service in config['services'].items() if not service.get('profiles') or name == 'migrate'}
    jobs = int(active['code-runner']['environment'].get('CODE_RUNNER_MAX_JOBS', '2')) if 'code-runner' in active else 0
    if jobs not in [0, 1, 2]:
        raise ValueError('Invalid runner concurrency')
    return sum(bytes_value(service['mem_limit']) for service in active.values()) + jobs * 256 * 1024**2


def check_capacity(config, total, occupied):
    needed = required_memory(config) + GIB  # Docker VM/system headroom.
    if total - occupied < needed:
        raise RuntimeError(f'Insufficient Docker memory: selected services need {needed/GIB:.2f} GiB free including headroom; '
                           f'{max(0,total-occupied)/GIB:.2f} GiB currently available ({total/GIB:.2f} GiB total, '
                           f'{occupied/GIB:.2f} GiB currently used by existing containers). '
                           'No containers started; increase Docker memory without stopping other applications.')


def docker(*args, input=None):
    timeout = 300
    if args and args[0] == 'compose' and '--wait-timeout' in args:
        # The command must outlive Compose's readiness deadline and cleanup.
        timeout = max(timeout, int(args[args.index('--wait-timeout') + 1]) + 60)
    result = subprocess.run(['docker', *args], cwd=ROOT, input=input, text=True, capture_output=True, timeout=timeout)
    if result.returncode:
        raise RuntimeError('Docker operation failed; private output withheld: ' + args[0])
    return result.stdout


def compose(*args, input=None):
    # No shell environment interpolation or default .env can enter the resolved document.
    return docker('compose', '--project-name', PROJECT, '--env-file', '/dev/null', '-f',
                  str(LOCAL / 'compose.json'), *args, input=input)


def save_config():
    env = json.loads((LOCAL / 'credentials.json').read_text())
    state = json.loads((LOCAL / 'deployment.json').read_text())
    config = render_config(ROOT, LOCAL, env, state['subnet'], state.get('mode', 'lightweight'), state.get('features', []))
    private_write(LOCAL / 'compose.json', config)
    return config


def prepare(subnet, mode='lightweight', features=()):
    validate_subnet(subnet, [])
    LOCAL.mkdir(parents=True, mode=0o700, exist_ok=True)
    if LOCAL.is_symlink():
        raise ValueError('Experience directory must not be a symlink')
    LOCAL.chmod(0o700)
    for folder in ['certs', 'bindings', 'code-runner', 'stream-script', 'samples', 'flink-jars', 'source-reader-drivers']:
        path = LOCAL / folder
        if path.is_symlink():
            raise ValueError('Experience directory must not be a symlink')
        path.mkdir(mode=0o700, exist_ok=True)
        if folder in ['samples', 'flink-jars', 'source-reader-drivers']:
            path.chmod(0o755)  # Non-secret artifacts must be readable by non-root runtime UIDs.
        elif folder == 'stream-script':
            path.chmod(0o700)
    for filename, contents in [('credentials.json', new_environment()), ('accounts.json', new_accounts()),
                               ('deployment.json', {'subnet': subnet, 'mode': mode, 'features': list(features)}),
                               ('bootstrap-admin.json', dict(username='experience_bootstrap', password=secrets.token_urlsafe(24),
                                department_id='experience-bootstrap', platforms=['exchange', 'governance', 'assets', 'security'], roles=['admin']))]:
        path = LOCAL / filename
        if not path.exists():
            private_write(path, contents)
    state = json.loads((LOCAL / 'deployment.json').read_text())
    state.update(mode=mode, features=sorted(set(state.get('features', [])) | set(features)))
    private_write(LOCAL / 'deployment.json', state)
    # Preserve passwords on prepare while keeping the declared normal role scopes current.
    account_path = LOCAL / 'accounts.json'
    accounts = json.loads(account_path.read_text())
    for name, definition in new_accounts().items():
        if name not in accounts:
            accounts[name] = definition
        else:
            for field in ['department_id', 'platforms', 'roles']:
                accounts[name][field] = definition[field]
    private_write(account_path, accounts)
    private_write(LOCAL / 'account-guide.json', {
        'private_credentials_file': 'accounts.json',
        'main_instance_credentials_reused': False,
        'login_page_credentials_endpoint': 'disabled',
        'security_author': 'Full administrator of this isolated instance; security platform selection does not restrict backend administrator authority.',
        'security_reviewer': 'Separate full administrator of this isolated instance; use a different identity to review security_author submissions.',
        'other_accounts': 'Standard business roles and platform memberships; no administrator role.',
        'bootstrap_admin': 'Private setup identity outside shared accounts.json, never a main-instance identity.',
        'sharing': 'Private credentials; operator must deliberately choose recipients. Never publish accounts.json or expose it from the login page.'})
    token = LOCAL / 'code-runner/token'
    if not token.exists():
        private_write(token, secrets.token_hex(32))
        # Runner expects a raw token, not JSON.
        token.write_text(json.loads(token.read_text()))
    token = LOCAL / 'stream-script/token'
    if token.is_symlink():
        raise ValueError('Stream script token must not be a symlink')
    if not token.exists():
        fd = os.open(token, os.O_WRONLY | os.O_CREAT | os.O_EXCL, 0o600)
        with os.fdopen(fd, 'w') as stream:
            stream.write(secrets.token_hex(32))
    # The trusted Flink services receive this supplemental group and a file-only RO mount.
    token.chmod(0o640)
    if not (LOCAL / 'certs/server.crt').exists():
        subprocess.run(['openssl', 'req', '-x509', '-newkey', 'rsa:2048', '-noenc', '-days', '30',
                        '-subj', '/CN=localhost', '-addext', 'subjectAltName=DNS:localhost,IP:127.0.0.1',
                        '-keyout', str(LOCAL / 'certs/server.key'), '-out', str(LOCAL / 'certs/server.crt')],
                       check=True, stdout=subprocess.DEVNULL, stderr=subprocess.DEVNULL)
        (LOCAL / 'certs/server.key').chmod(0o600)
    sample = LOCAL / 'samples/customers.csv'
    if not sample.exists():
        sample.write_text('id,name,region,amount\n1,体验客户甲,east,100.25\n2,体验客户乙,west,250.50\n')
    # Copy only compiled driver artifacts. No primary bindings, samples, identities or credentials.
    for folder in ['flink-jars', 'source-reader-drivers']:
        source = ROOT / '.local' / folder
        if source.is_symlink():
            raise ValueError('Artifact directory must not be a symlink')
        for jar in source.glob('*.jar'):
            if jar.is_symlink() or not jar.is_file():
                raise ValueError('Driver artifact must be a regular file')
            with jar.open('rb') as stream:
                if stream.read(2) != b'PK':
                    raise ValueError('Invalid JAR artifact')
            shutil.copyfile(jar, LOCAL / folder / jar.name)
    prepare_business_files()
    save_config()
    print('Independent configuration prepared in .local/experience; no containers started.')


def validate_stream_script_artifacts(local):
    jar = local / 'flink-jars/datamesh-stream-script.jar'
    try:
        if jar.is_symlink():
            raise ValueError('symlink')
        with zipfile.ZipFile(jar) as artifact:
            if 'datamesh/governance/StreamScriptFunction.class' not in artifact.namelist():
                raise ValueError('missing adapter class')
    except (OSError, ValueError, zipfile.BadZipFile):
        raise RuntimeError('Missing or invalid stream script adapter JAR; build and prepare independent artifacts') from None
    token = local / 'stream-script/token'
    try:
        if token.is_symlink() or not token.is_file() or token.stat().st_mode & 0o007:
            raise ValueError('unsafe token')
        if not re.fullmatch(r'[0-9a-f]{64}', token.read_text(encoding='ascii')):
            raise ValueError('invalid token')
    except (OSError, ValueError, UnicodeError):
        raise RuntimeError('Missing, invalid or publicly readable stream script token; run prepare') from None


def preflight():
    config = save_config()
    info = json.loads(docker('info', '--format', '{{json .}}'))
    ids = docker('ps', '-q').split()
    occupied = 0
    if ids:
        containers = [json.loads(line) for line in docker(
            'inspect', '--format',
            '{"Id":{{json .Id}},"Memory":{{json .HostConfig.Memory}},"Labels":{{json .Config.Labels}}}',
            *ids).splitlines()]
        stats = {row['ID']: row for row in (json.loads(line) for line in docker('stats', '--no-stream', '--format', '{{json .}}').splitlines())}
        for container in containers:
            if (container.get('Labels') or {}).get('com.docker.compose.project') == PROJECT:
                current_mode = json.loads((LOCAL / 'deployment.json').read_text()).get('mode', 'lightweight')
                labels = container.get('Labels') or {}
                if labels.get('datamesh.experience.mode') != current_mode:
                    raise RuntimeError('Stop the current experience before changing deployment mode')
                service = labels.get('com.docker.compose.service')
                if service in config['services']:
                    if config['services'][service].get('profiles') and service != 'migrate':
                        raise RuntimeError('An unselected optional experience service is still running; retain its feature selection before preflight')
                    if labels.get('com.docker.compose.oneoff', 'False').lower() != 'true' or service == 'migrate':
                        continue
            # Measure existing applications; reserve the entire new suite and explicit system headroom.
            row = next((value for key, value in stats.items() if container['Id'].startswith(key)), None)
            if row is None:
                raise RuntimeError('Could not measure every existing container; refusing resource preflight')
            actual = bytes_value(row['MemUsage'].split('/')[0].strip())
            occupied += actual
    check_capacity(config, info['MemTotal'], occupied)
    state = json.loads((LOCAL / 'deployment.json').read_text())
    disk_gib = 10 if state.get('mode', 'lightweight') == 'full' else 2
    if shutil.disk_usage(ROOT).free < disk_gib * GIB:
        raise RuntimeError(f'Selected experience mode requires at least {disk_gib} GiB free workspace disk space')
    networks = json.loads(docker('network', 'inspect', *docker('network', 'ls', '-q').split()))
    subnets = [item['Subnet'] for network in networks if not network['Name'].startswith(PROJECT + '_')
               for item in ((network.get('IPAM') or {}).get('Config') or []) if item.get('Subnet')]
    state = json.loads((LOCAL / 'deployment.json').read_text())
    if state.get('mode', 'lightweight') == 'full':
        validate_subnet(state['subnet'], subnets)
    else:
        compute = next((network for network in networks if network['Name'] == PROJECT + '_compute'), None)
        if compute is None or not compute.get('Internal'):
            raise RuntimeError('Root must provision the dedicated internal compute bridge before lightweight startup')
        fe_ids = []
        for container_id in (compute.get('Containers') or {}):
            service = docker('inspect', '--format', '{{index .Config.Labels "com.docker.compose.service"}}', container_id).strip()
            if service == 'doris-fe':
                fe_ids.append(container_id)
        if len(fe_ids) != 1:
            raise RuntimeError('Dedicated compute bridge must expose exactly one approved Doris FE')
        ready = json.loads((LOCAL / 'compute-ready.json').read_text())
        if ready.get('status') != 'verified' or not ready.get('workload_group') or not ready.get('pressure'):
            raise RuntimeError('Dedicated compute workload readiness is not verified')
        env = json.loads((LOCAL / 'credentials.json').read_text())
        validate_workload_group(fe_ids[0], 'experience_reader', env['DORIS_READER_PASSWORD'], ready['workload_group'])
        for account, password_key, database in [('experience_reader', 'DORIS_READER_PASSWORD', 'experience_dwd'),
                                                 ('experience_writer', 'DORIS_WRITER_PASSWORD', 'experience_internal')]:
            output = docker('exec', '-i', fe_ids[0], 'sh', '-c',
                'IFS= read -r account; IFS= read -r password; MYSQL_PWD="$password" exec mysql --protocol=TCP -h127.0.0.1 -P9030 -u"$account" -N -B',
                input=account + '\n' + env[password_key] + '\nUSE ' + database + ';\nSHOW DATABASES;\n')
            databases = output.splitlines()
            if database not in databases or any(not name.startswith('experience_') and name not in ['information_schema', 'mysql', '__internal_schema'] for name in databases):
                raise RuntimeError('Dedicated Doris identity does not have the required exclusive database namespace')
    own_server = any((container.get('Labels') or {}).get('com.docker.compose.project') == PROJECT and
                     (container.get('Labels') or {}).get('com.docker.compose.service') == 'ingress' for container in (containers if ids else []))
    if not own_server:
        with socket.socket() as sock:
            sock.bind(('127.0.0.1', 19443))
    env = json.loads((LOCAL / 'credentials.json').read_text())
    for language in (['python', 'javascript'] if not config['services']['code-runner'].get('profiles') else []):
        env['CODE_' + language.upper() + '_IMAGE'] = json.loads(docker('image', 'inspect', 'datamesh-code-' + language + ':1'))[0]['Id']
    private_write(LOCAL / 'credentials.json', env)
    config = save_config()
    for image in {service['image'] for name, service in config['services'].items() if not service.get('profiles') or name == 'migrate'}:
        docker('image', 'inspect', image)  # No implicit pulls/builds or outbound container networking.
    required_artifacts = ['source-reader-drivers'] + (['flink-jars'] if state.get('mode') == 'full' or 'streaming' in state.get('features', []) else [])
    for folder in required_artifacts:
        if not list((LOCAL / folder).glob('*.jar')):
            raise RuntimeError('Missing independent driver artifacts: ' + folder)
    if state.get('mode') == 'full' or 'streaming' in state.get('features', []):
        validate_stream_script_artifacts(LOCAL)
    compose('--profile', '*', 'config', '--quiet')
    print(f'Selected-service preflight passed; reserved {required_memory(config)/GIB:.2f} GiB plus 1 GiB system headroom.')


def provision():
    accounts = json.loads((LOCAL / 'accounts.json').read_text())
    accounts['bootstrap'] = json.loads((LOCAL / 'bootstrap-admin.json').read_text())
    for key, account in accounts.items():
        username = account['username']
        if username != 'experience_' + key or not re.fullmatch(r'experience_[a-z_]+', username) or account.get('public_test'):
            raise ValueError('Unexpected experience account')
        if 'admin' in account['roles'] and key not in ['bootstrap', 'security_author', 'security_reviewer']:
            raise ValueError('Unexpected experience administrator')
        if key in ['security_author', 'security_reviewer'] and (account['roles'] != ['admin'] or account['platforms'] != ['security']):
            raise ValueError('Unexpected isolated security administrator configuration')
        exists = compose('exec', '-T', 'postgres', 'psql', '-U', 'datamesh_owner', '-d', 'datamesh', '-At',
                         '-v', 'ON_ERROR_STOP=1', '-c', f"SELECT count(*) FROM users WHERE username='{username}'").strip()
        if exists == '0':
            compose('run', '--rm', '--no-deps', '-T', '--entrypoint', '/app/manage', 'server', 'create-user', input=json.dumps(account))
        elif exists == '1':
            current = compose('exec', '-T', 'postgres', 'psql', '-U', 'datamesh_owner', '-d', 'datamesh', '-At',
                '-v', 'ON_ERROR_STOP=1', '-c',
                "SELECT json_build_object('department_id',department_id,'active',active,'roles',COALESCE((SELECT json_agg(role ORDER BY role) FROM user_roles WHERE user_id=users.id),'[]'::json),'platforms',COALESCE((SELECT json_agg(platform ORDER BY platform) FROM user_platform_access WHERE user_id=users.id),'[]'::json)) FROM users WHERE username='" + username + "'")
            actual = json.loads(current)
            if not actual['active'] or actual['department_id'] != account['department_id'] or sorted(actual['roles']) != sorted(account['roles']) or sorted(actual['platforms']) != sorted(account['platforms']):
                raise RuntimeError('Existing isolated identity differs from its declared role; refusing silent privilege changes')
        else:
            raise RuntimeError('Unexpected identity lookup result')
    print('Separate normal-role identities provisioned; credentials remain private in .local/experience/accounts.json.')


def prepare_business_files():
    from business_scenarios import SCENARIOS, VERSION, build_tables, table_schema_sql, insertion_sql
    tables = build_tables()
    statements = ['BEGIN;']
    for schema in SCENARIOS:
        statements += ['CREATE SCHEMA ' + schema + ';', 'GRANT USAGE ON SCHEMA ' + schema + ' TO datamesh_cdc;']
    for table in tables:
        statements += [table_schema_sql(table), insertion_sql(table), 'GRANT SELECT ON ' + table.qualified + ' TO datamesh_cdc;']
    statements.append('COMMIT;')
    sql = '\n'.join(statements) + '\n'
    target = LOCAL / 'samples/business-source.sql'
    target.write_text(sql)
    target.chmod(0o600)
    private_write(LOCAL / 'samples/business-manifest.json', {
        'version': VERSION, 'synthetic': True, 'sha256': hashlib.sha256(sql.encode()).hexdigest(),
        'tables': [{'table': table.qualified, 'rows': len(table.rows)} for table in tables]})


def source_sql(sql):
    state = json.loads((LOCAL / 'deployment.json').read_text()) if (LOCAL / 'deployment.json').exists() else {'mode': 'full'}
    service = 'postgres' if state.get('mode', 'lightweight') == 'lightweight' else 'postgres-source'
    return compose('exec', '-T', service, 'psql', '-U', 'source_owner', '-d', 'sample',
                   '-v', 'ON_ERROR_STOP=1', '-At', input=sql).strip()


def component_api(service, method, path, body=None):
    origins = {'connect': 'http://localhost:8083', 'jobmanager': 'http://localhost:8081'}
    args = ['exec', '-T', service, 'curl', '--fail', '--silent', '--show-error', '--max-time', '20',
            '-X', method, '-H', 'Content-Type: application/json', origins[service] + path]
    if body is not None:
        args += ['--data-binary', '@-']
    content = compose(*args, input=json.dumps(body) if body is not None else None)
    return json.loads(content) if content else {}




def wait_http_ready(timeout=45):
    opener = build_opener(ProxyHandler({}), HTTPSHandler(context=ssl.create_default_context(cafile=str(LOCAL / 'certs/server.crt'))))
    deadline = time.monotonic() + timeout
    request = Request('https://localhost:19443/health/ready')
    while time.monotonic() < deadline:
        try:
            with opener.open(request, timeout=2) as response:
                body = response.read(4097)
                if response.status != 200 or len(body) > 4096 or json.loads(body).get('status') != 'ready':
                    raise RuntimeError('Unexpected response from the private readiness endpoint')
                return
        except HTTPError as error:
            if error.code not in [502, 503, 504]:
                raise RuntimeError('Private HTTPS readiness returned an unexpected HTTP status') from None
        except URLError as error:
            if isinstance(error.reason, ssl.SSLCertVerificationError):
                raise RuntimeError('Private HTTPS readiness certificate could not be verified') from None
        time.sleep(.25)
    raise RuntimeError('Private HTTPS application did not become ready before the startup deadline')

class Client:
    def __init__(self, account):
        self.origin = 'https://localhost:19443'
        self.opener = build_opener(ProxyHandler({}), HTTPSHandler(context=ssl.create_default_context(cafile=str(LOCAL / 'certs/server.crt'))),
                                   HTTPCookieProcessor(http.cookiejar.CookieJar()))
        self.principal = self.call('POST', '/auth/login', {key: account[key] for key in ['username', 'password']})

    def call(self, method, path, body=None, key=None, expected=None):
        headers = {'Origin': self.origin, 'Content-Type': 'application/json'}
        if hasattr(self, 'principal'):
            headers['X-CSRF-Token'] = self.principal['csrf_token']
        if method != 'GET':
            headers['Idempotency-Key'] = key or str(uuid.uuid4())
        request = Request(self.origin + path, data=json.dumps(body or {}).encode() if method != 'GET' else None,
                          headers=headers, method=method)
        try:
            with self.opener.open(request, timeout=60) as response:
                if expected and response.status != expected:
                    raise RuntimeError('Unexpected experience business response status')
                value = json.load(response) if response.status != 204 else None
                if response.status == 202:
                    for _ in range(120):
                        if value['status'] in ['succeeded', 'failed', 'unknown']:
                            break
                        time.sleep(1)
                        value = self.call('GET', '/api/v1/operations/' + value['operation_id'])
                    if value['status'] != 'succeeded':
                        raise RuntimeError('Real experience operation did not succeed')
                return value
        except HTTPError as error:
            if expected == error.code:
                return None
            raw = error.read(4097)
            try:
                problem = json.loads(raw) if len(raw) <= 4096 else {}
            except (ValueError, UnicodeError):
                problem = {}
            code = problem.get('code', 'UNKNOWN') if isinstance(problem, dict) else 'UNKNOWN'
            if not isinstance(code, str) or not re.fullmatch(r'[A-Z0-9_]{1,100}', code):
                code = 'UNKNOWN'
            raise RuntimeError(f'Experience business interface failed: {method} {path.split("?")[0]} HTTP {error.code} {code}') from None

    def close(self):
        self.call('POST', '/auth/logout')


def bootstrap_pipeline():
    env = json.loads((LOCAL / 'credentials.json').read_text())
    manifest = json.loads((LOCAL / 'samples/business-manifest.json').read_text())
    ledger = LOCAL / 'business-source-installed.json'
    if not ledger.exists():
        # Apply a deterministic fixture once to empty, independently named schemas; never overwrite data.
        sql = (LOCAL / 'samples/business-source.sql').read_text()
        if hashlib.sha256(sql.encode()).hexdigest() != manifest['sha256']:
            raise RuntimeError('Synthetic source package checksum differs')
        source_sql(sql)
        private_write(ledger, {'sha256': manifest['sha256']})
    elif json.loads(ledger.read_text())['sha256'] != manifest['sha256']:
        raise RuntimeError('Installed synthetic fixture version differs; refusing overwrite')
    for table in manifest['tables']:
        if source_sql('SELECT count(*) FROM ' + table['table']) != str(table['rows']):
            raise RuntimeError('Synthetic source row counts changed; refusing overwrite')
    # Genuine Oracle sample, without LogMiner or replication grants being silently assumed.
    compose('exec', '-T', 'oracle-source', 'sqlplus', '-s', '/', 'as', 'sysdba', input="""whenever sqlerror exit failure
ALTER SESSION SET CONTAINER=FREEPDB1;
DECLARE n NUMBER; BEGIN
 SELECT COUNT(*) INTO n FROM all_tables WHERE owner='DATAMESH_SOURCE' AND table_name='CUSTOMERS';
 IF n=0 THEN
 EXECUTE IMMEDIATE 'CREATE TABLE DATAMESH_SOURCE.CUSTOMERS(id NUMBER(19) PRIMARY KEY,name VARCHAR2(100),amount NUMBER(20,6),updated_at TIMESTAMP(3))';
 EXECUTE IMMEDIATE 'INSERT INTO DATAMESH_SOURCE.CUSTOMERS VALUES(1,''体验客户'',100.25,TIMESTAMP ''2026-11-11 00:00:00'')';
 COMMIT;
 END IF;
END;
/
exit
""")
    for kind in ['postgres', 'mysql']:
        config = json.loads((ROOT / f'configs/connectors/{kind}.json').read_text())
        config['database.password'] = env['SOURCE_CDC_PASSWORD']
        name = 'datamesh-' + kind
        existing = component_api('connect', 'GET', '/connectors')
        if name not in existing:
            component_api('connect', 'POST', '/connectors', {'name': name, 'config': config})
        stored = component_api('connect', 'GET', '/connectors/' + name + '/config')
        if any(stored.get(key) != value for key, value in config.items()):
            raise RuntimeError('Existing isolated connector configuration differs; refusing replacement')
        for _ in range(90):
            status = component_api('connect', 'GET', '/connectors/' + name + '/status')
            states = [status['connector']['state']] + [task['state'] for task in status.get('tasks', [])]
            if 'FAILED' in states:
                raise RuntimeError('Isolated CDC connector failed')
            if len(states) > 1 and all(state == 'RUNNING' for state in states):
                break
            time.sleep(1)
        else:
            raise RuntimeError('Isolated CDC connector did not become running')
    # Password stays in stdin and a private experience SQL file; never in host command arguments.
    compose('exec', '-T', 'doris-fe', 'sh', '-c',
            'address=${FE_SERVERS#*:}; address=${address%%:*}; MYSQL_PWD="$DORIS_ROOT_PASSWORD" exec mysql -h"$address" -P9030 -uroot',
            input=(ROOT / 'tests/integration/fixtures/flink/doris-table.sql').read_text() +
                  "\nGRANT SELECT_PRIV ON datamesh_smoke.customers TO 'datamesh_reader';\n")
    jobs = component_api('jobmanager', 'GET', '/jobs/overview')['jobs']
    matching = [job for job in jobs if job['name'] == 'datamesh-experience-postgres' and job['state'] not in ['CANCELED', 'FINISHED', 'FAILED']]
    intent = LOCAL / 'pipeline-intent.json'
    if len(matching) > 1:
        raise RuntimeError('Multiple isolated Flink jobs require reconciliation')
    if matching:
        job_id = matching[0]['jid']
    else:
        if intent.exists():
            raise RuntimeError('Prior isolated Flink submission requires reconciliation before retry')
        sql = (ROOT / 'tests/integration/fixtures/flink/postgres-to-doris.sql').read_text()
        sql = "SET 'pipeline.name' = 'datamesh-experience-postgres';\n" + sql
        sql = sql.replace('${DORIS_WRITER_PASSWORD}', env['DORIS_WRITER_PASSWORD']).replace('${LABEL_PREFIX}', 'experience-' + secrets.token_hex(8))
        sql_file = LOCAL / 'samples/processing.sql'
        sql_file.write_text(sql)
        sql_file.chmod(0o600)
        private_write(intent, {'status': 'submitting'})
        response = compose('exec', '-T', '--user', '0', 'jobmanager', '/opt/flink/bin/sql-client.sh',
                           '-j', '/opt/flink/usrlib/flink-sql-connector-kafka-3.4.0-1.20.jar',
                           '-j', '/opt/flink/usrlib/flink-doris-connector-1.20-25.1.0.jar',
                           '--history', '/dev/null', '-f', '/opt/flink/smoke/processing.sql')
        matches = re.findall(r'Job ID:\s*([0-9a-f]{32})', response)
        if not matches:
            raise RuntimeError('No actual Flink job ID returned; reconcile before retry')
        job_id = matches[-1]
    if not re.fullmatch(r'[0-9a-f]{32}', job_id):
        raise RuntimeError('Invalid actual Flink job binding')
    for _ in range(90):
        state = component_api('jobmanager', 'GET', '/jobs/' + job_id)['state']
        if state == 'RUNNING':
            break
        if state in ['FAILED', 'CANCELED', 'FINISHED']:
            raise RuntimeError('Real isolated Flink job failed')
        time.sleep(1)
    else:
        raise RuntimeError('Real isolated Flink job did not start')
    expected = json.loads(source_sql("SELECT json_agg(json_build_object('id',id,'name',btrim(name),'amount',amount::text) ORDER BY id) FROM customers"))
    def normalized(rows):
        return {int(row['id']): (row['name'], None if row['amount'] is None else Decimal(str(row['amount']))) for row in rows}
    if not expected:
        raise RuntimeError('Independent source fixture is empty')
    expected = normalized(expected)
    for _ in range(90):
        output = compose('exec', '-T', 'doris-fe', 'sh', '-c',
            'address=${FE_SERVERS#*:}; address=${address%%:*}; MYSQL_PWD="$DORIS_ROOT_PASSWORD" exec mysql -h"$address" -P9030 -uroot -N -B',
            input="SELECT JSON_OBJECT('id',id,'name',name,'amount',CAST(amount AS STRING)) FROM datamesh_smoke.customers WHERE source_id='source-postgres-demo' AND object_id='public.customers' ORDER BY id;\n")
        actual = normalized([json.loads(line) for line in output.splitlines() if line.strip()])
        if actual == expected:
            break
        if component_api('jobmanager', 'GET', '/jobs/' + job_id)['state'] != 'RUNNING':
            raise RuntimeError('Actual job stopped before source/result reconciliation')
        time.sleep(1)
    else:
        raise RuntimeError('Actual source and processed result differ; no confirmed job binding written')
    private_write(intent, {'status': 'running', 'job_id': job_id})
    private_write(LOCAL / 'bindings/flink.json', {'job_id': job_id})


def compute_query(container, username, password, sql, allow_error=False, headers=False):
    if not re.fullmatch(r'[a-z][a-z0-9_]{0,63}', username) or '\n' in password:
        raise ValueError('Invalid compute identity input')
    command = ['docker', 'exec', '-i', container, 'sh', '-c',
               'IFS= read -r account; IFS= read -r password; MYSQL_PWD="$password" exec mysql --protocol=TCP -h127.0.0.1 -P9030 -u"$account" -B' + ('' if headers else ' -N')]
    query = 'SET exec_mem_limit=134217728; SET query_timeout=30;\n' + sql
    result = subprocess.run(command, input=username + '\n' + password + '\n' + query,
                            cwd=ROOT, text=True, capture_output=True, timeout=60)
    if result.returncode and not allow_error:
        raise RuntimeError('Compute SQL failed; credentials and private diagnostics withheld')
    return result.stdout, result.stderr, result.returncode


def workload_capabilities(container, admin_user, admin_password):
    output, _, _ = compute_query(container, admin_user, admin_password, 'DESC information_schema.workload_groups;')
    columns = {line.split('\t')[0].lower() for line in output.splitlines() if line}
    needed = {'name', 'max_concurrency', 'max_queue_size', 'queue_timeout'}
    if not needed <= columns:
        raise RuntimeError('Doris cannot verify required workload-group queue controls; shared readiness refused')
    properties = {'max_concurrency': '2', 'max_queue_size': '20', 'queue_timeout': '5000'}
    if {'min_memory_percent', 'max_memory_percent', 'min_cpu_percent', 'max_cpu_percent'} <= columns:
        properties.update(min_memory_percent='0%', max_memory_percent='10%', min_cpu_percent='0%', max_cpu_percent='10%')
    elif {'memory_limit', 'enable_memory_overcommit', 'cpu_share'} <= columns:
        properties.update(memory_limit='10%', enable_memory_overcommit='false', cpu_share='1')
    else:
        raise RuntimeError('Doris cannot verify an aggregate hard memory cap; shared readiness refused')
    for flag in ['enable_workload_group', 'enable_query_queue']:
        value, _, _ = compute_query(container, admin_user, admin_password, "SHOW GLOBAL VARIABLES LIKE '" + flag + "';")
        if value.strip() and value.strip().split('\t')[-1].lower() not in ['true', '1', 'on']:
            raise RuntimeError('Required Doris workload controls are disabled; global settings were not changed')
    groups = workload_group_stats(container, admin_user, admin_password)
    return properties, list(groups)



def workload_group_stats(container, username, password):
    # Doris4 exposes queue counters through SHOW, not information_schema.workload_groups.
    output, _, _ = compute_query(container, username, password, 'SHOW WORKLOAD GROUPS;', headers=True)
    lines = output.splitlines()
    if not lines:
        raise RuntimeError('Actual workload queue statistics are missing')
    columns = [name.lower() for name in lines[0].split('\t')]
    if not {'name', 'running_query_num', 'waiting_query_num'} <= set(columns):
        raise RuntimeError('Actual workload queue counters are unsupported; shared readiness refused')
    groups = {}
    for line in lines[1:]:
        cells = line.split('\t')
        if len(cells) != len(columns):
            raise RuntimeError('Malformed actual workload statistics')
        row = dict(zip(columns, cells))
        groups[row['name']] = (int(row['running_query_num']), int(row['waiting_query_num']))
    return groups

def validate_workload_group(container, username, password, properties):
    names = list(properties)
    output, _, _ = compute_query(container, username, password,
        'SELECT ' + ','.join(names) + " FROM information_schema.workload_groups WHERE name='experience_compute';")
    values = output.strip().split('\t')
    if len(values) != len(names):
        raise RuntimeError('Dedicated workload-group metadata is missing')
    for name, actual in zip(names, values):
        expected = properties[name]
        if expected in ['true', 'false']:
            matches = actual.lower() == expected
        else:
            matches = Decimal(actual.rstrip('%')) == Decimal(expected.rstrip('%'))
        if not matches:
            raise RuntimeError('Dedicated workload-group limits differ; existing groups were not altered')


def configure_workload_group(container, admin_user, admin_password, accounts, properties, existing_groups):
    if 'experience_compute' not in existing_groups:
        assignments = ','.join("'" + key + "'='" + value + "'" for key, value in properties.items())
        compute_query(container, admin_user, admin_password, 'CREATE WORKLOAD GROUP experience_compute PROPERTIES (' + assignments + ');')
    validate_workload_group(container, admin_user, admin_password, properties)
    for username, password in accounts:
        compute_query(container, admin_user, admin_password, "GRANT USAGE_PRIV ON WORKLOAD GROUP 'experience_compute' TO '" + username + "';")
        compute_query(container, admin_user, admin_password, "SET PROPERTY FOR '" + username + "' 'default_workload_group'='experience_compute';")
        output, _, _ = compute_query(container, admin_user, admin_password, "SHOW PROPERTY FOR '" + username + "';")
        binding = [line.split('\t') for line in output.splitlines() if line.split('\t')[0] == 'default_workload_group']
        if binding != [['default_workload_group', 'experience_compute']]:
            raise RuntimeError('Dedicated compute user is not persistently bound to its workload group')
        compute_query(container, username, password, "SET workload_group='experience_compute'; SELECT 1;")


def probe_workload_pressure(container, admin_user, admin_password, accounts, table):
    from concurrent.futures import ThreadPoolExecutor
    # Four one-row two-second queries bound both memory and work; no main business table is read.
    start = time.monotonic()
    compute_query(container, admin_user, admin_password, 'SELECT 1;')
    baseline = time.monotonic() - start
    max_latency, peak_running, peak_waiting = baseline, 0, 0
    with ThreadPoolExecutor(max_workers=4) as pool:
        pending = [pool.submit(compute_query, container, *accounts[index % len(accounts)],
                               "SELECT SLEEP(2) FROM " + table + ' WHERE id=1;') for index in range(4)]
        while any(not future.done() for future in pending):
            start = time.monotonic()
            compute_query(container, admin_user, admin_password, 'SELECT 1;')
            max_latency = max(max_latency, time.monotonic() - start)
            groups = workload_group_stats(container, admin_user, admin_password)
            if 'experience_compute' not in groups:
                raise RuntimeError('Dedicated group is missing from actual queue statistics')
            running, waiting = groups['experience_compute']
            peak_running, peak_waiting = max(peak_running, running), max(peak_waiting, waiting)
            if running > 2 or waiting > 20:
                raise RuntimeError('Observed dedicated workload queue exceeded its configured bound')
            time.sleep(.1)
        for future in pending:
            future.result()
    if peak_waiting < 1 or peak_running < 1:
        raise RuntimeError('Could not observe actual workload admission/queueing; shared readiness refused')
    if max_latency > max(2.0, baseline * 5):
        raise RuntimeError('Main Doris FE responsiveness degraded during the bounded experience probe')
    return {'peak_running': peak_running, 'peak_waiting': peak_waiting, 'fe_baseline_seconds': baseline,
            'fe_max_probe_seconds': max_latency, 'query_count': 4, 'probe_rows': 1}


def attach_compute():
    state = json.loads((LOCAL / 'deployment.json').read_text())
    if state.get('mode', 'lightweight') != 'lightweight':
        raise ValueError('Shared compute attachment applies only to lightweight mode')
    admin_password = os.environ.get('COMPUTE_ADMIN_PASSWORD', '')
    admin_user = os.environ.get('COMPUTE_ADMIN_USER', 'root')
    if len(admin_password) < 12:
        raise RuntimeError('Supply COMPUTE_ADMIN_PASSWORD transiently; it will never be saved in experience files')
    fe_ids = docker('ps', '-q', '--filter', 'label=com.docker.compose.project=datamesh',
                    '--filter', 'label=com.docker.compose.service=doris-fe').split()
    if len(fe_ids) != 1:
        raise RuntimeError('Exactly one running main Doris FE is required for explicit shared compute attachment')
    fe = docker('inspect', '--format', '{{.Id}}', fe_ids[0]).strip()
    credentials = json.loads((LOCAL / 'credentials.json').read_text())
    layers = ['internal', 'ods', 'quality', 'dwd', 'dim', 'dws', 'ads', 'smoke']
    databases = ['experience_' + layer for layer in layers]
    accounts = [('experience_reader', credentials['DORIS_READER_PASSWORD']),
                ('experience_writer', credentials['DORIS_WRITER_PASSWORD'])]
    if any(not re.fullmatch(r'[0-9a-f]{48}', password) for _, password in accounts):
        raise ValueError('Dedicated compute passwords must remain independent generated hex secrets')
    identity = {'fe': fe, 'network': PROJECT + '_compute', 'databases': databases,
                'workload_group': 'experience_compute', 'credentials_sha256': hashlib.sha256(json.dumps(accounts, sort_keys=True).encode()).hexdigest()}
    intent_path, ready_path = LOCAL / 'compute-intent.json', LOCAL / 'compute-ready.json'
    records = [json.loads(path.read_text()) for path in [intent_path, ready_path] if path.exists()]
    if any(record['identity'] != identity for record in records):
        raise RuntimeError('Compute ownership record differs; refusing namespace or credential replacement')
    output, _, _ = compute_query(fe, admin_user, admin_password, 'SHOW DATABASES;')
    existing_databases = output.splitlines()
    existing_users = []
    for username, _ in accounts:
        _, error, code = compute_query(fe, admin_user, admin_password, "SHOW GRANTS FOR '" + username + "';", allow_error=True)
        if not code:
            existing_users.append(username)
        elif not any(message in error.lower() for message in ['does not exist', 'not exist', 'unknown user']):
            raise RuntimeError('Could not establish dedicated compute user ownership')
    if any(name.startswith('experience_') and name not in databases for name in existing_databases):
        raise RuntimeError('Unexpected database in the experience prefix; refusing namespace adoption')
    if not records and (any(name.startswith('experience_') for name in existing_databases) or existing_users):
        raise RuntimeError('Experience namespace or users already exist without our ownership record; nothing was changed')
    if 'datamesh_dwd' not in existing_databases:
        raise RuntimeError('Expected existing main database is absent; cannot validate negative isolation probes')
    properties, existing_groups = workload_capabilities(fe, admin_user, admin_password)
    if 'experience_compute' in existing_groups and not records:
        raise RuntimeError('Dedicated workload group already exists without our ownership record')
    # Persist intent before any Docker/SQL mutation. Retry can reconcile this exact namespace only.
    private_write(intent_path, {'identity': identity, 'status': 'provisioning'})
    networks = docker('network', 'ls', '-q', '--filter', 'name=^' + PROJECT + '_compute$').split()
    if not networks:
        network_id = docker('network', 'create', '--internal', '--label', 'datamesh.experience.owner=' + PROJECT,
                             PROJECT + '_compute').strip()
    elif len(networks) == 1:
        network_id = networks[0]
    else:
        raise RuntimeError('Ambiguous dedicated compute network')
    network = json.loads(docker('network', 'inspect', network_id))[0]
    if not network.get('Internal') or (network.get('Labels') or {}).get('datamesh.experience.owner') != PROJECT:
        raise RuntimeError('Dedicated compute network must be internal and owned by this experience project')
    if fe not in (network.get('Containers') or {}):
        docker('network', 'connect', '--alias', 'doris-fe', network_id, fe)
    for database in databases:
        compute_query(fe, admin_user, admin_password, 'CREATE DATABASE IF NOT EXISTS ' + database + ';')
    for username, password in accounts:
        if username not in existing_users:
            compute_query(fe, admin_user, admin_password, "CREATE USER '" + username + "' IDENTIFIED BY '" + password + "';")
        # Existing users must authenticate with our original secret before any grant changes.
        compute_query(fe, username, password, 'SELECT 1;')
    for database in databases:
        compute_query(fe, admin_user, admin_password,
            "GRANT SELECT_PRIV, LOAD_PRIV, CREATE_PRIV, DROP_PRIV, ALTER_PRIV ON " + database + ".* TO 'experience_writer';")
    for layer in ['dwd', 'dim', 'dws', 'ads']:
        compute_query(fe, admin_user, admin_password, "GRANT SELECT_PRIV ON experience_" + layer + ".* TO 'experience_reader';")
    configure_workload_group(fe, admin_user, admin_password, accounts, properties, existing_groups)
    # The legacy result reader receives only its own explicit fixture table.
    legacy = (ROOT / 'tests/integration/fixtures/flink/doris-table.sql').read_text().replace('datamesh_smoke', 'experience_smoke')
    compute_query(fe, 'experience_writer', accounts[1][1], legacy)
    compute_query(fe, admin_user, admin_password, "GRANT SELECT_PRIV ON experience_smoke.customers TO 'experience_reader';")
    connection_limits = {}
    for username, _ in accounts:
        _, error, code = compute_query(fe, admin_user, admin_password,
            "SET PROPERTY FOR '" + username + "' 'max_user_connections' = '8';", allow_error=True)
        if code and not any(message in error.lower() for message in ['unknown property', 'unsupported', 'not support']):
            raise RuntimeError('Dedicated compute connection limit could not be configured')
        connection_limits[username] = 'unsupported' if code else 8
    marker = secrets.token_hex(16)
    table = 'experience_dwd.isolation_probe_' + marker
    compute_query(fe, 'experience_writer', accounts[1][1], 'CREATE TABLE ' + table +
        " (id INT NOT NULL,probe_value VARCHAR(64) NOT NULL) UNIQUE KEY(id) DISTRIBUTED BY HASH(id) BUCKETS 1 PROPERTIES ('replication_num'='1');\n" +
        "INSERT INTO " + table + " VALUES(1,'" + marker + "');")
    for username, password in accounts:
        output, _, _ = compute_query(fe, username, password, 'SELECT probe_value FROM ' + table + ' WHERE id=1;')
        if output.strip() != marker:
            raise RuntimeError('Dedicated compute positive read/write probe failed')
        _, error, code = compute_query(fe, username, password, 'USE datamesh_dwd;', allow_error=True)
        if not code or 'denied' not in error.lower():
            raise RuntimeError('Dedicated compute identity did not prove access denial to the main database')
    pressure = probe_workload_pressure(fe, admin_user, admin_password, accounts, table)
    compute_query(fe, 'experience_writer', accounts[1][1], 'DROP TABLE ' + table + ';')
    private_write(ready_path, {'identity': identity, 'status': 'verified', 'connection_limits': connection_limits,
        'probes': ['dedicated writer insert', 'dedicated reader select', 'reader denied main DB', 'writer denied main DB'],
        'workload_group': properties, 'pressure': pressure,
        'verified_at': datetime.now(timezone.utc).isoformat()})
    print('Dedicated compute namespace and internal attachment verified; main databases and services were preserved.')


def ensure_lightweight_sample_database():
    # Separate source DB/owner on our private metadata PG avoids another database server.
    compose('exec', '-T', 'postgres', 'sh', '-c',
        'exec psql -U datamesh_owner -d datamesh -v ON_ERROR_STOP=1 --set=sample_password="$SOURCE_DB_PASSWORD"', input=r"""
SELECT format('CREATE ROLE source_owner LOGIN PASSWORD %L', :'sample_password') WHERE NOT EXISTS(SELECT 1 FROM pg_roles WHERE rolname='source_owner') \gexec
SELECT 'CREATE DATABASE sample OWNER source_owner' WHERE NOT EXISTS(SELECT 1 FROM pg_database WHERE datname='sample') \gexec
""")



def prepare_delivery_fixture():
    state = json.loads((LOCAL / 'deployment.json').read_text())
    if state.get('mode') != 'lightweight':
        raise RuntimeError('Delivery fixture currently requires the lightweight isolated database')
    path = LOCAL / 'delivery-fixture.json'
    if path.exists():
        fixture = json.loads(path.read_text())
        if fixture.get('status') != 'prepared':
            raise RuntimeError('Prior delivery fixture provisioning requires reconciliation before retry')
        return fixture
    args = ('exec', '-T', 'postgres', 'psql', '-U', 'datamesh_owner', '-d', 'sample',
            '-v', 'ON_ERROR_STOP=1', '-At')
    existing = compose(*args, input="SELECT (SELECT count(*) FROM pg_roles WHERE rolname='experience_etl') + (SELECT count(*) FROM pg_namespace WHERE nspname='experience_delivery');").strip()
    if existing != '0':
        raise RuntimeError('Delivery destination exists without our ownership record; refusing adoption')
    fixture = {'status': 'provisioning', 'synthetic': True, 'kind': 'postgres', 'host': 'postgres-source',
        'port': 5432, 'database': 'sample', 'schema': 'experience_delivery', 'table': 'customers',
        'credentials': {'username': 'experience_etl', 'password': secrets.token_hex(24)},
        'fields': ['id', 'name', 'age', 'city'], 'keys': ['id']}
    private_write(path, fixture)  # Retain intent if the database response is lost; never silently adopt objects.
    compose(*args, input="""BEGIN;
CREATE ROLE experience_etl LOGIN NOSUPERUSER NOCREATEDB NOCREATEROLE NOREPLICATION PASSWORD '%s';
GRANT CONNECT ON DATABASE sample TO experience_etl;
CREATE SCHEMA experience_delivery AUTHORIZATION source_owner;
SET ROLE source_owner;
CREATE TABLE experience_delivery.customers (id bigint PRIMARY KEY, name varchar(100), age integer, city varchar(100));
GRANT USAGE ON SCHEMA experience_delivery TO experience_etl;
GRANT SELECT, INSERT, UPDATE, DELETE ON experience_delivery.customers TO experience_etl;
COMMIT;
""" % fixture['credentials']['password'])
    fixture['status'] = 'prepared'
    private_write(path, fixture)
    return fixture


def register_delivery_fixture():
    fixture = prepare_delivery_fixture()
    accounts = json.loads((LOCAL / 'accounts.json').read_text())
    provider = Client(accounts['provider'])
    try:
        if fixture.get('source_id'):
            provider.call('GET', '/api/v1/sources/' + fixture['source_id'])
            return fixture
        source = provider.call('POST', '/api/v1/sources', {
            'name': '独立体验ETL交付目标', **{key: fixture[key] for key in ['kind', 'host', 'port', 'database', 'credentials']},
            'owner_id': provider.principal['user_id']}, str(uuid.uuid5(uuid.NAMESPACE_URL, 'experience-delivery-target-v1')))
        fixture['source_id'] = source['source_id']
        private_write(LOCAL / 'delivery-fixture.json', fixture)
        return fixture
    finally:
        provider.close()


def ensure_processing_authorization(reviewer, actor, resource, fields):
    if reviewer.principal['user_id'] == actor.principal['user_id']:
        raise ValueError('Processing authorization requires a different reviewer identity')
    rows = reviewer.call('GET', '/api/v1/processing-authorizations?page_size=100')['items']
    now = datetime.now(timezone.utc)
    for row in rows:
        definition = row['definition']
        if row['state'] == 'active' and definition['resource'] == resource and definition['user_id'] == actor.principal['user_id'] and \
           definition['purpose'] == '数据治理' and definition['allow_derived'] and definition['allow_sharing'] and \
           set(fields) <= set(definition['fields']) and datetime.fromisoformat(definition['expires_at'].replace('Z', '+00:00')) > now:
            return row
    return reviewer.call('POST', '/api/v1/processing-authorizations', {
        'resource': resource, 'user_id': actor.principal['user_id'], 'fields': fields,
        'purpose': '数据治理', 'expires_at': (now + timedelta(days=30)).isoformat(),
        'allow_derived': True, 'allow_sharing': True,
        'sharing_basis': '独立虚构数据责任方允许真实治理及审批后的受控共享',
        'approval_ref': 'EXPERIENCE-LIGHT-INDEPENDENT-REVIEW'})


def approve_delivery_request(applicant, reviewer, resource, fields, purpose):
    if applicant.principal['user_id'] == reviewer.principal['user_id']:
        raise ValueError('Controlled delivery requires a different approver identity')
    request = {'materials': {'legal_basis': '独立虚构体验', 'scenario': '受控共享核验', 'scope': '独立样例客户',
                'method': 'query', 'department_approval_ref': 'EXPERIENCE-REQUEST'},
               'resource': resource, 'department_id': applicant.principal['department_id'], 'purpose': purpose,
               'fields': fields, 'expires_at': (datetime.now(timezone.utc) + timedelta(days=7)).isoformat()}
    application = applicant.call('POST', '/api/v1/applications', request)
    applicant.call('POST', '/api/v1/applications/' + application['application_id'] + '/approve', expected=403)
    reviewer.call('POST', '/api/v1/applications/' + application['application_id'] + '/approve')
    return application

def bootstrap_lightweight():
    # Reuse established fixture/model/flow contracts, but override every main-instance boundary.
    from seed_demo_assets import Seed, CUSTOMER_FIELDS, QUALITY_CHECKS
    ensure_lightweight_sample_database()
    accounts = json.loads((LOCAL / 'accounts.json').read_text())
    clients = []
    try:
        admin = Client(json.loads((LOCAL / 'bootstrap-admin.json').read_text())); clients.append(admin)
        provider = Client(accounts['provider']); clients.append(provider)
        reviewer = Client(accounts['directory_reviewer']); clients.append(reviewer)
        governor = Client(accounts['governor']); clients.append(governor)
        expiry = (datetime.now(timezone.utc) + timedelta(days=30)).isoformat()
        assignments = admin.call('GET', '/api/v1/resource-assignments?page_size=100')['items']
        for actor, capabilities in [(provider, ['manage_resource', 'operate_resource', 'govern_resource']),
                                    (reviewer, ['department_review', 'mount_review', 'share_approve']),
                                    (governor, ['govern_resource']), (admin, ['authority_review'])]:
            for capability in capabilities:
                department = '*' if actor == admin else actor.principal['department_id']
                if not any(row['state'] == 'active' and row['definition']['user_id'] == actor.principal['user_id'] and
                           row['department_id'] == department and row['definition']['capability'] == capability and
                           datetime.fromisoformat(row['definition']['expires_at'].replace('Z', '+00:00')) > datetime.now(timezone.utc)
                           for row in assignments):
                    admin.call('POST', '/api/v1/resource-assignments', {'user_id': actor.principal['user_id'],
                        'department_id': department, 'resource_id': '*', 'capability': capability, 'expires_at': expiry,
                        'reason': '独立轻量体验虚构数据岗位授权；提交人与复核人分离'})

        class IsolatedSeed(Seed):
            def __init__(self):
                self.path = LOCAL / 'lightweight-progress.json'
                self.state = json.loads(self.path.read_text()) if self.path.exists() else {'version': 1, 'synthetic': True, 'steps': {}}
                self.env = json.loads((LOCAL / 'credentials.json').read_text())
                self.principal = provider.principal

            def request(self, path, body=None, key=None, csrf=True):
                token = str(uuid.uuid5(uuid.NAMESPACE_URL, 'datamesh-experience-light:' + key)) if key else None
                return provider.call('POST' if body is not None else 'GET', path, body, token)

            def step(self, name, fn):
                if name not in self.state['steps']:
                    self.state['steps'][name] = fn()
                    private_write(self.path, self.state)
                return self.state['steps'][name]

            def sql(self, query, source=False):
                if not source:
                    raise RuntimeError('Lightweight seed cannot directly modify application metadata')
                return source_sql(query)

            def approve(self, target):
                for _ in range(4):
                    rows = admin.call('GET', '/api/v1/directory-reviews?page_size=100')['items']
                    pending = [row for row in rows if row['definition']['target_id'] == target]
                    if not pending:
                        return
                    for row in pending:
                        actor = admin if row['definition']['stage'] == 'authority' else reviewer
                        actor.call('POST', '/api/v1/directory-reviews/' + row['review_id'] + '/approve',
                                   {'expected_version': row['version'], 'reason': '独立身份核对虚构来源与实际治理结果'})
                raise RuntimeError('Actual independent directory review did not finish')

        seed = IsolatedSeed()
        seed.prepare_source()
        seed.step('dictionary', lambda: admin.call('POST', '/api/v1/directory-dictionaries', {
            'name': '独立轻量体验自定义字典', 'version': 'experience-light-v1', 'source': '虚构数据自定义编码，不代表正式国家字典',
            'verified': True, 'test_only': False, 'code_pattern': '^DEMO-[A-Z0-9-]+$',
            'classifications': ['department/demo', 'theme/demo'], 'verification_ref': '用户授权的独立虚构样例初始化'},
            str(uuid.uuid5(uuid.NAMESPACE_URL, 'experience-light-dictionary'))))
        workspace = seed.step('workspace', lambda: seed.request('/api/v1/governance/workspaces', {
            'name': '独立轻量体验治理空间', 'description': '实际批量治理，使用隔离Doris数据库与专属服务身份', 'max_concurrent_runs': 1}, 'workspace'))
        seed.step('source', lambda: seed.request('/api/v1/sources', {'name': '独立轻量客户与订单样例', 'kind': 'postgres',
            'host': 'postgres-source', 'port': 5432, 'database': 'sample', 'owner_id': provider.principal['user_id'],
            'credentials': {'username': 'source_owner', 'password': seed.env['SOURCE_DB_PASSWORD']}}, 'source'))
        source = seed.source_resource('customers', '独立体验客户来源', '八条虚构客户记录，包括可验证的质量问题', CUSTOMER_FIELDS)
        fields = [field['name'] for field in CUSTOMER_FIELDS]
        ensure_processing_authorization(reviewer, provider, source, fields)
        nodes = [{'id': 'customers', 'kind': 'source', 'source': {'resource': source, 'fields': fields}},
                 {'id': 'clean', 'kind': 'transform', 'inputs': [{'node': 'customers'}], 'columns': [
                     {'source': field, 'target': field, 'rule': 'trim' if field in ['name', 'city'] else 'copy'} for field in fields]},
                 {'id': 'quality', 'kind': 'quality', 'inputs': [{'node': 'clean'}], 'checks': QUALITY_CHECKS}]
        model = seed.model('quality', '独立体验客户结果模型', CUSTOMER_FIELDS, .3)
        nodes.append({'id': 'output', 'kind': 'output', 'inputs': [{'node': 'quality', 'port': 'good'}], 'model': model})
        run = seed.flow('quality', '独立体验客户质量处理', '真实读取、去空格、质量隔离并写入专属Doris数据库', nodes)
        asset = seed.publish_asset('quality', '独立体验客户成果', '八条来源保留六条合格记录，两个问题行真实隔离', CUSTOMER_FIELDS, run, 6, 2)
        seed.step('quality-delivery-access', lambda: approve_delivery_request(provider, reviewer, asset['resource'], fields, '独立体验发布者核验'))
        # Confirm values through normal authorized delivery, not only reported counts.
        records = seed.request('/delivery/v1/datasets/' + run['id'] + '/records?contract_version=1&limit=20')['records']
        if {int(row['id']) for row in records} != {1, 2, 3, 6, 7, 8} or any(
            not row['name'] or row['name'] != row['name'].strip() or row['city'] != row['city'].strip() or
            not 0 <= int(row['age']) <= 120 for row in records):
            raise RuntimeError('Actual lightweight delivery differs from expected quality output')
        private_write(LOCAL / 'bootstrap-result.json', {'engine': 'bounded-doris', 'workspace_id': workspace['id'],
            'run_id': run['id'], 'dataset_id': run['id'], 'source_resource': source, 'asset': asset, 'rows': len(records)})
        print('Real bounded governance, independent review and six-row delivery verified; streaming profiles remain optional.')
    finally:
        for client in clients:
            client.close()


def bootstrap():
    state = json.loads((LOCAL / 'deployment.json').read_text())
    if state.get('mode', 'lightweight') == 'lightweight':
        bootstrap_lightweight()
    else:
        bootstrap_full()


def bootstrap_full():
    bootstrap_pipeline()
    accounts = json.loads((LOCAL / 'accounts.json').read_text())
    clients = []
    try:
        admin = Client(json.loads((LOCAL / 'bootstrap-admin.json').read_text())); clients.append(admin)
        provider = Client(accounts['provider']); clients.append(provider)
        reviewer = Client(accounts['directory_reviewer']); clients.append(reviewer)
        governor = Client(accounts['governor']); clients.append(governor)
        env = json.loads((LOCAL / 'credentials.json').read_text())
        expiry = (datetime.now(timezone.utc) + timedelta(days=30)).isoformat()
        assignments = admin.call('GET', '/api/v1/resource-assignments?page_size=100')['items']
        for client, capabilities in [(provider, ['manage_resource', 'operate_resource', 'govern_resource']),
                                     (reviewer, ['department_review', 'mount_review', 'share_approve']),
                                     (governor, ['govern_resource']), (admin, ['authority_review'])]:
            for capability in capabilities:
                department = '*' if client == admin else client.principal['department_id']
                if not any(row['state'] == 'active' and row['definition']['user_id'] == client.principal['user_id'] and
                           row['department_id'] == department and row['definition']['capability'] == capability for row in assignments):
                    admin.call('POST', '/api/v1/resource-assignments', {'user_id': client.principal['user_id'],
                               'department_id': department, 'resource_id': '*', 'capability': capability,
                               'expires_at': expiry, 'reason': '独立体验环境虚构数据岗位授权，提交人与复核人分离'})
        dictionaries = admin.call('GET', '/api/v1/directory-dictionaries')['items']
        dictionary = next((row for row in dictionaries if row['definition']['name'] == '独立体验自定义字典'), None)
        if dictionary is None:
            dictionary = admin.call('POST', '/api/v1/directory-dictionaries', {'name': '独立体验自定义字典',
                'version': 'experience-v1', 'source': '隔离虚构数据自定义编码，不代表正式国家编码', 'verified': True,
                'test_only': False, 'code_pattern': '^TEST-[A-Za-z0-9-]+$', 'classifications': ['department/local-test'],
                'verification_ref': '用户授权的独立虚构数据体验初始化'})
        sources = provider.call('GET', '/api/v1/sources?page_size=100')['items']
        source_ids = {}
        for kind, host, port, database, username, password in [
            ('postgres', 'postgres-source', 5432, 'sample', 'datamesh_cdc', env['SOURCE_CDC_PASSWORD']),
            ('mysql', 'mysql-source', 3306, 'sample', 'datamesh_cdc', env['SOURCE_CDC_PASSWORD']),
            ('oracle', 'oracle-source', 1521, 'FREEPDB1', 'DATAMESH_SOURCE', env['SOURCE_DB_PASSWORD'])]:
            name = '独立体验样例-' + kind
            source = next((row for row in sources if row['name'] == name), None)
            if source is None:
                source = provider.call('POST', '/api/v1/sources', {'name': name, 'kind': kind, 'host': host,
                    'port': port, 'database': database, 'owner_id': provider.principal['user_id'],
                    'credentials': {'username': username, 'password': password}})
            if provider.call('POST', '/api/v1/sources/' + source['source_id'] + '/test-connection').get('passed') is not True:
                raise RuntimeError('Actual sample database connectivity failed')
            source_ids[kind] = source['source_id']
        workspaces = governor.call('GET', '/api/v1/governance/workspaces')['items']
        workspace = next((row for row in workspaces if row['name'] == '独立体验治理空间'), None)
        if workspace is None:
            workspace = governor.call('POST', '/api/v1/governance/workspaces', {'name': '独立体验治理空间',
                'description': '独立虚构数据治理体验，所有运行通过真实后端执行', 'max_concurrent_runs': 1})
        source = source_ids['postgres']
        if not provider.call('POST', '/api/v1/sources/' + source + '/preflight-cdc').get('passed'):
            raise RuntimeError('Actual PostgreSQL CDC preflight failed')
        obj = provider.call('GET', '/api/v1/sources/' + source + '/objects')['items'][0]
        sys.path.insert(0, str(ROOT / 'scripts/smoke'))
        from directory import prepare_processing, compile_and_mount
        prepare_processing(reviewer, provider, obj)
        tasks = provider.call('GET', '/api/v1/tasks?page_size=100')['items']
        task = next((row for row in tasks if row['object_id'] == obj['object_id']), None)
        if task is None:
            task = provider.call('POST', '/api/v1/tasks', {'source_id': source, 'object_id': obj['object_id'],
                'mode': 'cdc', 'dataset_id': None, 'schema_version': 1, 'rule_version': None})
        provider.call('POST', '/api/v1/tasks/' + task['task_id'] + '/start')
        provider.call('POST', '/api/v1/tasks/' + task['task_id'] + '/archive')
        rules = provider.call('GET', '/api/v1/rules?page_size=100')['items']
        if not any(row['definition']['object_id'] == obj['object_id'] and row['state'] == 'published' for row in rules):
            rule = provider.call('POST', '/api/v1/rules', {'name': '独立体验质量规则', 'object_id': obj['object_id'], 'schema_version': 1,
                'checks': [{'field': 'id', 'kind': 'required', 'blocking': True, 'parameters': {}},
                           {'field': 'id', 'kind': 'business_key', 'blocking': True, 'parameters': {}}]})
            provider.call('POST', '/api/v1/rules/' + rule['rule_id'] + '/publish')
        datasets = provider.call('GET', '/api/v1/datasets?page_size=100')['items']
        dataset = next((row for row in datasets if row['definition']['name'] == '独立体验客户成果'), None)
        if dataset is None:
            dataset = provider.call('POST', '/api/v1/datasets', {'name': '独立体验客户成果', 'description': '独立样例源经真实CDC与Flink处理形成的虚构客户数据',
                'source_object_id': obj['object_id'], 'owner_id': provider.principal['user_id'],
                'fields': obj['fields'], 'usage_restrictions': '仅用于独立体验，不用于真实业务决策'})
        quality = provider.call('POST', '/api/v1/datasets/' + dataset['dataset_id'] + '/check')
        if quality['status'] != 'passed' or quality['failed_count'] != 0:
            raise RuntimeError('Actual isolated quality gate failed')
        if dataset['publication_status'] != 'published':
            provider.call('POST', '/api/v1/datasets/' + dataset['dataset_id'] + '/publish')
        directories = []
        for kind, identifier, fields in [('raw_object', obj['object_id'], obj['fields']),
                                         ('governed_dataset', dataset['dataset_id'], dataset['fields'])]:
            directories.append(compile_and_mount(provider, reviewer, admin, dictionary['id'],
                {'resource_type': kind, 'resource_id': identifier, 'contract_version': 1}, fields,
                '独立体验客户' + kind, '独立虚构数据，经不同身份审核后挂载', open_summary=True))
        state = {'workspace_id': workspace['id'], 'source_ids': source_ids, 'object_id': obj['object_id'], 'task_id': task['task_id'],
                 'dataset_id': dataset['dataset_id'], 'directories': directories, 'quality': quality}
        private_write(LOCAL / 'bootstrap-result.json', state)
        print('Real sample sources, CDC job, archive, quality and independently reviewed directory mounts initialized.')
    finally:
        for client in clients:
            client.close()



def acceptance_status():
    code_path, browser_path = LOCAL / 'code-verification.json', LOCAL / 'browser-verification.json'
    code = json.loads(code_path.read_text()) if code_path.exists() else {}
    browser = json.loads(browser_path.read_text()) if browser_path.exists() else {}
    sequence_path = LOCAL / 'two-script-verification.json'
    sequence = json.loads(sequence_path.read_text()) if sequence_path.exists() else {}
    sequence_passed = sequence.get('state') == 'succeeded' and sequence.get('values_checked') is True and sequence.get('max_jobs') == 1 and sequence.get('peak_sandbox_containers') == 1
    code_passed = code.get('result') == 'passed' and code.get('namespace') == PROJECT and code.get('remaining_own_jobs') == 0
    browser_passed = browser.get('realBackend') is True and browser.get('dualCookieIsolation') is True and browser.get('mainPublicBoundaryPreserved') is True
    stream_path = LOCAL / 'script-stream-evidence.json'
    stream = json.loads(stream_path.read_text()) if stream_path.exists() else {}
    stream_passed = stream.get('status') == 'passed' and stream.get('namespace') == PROJECT and stream.get('synthetic') is True
    return {'normal_role_business_lifecycle': 'passed',
            'code_sandbox_integration': 'passed' if code_passed else 'not_verified',
            'ordinary_two_script_governance': 'passed' if sequence_passed else 'not_verified',
            'browser_dual_cookie_and_main_boundary': 'passed' if browser_passed else 'not_verified',
            'postgresql_continuous_python_javascript': 'passed' if stream_passed else 'not_verified',
            'deployed_security_policy_full_lifecycle': 'not_end_to_end_verified',
            'external_api_and_uncovered_carriers': 'not_verified',
            'optional_streaming_and_oracle_acceptance': 'not_verified'}

def verify():
    (LOCAL / 'verification.json').unlink(missing_ok=True)
    state = json.loads((LOCAL / 'bootstrap-result.json').read_text())
    accounts = json.loads((LOCAL / 'accounts.json').read_text())
    clients = []
    try:
        consumer = Client(accounts['consumer']); clients.append(consumer)
        reviewer = Client(accounts['directory_reviewer']); clients.append(reviewer)
        auditor = Client(accounts['auditor']); clients.append(auditor)
        if state.get('engine') == 'bounded-doris':
            provider = Client(accounts['provider']); clients.append(provider)
            job = state['run_id']
            run = provider.call('GET', '/api/v1/governance/runs/' + job + '?workspace_id=' + state['workspace_id'])
            if run['state'] != 'succeeded' or run['result']['output_rows'] != 6:
                raise RuntimeError('Actual bounded governance run no longer verifies')
        else:
            job = json.loads((LOCAL / 'bindings/flink.json').read_text())['job_id']
            if component_api('jobmanager', 'GET', '/jobs/' + job)['state'] != 'RUNNING':
                raise RuntimeError('Bound actual job is not running')
        approve_delivery_request(consumer, reviewer,
            {'resource_type': 'governed_dataset', 'resource_id': state['dataset_id'], 'contract_version': 1},
            ['id', 'name'], '独立体验受控交付核验')
        result = consumer.call('GET', '/delivery/v1/datasets/' + state['dataset_id'] + '/records?contract_version=1&limit=10')
        if not result['records'] or any(set(row) != {'id', 'name'} for row in result['records']):
            raise RuntimeError('Actual controlled delivery projection failed')
        audit = auditor.call('GET', '/api/v1/audit?search=deliver_')
        if audit['total'] == 0:
            raise RuntimeError('Actual delivery audit missing')
        private_write(LOCAL / 'verification.json', {'baseline_business_lifecycle': 'passed', 'job_id': job,
            'engine': state.get('engine', 'flink'), 'rows_checked': len(result['records']), 'verified_at': datetime.now(timezone.utc).isoformat(),
            'security_acceptance': acceptance_status()})
        print('Real baseline lifecycle verified; full security and all-feature acceptance remain pending.')
    finally:
        for client in clients:
            client.close()


def main():
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument('action', choices=['prepare', 'preflight', 'up', 'provision', 'bootstrap', 'verify', 'fixtures', 'enable', 'attach-compute', 'status', 'stop'])
    parser.add_argument('--subnet', default='172.30.239.0/24')
    parser.add_argument('--mode', choices=['lightweight', 'full'], default='lightweight')
    parser.add_argument('--feature', action='append', choices=['code', 'cdc', 'streaming', 'mysql', 'oracle'], default=[])
    args = parser.parse_args()
    if args.action == 'prepare':
        prepare(args.subnet, args.mode, args.feature)
    elif args.action == 'preflight':
        preflight()
    elif args.action in ['up', 'enable']:
        if args.action == 'enable':
            if not args.feature:
                raise ValueError('Select a feature to enable')
            state = json.loads((LOCAL / 'deployment.json').read_text())
            state['features'] = sorted(set(state.get('features', [])) | set(args.feature))
            private_write(LOCAL / 'deployment.json', state)
        preflight()  # Mandatory, before the first container mutation.
        config = save_config()
        backend = [name for name, service in config['services'].items() if name not in ['server', 'ingress', 'migrate'] and not service.get('profiles')]
        try:
            compose('up', '-d', '--wait', '--wait-timeout', '600', '--pull', 'never', *backend)
            compose('run', '--rm', '--no-deps', 'migrate')
            provision()
            compose('up', '-d', '--wait', '--wait-timeout', '60', '--pull', 'never', 'server', 'ingress')
            wait_http_ready()
            bootstrap()
            verify()
        except (Exception, KeyboardInterrupt):
            compose('stop')  # Only this project; preserve independent data for diagnosis.
            raise
        print('Real services and baseline business lifecycle verified on https://localhost:19443; additional security acceptance remains required.')
    elif args.action == 'provision':
        provision()
    elif args.action == 'bootstrap':
        bootstrap()
    elif args.action == 'verify':
        verify()
    elif args.action == 'fixtures':
        register_delivery_fixture()
        print('Isolated ETL destination registered; private connection guide: .local/experience/delivery-fixture.json. No ETL delivery has run yet.')
    elif args.action == 'attach-compute':
        attach_compute()
    elif args.action == 'status':
        print(compose('ps'), end='')
    else:
        compose('stop')
        print('Experience project stopped; independent volumes preserved.')


if __name__ == '__main__':
    try:
        main()
    except (RuntimeError, ValueError, KeyError, OSError, subprocess.SubprocessError):
        # RuntimeError messages are deliberately credential-free; unexpected failures stay private.
        exc = sys.exc_info()[1]
        print(str(exc) if isinstance(exc, RuntimeError) else 'Experience operation failed; private details withheld.', file=sys.stderr)
        sys.exit(1)
