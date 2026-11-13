#!/usr/bin/env python3
"""Provision only the dedicated SQL sandbox namespace; credentials never enter output."""
import json
import os
from pathlib import Path
import secrets
import sys

from dev_env import ROOT, LOCAL, ENV_FILE, load_env
from experience_env import compute_query

DATABASE='datamesh_sandbox'
USERS={'reader':'datamesh_sandbox_reader','writer':'datamesh_sandbox_writer'}
INTENT=LOCAL/'csv-data-sandbox/doris-provisioning.json'


def prepare():
    values=load_env()
    with ENV_FILE.open('a') as stream:
        for key in ['SANDBOX_DORIS_READER_PASSWORD','SANDBOX_DORIS_WRITER_PASSWORD']:
            if not values.get(key):
                stream.write(key+'='+secrets.token_hex(24)+'\n')
    ENV_FILE.chmod(0o600)


def provision():
    prepare()
    env=load_env()
    root=env['DORIS_ROOT_PASSWORD']
    def query(text,user='root',password=root,allow_error=False):
        return compute_query('datamesh-doris-fe-1',user,password,text,allow_error=allow_error)
    databases={line.strip() for line in query('SHOW DATABASES;')[0].splitlines()}
    existing=[name for name in USERS.values() if query("SHOW GRANTS FOR '"+name+"';",allow_error=True)[2]==0]
    if not INTENT.exists():
        if DATABASE in databases or existing:
            raise RuntimeError('Sandbox namespace already exists without an ownership record; no changes made')
        INTENT.parent.mkdir(mode=0o700,parents=True,exist_ok=True)
        fd=os.open(INTENT,os.O_CREAT|os.O_EXCL|os.O_WRONLY,0o600)
        with os.fdopen(fd,'w') as stream:json.dump({'database':DATABASE,'users':USERS,'state':'intent'},stream)
    state=json.loads(INTENT.read_text())
    if state.get('database')!=DATABASE or state.get('users')!=USERS:
        raise RuntimeError('Sandbox provisioning ownership differs')
    if DATABASE not in databases:query('CREATE DATABASE `'+DATABASE+'`;')
    for role,user in USERS.items():
        password=env['SANDBOX_DORIS_'+role.upper()+'_PASSWORD']
        if len(password)!=48 or any(char not in '0123456789abcdef' for char in password):
            raise RuntimeError('Invalid sandbox generated credential')
        if user not in existing:query("CREATE USER '"+user+"' IDENTIFIED BY '"+password+"';")
        privileges='SELECT_PRIV' if role=='reader' else 'SELECT_PRIV,LOAD_PRIV,CREATE_PRIV,DROP_PRIV'
        query('GRANT '+privileges+' ON `'+DATABASE+"`.* TO '"+user+"';")
        query('SET PROPERTY FOR \''+user+"' 'max_user_connections'='4';")
        query('USE `'+DATABASE+'`; SHOW TABLES;',user,password)
        if query('SELECT 1 FROM datamesh_smoke.customers LIMIT 0;',user,password,True)[2]==0:
            raise RuntimeError('Sandbox identity unexpectedly reads main namespace')
    state.update(state='verified',main_namespace_denied=True,root_credential_in_app=False)
    INTENT.write_text(json.dumps(state,indent=2)+'\n');INTENT.chmod(0o600)
    print(json.dumps({'sandbox_database':DATABASE,'dedicated_identities':2,'main_namespace_denied':True,'credentials_printed':False}))


if __name__=='__main__':
    if len(sys.argv)!=2 or sys.argv[1] not in ['prepare','provision']:
        raise SystemExit('Use sandbox_env.py prepare|provision')
    if sys.argv[1]=='prepare':prepare();print('Private sandbox credentials prepared; no database changes.')
    else:provision()
