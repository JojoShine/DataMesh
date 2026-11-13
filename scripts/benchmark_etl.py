#!/usr/bin/env python3
"""Run the real ETL million-row workload in isolated databases and a 512MiB executor."""
import hashlib
import platform
import os
import json
import subprocess
import sys
import uuid
from urllib.parse import urlsplit,urlunsplit,quote
from dev_env import ROOT,ENV_FILE,load_env,compose

def main():
    env=load_env();name='datamesh_test_'+uuid.uuid4().hex
    target=urlsplit(env['MIGRATION_DSN']);target=target._replace(path='/'+name)
    env['TEST_DSN']=urlunsplit(target)
    env['TEST_APP_DSN']=urlunsplit(urlsplit(env['APP_DSN'])._replace(path='/'+name))
    env['MIGRATION_DSN']=env['TEST_DSN']
    env['ETL_BENCH_SOURCE_DSN']='postgres://source_owner:'+quote(env['SOURCE_DB_PASSWORD'],safe='')+'@postgres-source:5432/'+name+'?sslmode=disable'
    env['ETL_BENCHMARK']='1';env['ETL_BENCHMARK_RESULT']='/results/million.json'
    out=ROOT/'.local/smoke/etl-million';out.mkdir(parents=True,exist_ok=True)
    result_path=out/'million.json'
    if result_path.exists():result_path.unlink()
    def fingerprint():
        h=hashlib.sha256()
        for path in sorted((ROOT/'internal').rglob('*.go')):
            if not path.name.endswith('_test.go'):h.update(str(path.relative_to(ROOT)).encode());h.update(path.read_bytes())
        return h.hexdigest()
    code_fingerprint=fingerprint()
    base=['docker','compose' ,'--project-name','datamesh','--env-file',str(ENV_FILE)]
    def run(command,timeout=900):
        result=subprocess.run(command,cwd=ROOT,env=env,capture_output=True,text=True,timeout=timeout)
        output=result.stdout+result.stderr
        for k,v in env.items():
            if any(s in k for s in ('PASSWORD','DSN','SECRET','KEY')) and len(v)>8:output=output.replace(v,'[redacted]')
        print(output,end='',flush=True)
        if result.returncode:raise RuntimeError('ETL benchmark failed; credentials omitted')
    created=[]
    try:
        compose('up','-d','--wait','--wait-timeout','180','postgres-source')
        for service,user in [('postgres','datamesh_owner'),('postgres-source','source_owner')]:
            compose('exec','-T',service,'createdb','-U',user,name);created.append((service,user))
        run(base+['run','--rm','migrate'])
        run(base+['run','--rm','go-tools','sh','-c','CGO_ENABLED=0 go test -c -o .local/etl-bench.test ./tests/integration'])
        network=json.loads((ROOT/'compose.yaml').read_text())['networks']['default']['name']
        command=['docker','run','--rm','--memory=512m','--cpus=2','--network',network,'-e','TEST_DSN','-e','TEST_APP_DSN','-e','ETL_BENCH_SOURCE_DSN','-e','ETL_BENCHMARK','-e','ETL_BENCHMARK_RESULT','-e','CREDENTIAL_KEY','-e','GOMAXPROCS=2','-e','GOMEMLIMIT=384MiB','-v',str(ROOT/'.local/etl-bench.test')+':/app/etl-bench:ro','-v',str(out)+':/results','--entrypoint','/app/etl-bench','datamesh-server:0.1.0','-test.run=^TestETLMillionRows$','-test.v','-test.timeout=20m']
        run(command,timeout=1250)
        if fingerprint()!=code_fingerprint:raise RuntimeError('Execution source changed during benchmark; rerun required')
        result=json.loads(result_path.read_text())
        result.update({'code_fingerprint_sha256':code_fingerprint,'executor_container_limit_mib':512,'executor_cpu_limit':2,'source_container_limit_mib':256,'target_container_limit_mib':384,'host_architecture':platform.machine(),'host_os':platform.system()})
        if platform.system()=='Darwin':result['host_cpu']=subprocess.check_output(['sysctl','-n','machdep.cpu.brand_string'],text=True).strip()
        result_path.write_text(json.dumps(result,ensure_ascii=False,indent=2)+'\n')
        print('Saved '+str(result_path))
    finally:
        for service,user in reversed(created):compose('exec','-T',service,'dropdb','--force','-U',user,name)

if __name__=='__main__':
    try:main()
    except (RuntimeError,OSError,ValueError,subprocess.SubprocessError) as e:
        print(str(e) if isinstance(e,RuntimeError) else 'ETL benchmark failed; credentials omitted',file=sys.stderr);sys.exit(1)
