"""Local-only directory acceptance helpers; every approval uses its real HTTP route."""
from datetime import datetime, timedelta, timezone


def expiry():
    return (datetime.now(timezone.utc)+timedelta(days=30)).isoformat()


def prepare_permissions(admin,provider,reviewer):
    assignments=admin.call('GET','/api/v1/resource-assignments?page_size=100')['items']
    desired=[(provider,provider.principal['department_id'],c) for c in ['manage_resource','operate_resource','govern_resource']]
    desired += [(reviewer,reviewer.principal['department_id'],c) for c in ['department_review','mount_review','share_approve']]
    desired += [(admin,'*','authority_review')]
    for client,dept,cap in desired:
        if any(r['state']=='active' and r['definition']['user_id']==client.principal['user_id'] and r['definition']['capability']==cap and r['department_id']==dept and datetime.fromisoformat(r['definition']['expires_at'].replace('Z','+00:00'))>datetime.now(timezone.utc) for r in assignments):
            continue
        admin.call('POST','/api/v1/resource-assignments',{'user_id':client.principal['user_id'],'department_id':dept,'resource_id':'*','capability':cap,'expires_at':expiry(),'reason':'本地开发验收：明确岗位分离，非真实行政授权'})
    dictionaries=admin.call('GET','/api/v1/directory-dictionaries')['items']
    existing=next((r for r in dictionaries if r['definition']['test_only'] and r['definition']['name']=='本地验证字典'),None)
    if existing:return existing['id']
    return admin.call('POST','/api/v1/directory-dictionaries',{'name':'本地验证字典','version':'test-v1','source':'本机隔离开发验证，不是国家正式编码字典','verified':True,'test_only':True,'code_pattern':'^TEST-[A-Za-z0-9-]+$','classifications':['department/local-test'],'verification_ref':'LOCAL-TEST-ONLY'})['id']


def prepare_processing(reviewer,provider,obj):
    records=reviewer.call('GET','/api/v1/processing-authorizations?page_size=100')['items']
    if any(r['state']=='active' and r['definition']['user_id']==provider.principal['user_id'] and r['definition']['resource']['resource_id']==obj['object_id'] and r['definition']['allow_sharing'] and datetime.fromisoformat(r['definition']['expires_at'].replace('Z','+00:00'))>datetime.now(timezone.utc) for r in records):return
    reviewer.call('POST','/api/v1/processing-authorizations',{'resource':{'resource_type':'raw_object','resource_id':obj['object_id'],'contract_version':obj['schema_version']},'user_id':provider.principal['user_id'],'fields':[f['name'] for f in obj['fields']],'purpose':'数据治理','expires_at':expiry(),'allow_derived':True,'allow_sharing':True,'sharing_basis':'LOCAL-TEST：来源责任部门允许本地治理及受控共享验证','approval_ref':'LOCAL-TEST-PROCESSING'})


def review_pending(client,target,stage):
    rows=client.call('GET','/api/v1/directory-reviews?stage='+stage+'&page_size=100')['items']
    row=next(r for r in rows if r['definition']['target_id']==target)
    return client.call('POST','/api/v1/directory-reviews/'+row['review_id']+'/approve',{'expected_version':row['version'],'reason':'LOCAL-TEST：已核对当前提交及材料引用'})


def compile_and_mount(provider,reviewer,admin,dictionary_id,ref,fields,name,description,open_summary=False):
    code='TEST-'+('RAW-' if ref['resource_type']=='raw_object' else 'GOV-')+ref['resource_id']
    existing=provider.call('GET','/api/v1/resource-directories?scope=mine&page_size=100')['items']
    row=next((r for r in existing if r['definition']['resource_code']==code),None)
    if not row:
        row=provider.call('POST','/api/v1/resource-directories',{'name':name,'description':description,'classification':'department','classification_path':'department/local-test','resource_code':code,'dictionary_id':dictionary_id,'provider_name':provider.principal['department_id'],'provider_code':'LOCAL-TEST','responsible_user_id':provider.principal['user_id'],'update_frequency':'持续同步','format':'database','data_layer':ref['resource_type'],'items':[{**{k:v for k,v in f.items() if k!='primary_key'},'business_name':{'id':'客户编号','name':'客户姓名','amount':'金额'}.get(f['name'],f['name']),'length':None,'description':''} for f in fields],'sharing_type':'conditional','sharing_conditions':'仅限本地验证，按批准用途、字段和期限使用','non_sharing_reason':'','legal_basis':'','sharing_method':'query','usage_restrictions':'LOCAL-TEST：仅供本机开发验证；敏感姓名脱敏交付','security_classification':'一般数据（测试标记）','open_to_public':open_summary,'open_conditions':'仅展示本地测试摘要，不公开数据内容' if open_summary else '','department_approval_ref':'LOCAL-TEST-DEPARTMENT','assessment_ref':'LOCAL-TEST-ASSESSMENT','related_resource_codes':[]})
    if row['state']!='published':
        if row['revision_state']=='draft':provider.call('POST','/api/v1/resource-directories/'+row['directory_id']+'/submit',{'expected_version':row['version']})
        current=provider.call('GET','/api/v1/resource-directories/'+row['directory_id'])
        if current['revision_state']=='department_pending':review_pending(reviewer,row['directory_id'],'department')
        current=provider.call('GET','/api/v1/resource-directories/'+row['directory_id'])
        if current['revision_state']=='authority_pending':review_pending(admin,row['directory_id'],'authority')
    mounts=provider.call('GET','/api/v1/resource-mounts?directory_id='+row['directory_id']+'&page_size=100')['items']
    mount=next((r for r in mounts if r['state']=='active'),None)
    if mount:return row['directory_id'],mount['mount_id']
    mount=next((r for r in mounts if r['revision_state'] in ['draft','validated','pending','approved']),None)
    if mount is None:mount=provider.call('POST','/api/v1/resource-mounts',{'directory_id':row['directory_id'],'resource':ref,'mapping':{f['name']:f['name'] for f in fields},'expected_version':0})
    if mount['revision_state'] in ['draft','validated']:
        mount=provider.call('POST','/api/v1/resource-mounts/'+mount['mount_id']+'/validate',{'expected_version':mount['version']})
        if not mount['definition']['validation']['passed']:raise RuntimeError('真实挂载校验失败：'+str([(c['name'],c['message']) for c in mount['definition']['validation']['checks'] if not c['passed']]))
        mount=provider.call('POST','/api/v1/resource-mounts/'+mount['mount_id']+'/submit',{'expected_version':mount['version']})
    if mount['revision_state']=='pending':review_pending(reviewer,mount['mount_id'],'mount')
    mount=provider.call('GET','/api/v1/resource-mounts/'+mount['mount_id'])
    if mount['state']!='active':mount=provider.call('POST','/api/v1/resource-mounts/'+mount['mount_id']+'/activate',{'expected_version':mount['version']})
    return row['directory_id'],mount['mount_id']
