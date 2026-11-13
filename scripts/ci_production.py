"""Build, verify and optionally publish immutable native production candidates."""
import argparse
import hashlib
import json
import os
from pathlib import Path
import re
import shutil
import subprocess
import sys
from urllib.error import HTTPError
from urllib.request import Request, urlopen

from package_release import schema_version

ROOT = Path(__file__).resolve().parents[1]
UPSTREAM = {
    'POSTGRES_IMAGE': 'postgres:17.11-alpine',
    'RUSTFS_IMAGE': 'rustfs/rustfs:1.0.0',
    'DORIS_FE_IMAGE': 'apache/doris:fe-4.0.3',
    'DORIS_BE_IMAGE': 'apache/doris:be-4.0.3',
    'FLINK_IMAGE': 'flink:1.20.3-scala_2.12-java17',
    'KAFKA_IMAGE': 'apache/kafka:3.9.1',
    'CONNECT_IMAGE': 'quay.io/debezium/connect:3.6.3.Final',
}
COMPONENTS = {'APP_IMAGE': 'datamesh', 'SOURCE_READER_IMAGE': 'datamesh-source-reader', 'CODE_PYTHON_REF': 'datamesh-python', 'CODE_JAVASCRIPT_REF': 'datamesh-javascript'}


def run(args, **kwargs):
    result = subprocess.run(args, cwd=ROOT, check=True, **kwargs)
    return result.stdout


def image_info(reference):
    result = json.loads(run(['docker', 'image', 'inspect', reference], capture_output=True, text=True))[0]
    if result['Os'] != 'linux' or result['Architecture'] != 'amd64':
        raise RuntimeError('Production candidate is not native linux/amd64')
    return result


def existing_tag(owner, component, tag):
    token = os.environ.get('GITHUB_TOKEN', '')
    for page in range(1, 101):
        url = f'https://api.github.com/users/{owner}/packages/container/{component}/versions?per_page=100&page={page}'
        try:
            with urlopen(Request(url, headers={'Authorization': 'Bearer ' + token, 'Accept': 'application/vnd.github+json', 'X-GitHub-Api-Version': '2022-11-28'}), timeout=30) as response:
                versions = json.load(response)
        except HTTPError as error:
            if error.code == 404: return None
            raise RuntimeError('Cannot verify immutable package tags') from None
        for version in versions:
            if tag in version['metadata']['container']['tags']: return version['name']
        if len(versions) < 100: return None
    raise RuntimeError('Package tag enumeration exceeded its bound')


def download_verified(url, target, digest):
    if not url.startswith('https://repo.maven.apache.org/') or not re.fullmatch(r'[a-f0-9]{64}', digest):
        raise RuntimeError('Unapproved plugin source or digest')
    target.parent.mkdir(parents=True, exist_ok=True)
    checksum = hashlib.sha256()
    size = 0
    with urlopen(url, timeout=60) as response, target.open('wb') as stream:
        while block := response.read(1024 * 1024):
            size += len(block)
            if size > 128 * 1024 * 1024: raise RuntimeError('Plugin exceeds download budget')
            checksum.update(block); stream.write(block)
    if checksum.hexdigest() != digest: raise RuntimeError('Plugin checksum mismatch')


def script_probe(image, language):
    source = 'def transform(row, context):\n return {"code": row["code"], "amount": str(Decimal(row["amount"])), "empty": row["empty"], "missing": row["missing"]}' if language == 'python' else 'function transform(row, context) { return {code: row.code, amount: new Decimal(row.amount).toFixed(6), empty: row.empty, missing: row.missing}; }'
    values = {'code': '001', 'amount': '9007199254740993.123456', 'empty': '', 'missing': None}
    payload = json.dumps({'source': source, 'context': {}}) + '\n' + json.dumps({'sequence': 1, 'rows': [{'ordinal': 1, 'values': values}]}) + '\n'
    result = run(['docker', 'run', '--rm', '-i', '--network', 'none', '--memory', '256m', '--read-only', '--cap-drop', 'ALL', '--security-opt', 'no-new-privileges:true', image], input=payload, capture_output=True, text=True, timeout=45)
    output = [json.loads(line) for line in result.splitlines()]
    if len(output) != 2 or output[0].get('type') != 'ready' or output[1].get('rows') != [{'ordinal': 1, 'values': values}]:
        raise RuntimeError('Native script runtime precision/null protocol failed')


def main(publish):
    sha = os.environ.get('GITHUB_SHA', '')
    owner = os.environ.get('GITHUB_REPOSITORY_OWNER', '').lower()
    if not re.fullmatch(r'[a-f0-9]{40}', sha) or not re.fullmatch(r'[a-z0-9-]+', owner): raise RuntimeError('Explicit source revision and owner required')
    if publish and (os.environ.get('GITHUB_REF') != 'refs/heads/main' or os.environ.get('GITHUB_EVENT_NAME') != 'push'):
        raise RuntimeError('Candidates may only be published from main push CI')
    directory = ROOT / 'artifacts/production'
    if directory.exists(): raise RuntimeError('Refusing to overwrite a candidate directory')
    directory.mkdir(parents=True)
    if publish:
        token = os.environ.get('GITHUB_TOKEN', '')
        if not token: raise RuntimeError('Package publication token missing')
        run(['docker', 'login', 'ghcr.io', '--username', os.environ['GITHUB_ACTOR'], '--password-stdin'], input=token, text=True, capture_output=True)
    images = {}
    for key, reference in UPSTREAM.items():
        run(['docker', 'pull', '--platform', 'linux/amd64', reference])
        metadata = image_info(reference)
        if not metadata.get('RepoDigests'): raise RuntimeError('Upstream digest missing')
        images[key] = {'reference': metadata['RepoDigests'][0], 'config_id': metadata['Id'], 'version_reference': reference, 'architecture': 'amd64'}

    recipes = {
        'APP_IMAGE': ['-f', 'Dockerfile', '--build-arg', 'SOURCE_REVISION=' + sha, '--build-arg', 'RELEASE_VERSION=sha-' + sha, '--build-arg', 'BASE_PATH=/datamesh', '.'],
        'SOURCE_READER_IMAGE': ['-f', 'tools/source-reader/Dockerfile', '--build-arg', 'MAVEN_IMAGE=maven:3.9.9-eclipse-temurin-21', '--build-arg', 'JAVA_IMAGE=eclipse-temurin:21-jre', '.'],
        'CODE_PYTHON_REF': ['-f', 'deploy/docker/governance-code/python.Dockerfile', '--build-arg', 'PYTHON_BASE_IMAGE=python:3.14.9-slim', '.'],
        'CODE_JAVASCRIPT_REF': ['-f', 'deploy/docker/governance-code/javascript.Dockerfile', '--build-arg', 'NODE_BASE_IMAGE=node:24.22.1-slim', '.'],
    }
    local = {}
    existing = {}
    for key, component in COMPONENTS.items():
        target = f'ghcr.io/{owner}/{component}:sha-{sha}'
        digest = existing_tag(owner, component, 'sha-' + sha) if publish else None
        if digest:
            run(['docker', 'pull', '--platform', 'linux/amd64', target]); existing[key] = digest
        else:
            run(['docker', 'build', '--platform', 'linux/amd64', '--label', 'org.opencontainers.image.source=https://github.com/' + os.environ['GITHUB_REPOSITORY'], '--label', 'org.opencontainers.image.revision=' + sha, '--label', 'org.opencontainers.image.version=sha-' + sha, '--label', 'org.opencontainers.image.licenses=NOASSERTION', '-t', target, *recipes[key]])
        metadata = image_info(target)
        if metadata['Config'].get('Labels', {}).get('org.opencontainers.image.revision') != sha:
            raise RuntimeError('Candidate source revision mismatch')
        local[key] = target
    report = directory / 'image-verification.json'
    run([sys.executable, 'scripts/verify_production_image.py', '--image', local['APP_IMAGE'], '--postgres-image', images['POSTGRES_IMAGE']['reference'], '--output', str(report)])
    script_probe(local['CODE_PYTHON_REF'], 'python'); script_probe(local['CODE_JAVASCRIPT_REF'], 'javascript')
    plugin = ROOT / 'artifacts/stream-plugin'
    run(['docker', 'build', '--platform', 'linux/amd64', '--build-arg', 'FLINK_BUILD_IMAGE=' + images['FLINK_IMAGE']['reference'], '--build-arg', 'MAVEN_IMAGE=maven:3.9.9-eclipse-temurin-21', '--output', 'type=local,dest=' + str(plugin), '-f', 'tools/stream-script/Dockerfile', 'tools/stream-script'])
    (directory / 'plugins/flink').mkdir(parents=True)
    shutil.copy2(plugin / 'datamesh-stream-script.jar', directory / 'plugins/flink/datamesh-stream-script.jar')
    versions = json.loads((ROOT / 'deploy/versions.lock.json').read_text())
    for entry in versions['plugins']:
        if Path(entry['file']).name != entry['file']: raise RuntimeError('Invalid plugin filename')
        download_verified(entry['url'], directory / 'plugins/flink' / entry['file'], entry['sha256'])
    drivers = json.loads((ROOT / 'tools/source-reader/drivers/versions.lock.json').read_text())
    for entry in drivers['optional_native_drivers']:
        if Path(entry['file']).name != entry['file']: raise RuntimeError('Invalid JDBC filename')
        download_verified(entry['url'], directory / 'plugins/jdbc' / entry['file'], entry['sha256'])
    if not publish:
        print('Native application, database, script runtimes and plugin checks passed; no packages published.')
        return
    for key, target in local.items():
        metadata = image_info(target)
        if key not in existing: run(['docker', 'push', target])
        refreshed = image_info(target)
        repository = target.split(':sha-', 1)[0]
        references = [value for value in refreshed.get('RepoDigests', []) if value.startswith(repository + '@')]
        if not references: raise RuntimeError('Published candidate digest unavailable')
        images[key] = {'reference': references[0], 'config_id': metadata['Id'], 'architecture': 'amd64', 'source_revision': sha}
    files = {'compose.prod.yaml': 'compose.prod.yaml', '.env.prod.example': '.env.prod.example', 'DEPLOYMENT.md': 'DEPLOYMENT.md', 'nginx/business.conf.example': 'nginx/business.conf.example', 'deploy/production/postgres/01-roles.sh': 'config/postgres/01-roles.sh', 'deploy/production/source-postgres/01-source.sh': 'config/source-postgres/01-source.sh', 'deploy/docker/doris/fe-start.sh': 'config/doris/fe-start.sh', 'deploy/docker/doris/be-start.sh': 'config/doris/be-start.sh', 'deploy/docker/flink/sql-gateway.sh': 'config/flink/sql-gateway.sh'}
    for source, destination in files.items():
        target = directory / destination; target.parent.mkdir(parents=True, exist_ok=True); shutil.copy2(ROOT / source, target)
    template = (directory / '.env.prod.example').read_text()
    for key, metadata in images.items(): template = re.sub(r'^' + key + r'=$', key + '=' + metadata['reference'], template, flags=re.MULTILINE)
    template = re.sub(r'^CODE_PYTHON_IMAGE=$', 'CODE_PYTHON_IMAGE=' + images['CODE_PYTHON_REF']['config_id'], template, flags=re.MULTILINE)
    template = re.sub(r'^CODE_JAVASCRIPT_IMAGE=$', 'CODE_JAVASCRIPT_IMAGE=' + images['CODE_JAVASCRIPT_REF']['config_id'], template, flags=re.MULTILINE)
    (directory / '.env.prod.example').write_text(template)
    checksums = {str(path.relative_to(directory)): hashlib.sha256(path.read_bytes()).hexdigest() for path in sorted(directory.rglob('*')) if path.is_file() and 'private' not in path.name}
    manifest = {'source_revision': sha, 'candidate_run_id': os.environ['GITHUB_RUN_ID'], 'database_mode': 'bundled', 'web_mode': 'combined', 'base_path': '/datamesh', 'architecture': 'amd64', 'images': images, 'files': checksums, 'verified': json.loads(report.read_text()), 'version': 'sha-' + sha, 'license': 'NOASSERTION', 'schema': schema_version(ROOT / 'migrations'), 'additional_checks': ['native-python-javascript-precision-null', 'compiled-stream-plugin-tests', 'jdbc-plugin-checksums']}
    (directory / 'release-manifest.json').write_text(json.dumps(manifest, indent=2) + '\n')
    print(json.dumps({'state': 'candidate_verified', 'source_revision': sha, 'components': len(images), 'files': len(checksums)}))


if __name__ == '__main__':
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument('--publish', action='store_true')
    arguments = parser.parse_args()
    try: main(arguments.publish)
    except (OSError, ValueError, RuntimeError, subprocess.SubprocessError):
        print('Production CI failed; no existing immutable tags were overwritten.')
        raise SystemExit(1)
