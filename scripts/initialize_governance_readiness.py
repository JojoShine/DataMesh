"""Versioned quality enrichment for the six existing fictional business snapshots.

Creates real audits, distribution baselines and reviewable revisions. Never approves
releases, confirms entities, replaces source rows or republishes existing assets.
"""
import argparse
import copy
import json

from seed_demo_assets import Seed, ROOT
from business_scenarios import AS_OF, SCENARIOS, build_tables, sql_literal
from initialize_governance_scenarios import GovernanceInitializer
from initialize_business_scenarios import persist

NAME = 'municipal-governance-readiness'
VERSION = '2026.11.10'
# Allowed values describe the initialized snapshot's documented business vocabulary.
RULES = {
 'enrollment': {'field':'application_status','values':['registered','pending'],'meanings':{'registered':'已登记','pending':'待核查'},'time':'application_date','dates':('birth_date','application_date')},
 'population': {'field':'population_status','values':['正常','死亡'],'time':'updated_at','dates':('birth_date','updated_at')},
 'corporate': {'field':'business_status','values':['存续','注销'],'time':'registration_date','right_enum':('position_name',['经营者','法定代表人'])},
 'deceased': {'field':'verification_status','values':['已核验'],'time':'registration_date','dates':('death_date','registration_date'),'right_enum':('cancellation_status',['待核查','已注销'])},
 'mediation': {'field':'case_status','values':['办理中','已办结'],'time':'accepted_date','right_enum':('resolution_status',['继续调解','达成协议'])},
 'real_estate': {'field':'ownership_share','values':list(range(1,101)),'time':'','right_enum':('property_type',['住宅','商业'])},
}

def check(field, kind, **parameters):
 result={'field':field,'kind':kind,'blocking':True}
 if parameters: result['parameters']=parameters
 return result

def enrich_flow(definition, scenario):
 """Preserve all original output fields; check reference existence before the inner join."""
 result=copy.deepcopy(definition);nodes=result['dataflow']['nodes'];by_id={node['id']:node for node in nodes}
 rules=RULES[scenario];left=by_id['left'];right=by_id['right'];join=by_id['linked']['join']
 left_key=join['left_keys'][0];right_key=join['right_keys'][0]
 original_key=next(item['field'] for item in by_id['quality']['checks'] if item['kind']=='required')
 checks=[check(original_key,'required'),check(original_key,'business_key'),check(left_key,'required'),check(left_key,'reference_exists',other_field=right_key),check(rules['field'],'required'),check(rules['field'],'enum',values=rules['values'])]
 if rules.get('dates'): checks.append(check(rules['dates'][0],'compare_fields',other_field=rules['dates'][1],operator='lte'))
 if scenario=='corporate': checks.append(check('registered_capital','range',min=0))
 if scenario=='mediation': checks.append(check('dispute_amount','range',min=0))
 if scenario=='real_estate': checks.append(check('ownership_share','range',min=1,max=100))
 right_checks=[check(right_key,'required'),check(right_key,'business_key')]
 if rules.get('right_enum'):
  field,values=rules['right_enum'];right_checks.extend([check(field,'required'),check(field,'enum',values=values)])
 if scenario=='real_estate': right_checks.append(check('property_area','range',min=0.01))
 nodes.append({'id':'reference_quality','kind':'quality','inputs':[{'node':'right_clean'}],'checks':right_checks})
 by_id['quality']['inputs']=[{'node':'left_clean'},{'node':'reference_quality','port':'good'}];by_id['quality']['checks']=checks
 by_id['linked']['inputs'][1]={'node':'reference_quality','port':'good'}
 if scenario=='corporate':
  output_fields=[column['target'] for column in join['columns']]
  join['columns'] += [{'side':'left','source':'registration_date','target':'basis_registration_date'},{'side':'right','source':'effective_date','target':'basis_effective_date'}]
  nodes.extend([{'id':'date_quality','kind':'quality','inputs':[{'node':'linked'}],'checks':[check('basis_registration_date','compare_fields',other_field='basis_effective_date',operator='lte')]},
   {'id':'delivery_fields','kind':'project','inputs':[{'node':'date_quality','port':'good'}],'columns':[{'source':name,'target':name,'rule':'copy'} for name in output_fields]}])
  by_id['output']['inputs']=[{'node':'delivery_fields'}]
 result['description']+=' 补充业务码值、日期先后、取值范围及关联完整性门禁；静态快照截至'+AS_OF.isoformat()+'，业务日期不作为实时更新承诺。'
 return result

class ReadinessInitializer(GovernanceInitializer):
 def __init__(self):
  super().__init__()
  governance=self.sql("SELECT state::text FROM platform_initializations WHERE name='municipal-governance-scenarios'")
  if not governance or json.loads(governance).get('status')!='configured':raise RuntimeError('请先完成六类治理基础初始化')
  self.folder=ROOT/'.local/governance-readiness'/VERSION;self.folder.mkdir(mode=0o700,parents=True,exist_ok=True)
  saved=self.sql('SELECT state::text FROM platform_initializations WHERE name='+sql_literal(NAME))
  self.state=json.loads(saved) if saved else {'name':NAME,'version':VERSION,'steps':{},'snapshot_as_of':AS_OF.isoformat()}
  if self.state['version']!=VERSION:raise RuntimeError('治理补充初始化版本不一致')
  self.tables={table.qualified:table for table in build_tables()}
 def request(self,path,body=None,key=None,csrf=True):
  return Seed.request(self,path,body,key=(NAME+':'+VERSION+':'+key) if key else None,csrf=csrf)
 def save(self):
  persist(self.folder/'progress.json',self.state)
  self.sql('INSERT INTO platform_initializations(name,version,state) VALUES('+sql_literal(NAME)+','+sql_literal(VERSION)+','+sql_literal(json.dumps(self.state,ensure_ascii=False))+'::jsonb) ON CONFLICT(name) DO UPDATE SET version=excluded.version,state=excluded.state,updated_at=now()')
 def configure(self,scenario,title):
  self.workspace=self.source['steps'][scenario+'-workspace']['id'];flow_ref=self.source['steps'][scenario+'-asset-flow'];rules=RULES[scenario]
  flow=self.request(self.scoped('/api/v1/governance/dataflows/'+flow_ref['id']+'/versions/1'));by_id={node['id']:node for node in flow['definition']['dataflow']['nodes']}
  resource=by_id['left']['source']['resource'];table=next(table for table in self.tables.values() if self.source['steps'].get(table.qualified+'-resource',{}).get('resource')==resource)
  field=next(field for field in table.fields if field['name']==rules['field'])
  standard=self.definition(scenario+'-state-standard','standards',{'name':title+field['business_name']+'标准','description':'业务快照码值口径，截至'+AS_OF.isoformat()+'；非业务资格认定依据。','standard':{'code':'municipal.'+scenario+'.business-state','logical_type':field['logical_type'],'nullable':False,'values':[str(value) for value in rules['values']],'value_meanings':rules.get('meanings',{str(value):str(value) for value in rules['values']})}})
  audit=self.execute(scenario+'-state-audit','standard-audits',{'resource':resource,'bindings':[{'field':rules['field'],'standard':standard}]})
  if audit['result']['standard_audit']['rows']!=len(table.rows) or audit['result']['standard_audit']['rejected_rows']!=0:raise RuntimeError('业务码值完整核验不合格：'+scenario)
  fields=[rules['field']]+([rules['time']] if rules['time'] else [])
  monitor=self.step(scenario+'-distribution-monitor',lambda:self.post('monitors',{'name':title+'业务分布与快照日期监测','enabled':True,'interval_minutes':1440,'policy':{'resource':resource,'fields':fields,'time_field':rules['time'],'freshness_seconds':0,'max_null_rate':0,'max_duplicate_rate':1,'max_volume_change':.2,'max_distribution_change':.2}},scenario+'-distribution-monitor'))
  observed=self.step(scenario+'-distribution-observation',lambda:self.monitor_observation(monitor,scenario+'-distribution-observation'))
  self.step(scenario+'-distribution-result',lambda:self.monitor_finished(monitor,observed))
  definition=enrich_flow(flow['definition'],scenario)
  candidate=self.step(scenario+'-flow-candidate',lambda:self.post('dataflows/'+flow['id']+'/versions',{'base_version':flow['latest_version'],'definition':definition,'change_note':'补充码值、日期、数值范围与关联完整性质量门禁'},scenario+'-flow-candidate'))
  release=self.step(scenario+'-flow-release',lambda:self.post('releases',{'kind':'dataflow','candidate':{'id':flow['id'],'version':candidate['version']},'base_version':1,'reason':'复核'+title+'完整质量门禁与关联证据','migration_note':'旧版本和公开成果保留；独立审核通过后新运行采用新版本，新成果另行核验和交付，不自动覆盖资产。'},scenario+'-flow-release'))
  trial=self.execute(scenario+'-flow-trial','releases/'+release['id']+'/trial',{'expected_version':release['version']})
  if trial['result']['rejected_rows']!=0 or trial['result']['output_rows']!=len(table.rows):raise RuntimeError('增强流程试跑覆盖不完整或仍有隔离：'+scenario)
  model_ref=by_id['output']['model'];model=self.request(self.scoped('/api/v1/governance/models/'+model_ref['id']+'/versions/'+str(model_ref['version'])))
  model_definition=copy.deepcopy(model['definition'])
  sources={side:next(table for table in self.tables.values() if self.source['steps'].get(table.qualified+'-resource',{}).get('resource')==by_id[side]['source']['resource']) for side in ['left','right']}
  mapping={column['target']:next(field for field in sources[column['side']].fields if field['name']==column['source']) for column in by_id['linked']['join']['columns']}
  for target in model_definition['model']['fields']:
   source=mapping[target['name']];target['business_name']=source['business_name'];target['description']=source['description']+'。用于'+title+'，按固定版本和授权用途使用。'
  model_candidate=self.step(scenario+'-model-candidate',lambda:self.post('models/'+model['id']+'/versions',{'base_version':model['latest_version'],'definition':model_definition,'change_note':'补齐独立中文名称及业务含义，保留技术契约'},scenario+'-model-candidate'))
  self.step(scenario+'-model-release',lambda:self.post('releases',{'kind':'model','candidate':{'id':model['id'],'version':model_candidate['version']},'base_version':model_ref['version'],'reason':'复核'+title+'字段中文名称与业务口径','migration_note':'原发布模型保持不变；审核后后续流程可显式绑定新版本。'},scenario+'-model-release'))
 def initialize(self):
  for scenario,title in SCENARIOS.items():self.configure(scenario,title)
  self.state['status']='configured';self.state['notes']=['六次完整业务码值核验及六次增强流程真实试跑完成。','新增分布监测不把业务日期伪称为实时更新，保留原主键重复监测。','六个流程修订与六个模型修订等待独立审核，不自动替换资产。','主数据候选仍待人工确认。'];self.save()
  print(json.dumps({'status':'configured','version':VERSION,'scenarios':6,'new_reviews':12,'snapshot_as_of':AS_OF.isoformat()},ensure_ascii=False),flush=True)

def main():
 parser=argparse.ArgumentParser(description=__doc__);parser.add_argument('--initialize',action='store_true',required=True);parser.parse_args()
 initializer=ReadinessInitializer()
 try:initializer.initialize()
 finally:initializer.close()
if __name__=='__main__':main()
