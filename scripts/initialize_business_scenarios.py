#!/usr/bin/env python3
"""Initialize versioned business sources and governed assets through the existing platform."""
import argparse
import csv
import hashlib
import json
import os
from pathlib import Path
import subprocess
import sys
import time
from datetime import datetime
from decimal import Decimal

from business_scenarios import VERSION, SCENARIOS, build_tables, table_schema_sql, insertion_sql, sql_literal
from seed_demo_assets import Seed, ROOT

NAME='municipal-business-scenarios'
RELATIONS=[
 ('population.residents','household_id','population.households','household_id'),
 ('population.residence_records','person_id','population.residents','person_id'),
 ('enrollment.education_students','child_id','population.residents','person_id'),
 ('enrollment.police_children','child_id','population.residents','person_id'),
 ('enrollment.police_children','guardian_id','population.residents','person_id'),
 ('enrollment.civil_marriages','guardian_id','population.residents','person_id'),
 ('enrollment.civil_marriages','spouse_id','population.residents','person_id'),
 ('enrollment.market_entities','entity_code','corporate.enterprises','entity_code'),
 ('enrollment.tax_registrations','entity_code','corporate.enterprises','entity_code'),
 ('corporate.representatives','entity_code','corporate.enterprises','entity_code'),
 ('corporate.representatives','person_id','population.residents','person_id'),
 ('corporate.annual_tax','entity_code','corporate.enterprises','entity_code'),
 ('deceased.death_registrations','person_id','population.residents','person_id'),
 ('deceased.household_cancellations','death_record_id','deceased.death_registrations','death_record_id'),
 ('mediation.parties','case_id','mediation.cases','case_id'),
 ('mediation.parties','person_id','population.residents','person_id'),
 ('mediation.resolutions','case_id','mediation.cases','case_id'),
 ('real_estate.ownerships','property_id','real_estate.property_units','property_id'),
 ('real_estate.ownerships','person_id','population.residents','person_id'),
 ('real_estate.transactions','property_id','real_estate.property_units','property_id'),
]

def persist(path,data):
 path.parent.mkdir(parents=True,exist_ok=True)
 tmp=path.with_suffix('.tmp');fd=os.open(tmp,os.O_WRONLY|os.O_CREAT|os.O_TRUNC,0o600)
 with os.fdopen(fd,'w') as stream:json.dump(data,stream,ensure_ascii=False,indent=2);stream.write('\n')
 os.replace(tmp,path)

def export_package(folder):
 folder=Path(folder);folder.mkdir(parents=True,exist_ok=True)
 tables=build_tables();sql=['BEGIN;']
 for schema in SCENARIOS:sql.append('CREATE SCHEMA IF NOT EXISTS '+schema+';')
 manifest={'name':NAME,'version':VERSION,'synthetic':True,'as_of':'2026-10-31','tables':[]}
 for table in tables:
  sql.extend([table_schema_sql(table),insertion_sql(table)])
  with (folder/(table.qualified+'.csv')).open('w',newline='',encoding='utf-8-sig') as stream:
   writer=csv.writer(stream);writer.writerow([c['name'] for c in table.columns]);writer.writerows(table.rows)
  manifest['tables'].append({'table':table.qualified,'title':table.title,'provider':table.provider,'rows':len(table.rows),'fields':table.fields,'sha256':table.digest})
 sql.append('COMMIT;')
 (folder/'source.sql').write_text('\n'.join(sql)+'\n')
 persist(folder/'datasets.json',manifest)
 (folder/'README.md').write_text('# 业务初始化迁移包\n\n版本 '+VERSION+'。包含21张业务表、75,300行虚构业务记录，不含账号、连接凭据或真实自然人资料。\n\n新环境请复制项目代码，完成数据库迁移及管理员配置，再运行 `python3 scripts/initialize_business_scenarios.py --initialize`；脚本会重建相同业务记录并通过平台正常编目、治理和审核流程建立资产。\n\n迁移已有平台时，备份并恢复平台 PostgreSQL、来源 PostgreSQL、Doris 与对象存储，以及私密运行配置。平台初始化登记表和来源内容摘要随数据库迁移；运行同一命令核对并续执行，不复制旧机器随机连接标识或导出凭据。治理配置另由 `python3 scripts/initialize_governance_scenarios.py --initialize` 追加，需在平台迁移至45且业务初始化核对完成后运行；复用已有来源与空间，并独立保存检查点。监测、落标、指标和场景方案会建立真实运行；主数据确认与六份口径修订的独立审核由实际人员办理。\n\nsource.sql 只适用于空业务库，已有库应使用初始化脚本的归属与摘要核对，禁止直接覆盖业务表。\n')
 return manifest

class Initializer(Seed):
 def __init__(self):
  super().__init__()
  self.folder=ROOT/'.local/business-initialization'/VERSION
  self.folder.mkdir(parents=True,exist_ok=True)
  raw=self.sql("SELECT state::text FROM platform_initializations WHERE name="+sql_literal(NAME))
  self.state=json.loads(raw) if raw else {'name':NAME,'version':VERSION,'synthetic':True,'steps':{}}
  if self.state['version']!=VERSION:raise RuntimeError('Initialization version differs; use the matching versioned migration')
  self.tables=build_tables();self.by_name={t.qualified:t for t in self.tables}
  self.current_scenario='enrollment'
 def save(self):
  persist(self.folder/'progress.json',self.state)
  self.sql('INSERT INTO platform_initializations(name,version,state) VALUES('+sql_literal(NAME)+','+sql_literal(VERSION)+','+sql_literal(json.dumps(self.state,ensure_ascii=False))+'::jsonb) ON CONFLICT(name) DO UPDATE SET version=excluded.version,state=excluded.state,updated_at=now()')
 def step(self,key,fn):
  if key not in self.state['steps']:
   self.state['steps'][key]=fn();self.save()
  return self.state['steps'][key]
 def request(self,path,body=None,key=None,csrf=True):
  return super().request(path,body,key=(NAME+':'+VERSION+':'+key) if key else None,csrf=csrf)
 def sql(self,statement,source=False):
  service,user,database=('postgres-source','source_owner','sample') if source else ('postgres','datamesh_owner','datamesh')
  result=subprocess.run(self.base+['exec','-T',service,'psql','-v','ON_ERROR_STOP=1','-U',user,'-d',database,'-At'],cwd=ROOT,env=self.env,input=statement+'\n',text=True,capture_output=True,timeout=180)
  if result.returncode:
   (self.folder/'database-error.log').write_text(result.stderr)
   raise RuntimeError('Initialization database operation failed; inspect the private database-error.log')
  return result.stdout.strip()
 def backup_current(self):
  folder=self.folder/('backup-'+datetime.now().strftime('%Y%m%d-%H%M%S'));folder.mkdir(mode=0o700)
  for key,service,user,database in [('platform','postgres','datamesh_owner','datamesh'),('source','postgres-source','source_owner','sample')]:
   result=subprocess.run(self.base+['exec','-T',service,'pg_dump','-U',user,'-d',database,'-Fc'],cwd=ROOT,env=self.env,capture_output=True,check=True,timeout=120)
   fd=os.open(folder/(key+'.dump'),os.O_WRONLY|os.O_CREAT|os.O_EXCL,0o600)
   with os.fdopen(fd,'wb') as stream:stream.write(result.stdout)
   subprocess.run(self.base+['exec','-T',service,'pg_restore','--list'],cwd=ROOT,env=self.env,input=result.stdout,capture_output=True,check=True,timeout=60)
  return str(folder)
 def source_hash(self,table):
  return self.sql("SELECT md5(COALESCE(jsonb_agg(to_jsonb(t) ORDER BY t."+table.columns[0]['name']+")::text,'[]')) FROM "+table.qualified+' t;',True)
 def legacy_matches(self,table):
  if table.scenario!='enrollment':return False
  candidates=[ROOT/'.local/enrollment-init'/(table.name+'.csv'),ROOT/'.local/enrollment-students'/(table.name+'.csv')]
  file=next((p for p in candidates if p.exists()),None)
  if file is None:return False
  with file.open(encoding='utf-8-sig',newline='') as stream:old=list(csv.DictReader(stream))
  if len(old)!=260:return False
  current=json.loads(self.sql("SELECT COALESCE(jsonb_agg(to_jsonb(t)),'[]')::text FROM "+table.qualified+' t;',True))
  def normalize(rows):
   result=[]
   for row in rows:
    values=[]
    for col in table.columns:
     value=row[col['name']]
     if value is None or (value=='' and col['nullable']):values.append(None)
     elif col['logical_type']=='decimal':values.append(str(Decimal(str(value)).quantize(Decimal('.01'))))
     else:values.append(str(value))
    result.append(tuple(values))
   return sorted(result,key=lambda row:row[0])
  return normalize(old)==normalize(current)
 def prepare_sources(self):
  ledger_exists=self.sql("SELECT to_regclass('platform_initialization.table_versions') IS NOT NULL;",True)=='t'
  ledger={}
  if ledger_exists:
   ledger=json.loads(self.sql("SELECT COALESCE(jsonb_object_agg(table_name,to_jsonb(t)),'{}') FROM platform_initialization.table_versions t;",True))
  changes=[];current_hashes={}
  for table in self.tables:
   exists=self.sql("SELECT to_regclass("+sql_literal(table.qualified)+") IS NOT NULL;",True)=='t'
   if exists:
    digest=self.source_hash(table);current_hashes[table.qualified]=digest
    previous=ledger.get(table.qualified)
    if previous:
     if digest!=previous['data_hash']:raise RuntimeError('Business table was modified outside initialization; not overwritten: '+table.qualified)
     if previous['version']==VERSION and previous['generator_hash']==table.digest:continue
    elif not self.legacy_matches(table):raise RuntimeError('Existing table is not a recognized initialization table: '+table.qualified)
   changes.append((table,exists))
  if not changes:return {'changed':0,'tables':len(self.tables),'rows':sum(len(t.rows) for t in self.tables)}
  sql=['BEGIN;','SELECT pg_advisory_xact_lock(20261104);','CREATE SCHEMA IF NOT EXISTS platform_initialization;',
   'CREATE TABLE IF NOT EXISTS platform_initialization.table_versions(table_name text PRIMARY KEY,version text NOT NULL,generator_hash text NOT NULL,data_hash text NOT NULL,rows bigint NOT NULL,updated_at timestamptz NOT NULL DEFAULT now());','SET CONSTRAINTS ALL DEFERRED;']
  for schema in SCENARIOS:sql+=['CREATE SCHEMA IF NOT EXISTS '+schema+';','GRANT USAGE ON SCHEMA '+schema+' TO datamesh_cdc;']
  for table,exists in changes:
   if exists:
    expected=current_hashes[table.qualified]
    hash_sql="SELECT md5(COALESCE(jsonb_agg(to_jsonb(t) ORDER BY t."+table.columns[0]['name']+")::text,'[]')) FROM "+table.qualified+' t'
    sql+=['DO $$ BEGIN IF ('+hash_sql+')<>'+sql_literal(expected)+" THEN RAISE EXCEPTION 'Initialization table changed concurrently'; END IF; END $$;",'DELETE FROM '+table.qualified+';']
   else:sql.append(table_schema_sql(table))
   sql += [insertion_sql(table),'GRANT SELECT ON '+table.qualified+' TO datamesh_cdc;']
   hash_sql="SELECT md5(COALESCE(jsonb_agg(to_jsonb(t) ORDER BY t."+table.columns[0]['name']+")::text,'[]')) FROM "+table.qualified+' t'
   sql.append('INSERT INTO platform_initialization.table_versions(table_name,version,generator_hash,data_hash,rows) VALUES('+','.join([sql_literal(table.qualified),sql_literal(VERSION),sql_literal(table.digest),'('+hash_sql+')',str(len(table.rows))])+') ON CONFLICT(table_name) DO UPDATE SET version=excluded.version,generator_hash=excluded.generator_hash,data_hash=excluded.data_hash,rows=excluded.rows,updated_at=now();')
  for n,(table,col,parent,key) in enumerate(RELATIONS):
   sql.append("DO $$ BEGIN IF NOT EXISTS(SELECT 1 FROM pg_constraint WHERE conrelid="+sql_literal(table)+"::regclass AND conname='business_relation_"+str(n)+"') THEN ALTER TABLE "+table+' ADD CONSTRAINT business_relation_'+str(n)+' FOREIGN KEY ('+col+') REFERENCES '+parent+'('+key+') DEFERRABLE INITIALLY DEFERRED; END IF; END $$;')
  sql.append('COMMIT;');self.sql('\n'.join(sql),True)
  return {'changed':len(changes),'tables':len(self.tables),'rows':sum(len(t.rows) for t in self.tables)}
 def approve(self,target):
  for _ in range(4):
   rows=self.request('/api/v1/directory-reviews?page_size=100')['items']
   matches=[r for r in rows if r['definition']['target_id']==target]
   if not matches:return
   for row in matches:self.request('/api/v1/directory-reviews/'+row['review_id']+'/approve',{'expected_version':row['version'],'reason':'核对初始化业务来源、字段和使用范围'},'review-'+row['review_id'])
  raise RuntimeError('Directory approval has not reached a terminal state')
 def directory(self,key,name,description,fields,governed=False,public=False,api=False):
  def create():
   value={'name':name,'description':description,'classification':'theme' if governed else 'department','classification_path':SCENARIOS[self.current_scenario],
    'resource_code':'','dictionary_id':self.state['steps']['dictionary']['id'],'provider_name':'市数据资源中心','provider_code':'RES-CENTER',
    'responsible_user_id':self.principal['user_id'],'update_frequency':'每月','format':'api' if api else 'database','data_layer':'governed_dataset' if governed else 'raw_object','items':fields,
    'sharing_type':'conditional','sharing_conditions':'按职责范围申请使用，限定业务用途和使用期限。','sharing_method':'invoke' if api else 'query',
    'usage_restrictions':'按批准用途使用，未经许可不得转供或作出个人资格决定。','security_classification':'内部业务数据','open_to_public':public,
    'open_conditions':'公开目录摘要和字段说明；实际数据按授权范围交付。' if public else '',
    'department_approval_ref':'业务资源编目审核记录','assessment_ref':'初始化数据来源与安全核对记录','related_resource_codes':[]}
   return self.request('/api/v1/resource-directories',value,'directory-'+key)
  row=self.step(key+'-directory',create);base='/api/v1/resource-directories/'+row['directory_id']
  current=self.request(base)
  if current['state']!='published':
   if current['revision_state']=='draft':self.request(base+'/submit',{'expected_version':current['version'],'reason':'新增业务资源目录'},'submit-'+key)
   self.approve(row['directory_id'])
  return self.request(base)
 def mount(self,key,directory,resource,fields):
  row=self.step(key+'-mount',lambda:self.request('/api/v1/resource-mounts',{'directory_id':directory['directory_id'],'resource':resource,'mapping':{f['name']:f['name'] for f in fields}},'mount-'+key))
  base='/api/v1/resource-mounts/'+row['mount_id'];current=self.request(base)
  if current['state']=='active':return current
  if current['revision_state']=='draft':
   current=self.request(base+'/validate',{'expected_version':current['version']},'validate-'+key+'-v'+str(current['version']))
   if not current.get('definition',{}).get('validation',{}).get('passed'):raise RuntimeError('Mount validation failed for '+key+': '+json.dumps(current['definition'].get('validation'),ensure_ascii=False))
  if current['revision_state']=='validated':
   self.request(base+'/submit',{'expected_version':current['version']},'submit-mount-'+key+'-v'+str(current['version']))
  self.approve(row['mount_id']);current=self.request(base)
  if current['revision_state']=='approved':return self.request(base+'/activate',{'expected_version':current['version']},'activate-'+key+'-v'+str(current['version']))
  raise RuntimeError('Mount is not ready to activate: '+key)
 def register_table(self,table):
  key=table.qualified;self.current_scenario=table.scenario
  directory=self.directory(key,table.title,'包含'+table.title+'的业务字段。来源单位：'+table.provider+'。可用于'+SCENARIOS[table.scenario]+'的治理、关联和受控共享。',table.fields)
  resource=self.step(key+'-resource',lambda:self.request('/api/v1/table-resources',{'directory_id':directory['directory_id'],'source_id':self.state['steps']['source']['source_id'],'schema':table.scenario,'table':table.name,'expected_directory_version':directory['version']},'table-'+key))['resource']
  self.mount(key,directory,resource,table.fields)
  self.step(key+'-workspace-source',lambda:self.request(self.scoped('/api/v1/governance/workspaces/'+self.state['steps']['workspace']['id']+'/sources'),{'resource':resource,'workspace_id':self.state['steps']['workspace']['id']},'workspace-source-'+key))
  return resource
 def model(self,key,name,fields,failure_rate=0):
  model_fields=[{k:f[k] for k in ['name','logical_type','nullable','sensitive','precision','scale'] if k in f} for f in fields]
  for target,source in zip(model_fields,fields):target['description']=source.get('description') or source.get('business_name') or ''
  model_fields[0]['primary_key']=True
  definition={'name':name,'description':'统一业务主键、字段类型和质量要求。','owner_id':self.principal['user_id'],'steward_id':self.principal['user_id'],
   'model':{'layer':'logical','data_layer':'dwd','fields':model_fields,'contract':{'max_failure_rate':failure_rate,'usage_restrictions':'按批准用途使用，未经许可不得转供。'}}}
  row=self.step(key+'-model',lambda:self.request(self.scoped('/api/v1/governance/models'),{'definition':definition},'model-'+key))
  self.step(key+'-model-publish',lambda:self.request(self.scoped('/api/v1/governance/models/'+row['id']+'/versions/1/publish'),{},'model-publish-'+key))
  return {'id':row['id'],'version':1}
 def build_asset(self,scenario,resources):
  definitions={
   'enrollment':('招生报名综合信息','enrollment.education_students','enrollment.police_children','child_id','child_id',[
    ('left','student_id','student_id'),('left','child_id','child_id'),('left','student_name','student_name'),('left','birth_date','birth_date'),('left','enrollment_year','enrollment_year'),('left','application_status','application_status'),('right','guardian_id','guardian_id'),('right','guardian_name','guardian_name'),('right','residence_address','residence_address')]),
   'population':('人口基础信息库','population.residents','population.households','household_id','household_id',[
    ('left','person_id','person_id'),('left','person_name','person_name'),('left','gender_code','gender_code'),('left','birth_date','birth_date'),('left','population_status','population_status'),('left','household_id','household_id'),('right','household_address','household_address'),('right','community_name','community_name')]),
   'corporate':('法人主体综合信息库','corporate.enterprises','corporate.representatives','entity_code','entity_code',[
    ('left','entity_code','entity_code'),('left','entity_name','entity_name'),('left','entity_type','entity_type'),('left','industry_name','industry_name'),('left','business_status','business_status'),('left','registered_capital','registered_capital'),('right','representative_name','representative_name'),('right','person_id','representative_person_id')]),
   'deceased':('死亡人口核验专项库','deceased.death_registrations','deceased.household_cancellations','death_record_id','death_record_id',[
    ('left','death_record_id','death_record_id'),('left','person_id','person_id'),('left','person_name','person_name'),('left','death_date','death_date'),('right','cancellation_status','cancellation_status'),('right','cancellation_date','cancellation_date'),('right','handling_unit','handling_unit')]),
   'mediation':('矛盾纠纷调解办理信息','mediation.cases','mediation.resolutions','case_id','case_id',[
    ('left','case_id','case_id'),('left','case_type','case_type'),('left','community_name','community_name'),('left','accepted_date','accepted_date'),('left','case_status','case_status'),('left','dispute_amount','dispute_amount'),('right','resolution_status','resolution_status'),('right','agreement_date','agreement_date'),('right','agreed_amount','agreed_amount'),('right','performance_status','performance_status')]),
   'real_estate':('不动产权属综合信息','real_estate.ownerships','real_estate.property_units','property_id','property_id',[
    ('left','ownership_id','ownership_id'),('left','property_id','property_id'),('left','person_id','person_id'),('left','owner_name','owner_name'),('left','ownership_share','ownership_share'),('left','certificate_no','certificate_no'),('right','property_address','property_address'),('right','property_area','property_area'),('right','property_type','property_type')]),
  }
  name,left_name,right_name,left_key,right_key,selected=definitions[scenario]
  left,right=self.by_name[left_name],self.by_name[right_name]
  fields=[]
  for side,source,target in selected:
   field=next(f for f in (left.fields if side=='left' else right.fields) if f['name']==source).copy();field['name']=target;fields.append(field)
  key=scenario+'-asset';model=self.model(key,name+'结果模型',fields)
  nodes=[]
  for side,table in [('left',left),('right',right)]:
   nodes.append({'id':side,'kind':'source','source':{'resource':resources[table.qualified],'fields':[f['name'] for f in table.fields]}})
   nodes.append({'id':side+'_clean','kind':'transform','inputs':[{'node':side}],'columns':[{'source':f['name'],'target':f['name'],'rule':'trim' if f['logical_type']=='string' else 'copy'} for f in table.fields]})
  nodes+=[{'id':'quality','kind':'quality','inputs':[{'node':'left_clean'}],'checks':[{'field':left.columns[0]['name'],'kind':'required','blocking':True},{'field':left.columns[0]['name'],'kind':'length','blocking':True,'parameters':{'min':1}}]},
   {'id':'linked','kind':'join','inputs':[{'node':'quality','port':'good'},{'node':'right_clean'}],'join':{'kind':'inner','left_keys':[left_key],'right_keys':[right_key],'columns':[{'side':side,'source':source,'target':target} for side,source,target in selected]}},
   {'id':'output','kind':'output','inputs':[{'node':'linked'}],'model':model}]
  description='对'+left.title+'进行主键与必填检查、文本规范化，并关联'+right.title+'，形成'+name+'。'
  run=self.flow(key,name+'治理流程',description,nodes)
  if run['result']['output_rows']!=len(left.rows) or run['result']['rejected_rows']!=0:raise RuntimeError('Governed asset row count differs: '+scenario)
  self.step(key+'-published',lambda:self.request(self.scoped('/api/v1/datasets/'+run['id']+'/publish'),{},'publish-'+key))
  directory=self.directory(key,name,description,fields,governed=True,public=True)
  resource={'resource_type':'governed_dataset','resource_id':run['id'],'contract_version':1};self.mount(key,directory,resource,fields)
  return {'scenario':scenario,'name':name,'resource':resource,'directory_id':directory['directory_id'],'rows':len(left.rows),'fields':fields,'sources':[resources[left_name],resources[right_name]],'workspace_id':self.state['steps']['workspace']['id']}
 def create_service(self,asset):
  self.current_scenario='enrollment'
  fields=[f for f in asset['fields'] if f['name'] in ['student_id','child_id','student_name','enrollment_year','application_status']]
  directory=self.request('/api/v1/resource-directories/'+asset['directory_id'])
  body={'name':'招生报名信息查询','dataset_id':asset['resource']['resource_id'],'mode':'snapshot','sql':'SELECT '+', '.join(f['name'] for f in fields)+' FROM result WHERE enrollment_year = :year;','parameters':{'year':'2027'},'max_page_size':100,'directory_id':directory['directory_id'],'expected_directory_version':directory['version']}
  preview=self.request('/api/v1/data-services/validate',body)
  if len(preview['records'])!=20:raise RuntimeError('SQL service preview did not return 20 rows')
  def create():
   for page in range(1,100):
    items=self.request('/api/v1/data-services?page='+str(page))
    existing=next((r for r in items['items'] if r['name']==body['name'] and r['directory_id']==directory['directory_id'] and r['query']['dataset_id']==asset['resource']['resource_id']),None)
    if existing:return {'resource':{'resource_type':'api_resource','resource_id':existing['id'],'contract_version':1},'directory_id':directory['directory_id']}
    if page*20>=items['total']:break
   return self.request('/api/v1/data-services',body)
  result=self.step('enrollment-query-service',create)
  self.mount('enrollment-query-service',directory,result['resource'],fields)
  return {'name':body['name'],'resource':result['resource'],'directory_id':directory['directory_id'],'preview_rows':len(preview['records'])}
 def retire_legacy(self):
  targets=set()
  for file in [ROOT/'.local/enrollment-init/registered.json',ROOT/'.local/enrollment-students/registered.json']:
   if file.exists():targets.update(x['directory_id'] for x in json.loads(file.read_text()).get('datasets',[]))
  old=ROOT/'.local/demo-assets-v1/manifest.json'
  if old.exists():
   content=json.loads(old.read_text())
   for value in content.get('steps',{}).values():
    if isinstance(value,dict) and value.get('directory_id'):targets.add(value['directory_id'])
  if old.exists():
   content=json.loads(old.read_text())
   for asset in content.get('assets',[]):
    rid=asset['resource']['resource_id'];row=self.request('/api/v1/datasets/'+rid)
    if row['publication_status']=='published':self.request('/api/v1/datasets/'+rid+'/withdraw?workspace_id='+content['steps']['workspace']['id'],{},'withdraw-old-asset-'+rid)
  for scenario_file in [ROOT/'.local/enrollment-init/registered.json',old]:
   if not scenario_file.exists():continue
   content=json.loads(scenario_file.read_text());workspace=content.get('workspace') or content.get('steps',{}).get('workspace')
   if workspace:
    row=self.request('/api/v1/governance/workspaces/'+workspace['id'])
    if row['state']=='active':
     title='招生报名历史治理记录' if scenario_file!=old else '客户交易历史治理记录'
     if row['name']!=title:self.request('/api/v1/governance/workspaces/'+row['id']+'/update',{'base_version':row['version'],'name':title,'description':'保留前期来源关系、定义与运行记录，当前业务使用新场景治理空间。','max_concurrent_runs':row['max_concurrent_runs']},'rename-old-workspace-'+row['id'])
  source_names={}
  enrollment_source=self.sql("SELECT id FROM business_records WHERE kind='source' AND body->>'name'='招生报名演示源库'")
  if enrollment_source:source_names[enrollment_source]='招生报名历史数据源'
  if old.exists():
   previous=json.loads(old.read_text()).get('steps',{}).get('source',{})
   if previous.get('source_id'):source_names[previous['source_id']]='客户交易历史数据源'
  for source_id,title in source_names.items():
   row=self.request('/api/v1/sources/'+source_id)
   if row['name']!=title:self.request('/api/v1/sources/'+source_id+'/metadata',{'name':title,'expected_version':row['source_version'],'reason':'业务初始化升级，保留历史连接并规范展示名称'},'rename-old-source-'+source_id)
  retired=[]
  for directory_id in sorted(targets):
   row=self.request('/api/v1/resource-directories/'+directory_id)
   if row['state']=='published':
    self.request('/api/v1/resource-directories/'+directory_id+'/withdraw',{'expected_version':row['version'],'reason':'业务初始化升级，旧条目保留历史并退出在用目录'},'retire-directory-'+directory_id)
   retired.append(directory_id)
  return retired
 def verify(self,assets):
  checks={}
  for table in self.tables:
   count=int(self.sql('SELECT count(*) FROM '+table.qualified+';',True))
   if count!=len(table.rows):raise RuntimeError('Source scale differs: '+table.qualified)
   checks[table.qualified]=count
  for table,col,parent,key in RELATIONS:
   if self.sql('SELECT count(*) FROM '+table+' t LEFT JOIN '+parent+' p ON t.'+col+'=p.'+key+' WHERE p.'+key+' IS NULL;',True)!='0':raise RuntimeError('Broken relationship: '+table+'.'+col)
  if self.sql('SELECT count(*) FROM (SELECT property_id,sum(ownership_share) AS total FROM real_estate.ownerships GROUP BY property_id) s WHERE total<>100;',True)!='0':raise RuntimeError('Property ownership shares differ')
  public=self.request('/api/public/assets?page_size=100');public_ids={r['dataset_id'] for r in public['items']}
  for asset in assets:
   if asset['resource']['resource_id'] not in public_ids:raise RuntimeError('Asset is absent from homepage: '+asset['name'])
   sample=self.request('/delivery/v1/datasets/'+asset['resource']['resource_id']+'/records?contract_version=1&limit=20')
   if len(sample['records'])!=20:raise RuntimeError('Controlled asset preview failed: '+asset['name'])
   lineage=self.request('/api/v1/datasets/'+asset['resource']['resource_id']+'/lineage?workspace_id='+asset['workspace_id'])
   sources={origin['resource']['resource_id'] for origins in lineage['fields'].values() for origin in origins}
   if sources!={r['resource_id'] for r in asset['sources']}:raise RuntimeError('Asset lineage differs: '+asset['name'])
  return {'source_tables':checks,'rows':sum(checks.values()),'assets':[{'name':a['name'],'rows':a['rows'],'dataset_id':a['resource']['resource_id']} for a in assets],'homepage_total':public['total'],'relationships':len(RELATIONS),'sql_service':self.state['service'],'synthetic':True}
 def initialize(self):
  backup=self.backup_current();print(json.dumps({'stage':'backup','path':backup}),flush=True)
  source=self.prepare_sources();print(json.dumps({'stage':'sources',**source}),flush=True)
  self.step('dictionary',lambda:self.request('/api/v1/directory-dictionaries',{'name':'市级数据资源内部编码字典','version':'2026','source':'平台内部业务编目规则，不作为国家资源编码标准','verified':True,'test_only':False,'code_pattern':'^DR-[0-9]{8}$','code_template':'DR-{sequence}','sequence_width':8,'classifications':list(SCENARIOS.values()),'verification_ref':'业务初始化编目规则核对记录'},'dictionary'))
  self.step('source',lambda:self.request('/api/v1/sources',{'name':'市级业务数据源','kind':'postgres','host':'postgres-source','port':5432,'database':'sample','owner_id':self.principal['user_id'],'credentials':{'username':'source_owner','password':self.env['SOURCE_DB_PASSWORD']}},'source'))
  resources={};assets=[]
  for scenario,title in SCENARIOS.items():
   self.current_scenario=scenario
   workspace=self.step(scenario+'-workspace',lambda:self.request('/api/v1/governance/workspaces',{'name':title+'治理空间','description':'管理'+title+'相关业务来源、字段、质量规则、关联处理和成果交付。','max_concurrent_runs':2},'workspace-'+scenario))
   self.state['steps']['workspace']=workspace;self.save()
   for table in self.tables:
    if table.scenario==scenario:resources[table.qualified]=self.register_table(table)
   self.current_scenario=scenario;asset=self.build_asset(scenario,resources);assets.append(asset)
   print(json.dumps({'stage':'asset','name':asset['name'],'rows':asset['rows']},ensure_ascii=False),flush=True)
  self.state['assets']=assets;self.state['service']=self.create_service(assets[0]);self.save()
  self.state['retired_legacy_directories']=self.retire_legacy();self.save()
  report=self.verify(assets);report.update(status='verified',version=VERSION,backup=backup,source_changes=source['changed'])
  self.state['status']='verified';self.save();persist(self.folder/'verification.json',report)
  export_package(self.folder/'migration-package')
  print(json.dumps(report,ensure_ascii=False),flush=True)

def main():
 parser=argparse.ArgumentParser(description=__doc__);group=parser.add_mutually_exclusive_group(required=True);group.add_argument('--initialize',action='store_true');group.add_argument('--export',type=Path)
 args=parser.parse_args()
 if args.export:
  result=export_package(args.export);print(json.dumps({'version':VERSION,'tables':len(result['tables']),'rows':sum(t['rows'] for t in result['tables'])}));return
 init=Initializer()
 try:init.initialize()
 finally:init.close()

if __name__=='__main__':
 try:main()
 except (RuntimeError,OSError,ValueError,subprocess.SubprocessError) as error:
  print(str(error) if isinstance(error,RuntimeError) else 'Business initialization failed; checkpoints retained and credentials omitted',file=sys.stderr);sys.exit(1)
