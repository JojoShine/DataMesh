import sys
import unittest
from pathlib import Path
from collections import Counter
from datetime import date
sys.path.insert(0,str(Path(__file__).resolve().parents[2]/'scripts'))
from business_scenarios import build_tables, AS_OF
from initialize_business_scenarios import RELATIONS

class BusinessScenarios(unittest.TestCase):
 @classmethod
 def setUpClass(cls):
  cls.tables=build_tables();cls.rows={t.qualified:[dict(zip([c['name'] for c in t.columns],row)) for row in t.rows] for t in cls.tables}
 def test_scale_keys_and_business_values(self):
  self.assertEqual(len(self.tables),21);self.assertEqual(sum(len(t.rows) for t in self.tables),75300)
  for table in self.tables:
   self.assertTrue(1000<=len(table.rows)<=10000,table.qualified)
   self.assertEqual(len({row[0] for row in table.rows}),len(table.rows))
   for row in table.rows:
    for col,value in zip(table.columns,row):
     self.assertTrue(col['nullable'] or value is not None)
     if isinstance(value,str):
      self.assertNotIn('demo',value.lower());self.assertNotIn('test-',value.lower());self.assertNotIn('模拟',value)
 def test_cross_scenario_relationships(self):
  for table,col,parent,key in RELATIONS:
   targets={r[key] for r in self.rows[parent]}
   self.assertFalse({r[col] for r in self.rows[table]}-targets,table+'.'+col)
  students={r['child_id']:r for r in self.rows['enrollment.education_students']}
  for row in self.rows['enrollment.police_children']:
   self.assertEqual(row['guardian_id'],students[row['child_id']]['declared_guardian_id'])
   self.assertEqual(row['birth_date'],students[row['child_id']]['birth_date'])
 def test_business_consistency(self):
  shares=Counter()
  for row in self.rows['real_estate.ownerships']:shares[row['property_id']]+=row['ownership_share']
  self.assertEqual(set(shares.values()),{100})
  residents={r['person_id']:r for r in self.rows['population.residents']}
  for row in self.rows['deceased.death_registrations']:
   self.assertEqual(residents[row['person_id']]['population_status'],'死亡')
   self.assertLess(residents[row['person_id']]['birth_date'],row['death_date']);self.assertLessEqual(date.fromisoformat(row['registration_date']),AS_OF)
  for row in self.rows['mediation.resolutions']:
   if row['resolution_status']=='继续调解':
    self.assertIsNone(row['agreement_date']);self.assertEqual(row['agreed_amount'],'0')
  companies=self.rows['corporate.enterprises']
  self.assertEqual(len({r['entity_name'] for r in companies}),len(companies))
 def test_generation_is_deterministic(self):
  self.assertEqual([t.digest for t in self.tables],[t.digest for t in build_tables()])


class GovernanceReadinessRules(unittest.TestCase):
 def test_state_vocabularies_and_static_snapshot_policy(self):
  from initialize_governance_readiness import RULES
  primary={'enrollment':'education_students','population':'residents','corporate':'enterprises','deceased':'death_registrations','mediation':'cases','real_estate':'ownerships'}
  for table in build_tables():
   if table.name!=primary[table.scenario]:continue
   rule=RULES[table.scenario];fields=[column['name'] for column in table.columns]
   self.assertTrue({row[fields.index(rule['field'])] for row in table.rows}<=set(rule['values']))
   if rule.get('dates'):
    left,right=rule['dates']
    self.assertTrue(all(row[fields.index(left)]<=row[fields.index(right)] for row in table.rows))
  self.assertEqual(RULES['real_estate']['time'],'')

 def test_reference_gate_precedes_inner_join_and_preserves_contract(self):
  from initialize_governance_readiness import enrich_flow
  import copy
  base={'description':'业务治理','dataflow':{'nodes':[
   {'id':'left','kind':'source'},{'id':'right','kind':'source'},
   {'id':'quality','kind':'quality','checks':[{'field':'entity_code','kind':'required'}]},
   {'id':'linked','kind':'join','inputs':[{'node':'quality'},{'node':'right_clean'}],'join':{'left_keys':['entity_code'],'right_keys':['entity_code'],'columns':[{'side':'left','source':'entity_code','target':'entity_code'}]}},
   {'id':'output','kind':'output','inputs':[{'node':'linked'}],'model':{'id':'model','version':1}}]}}
  original=copy.deepcopy(base);enhanced=enrich_flow(base,'corporate');self.assertEqual(base,original)
  nodes={node['id']:node for node in enhanced['dataflow']['nodes']}
  self.assertIn({'node':'reference_quality','port':'good'},nodes['quality']['inputs'])
  self.assertTrue(any(check['kind']=='reference_exists' for check in nodes['quality']['checks']))
  self.assertEqual(nodes['delivery_fields']['columns'],[{'source':'entity_code','target':'entity_code','rule':'copy'}])
  self.assertEqual(nodes['output']['model'],{'id':'model','version':1})
  self.assertIn('2026-10-31',enhanced['description'])

if __name__=='__main__':unittest.main()
