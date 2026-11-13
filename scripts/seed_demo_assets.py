#!/usr/bin/env python3
"""Initialize two small synthetic assets through the existing platform APIs."""
import argparse
import http.cookiejar
import json
import os
import ssl
import subprocess
import sys
import time
import uuid
from decimal import Decimal
from pathlib import Path
from urllib.error import HTTPError
from urllib.parse import urlencode
from urllib.request import HTTPSHandler, HTTPCookieProcessor, Request, build_opener

from dev_env import ENV_FILE, ROOT, load_env

FOLDER = ROOT / '.local/demo-assets-v1'
STATE = FOLDER / 'manifest.json'
ORIGIN = 'https://localhost:18443'
SCHEMA = 'datamesh_demo_v1'
CUSTOMER_FIELDS = [
    {'name':'id','business_name':'客户编号','logical_type':'integer','nullable':False},
    {'name':'name','business_name':'客户名称','logical_type':'string','nullable':True},
    {'name':'age','business_name':'年龄','logical_type':'integer','nullable':True},
    {'name':'city','business_name':'城市','logical_type':'string','nullable':True},
]
ORDER_FIELDS = [
    {'name':'order_id','business_name':'订单编号','logical_type':'integer','nullable':False},
    {'name':'customer_id','business_name':'客户编号','logical_type':'integer','nullable':False},
    {'name':'amount','business_name':'订单金额','logical_type':'decimal','precision':12,'scale':2,'nullable':False},
    {'name':'order_date','business_name':'订单日期','logical_type':'date','nullable':False},
]
JOIN_FIELDS = [ORDER_FIELDS[0], {'name':'customer_id','business_name':'客户编号','logical_type':'integer','nullable':False},
    {'name':'customer_name','business_name':'客户名称','logical_type':'string','nullable':True}, CUSTOMER_FIELDS[3], ORDER_FIELDS[2], ORDER_FIELDS[3]]
QUALITY_CHECKS = [
    {'field':'name','kind':'required','blocking':True},
    {'field':'name','kind':'length','blocking':True,'parameters':{'min':1}},
    {'field':'age','kind':'range','blocking':True,'parameters':{'min':0,'max':120}},
]


def save(data):
    temporary=STATE.with_suffix('.tmp')
    fd=os.open(temporary,os.O_WRONLY|os.O_CREAT|os.O_TRUNC,0o600)
    with os.fdopen(fd,'w') as stream:
        json.dump(data,stream,ensure_ascii=False,indent=2);stream.write('\n')
    os.replace(temporary,STATE)


class Seed:
    def __init__(self):
        FOLDER.mkdir(mode=0o700,parents=True,exist_ok=True)
        self.state=json.loads(STATE.read_text()) if STATE.exists() else {'version':1,'synthetic':True,'steps':{},'created_at':time.strftime('%Y-%m-%dT%H:%M:%S%z')}
        self.env=load_env()
        self.base=['docker','compose','--project-name','datamesh','--env-file',str(ENV_FILE)]
        self.client=build_opener(HTTPSHandler(context=ssl.create_default_context(cafile=str(ROOT/'.local/certs/server.crt'))),HTTPCookieProcessor(http.cookiejar.CookieJar()))
        credentials=json.loads((ROOT/'.local/bootstrap-admin.json').read_text())
        self.principal=self.request('/auth/login',{k:credentials[k] for k in ['username','password']},csrf=False)

    def request(self,path,body=None,key=None,csrf=True):
        headers={'Origin':ORIGIN,'Accept':'application/json'}
        if body is not None:
            headers['Content-Type']='application/json'
            if csrf:headers['X-CSRF-Token']=self.principal['csrf_token']
            if key:headers['Idempotency-Key']=str(uuid.uuid5(uuid.NAMESPACE_URL,'datamesh-demo-assets-v1:'+key))
        req=Request(ORIGIN+path,data=json.dumps(body,ensure_ascii=False).encode() if body is not None else None,headers=headers)
        try:
            with self.client.open(req,timeout=60) as response:
                raw=response.read();return json.loads(raw) if raw else None
        except HTTPError as error:
            try:detail=json.loads(error.read())
            except (ValueError,UnicodeError):detail={}
            raise RuntimeError(f"{path.split('?')[0]}: HTTP {error.code} {detail.get('code','')} {detail.get('message','请求未成功')} 追踪号 {detail.get('trace_id','')}") from None

    def close(self):
        req=Request(ORIGIN+'/auth/logout',method='POST',headers={'Origin':ORIGIN,'X-CSRF-Token':self.principal['csrf_token']})
        with self.client.open(req,timeout=15) as response:
            if response.status!=204:raise RuntimeError('Initialization session logout failed')

    def command(self,args,**kwargs):
        result=subprocess.run(self.base+args,cwd=ROOT,env=self.env,capture_output=True,**kwargs)
        if result.returncode:raise RuntimeError('Local data preparation failed; credentials and command output omitted')
        return result

    def sql(self,query,source=False):
        return self.command(['exec','-T','postgres-source' if source else 'postgres','psql','-v','ON_ERROR_STOP=1','-U','source_owner' if source else 'datamesh_owner','-d','sample' if source else 'datamesh','-At'],input=query,text=True).stdout.strip()

    def step(self,name,fn):
        if name not in self.state['steps']:
            result=fn();self.state['steps'][name]=result;save(self.state)
            print(json.dumps({'completed':name},ensure_ascii=False),flush=True)
        return self.state['steps'][name]

    def scoped(self,path):
        return path+('?' if '?' not in path else '&')+urlencode({'workspace_id':self.state['steps']['workspace']['id']})

    def backup(self):
        for service,user,database,label in [('postgres','datamesh_owner','datamesh','platform'),('postgres-source','source_owner','sample','source')]:
            file=FOLDER/(label+'-before.dump')
            if not file.exists():
                result=self.command(['exec','-T',service,'pg_dump','-U',user,'-d',database,'--format=custom'])
                fd=os.open(file,os.O_WRONLY|os.O_CREAT|os.O_EXCL,0o600)
                with os.fdopen(fd,'wb') as stream:stream.write(result.stdout)
        return {'platform':str(FOLDER/'platform-before.dump'),'source':str(FOLDER/'source-before.dump')}

    def prepare_source(self):
        exists=self.sql("SELECT COALESCE(obj_description(oid,'pg_namespace'),'') FROM pg_namespace WHERE nspname='"+SCHEMA+"';",True)
        if not exists:
            self.sql("""BEGIN;
CREATE SCHEMA datamesh_demo_v1;
COMMENT ON SCHEMA datamesh_demo_v1 IS 'DataMesh demo-assets-v1 synthetic fixture';
CREATE TABLE datamesh_demo_v1.customers(id bigint PRIMARY KEY,name text,age integer,city text);
CREATE TABLE datamesh_demo_v1.orders(order_id bigint PRIMARY KEY,customer_id bigint NOT NULL,amount numeric(12,2) NOT NULL,order_date date NOT NULL);
INSERT INTO datamesh_demo_v1.customers VALUES
(1,'  演示客户甲  ',28,' 杭州 '),(2,'演示客户乙 ',35,'上海'),(3,' 演示客户丙',42,'北京'),
(4,'   ',26,'深圳'),(5,'演示客户戊',-3,'杭州'),(6,'演示客户己',31,' 成都 '),(7,'演示客户庚 ',55,'广州'),(8,'演示客户辛',40,'南京');
INSERT INTO datamesh_demo_v1.orders VALUES
(101,1,120.00,'2026-10-01'),(102,2,210.00,'2026-10-02'),(103,1,80.00,'2026-10-03'),(104,3,350.00,'2026-10-04'),
(105,6,88.00,'2026-10-05'),(106,7,160.00,'2026-10-06'),(107,8,240.00,'2026-10-07'),(108,2,90.00,'2026-10-08'),
(109,6,66.00,'2026-10-09'),(110,3,175.00,'2026-10-10');
COMMIT;
""",True)
        elif exists!='DataMesh demo-assets-v1 synthetic fixture':raise RuntimeError('The demo schema is already used by other data; it was not modified')
        counts=self.sql('SELECT (SELECT count(*) FROM '+SCHEMA+'.customers),(SELECT count(*) FROM '+SCHEMA+'.orders);',True)
        if counts!='8|10':raise RuntimeError('Existing demo source rows differ; no rows were overwritten')
        return {'schema':SCHEMA,'customers':8,'orders':10}

    def approve(self,target):
        for _ in range(3):
            rows=self.request('/api/v1/directory-reviews?page_size=100')['items']
            reviews=[r for r in rows if r['definition']['target_id']==target]
            if not reviews:return
            for review in reviews:self.request('/api/v1/directory-reviews/'+review['review_id']+'/approve',{'expected_version':review['version'],'reason':'用户授权的虚构演示数据初始化，沿用管理员审核权限'},'approve-'+review['review_id'])
        raise RuntimeError('Review workflow did not reach a terminal state')

    def directory(self,key,name,description,fields,governed=False,public=False):
        definition={'name':name,'description':description,'classification':'theme' if governed else 'department','classification_path':'theme/demo' if governed else 'department/demo',
            'resource_code':'DEMO-'+key.upper(),'dictionary_id':self.state['steps']['dictionary']['id'],'provider_name':'DataMesh演示数据组','provider_code':'DEMO',
            'responsible_user_id':self.principal['user_id'],'update_frequency':'静态演示样例','format':'database','data_layer':'governed_dataset' if governed else 'raw_object','items':fields,
            'sharing_type':'conditional','sharing_conditions':'仅用于平台功能学习和演示，数据为虚构样例；使用仍需申请授权。','sharing_method':'query',
            'usage_restrictions':'虚构演示数据，不用于真实业务决策。','security_classification':'虚构演示/无真实个人信息','open_to_public':public,
            'open_conditions':'仅展示资产摘要，具体数据须登录并取得授权。' if public else '',
            'department_approval_ref':'DEMO-INIT-20261102-USER-AUTHORIZED','assessment_ref':'DEMO-SYNTHETIC-NO-REAL-PII'}
        row=self.step(key+'-directory',lambda:self.request('/api/v1/resource-directories',definition,'directory-'+key))
        self.step(key+'-directory-submit',lambda:self.request('/api/v1/resource-directories/'+row['directory_id']+'/submit',{'expected_version':row['version']},'submit-directory-'+key))
        self.approve(row['directory_id'])
        return self.request('/api/v1/resource-directories/'+row['directory_id'])

    def mount(self,key,directory,resource,fields):
        row=self.step(key+'-mount',lambda:self.request('/api/v1/resource-mounts',{'directory_id':directory['directory_id'],'resource':resource,'mapping':{f['name']:f['name'] for f in fields}},'mount-'+key))
        base='/api/v1/resource-mounts/'+row['mount_id']
        current=self.request(base)
        if current['state']=='active':return current
        if current['revision_state']=='draft':
            validated=self.step(key+'-mount-validation',lambda:self.request(base+'/validate',{'expected_version':current['version']},'validate-'+key))
            if not validated.get('definition',{}).get('validation',{}).get('passed',False):
                # Return the full validation only to the private seed log; it contains metadata, never source rows.
                raise RuntimeError('Mount validation failed for '+key+': '+json.dumps(validated.get('validation',validated.get('definition',{}).get('validation',{})),ensure_ascii=False))
        current=self.request(base)
        if current['revision_state']=='validated':self.step(key+'-mount-submit',lambda:self.request(base+'/submit',{'expected_version':current['version']},'submit-mount-'+key))
        self.approve(row['mount_id']);current=self.request(base)
        return self.step(key+'-mount-activate',lambda:self.request(base+'/activate',{'expected_version':current['version']},'activate-'+key))

    def source_resource(self,key,name,description,fields):
        directory=self.directory(key,name,description,fields)
        resource=self.step(key+'-resource',lambda:self.request('/api/v1/table-resources',{'directory_id':directory['directory_id'],'source_id':self.state['steps']['source']['source_id'],'schema':SCHEMA,'table':key,'expected_directory_version':directory['version']},'table-'+key))['resource']
        self.mount(key,directory,resource,fields)
        self.step(key+'-workspace-source',lambda:self.request(self.scoped('/api/v1/governance/workspaces/'+self.state['steps']['workspace']['id']+'/sources'),{'resource':resource,'workspace_id':self.state['steps']['workspace']['id']},'workspace-source-v2-'+key))
        return resource

    def model(self,key,name,fields,failure_rate):
        model_fields=[{k:f[k] for k in ['name','logical_type','nullable','precision','scale'] if k in f} for f in fields]
        model_fields[0]['primary_key']=True
        definition={'name':name,'description':'用户授权的虚构演示数据结果模型','owner_id':self.principal['user_id'],'steward_id':self.principal['user_id'],
            'model':{'layer':'logical','data_layer':'dwd','fields':model_fields,'contract':{'max_failure_rate':failure_rate,'usage_restrictions':'虚构演示数据，不用于真实业务决策。'}}}
        row=self.step(key+'-model',lambda:self.request(self.scoped('/api/v1/governance/models'),{'definition':definition},'model-'+key))
        self.step(key+'-model-publish',lambda:self.request(self.scoped('/api/v1/governance/models/'+row['id']+'/versions/1/publish'),{},'publish-model-'+key))
        return {'id':row['id'],'version':1}

    def flow(self,key,name,description,nodes,version=1):
        definition={'name':name,'description':description,'owner_id':self.principal['user_id'],'steward_id':self.principal['user_id'],'dataflow':{'nodes':nodes}}
        row=self.step(key+'-flow',lambda:self.request(self.scoped('/api/v1/governance/dataflows'),{'definition':definition},'flow-'+key))
        suffix='' if version==1 else '-v'+str(version)
        if version>1:
            self.step(key+'-flow'+suffix,lambda:self.request(self.scoped('/api/v1/governance/dataflows/'+row['id']+'/versions'),{'base_version':version-1,'definition':definition,'change_note':'在去空格后明确检查名称长度，隔离空白名称。'},'flow-'+key+suffix))
        publish={'migration_note':'初版为初始化核验定义，未发布资产或绑定下游。新版增加空白名称检查，重新计算并仅发布新版结果；旧运行保留审计。'} if version>1 else {}
        self.step(key+'-flow-publish'+suffix,lambda:self.request(self.scoped('/api/v1/governance/dataflows/'+row['id']+'/versions/'+str(version)+'/publish'),publish,'publish-flow-'+key+suffix+('-migration' if version>1 else '')))
        run=self.step(key+'-run'+suffix,lambda:self.request(self.scoped('/api/v1/governance/dataflows/'+row['id']+'/versions/'+str(version)+'/run'),{},'run-'+key+suffix))
        for _ in range(120):
            result=self.request(self.scoped('/api/v1/governance/runs/'+run['id']))
            if result['state']=='succeeded':return result
            if result['state'] in ['failed','cancelled','interrupted']:raise RuntimeError('Governance execution failed: '+result.get('error_code','unknown'))
            time.sleep(1)
        raise RuntimeError('Governance run still pending; inspect the saved run instead of creating another')

    def publish_asset(self,key,name,description,fields,run,expected_rows,expected_rejected):
        report=run['result']
        if report['output_rows']!=expected_rows or report['rejected_rows']!=expected_rejected:raise RuntimeError('Actual result differs from expected demo counts: '+key)
        self.step(key+'-dataset-publish',lambda:self.request(self.scoped('/api/v1/datasets/'+run['id']+'/publish'),{},'publish-asset-'+key))
        directory=self.directory(key,name,description,fields,governed=True,public=True)
        resource={'resource_type':'governed_dataset','resource_id':run['id'],'contract_version':1}
        self.mount(key,directory,resource,fields)
        return {'name':name,'resource':resource,'directory_id':directory['directory_id'],'rows':expected_rows,'rejected_rows':expected_rejected}

    def initialize(self):
        self.step('backup',self.backup)
        self.step('source-tables',self.prepare_source)
        self.step('dictionary',lambda:self.request('/api/v1/directory-dictionaries',{'name':'DataMesh虚构演示目录字典','version':'demo-v1','source':'本项目演示专用自定义编码，不代表国家编码字典','verified':True,'test_only':False,'code_pattern':'^DEMO-[A-Z0-9-]+$','classifications':['department/demo','theme/demo'],'verification_ref':'用户授权初始化虚构数据资产；仅校验本演示自定义编码'},'dictionary'))
        self.step('workspace',lambda:self.request('/api/v1/governance/workspaces',{'name':'演示资产：单表质量与双表关联','description':'仅两张来源表、两条简单数据流，全部为虚构数据。','max_concurrent_runs':1},'workspace'))
        self.step('source',lambda:self.request('/api/v1/sources',{'name':'演示资产源库（客户与订单）','kind':'postgres','host':'postgres-source','port':5432,'database':'sample','owner_id':self.principal['user_id'],'credentials':{'username':'source_owner','password':self.env['SOURCE_DB_PASSWORD']}},'source'))
        customers=self.source_resource('customers','演示·客户档案来源（含质量问题）','8条虚构客户数据，包含空白名称、年龄异常及文字空格，供质量处理学习。',CUSTOMER_FIELDS)
        orders=self.source_resource('orders','演示·客户订单来源','10条虚构订单数据，与有效客户通过客户编号关联。',ORDER_FIELDS)
        customer_node={'id':'customers','kind':'source','source':{'resource':customers,'fields':[f['name'] for f in CUSTOMER_FIELDS]}}
        clean={'id':'clean','kind':'transform','inputs':[{'node':'customers'}],'columns':[{'source':f['name'],'target':f['name'],'rule':'trim' if f['name'] in ['name','city'] else 'copy'} for f in CUSTOMER_FIELDS]}
        quality={'id':'quality','kind':'quality','inputs':[{'node':'clean'}],'checks':QUALITY_CHECKS}
        model=self.model('quality','演示·客户档案质量结果模型',CUSTOMER_FIELDS,.3)
        quality_run=self.flow('quality','演示·单表客户质量处理','去除姓名/城市空格，检查名称必填、长度至少1及年龄范围，隔离2条问题记录。',[customer_node,clean,quality,{'id':'output','kind':'output','inputs':[{'node':'quality','port':'good'}],'model':model}],version=2)
        quality_asset=self.publish_asset('quality','演示·客户档案（单表质量处理）','客户档案经过文字去空格、名称必填和年龄范围检查：8条来源记录保留6条合格记录，2条问题记录单独保留。虚构数据，仅供平台演示。',CUSTOMER_FIELDS,quality_run,6,2)
        join_model=self.model('joined','演示·客户订单关联结果模型',JOIN_FIELDS,.3)
        columns=[{'side':'right','source':'order_id','target':'order_id'},{'side':'left','source':'id','target':'customer_id'},{'side':'left','source':'name','target':'customer_name'},{'side':'left','source':'city','target':'city'},{'side':'right','source':'amount','target':'amount'},{'side':'right','source':'order_date','target':'order_date'}]
        joined=self.flow('joined','演示·客户与订单双表关联','先检查客户质量，再按客户编号关联订单，产出10条客户订单明细。',[customer_node,{'id':'orders','kind':'source','source':{'resource':orders,'fields':[f['name'] for f in ORDER_FIELDS]}},clean,quality,{'id':'joined','kind':'join','inputs':[{'node':'quality','port':'good'},{'node':'orders'}],'join':{'kind':'inner','left_keys':['id'],'right_keys':['customer_id'],'columns':columns}},{'id':'output','kind':'output','inputs':[{'node':'joined'}],'model':join_model}])
        joined_asset=self.publish_asset('joined','演示·客户订单明细（双表关联）','将6条质量合格客户与10条订单按客户编号关联，得到10条带客户名称和城市的订单明细。可追溯至两张来源表。虚构数据，仅供平台演示。',JOIN_FIELDS,joined,10,2)
        public=self.request('/api/public/assets?page_size=100')
        ids={a['dataset_id'] for a in public['items']}
        if any(a['resource']['resource_id'] not in ids for a in [quality_asset,joined_asset]):raise RuntimeError('Published demo assets are not visible on the homepage')
        delivered=[]
        for asset,expected_sources in [(quality_asset,{customers['resource_id']}),(joined_asset,{customers['resource_id'],orders['resource_id']})]:
            response=self.request('/delivery/v1/datasets/'+asset['resource']['resource_id']+'/records?contract_version=1&limit=20')
            if len(response.get('records',[]))!=asset['rows']:raise RuntimeError('Controlled asset delivery row count differs')
            delivered.append(response['records'])
            lineage=self.request(self.scoped('/api/v1/datasets/'+asset['resource']['resource_id']+'/lineage'))
            actual_sources={origin['resource']['resource_id'] for origins in lineage['fields'].values() for origin in origins}
            if actual_sources!=expected_sources:raise RuntimeError('Asset source lineage differs from the prepared tables')
        if {int(row['id']) for row in delivered[0]}!={1,2,3,6,7,8}:raise RuntimeError('Quality output did not isolate the intended invalid customers')
        if any(not row['name'] or row['name']!=row['name'].strip() or row['city']!=row['city'].strip() or not 0<=int(row['age'])<=120 for row in delivered[0]):raise RuntimeError('Quality output still contains invalid or untrimmed values')
        customers_by_id={int(row['id']):row for row in delivered[0]}
        if {int(row['order_id']) for row in delivered[1]}!=set(range(101,111)):raise RuntimeError('Join output omitted or duplicated an order')
        if any(row['customer_name']!=customers_by_id.get(int(row['customer_id']),{}).get('name') or row['city']!=customers_by_id[int(row['customer_id'])]['city'] for row in delivered[1]):raise RuntimeError('Join output customer attributes differ')
        if sum(Decimal(str(row['amount'])) for row in delivered[1])!=Decimal('1579.00'):raise RuntimeError('Join output changed order amounts')
        self.prepare_source()
        self.state.update(status='verified',assets=[quality_asset,joined_asset],homepage_total=public['total']);save(self.state)
        print(json.dumps({'status':'verified','source_tables':2,'source_rows':[8,10],'assets':self.state['assets'],'homepage_total':public['total']},ensure_ascii=False),flush=True)


def main():
    from initialize_business_scenarios import main as initialize_business
    initialize_business()


if __name__=='__main__':
    try:main()
    except (RuntimeError,OSError,ValueError,subprocess.SubprocessError) as error:
        print(str(error) if isinstance(error,RuntimeError) else 'Initialization did not complete; existing data was not overwritten and credentials were omitted',file=sys.stderr);sys.exit(1)
