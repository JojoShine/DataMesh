#!/usr/bin/env python3
"""Exercise the real local workflow with distinct provider/consumer identities."""
import http.cookiejar
import json
from pathlib import Path
import ssl
import sys
import time
import uuid
from datetime import datetime, timedelta, timezone
from urllib.error import HTTPError
from urllib.request import Request, HTTPSHandler, HTTPCookieProcessor, build_opener

ROOT = Path(__file__).resolve().parents[2]
sys.path.insert(0,str(ROOT/'scripts'))
from dev_env import load_env
from directory import prepare_permissions,prepare_processing,compile_and_mount


class Client:
    def __init__(self, account):
        self.client = build_opener(HTTPSHandler(context=ssl.create_default_context(cafile=str(ROOT/'.local/certs/server.crt'))),HTTPCookieProcessor(http.cookiejar.CookieJar()))
        self.principal = self.call('POST','/auth/login',{k:account[k] for k in ['username','password']})

    def call(self, method, path, body=None, key=None, expected=None):
        headers={'Origin':'https://localhost:18443','Content-Type':'application/json'}
        if hasattr(self,'principal'):
            headers['X-CSRF-Token']=self.principal['csrf_token']
        if method not in ['GET'] and path not in ['/auth/login','/auth/logout']:
            headers['Idempotency-Key']=key or str(uuid.uuid4())
        request=Request('https://localhost:18443'+path,data=json.dumps(body or {}).encode() if method!='GET' else None,headers=headers,method=method)
        try:
            with self.client.open(request,timeout=25) as response:
                value=json.load(response) if response.status!=204 else None
                if expected and response.status!=expected:
                    raise RuntimeError(f'{method} {path} returned unexpected status')
                if response.status==202:
                    for _ in range(90):
                        if value['status'] in ['succeeded','failed','unknown']:break
                        time.sleep(1)
                        value=self.call('GET','/api/v1/operations/'+value['operation_id'])
                    if value['status']!='succeeded':
                        raise RuntimeError('Operation failed: '+str(value.get('result',{}).get('code','unknown')))
                return value
        except HTTPError as exc:
            value=json.load(exc)
            if expected==exc.code:return value
            raise RuntimeError(f'{method} {path}: HTTP {exc.code} {value.get("code","UNKNOWN")}') from None


def main():
    accounts=json.loads((ROOT/'.local/business-accounts.json').read_text())
    provider,consumer,auditor=[Client(accounts[name]) for name in ['provider','consumer','auditor']]
    admin=Client(json.loads((ROOT/'.local/bootstrap-admin.json').read_text()))
    reviewer=Client(accounts['directory_reviewer'])
    dictionary=prepare_permissions(admin,provider,reviewer)
    env=load_env()
    sources=provider.call('GET','/api/v1/sources?page_size=100')['items']
    if sources:
        source=next((v for v in sources if v['kind']=='postgres'),None)
        if source is None:raise RuntimeError('Existing sources have no project PostgreSQL template')
    else:
        source=provider.call('POST','/api/v1/sources',{'name':'本地客户数据库','kind':'postgres','host':'postgres-source','port':5432,'database':'sample','owner_id':provider.principal['user_id'],'credentials':{'username':'datamesh_cdc','password':env['SOURCE_CDC_PASSWORD']}})
    if 'password' in json.dumps(source) or 'ciphertext' in json.dumps(source):raise RuntimeError('Saved source leaked credential material')
    for action in ['test-connection','preflight-cdc']:
        report=provider.call('POST',f'/api/v1/sources/{source["source_id"]}/{action}')
        # Earlier in-development builds are never accepted as final contract evidence.
        if report.get('passed') is not True:raise RuntimeError('Source preflight did not pass final response contract')
    obj=provider.call('GET',f'/api/v1/sources/{source["source_id"]}/objects')['items'][0]
    prepare_processing(reviewer,provider,obj)
    tasks=provider.call('GET','/api/v1/tasks?page_size=100')['items']
    task=next((v for v in tasks if v['object_id']==obj['object_id']),None)
    if task is None:
        task=provider.call('POST','/api/v1/tasks',{'source_id':source['source_id'],'object_id':obj['object_id'],'mode':'cdc','dataset_id':None,'schema_version':1,'rule_version':None})
    provider.call('POST',f'/api/v1/tasks/{task["task_id"]}/start')
    provider.call('POST',f'/api/v1/tasks/{task["task_id"]}/archive')
    rules=provider.call('GET','/api/v1/rules?page_size=100')['items']
    rule=next((v for v in rules if v['definition']['object_id']==obj['object_id'] and v['state']=='published'),None)
    if rule is None:
        rule=provider.call('POST','/api/v1/rules',{'name':'客户基础质量规则','object_id':obj['object_id'],'schema_version':1,'checks':[{'field':'id','kind':'required','blocking':True,'parameters':{}},{'field':'id','kind':'business_key','blocking':True,'parameters':{}},{'field':'amount','kind':'type','blocking':True,'parameters':{'type':'decimal'}}]})
        provider.call('POST',f'/api/v1/rules/{rule["rule_id"]}/publish')
    datasets=provider.call('GET','/api/v1/datasets?page_size=100')['items']
    dataset=next((v for v in datasets if v['definition']['name']=='客户基础数据（本地验证）'),None)
    if dataset is None:
        dataset=provider.call('POST','/api/v1/datasets',{'name':'客户基础数据（本地验证）','description':'由本地 PostgreSQL 客户表持续采集，经姓名空白清理与精确小数处理后形成的真实治理成果。','source_object_id':obj['object_id'],'owner_id':provider.principal['user_id'],'fields':obj['fields'],'usage_restrictions':'仅供本地开发验证，敏感姓名以脱敏形式交付。'})
    quality=provider.call('POST',f'/api/v1/datasets/{dataset["dataset_id"]}/check')
    if quality['status']!='passed' or quality['failed_count']!=0:raise RuntimeError('Real quality gate was blocked')
    if dataset['publication_status']!='published':provider.call('POST',f'/api/v1/datasets/{dataset["dataset_id"]}/publish')
    raw_directory,raw_mount=compile_and_mount(provider,reviewer,admin,dictionary,{'resource_type':'raw_object','resource_id':obj['object_id'],'contract_version':obj['schema_version']},obj['fields'],'客户原始数据（本地验证）','本地客户表原始数据交换，仅供本地验证。')
    governed_directory,governed_mount=compile_and_mount(provider,reviewer,admin,dictionary,{'resource_type':'governed_dataset','resource_id':dataset['dataset_id'],'contract_version':dataset['contract_version']},dataset['definition']['fields'],dataset['definition']['name'],dataset['definition']['description'],True)
    public=consumer.call('GET','/api/public/assets')['items']
    if dataset['dataset_id'] not in [v['dataset_id'] for v in public]:raise RuntimeError('Published asset is absent from public portal')
    # Repeated validation replaces only this script's prior grants for its named development identity.
    prior=consumer.call('GET','/api/v1/grants?page_size=100')['items']
    fixture_purposes={'跨部门业务闭环验证','原始数据独立交换验证','本地界面体验'}
    for grant in prior:
        if grant['state']=='active' and grant['grantee_user_id']==consumer.principal['user_id'] and grant['scope']['resource']['resource_id'] in {dataset['dataset_id'],obj['object_id']} and grant['scope']['purpose'] in fixture_purposes:
            reviewer.call('POST',f'/api/v1/grants/{grant["grant_id"]}/revoke')
    consumer.call('GET','/api/v1/sources',expected=403)
    consumer.call('GET',f'/delivery/v1/datasets/{dataset["dataset_id"]}/records?contract_version=1',expected=403)
    application_request={'materials':{'legal_basis':'LOCAL-TEST 验证任务','scenario':'跨部门受控共享验证','scope':'本机测试数据','method':'query','department_approval_ref':'LOCAL-TEST-REQUEST'},'resource':{'resource_type':'governed_dataset','resource_id':dataset['dataset_id'],'contract_version':1},'department_id':consumer.principal['department_id'],'purpose':'跨部门业务闭环验证','fields':['id','name'],'expires_at':(datetime.now(timezone.utc)+timedelta(days=7)).isoformat()}
    key=str(uuid.uuid4())
    app=consumer.call('POST','/api/v1/applications',application_request,key)
    repeat=consumer.call('POST','/api/v1/applications',application_request,key)
    if repeat['application_id']!=app['application_id']:raise RuntimeError('Idempotency created another application')
    changed=dict(application_request,purpose='不同请求')
    consumer.call('POST','/api/v1/applications',changed,key,expected=409)
    consumer.call('POST',f'/api/v1/applications/{app["application_id"]}/approve',expected=403)
    reviewer.call('POST',f'/api/v1/applications/{app["application_id"]}/approve')
    grants=consumer.call('GET','/api/v1/grants?page_size=100')['items']
    grant=next(v for v in grants if v['application_id']==app['application_id'])
    first=consumer.call('GET',f'/delivery/v1/datasets/{dataset["dataset_id"]}/records?contract_version=1&limit=10')
    if not first['records'] or any(set(row)!=set(['id','name']) or row['name'] not in ['***',None] for row in first['records']):raise RuntimeError('Projection or masking failed')
    if first['next_cursor']:
        from urllib.parse import urlencode
        consumer.call('GET',f'/delivery/v1/datasets/{dataset["dataset_id"]}/records?'+urlencode({'contract_version':1,'limit':10,'cursor':first['next_cursor']}))
    consumer.call('GET',f'/delivery/v1/datasets/{dataset["dataset_id"]}/records?contract_version=1&fields=amount',expected=403)
    consumer.call('GET',f'/delivery/v1/raw/{obj["object_id"]}/records?contract_version=1',expected=403)
    raw_request=dict(application_request,resource={'resource_type':'raw_object','resource_id':obj['object_id'],'contract_version':1},purpose='原始数据独立交换验证')
    raw_app=consumer.call('POST','/api/v1/applications',raw_request)
    reviewer.call('POST',f'/api/v1/applications/{raw_app["application_id"]}/approve')
    raw=consumer.call('GET',f'/delivery/v1/raw/{obj["object_id"]}/records?contract_version=1&limit=5')
    if len(raw['events'])!=5 or any(set(event['after'] or {})-{'id','name'} for event in raw['events']):raise RuntimeError('Raw delivery projection failed')
    reviewer.call('POST',f'/api/v1/grants/{grant["grant_id"]}/revoke')
    consumer.call('GET',f'/delivery/v1/datasets/{dataset["dataset_id"]}/records?contract_version=1',expected=403)
    # Reissue a usable governed grant so the delivered portal can be explored after validation.
    retained_app=consumer.call('POST','/api/v1/applications',dict(application_request,purpose='本地界面体验'))
    reviewer.call('POST',f'/api/v1/applications/{retained_app["application_id"]}/approve')
    trace=auditor.call('GET','/api/v1/audit?search=deliver_')
    if trace['total']==0:raise RuntimeError('Delivery audit missing')
    output={'result':'passed','dataset_id':dataset['dataset_id'],'object_id':obj['object_id'],'task_id':task['task_id'],'raw_directory_id':raw_directory,'raw_mount_id':raw_mount,'governed_directory_id':governed_directory,'governed_mount_id':governed_mount,'rows_checked':quality['passed_count'],'checks':['directory-review','mount-validation','mount-review-and-activation','processing-authorization','explicit-role-scopes','real-preflight','actual-task-control','kafka-s3-archive','source-result-reconciliation','quality','publication','public-summary','cross-department-approval','no-self-approval','idempotency','field-projection','masking','signed-pagination','raw-layer-separation','raw-delivery','revocation','audit']}
    (ROOT/'.local/smoke/business-result.json').write_text(json.dumps(output,indent=2)+'\n')
    for client in [provider,consumer,auditor,admin,reviewer]:client.call('POST','/auth/logout')
    print(json.dumps(output))


if __name__=='__main__':
    try:main()
    except (RuntimeError,OSError,ValueError,StopIteration) as exc:
        print(str(exc) if isinstance(exc,RuntimeError) else 'Business integration failed; no credentials logged',file=sys.stderr)
        sys.exit(1)
