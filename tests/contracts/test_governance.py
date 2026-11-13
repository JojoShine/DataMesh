import copy
import json
import unittest
from pathlib import Path
from jsonschema import Draft202012Validator

ROOT = Path(__file__).resolve().parents[2]


class GovernanceContracts(unittest.TestCase):
    def test_model_version_reference_is_explicit_and_payload_strict(self):
        doc = json.loads((ROOT / 'api/openapi/platform.yaml').read_text())
        schema = {'$ref': '#/components/schemas/GovernanceWrite', 'components': doc['components']}
        validator = Draft202012Validator(schema)
        value = {'base_version': 1, 'change_note': '测试', 'definition': {
            'name': '客户', 'owner_id': 'owner', 'steward_id': 'owner',
            'domain': {'id': 'customer', 'version': 1},
            'model': {'layer': 'physical', 'schema': 'public', 'table': 'customers',
                      'fields': [{'name': 'id', 'logical_type': 'integer', 'primary_key': True}],
                      'relations': [], 'contract': {'freshness_seconds': 3600, 'max_failure_rate': 0}},
        }}
        validator.validate(value)
        for change in ['missing_version', 'bad_type', 'unknown_field', 'bad_quality']:
            bad = copy.deepcopy(value)
            if change == 'missing_version':
                del bad['definition']['domain']['version']
            elif change == 'bad_type':
                bad['definition']['model']['fields'][0]['logical_type'] = 'sql'
            elif change == 'unknown_field':
                bad['definition']['model']['sql'] = 'SELECT * FROM users'
            else:
                bad['definition']['model']['contract']['max_failure_rate'] = 2
            with self.subTest(change=change):
                self.assertFalse(validator.is_valid(bad))

    def test_migration_and_reference_errors_remain_explicit(self):
        doc = json.loads((ROOT / 'api/openapi/platform.yaml').read_text())
        for kind in ['domains', 'terms', 'standards', 'models']:
            path = f'/api/v1/governance/{kind}/{{id}}/versions/{{version}}/publish'
            op = doc['paths'][path]['post']
            self.assertEqual(op['x-implementation'], 'implemented')
            self.assertEqual(op['requestBody']['content']['application/json']['schema']['$ref'], '#/components/schemas/GovernancePublish')
            self.assertTrue(any(p['name'] == 'Idempotency-Key' and p['required'] for p in op['parameters']))

    def test_profile_scope_is_explicit_and_cannot_inject_query(self):
        doc = json.loads((ROOT / 'api/openapi/platform.yaml').read_text())
        validator = Draft202012Validator({'$ref': '#/components/schemas/GovernanceProfileInput', 'components': doc['components']})
        value = {'resource': {'resource_type': 'table_resource', 'resource_id': 'orders', 'contract_version': 1}, 'fields': ['order_id'], 'mode': 'sample', 'sample_rows': 1000}
        validator.validate(value)
        for change in ['unknown_mode', 'query', 'empty_fields', 'large_sample']:
            bad = copy.deepcopy(value)
            if change == 'unknown_mode': bad['mode'] = 'approximate'
            elif change == 'query': bad['sql'] = 'SELECT * FROM users'
            elif change == 'empty_fields': bad['fields'] = []
            else: bad['sample_rows'] = 10001
            with self.subTest(change=change): self.assertFalse(validator.is_valid(bad))

    def test_dataflow_nodes_are_structured_not_arbitrary_sql(self):
        doc = json.loads((ROOT / 'api/openapi/platform.yaml').read_text())
        validator = Draft202012Validator({'$ref': '#/components/schemas/GovernanceDataflow', 'components': doc['components']})
        value = {'nodes': [
            {'id': 'source', 'kind': 'source', 'inputs': [], 'source': {'resource': {'resource_type': 'table_resource', 'resource_id': 'orders', 'contract_version': 1}, 'fields': ['id']}},
            {'id': 'output', 'kind': 'output', 'inputs': [{'node': 'source'}], 'model': {'id': 'model', 'version': 1}},
        ]}
        validator.validate(value)
        for change in ['sql_operator', 'sql_property', 'unversioned_model', 'unknown_port']:
            bad = copy.deepcopy(value)
            if change == 'sql_operator': bad['nodes'][1]['kind'] = 'sql'
            elif change == 'sql_property': bad['nodes'][1]['sql'] = 'SELECT * FROM users'
            elif change == 'unversioned_model': del bad['nodes'][1]['model']['version']
            else: bad['nodes'][1]['inputs'][0]['port'] = 'raw_sql'
            with self.subTest(change=change): self.assertFalse(validator.is_valid(bad))

    def test_remaining_actions_have_bounded_closed_payloads(self):
        doc=json.loads((ROOT / 'api/openapi/platform.yaml').read_text())
        examples={
            'GovernanceRunInput': {'parameters': {'minimum': 10}},
            'GovernanceModelDeployInput': {'source_id':'target','source_version':1,'preview_token':'verified','confirm_table':'public.new_table'},
            'GovernanceRetentionPolicyInput': {'flow_id':'flow','days':30,'keep_last':3,'enabled':False},
            'GovernanceFileContractInput': {'expected_version':1,'model':{'id':'model','version':1},'sheet':'Data','header_row':1},
        }
        for name,value in examples.items():
            validator=Draft202012Validator({'$ref':'#/components/schemas/'+name,'components':doc['components']})
            with self.subTest(name=name):
                validator.validate(value)
                self.assertFalse(validator.is_valid(dict(value,sql='SELECT secret FROM private')))
        validator=Draft202012Validator({'$ref':'#/components/schemas/GovernanceRunInput','components':doc['components']})
        self.assertFalse(validator.is_valid({'parameters':{'minimum': {'expression':'untrusted'}}}))

    def test_calendar_and_independent_workflow_tasks_are_documented(self):
        doc=json.loads((ROOT / 'api/openapi/platform.yaml').read_text())
        validator=Draft202012Validator({'$ref':'#/components/schemas/GovernanceWorkflow','components':doc['components']})
        validator.validate({'tasks':[
            {'id':'load','dataflow':{'id':'flow','version':1},'depends_on':[],'condition':'all_success'},
            {'id':'check','kind':'quality_check','input_task':'load','depends_on':['load'],'condition':'all_success','max_failure_rate':0.01},
            {'id':'publish','kind':'publish_approval','input_task':'load','depends_on':['check'],'condition':'all_success'},
        ],'parameters':{'minimum':10}})
        for suffix in ['impact','alerts','alert-rules','lifecycle','retention-policies','migrations','schedules/preview','model-deployments']:
            self.assertIn('/api/v1/governance/'+suffix,doc['paths'])

    def test_direct_schedule_has_exactly_one_explicit_target(self):
        doc=json.loads((ROOT / 'api/openapi/platform.yaml').read_text())
        validator=Draft202012Validator({'$ref':'#/components/schemas/GovernanceScheduleInput','components':doc['components']})
        base={'timezone':'UTC','interval_seconds':3600,'concurrency_policy':'forbid','missed_policy':'skip','next_run_at':'2030-01-01T00:00:00Z'}
        ref={'id':'flow','version':1}
        validator.validate(dict(base,dataflow=ref))
        validator.validate(dict(base,workflow=ref))
        self.assertFalse(validator.is_valid(base))
        self.assertFalse(validator.is_valid(dict(base,dataflow=ref,workflow=ref)))
        self.assertFalse(validator.is_valid(dict(base,dataflow={'id':'flow','version':0})))
