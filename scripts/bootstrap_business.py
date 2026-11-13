#!/usr/bin/env python3
"""Provision only this project's named development identities; preserve existing credentials."""
import json
import os
import re
import secrets
import subprocess
import sys
from pathlib import Path

from dev_env import ROOT, LOCAL, ENV_FILE, load_env, compose


def private_write(path, value):
    fd = os.open(path, os.O_WRONLY | os.O_CREAT | os.O_TRUNC, 0o600)
    with os.fdopen(fd, 'w') as stream:
        json.dump(value, stream, ensure_ascii=False, indent=2)
        stream.write('\n')


def main():
    binding_dir=LOCAL/'bindings'
    binding_dir.mkdir(mode=0o700,exist_ok=True)
    job_file=LOCAL/'smoke/flink-job.json'
    if job_file.exists():
        job=json.loads(job_file.read_text())['job_id']
        if not re.fullmatch(r'[0-9a-f]{32}',job):raise RuntimeError('Invalid local processing job binding')
        private_write(binding_dir/'flink.json',{'job_id':job})
    initial = json.loads((LOCAL / 'bootstrap-admin.json').read_text())
    username = initial['username']
    if not re.fullmatch(r'[a-z][a-z0-9_.-]{2,63}', username):
        raise RuntimeError('Invalid bootstrap username')
    compose('exec', '-T', 'postgres', 'psql', '-U', 'datamesh_owner', '-d', 'datamesh', '-v', 'ON_ERROR_STOP=1', '-c',
            f"INSERT INTO user_roles(user_id,role) SELECT id,'admin' FROM users WHERE username='{username}' AND active=true ON CONFLICT DO NOTHING", capture=True)
    credentials = LOCAL / 'business-accounts.json'
    if not credentials.exists():
        private_write(credentials, {
            'provider': {'username': 'provider', 'password': secrets.token_urlsafe(24), 'department_id': '数据提供部门',
                         'platforms': ['exchange', 'governance', 'assets'], 'roles': ['operator', 'governor', 'provider']},
            'consumer': {'username': 'consumer', 'password': secrets.token_urlsafe(24), 'department_id': '数据使用部门',
                         'platforms': ['exchange', 'assets'], 'roles': ['consumer']},
            'auditor': {'username': 'auditor', 'password': secrets.token_urlsafe(24), 'department_id': '安全管理部门',
                        'platforms': ['security'], 'roles': ['auditor']}})
    saved=json.loads(credentials.read_text())
    if 'directory_reviewer' not in saved:
        saved['directory_reviewer']={'username':'directory_reviewer','password':secrets.token_urlsafe(24),'department_id':saved['provider']['department_id'],'platforms':['exchange','assets'],'roles':['provider']}
        private_write(credentials,saved)
    for account in saved.values():
        name = account['username']
        if not re.fullmatch(r'[a-z][a-z0-9_.-]{2,63}', name):
            raise RuntimeError('Invalid local account name')
        exists = compose('exec', '-T', 'postgres', 'psql', '-U', 'datamesh_owner', '-d', 'datamesh', '-At', '-c',
                         f"SELECT count(*) FROM users WHERE username='{name}'", capture=True).stdout.strip()
        if exists != '0':
            continue
        result = subprocess.run(['docker', 'compose', '--project-name', 'datamesh', '--env-file', str(ENV_FILE),
                                 'run', '--rm', '--no-deps', '-T', '--entrypoint', '/app/manage', 'server', 'create-user'],
                                input=json.dumps(account), text=True, capture_output=True, env=load_env(), cwd=ROOT, timeout=45)
        if result.returncode:
            raise RuntimeError('Local business account creation failed; no credentials logged')
    print('Business identities ready; private credentials preserved in .local/business-accounts.json.')


if __name__ == '__main__':
    try:
        main()
    except (RuntimeError, OSError, ValueError, subprocess.SubprocessError) as exc:
        print(str(exc) if isinstance(exc, RuntimeError) else 'Business provisioning failed; no credentials logged', file=sys.stderr)
        sys.exit(1)
