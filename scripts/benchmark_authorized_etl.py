#!/usr/bin/env python3
"""Measure authorized PG table/raw/governed exchanges in an isolated 512MiB executor."""
import hashlib
import argparse
import json
import platform
import subprocess
import sys
import uuid
from urllib.parse import urlsplit, urlunsplit
from dev_env import ROOT, ENV_FILE, load_env, compose


def main():
    parser=argparse.ArgumentParser()
    parser.add_argument('--pilot',action='store_true',help='Validate the six-trial harness with 10001 rows; never counts as scale acceptance')
    args=parser.parse_args()
    env = load_env()
    name = 'datamesh_test_' + uuid.uuid4().hex
    for target, source in [('TEST_DSN', 'MIGRATION_DSN'), ('TEST_APP_DSN', 'APP_DSN')]:
        env[target] = urlunsplit(urlsplit(env[source])._replace(path='/' + name))
    env['MIGRATION_DSN'] = env['TEST_DSN']
    env['ETL_RESOURCE_BENCHMARK'] = 'pilot' if args.pilot else '1'
    env['ETL_RESOURCE_BENCHMARK_RESULT'] = '/results/million.json'
    out = ROOT / '.local/smoke/etl-authorized-million' / uuid.uuid4().hex
    out.mkdir(parents=True)
    def fingerprint():
        digest = hashlib.sha256()
        for directory in ['internal', 'tests/integration']:
            for path in sorted((ROOT / directory).rglob('*.go')):
                digest.update(str(path.relative_to(ROOT)).encode())
                digest.update(path.read_bytes())
        return digest.hexdigest()
    code = fingerprint()
    base = ['docker', 'compose', '--project-name', 'datamesh', '--env-file', str(ENV_FILE)]
    def run(command, timeout=900):
        result = subprocess.run(command, cwd=ROOT, env=env, capture_output=True, text=True, timeout=timeout)
        output = result.stdout + result.stderr
        for key, value in env.items():
            if any(part in key for part in ('PASSWORD', 'DSN', 'SECRET', 'KEY')) and len(value) > 8:
                output = output.replace(value, '[redacted]')
        with (out / 'run.log').open('a') as log:
            log.write(output)
        print(output, end='', flush=True)
        if result.returncode:
            raise RuntimeError('Authorized benchmark failed; credentials omitted. Evidence: ' + str(out))
    created = False
    try:
        compose('up', '-d', '--wait', '--wait-timeout', '180', 'postgres', 'rustfs')
        compose('exec', '-T', 'postgres', 'createdb', '-U', 'datamesh_owner', name)
        created = True
        run(base + ['run', '--rm', 'migrate'])
        run(base + ['run', '--rm', 'go-tools', 'sh', '-c', 'CGO_ENABLED=0 go test -c -o .local/etl-resource-bench.test ./tests/integration'])
        network = json.loads((ROOT / 'compose.yaml').read_text())['networks']['default']['name']
        command = ['docker', 'run', '--rm', '--memory=512m', '--cpus=2', '--network', network]
        for key in ['TEST_DSN', 'TEST_APP_DSN', 'ETL_RESOURCE_BENCHMARK', 'ETL_RESOURCE_BENCHMARK_RESULT', 'CREDENTIAL_KEY', 'RUSTFS_ACCESS_KEY', 'RUSTFS_SECRET_KEY']:
            command += ['-e', key]
        test_name='TestETLResourceBenchmarkPilot' if args.pilot else 'TestETLResourceMillionRows'
        command += ['-e', 'GOMAXPROCS=2', '-e', 'GOMEMLIMIT=384MiB', '-e', 'DIRECTORY_TEST_MODE=1', '-v', str(ROOT / '.local/etl-resource-bench.test') + ':/app/etl-bench:ro', '-v', str(out) + ':/results', '--entrypoint', '/app/etl-bench', 'datamesh-server:0.1.0', '-test.run=^'+test_name+'$', '-test.v', '-test.timeout=140m']
        run(command, timeout=8500)
        if fingerprint() != code:
            raise RuntimeError('Execution source changed during benchmark; rerun required: ' + str(out))
        results = json.loads((out / 'million.json').read_text())
        expected_rows=10001 if args.pilot else 1000000
        if len(results) != 6 or any(r['rows'] != expected_rows or not r['full_values_verified'] or not r['receipt_accepted'] for r in results):
            raise RuntimeError('Benchmark evidence is incomplete: ' + str(out))
        metadata = {'is_scale_acceptance':not args.pilot,'code_fingerprint_sha256': code, 'executor_memory_mib': 512, 'executor_cpus': 2, 'host_os': platform.system(), 'host_architecture': platform.machine(), 'topology': 'PostgreSQL source, target and metadata in one isolated database; RustFS archive; synthetic verified CDC events for raw/governed fixtures', 'mapped_columns': ['bigint primary key', 'text sensitive value masked before transform'], 'results': results}
        (out / 'report.json').write_text(json.dumps(metadata, ensure_ascii=False, indent=2) + '\n')
        print('Saved ' + str(out / 'report.json'))
    finally:
        if created:
            compose('exec', '-T', 'postgres', 'dropdb', '--force', '-U', 'datamesh_owner', name)


if __name__ == '__main__':
    try:
        main()
    except (RuntimeError, OSError, ValueError, subprocess.SubprocessError) as exc:
        print(str(exc) if isinstance(exc, RuntimeError) else 'Authorized benchmark failed; credentials omitted', file=sys.stderr)
        sys.exit(1)
