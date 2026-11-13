"""Idempotent governance configuration for existing municipal business sources.

Run after initialize_business_scenarios.py; never recreates sources, users or assets.
"""
import argparse
import copy
import json
import time
from pathlib import Path
from urllib.parse import urlencode
from seed_demo_assets import Seed,ROOT
from business_scenarios import SCENARIOS,VERSION as SOURCE_VERSION,build_tables,sql_literal
from initialize_business_scenarios import persist

NAME='municipal-governance-scenarios'
VERSION='2026.11.9'
PRIMARY={
 'enrollment':('enrollment.education_students','application_status'),
 'population':('population.residents','population_status'),
 'corporate':('corporate.enterprises','business_status'),
 'deceased':('deceased.death_registrations','registration_unit'),
 'mediation':('mediation.cases','case_status'),
 'real_estate':('real_estate.ownerships','ownership_share'),
}
class GovernanceInitializer(Seed):
 def __init__(self):
  super().__init__()
  self.folder=ROOT/'.local/governance-initialization'/VERSION
  self.folder.mkdir(parents=True,exist_ok=True)
  source=self.sql("SELECT state::text FROM platform_initializations WHERE name='municipal-business-scenarios'")
  if not source:raise RuntimeError('请先完成六类业务来源初始化')
  self.source=json.loads(source)
  if self.source.get('version')!=SOURCE_VERSION or self.source.get('status')!='verified':raise RuntimeError('业务初始化版本未完成核对')
  saved=self.sql('SELECT state::text FROM platform_initializations WHERE name='+sql_literal(NAME))
  self.state=json.loads(saved) if saved else {'name':NAME,'version':VERSION,'steps':{},'source_version':SOURCE_VERSION}
  if self.state['version']!=VERSION:raise RuntimeError('治理初始化版本不同，请使用匹配版本的迁移脚本')
  self.tables={t.qualified:t for t in build_tables()}
  self.workspace=''
 def request(self,path,body=None,key=None,csrf=True):
  return super().request(path,body,key=(NAME+':'+VERSION+':'+key) if key else None,csrf=csrf)
 def scoped(self,path):return path+('?' if '?' not in path else '&')+urlencode({'workspace_id':self.workspace})
 def save(self):
  persist(self.folder/'progress.json',self.state)
  self.sql('INSERT INTO platform_initializations(name,version,state) VALUES('+sql_literal(NAME)+','+sql_literal(VERSION)+','+sql_literal(json.dumps(self.state,ensure_ascii=False))+'::jsonb) ON CONFLICT(name) DO UPDATE SET version=excluded.version,state=excluded.state,updated_at=now()')
 def step(self,key,fn):
  if key not in self.state['steps']:
   self.state['steps'][key]=fn();self.save();print(json.dumps({'completed':key},ensure_ascii=False),flush=True)
  return self.state['steps'][key]
 def post(self,path,body,key):return self.request(self.scoped('/api/v1/governance/'+path),body,key)
 def definition(self,key,kind,definition,publish=True):
  definition.update(owner_id=self.principal['user_id'],steward_id=self.principal['user_id'])
  row=self.step(key,lambda:self.post(kind,{'definition':definition},key))
  publish_kind='dataflows' if kind=='metrics' else kind
  if publish:self.step(key+'-published',lambda:self.post(publish_kind+'/'+row['id']+'/versions/1/publish',{},key+'-published'))
  return {'id':row['id'],'version':1}
 def wait(self,run):
  for _ in range(240):
   current=self.request(self.scoped('/api/v1/governance/runs/'+run['id']))
   if current['state']=='succeeded':return {'id':current['id'],'state':current['state'],'result':current['result']}
   if current['state'] in ['failed','cancelled','interrupted']:raise RuntimeError('治理执行未完成：'+current.get('error_code','unknown'))
   time.sleep(.5)
  raise RuntimeError('治理运行仍在处理中，请保留检查点后重跑')
 def execute(self,key,path,body):
  queued=self.step(key+'-queued',lambda:self.post(path,body,key+'-queued'))
  return self.step(key+'-finished',lambda:self.wait(queued))
 def monitor_observation(self,monitor,key):
  for _ in range(100):
   history=self.request(self.scoped('/api/v1/governance/monitors/'+monitor['id']+'/observations'))
   matching=[item for item in history['items'] if item['policy_version']==monitor['version']]
   if matching:return matching[0]
   if history['monitor']['pending_run_id']:time.sleep(.2);continue
   try:return self.post('monitors/'+monitor['id']+'/run',{'expected_version':monitor['version']},key)
   except RuntimeError as error:
    if 'HTTP 409' not in str(error):raise
    time.sleep(.2)
  raise RuntimeError('监测尚在处理中，保留检查点后重跑')
 def monitor_finished(self,monitor,observation):
  result=self.wait({'id':observation['run_id']})
  for _ in range(100):
   history=self.request(self.scoped('/api/v1/governance/monitors/'+monitor['id']+'/observations'))
   if not history['monitor']['pending_run_id']:
    if not any(item['id']==observation['id'] and item['state'] in ['passed','alert'] for item in history['items']):raise RuntimeError('监测未生成完整观察结论')
    return result
   time.sleep(.2)
  raise RuntimeError('监测结论仍在生成')
 def configure(self,scenario,title):
  self.workspace=self.source['steps'][scenario+'-workspace']['id']
  table_name,dimension=PRIMARY[scenario];table=self.tables[table_name]
  source=self.source['steps'][table_name+'-resource']['resource'];key=table.columns[0]['name'];available={f['name']:f for f in table.fields}
  if dimension not in available or available[dimension]['sensitive']:dimension=key
  standard=self.definition(scenario+'-identifier','standards',{'name':title+'业务编号标准','description':'业务内部编号用于关联与追溯，不作为自然人身份证件号或国家法人统一编码。','standard':{'code':'municipal.'+scenario+'.identifier','logical_type':'string','nullable':False,'length':64,'pattern':'^[A-Z0-9-]+$','values':[]}})
  model_fields=[{k:available[key][k] for k in ['name','logical_type','nullable','sensitive']}];model_fields[0].update(primary_key=True,length=64,standard=standard,description=available[key].get('business_name') or available[key].get('description') or '')
  self.definition(scenario+'-standard-model','models',{'name':title+'编号核验模型','description':'固定业务编号标准，供完整落标核验和后续成果复用。','model':{'layer':'logical','data_layer':'dwd','fields':model_fields,'relations':[],'contract':{'max_failure_rate':0,'usage_restrictions':'按批准职责与用途使用。'}}})
  audit=self.execute(scenario+'-standard-audit','standard-audits',{'resource':source,'bindings':[{'field':key,'standard':standard}]})
  if audit['result']['standard_audit']['rows']!=len(table.rows):raise RuntimeError('落标检查未完整读取来源')
  monitor=self.step(scenario+'-monitor',lambda:self.post('monitors',{'name':title+'来源质量监测','enabled':False,'interval_minutes':1440,'policy':{'resource':source,'fields':[key],'time_field':'','freshness_seconds':0,'max_null_rate':0,'max_duplicate_rate':0,'max_volume_change':.2,'max_distribution_change':1}},scenario+'-monitor'))
  observation=self.step(scenario+'-observation',lambda:self.monitor_observation(monitor,scenario+'-observation'))
  self.step(scenario+'-monitor-result',lambda:self.monitor_finished(monitor,observation))
  self.step(scenario+'-monitor-enabled',lambda:self.post('monitors/'+monitor['id'],{'name':monitor['name'],'enabled':True,'interval_minutes':1440,'policy':monitor['policy'],'expected_version':monitor['version']},scenario+'-monitor-enabled'))
  # Saving a policy changes its version and resets the baseline: establish the actual enabled policy baseline.
  enabled=self.state['steps'][scenario+'-monitor-enabled']
  observed=self.step(scenario+'-enabled-observation',lambda:self.monitor_observation(enabled,scenario+'-enabled-observation'))
  self.step(scenario+'-enabled-result',lambda:self.monitor_finished(enabled,observed))
  metric_definition={'name':title+'分类业务量','description':'按'+available[dimension]['business_name']+'分类，统计已登记业务编号的非空记录数。统计的是业务记录数，不作人数认定或业务资格判断。','metric':{'source':{'resource':source,'fields':[key,dimension] if dimension!=key else [key]},'dimensions':[dimension],'filters':[],'aggregates':[{'operation':'count','source':key,'target':'record_count'}],'period':'all_time','time_field':'','from':'','until':''}}
  metric=self.definition(scenario+'-metric','metrics',metric_definition)
  self.execute(scenario+'-metric-run','dataflows/'+metric['id']+'/versions/1/run',{})
  flow=self.source['steps'][scenario+'-asset-flow']
  self.definition(scenario+'-scenario','scenarios',{'name':title+'关联治理方案','description':'复用现有来源清理、质量检查及双表关联流程。实例须重新选择授权来源。','scenario':{'kind':'dataflow','reference':{'id':flow['id'],'version':1},'acceptance':'完整读取所选来源；业务编号完整唯一；关联字段符合模型，隔离记录核清后再交付。','inputs':[]}})
  if scenario in ['population','corporate']:
   fields=[key, 'person_name' if scenario=='population' else 'entity_name']
   actual=[{k:available[name][k] for k in ['name','logical_type','nullable','sensitive']} for name in fields];actual[0]['primary_key']=True
   for target,name in zip(actual,fields):target['description']=available[name].get('description') or available[name].get('business_name') or ''
   model=self.definition(scenario+'-entity-model','models',{'name':title+'主体实体模型','description':'以业务内部编号精确识别主体，姓名或名称保持来源敏感标记。','model':{'layer':'logical','data_layer':'dwd','fields':actual,'relations':[],'contract':{'max_failure_rate':0,'usage_restrictions':'确认后通过正常成果核验与受控共享交付。'}}})
   entity=self.definition(scenario+'-master','masters',{'name':title+'主体主数据','description':'基于现有业务登记精确匹配，保留来源编号。人工确认后形成实体版本，不自动合并或代替业务认定。','master':{'model_ref':model,'keys':[key],'sources':[{'id':'registration','resource':source,'record_id':key,'mapping':{field:field for field in fields}}]}})
   self.execute(scenario+'-master-match','masters/'+entity['id']+'/match',{'version':1})
  # A reviewable revision documents the actual statistical limits; no person is impersonated for approval.
  next_definition=copy.deepcopy(metric_definition);next_definition['description']+=' 交付前须复核当前来源范围、更新时点及分类口径，保持按固定版本计算。'
  candidate=self.step(scenario+'-metric-review-candidate',lambda:self.post('metrics/'+metric['id']+'/versions',{'base_version':1,'definition':next_definition,'change_note':'补充统计使用范围、更新时点与交付复核要求'},scenario+'-metric-review-candidate'))
  proposal=self.step(scenario+'-release',lambda:self.post('releases',{'kind':'dataflow','candidate':{'id':metric['id'],'version':candidate['version']},'base_version':1,'reason':'复核'+title+'分类统计口径及交付说明','migration_note':'既有版本继续保留；审核通过后新运行采用新版本，旧成果不自动替换。'},scenario+'-release'))
  self.execute(scenario+'-release-trial','releases/'+proposal['id']+'/trial',{'expected_version':proposal['version']})
  workspace=self.request('/api/v1/governance/workspaces/'+self.workspace)
  if workspace['owner_id']!=self.principal['user_id'] or workspace['department_id']!=self.principal['department_id']:raise RuntimeError('只允许配置本人所属部门拥有的治理空间')
  self.step(scenario+'-release-policy',lambda:self.request('/api/v1/governance/workspaces/'+self.workspace+'/update',{'base_version':workspace['version'],'name':workspace['name'],'description':workspace['description'],'max_concurrent_runs':workspace['max_concurrent_runs'],'required_release_kinds':['model','dataflow','workflow','master','scenario'],'release_policy_reason':'业务治理初始配置完成，后续处理和实体模型变更须独立审核，处理流程须完整试跑。'},scenario+'-release-policy'))
 def initialize(self):
  for scenario,title in SCENARIOS.items():self.configure(scenario,title)
  self.state['status']='configured';self.state['notes']=['监测每日执行；不以读取成功时间代替业务时效。','主数据候选等待人工确认；不伪造确认记录。','六份统计口径修订已完成真实试跑，等待另一位治理人员审核。','保留已有21张来源表及六份公开资产。'];self.save()
  print(json.dumps({'status':self.state['status'],'version':VERSION,'scenarios':len(SCENARIOS),'steps':len(self.state['steps']),'pending_reviews':6,'master_matches':2},ensure_ascii=False),flush=True)

def main():
 parser=argparse.ArgumentParser(description=__doc__);parser.add_argument('--initialize',action='store_true',required=True);parser.parse_args()
 initializer=GovernanceInitializer()
 try:initializer.initialize()
 finally:initializer.close()
if __name__=='__main__':main()
