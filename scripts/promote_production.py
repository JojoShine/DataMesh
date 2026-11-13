"""Promote a successful main candidate without rebuilding any artifact."""
import hashlib
import gzip
import json
import os
from pathlib import Path
import re
import subprocess
import tarfile
import tempfile
from urllib.request import Request, urlopen

from ci_production import COMPONENTS, existing_tag

ROOT = Path(__file__).resolve().parents[1]


def run(args, **kwargs):
    return subprocess.run(args, cwd=ROOT, check=True, **kwargs).stdout


def main():
    repository = os.environ['GITHUB_REPOSITORY']
    token = os.environ['GH_TOKEN']
    version = os.environ['GITHUB_REF_NAME']
    if not re.fullmatch(r'v[0-9]+\.[0-9]+\.[0-9]+', version): raise RuntimeError('A semantic release tag is required')
    revision = run(['git', 'rev-parse', 'HEAD'], capture_output=True, text=True).strip()
    run(['git', 'merge-base', '--is-ancestor', revision, 'origin/main'])
    url = f'https://api.github.com/repos/{repository}/actions/workflows/ci.yml/runs?head_sha={revision}&event=push&status=success&per_page=100'
    with urlopen(Request(url, headers={'Authorization': 'Bearer ' + token, 'Accept': 'application/vnd.github+json'}), timeout=30) as response:
        runs = json.load(response)['workflow_runs']
    accepted = [entry for entry in runs if entry['head_sha'] == revision and entry['head_branch'] == 'main' and entry['conclusion'] == 'success']
    if not accepted: raise RuntimeError('No successful main candidate for the tagged commit')
    selected = max(accepted, key=lambda entry: entry['id'])
    with tempfile.TemporaryDirectory(prefix='datamesh-promote-') as temporary:
        directory = Path(temporary)
        run(['gh', 'run', 'download', str(selected['id']), '--repo', repository, '--name', 'candidate-' + revision, '--dir', str(directory)])
        manifest_file = directory / 'release-manifest.json'
        manifest = json.loads(manifest_file.read_text())
        if manifest['source_revision'] != revision or str(manifest['candidate_run_id']) != str(selected['id']): raise RuntimeError('Candidate origin mismatch')
        if (manifest['database_mode'], manifest['web_mode'], manifest['base_path'], manifest['architecture']) != ('bundled', 'combined', '/datamesh', 'amd64'): raise RuntimeError('Candidate topology mismatch')
        proof = manifest['verified']
        if proof.get('state') != 'verified' or not proof.get('native') or proof.get('architecture') != 'amd64' or proof['image_id'] != manifest['images']['APP_IMAGE']['config_id']: raise RuntimeError('Native image evidence mismatch')
        actual = {str(path.relative_to(directory)) for path in directory.rglob('*') if path.is_file()}
        if actual != set(manifest['files']) | {'release-manifest.json'}: raise RuntimeError('Unexpected candidate files')
        for name, digest in manifest['files'].items():
            path = directory / name
            if path.is_symlink() or not path.resolve().is_relative_to(directory.resolve()): raise RuntimeError('Invalid artifact path')
            if hashlib.sha256(path.read_bytes()).hexdigest() != digest: raise RuntimeError('Candidate artifact checksum mismatch')
        os.environ['GITHUB_TOKEN'] = token
        owner = os.environ['GITHUB_REPOSITORY_OWNER'].lower()
        run(['docker', 'login', 'ghcr.io', '--username', os.environ['GITHUB_ACTOR'], '--password-stdin'], input=token, text=True, capture_output=True)
        version_tags = {}
        for key, component in COMPONENTS.items():
            reference = manifest['images'][key]['reference']
            if not re.fullmatch(r'ghcr\.io/[a-z0-9-]+/[a-z0-9-]+@sha256:[a-f0-9]{64}', reference): raise RuntimeError('Invalid candidate image reference')
            repository_name, digest = reference.split('@', 1)
            prior = existing_tag(owner, component, version)
            if prior and prior != digest: raise RuntimeError('Formal version tag already identifies another immutable artifact')
            target = repository_name + ':' + version
            if not prior: run(['docker', 'buildx', 'imagetools', 'create', '--prefer-index=false', '--tag', target, reference])
            version_tags[key] = target
        manifest.update(version=version, version_tags=version_tags)
        manifest_file.write_text(json.dumps(manifest, indent=2) + '\n')
        # GitHub artifact transport does not retain Unix executable permissions.
        for path in directory.rglob('*'):
            if path.is_file(): path.chmod(0o755 if path.suffix == '.sh' else 0o644)
        release = ROOT / 'artifacts/release'
        release.mkdir(parents=True, exist_ok=True)
        archive = release / ('datamesh-' + version + '-bundled-combined-amd64.tar.gz')
        timestamp = int(run(['git', 'show', '-s', '--format=%ct', revision], capture_output=True, text=True).strip())
        def archive_metadata(info):
            info.uid = info.gid = 0
            info.uname = info.gname = ''
            info.mtime = timestamp
            info.pax_headers = {}
            return info
        with archive.open('wb') as raw, gzip.GzipFile(filename='', mode='wb', fileobj=raw, mtime=timestamp) as compressed:
            with tarfile.open(fileobj=compressed, mode='w') as stream:
                for path in sorted(directory.iterdir()): stream.add(path, arcname=path.name, filter=archive_metadata)
        checksum = hashlib.sha256(archive.read_bytes()).hexdigest()
        checksum_file = Path(str(archive) + '.sha256')
        checksum_file.write_text(checksum + '  ' + archive.name + '\n')
        exists = subprocess.run(['gh', 'release', 'view', version, '--repo', repository], capture_output=True).returncode == 0
        if exists: raise RuntimeError('Release already exists; version assets are immutable')
        notes = release / 'notes.md'
        notes.write_text(f'DataMesh {version}\n\nCommit: {revision}\n\nMode: bundled PostgreSQL / combined React-Go / linux-amd64 / BASE_PATH=/datamesh.\n\nImages and plugins are promoted from successful main CI without rebuilding. Deployment files and exact digests are in the attached bundle and manifest.\n')
        run(['gh', 'release', 'create', version, '--repo', repository, '--verify-tag', '--draft', '--title', 'DataMesh ' + version, '--notes-file', str(notes), str(archive), str(checksum_file), str(manifest_file), str(directory / 'compose.prod.yaml'), str(directory / '.env.prod.example'), str(directory / 'DEPLOYMENT.md'), str(directory / 'nginx/business.conf.example')])
        run(['gh', 'release', 'edit', version, '--repo', repository, '--draft=false'])
        print(json.dumps({'state': 'promoted', 'version': version, 'source_revision': revision, 'candidate_run_id': selected['id'], 'sha256': checksum}))


if __name__ == '__main__':
    try: main()
    except (OSError, KeyError, ValueError, RuntimeError, subprocess.SubprocessError):
        print('Release promotion failed; no image was rebuilt or immutable version overwritten.')
        raise SystemExit(1)
