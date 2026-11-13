#!/usr/bin/env python3
"""Verify migration rollback preserves populated sharing and governance evidence."""
import subprocess
import sys
import unittest
import uuid
from pathlib import Path
from urllib.parse import urlsplit, urlunsplit

sys.path.insert(0, str(Path(__file__).resolve().parents[2] / 'scripts'))
from dev_env import ROOT, ENV_FILE, compose, load_env


class SharingMigrations(unittest.TestCase):
    def test_populated_evidence_blocks_rollback(self):
        name = 'datamesh_test_' + uuid.uuid4().hex
        compose('exec', '-T', 'postgres', 'createdb', '-U', 'datamesh_owner', name, capture=True)
        try:
            env = load_env()
            env['MIGRATION_DSN'] = urlunsplit(urlsplit(env['MIGRATION_DSN'])._replace(path='/' + name))
            base = ['docker', 'compose', '--project-name', 'datamesh', '--env-file', str(ENV_FILE)]
            migrated = subprocess.run(base + ['run', '--rm', 'migrate'], cwd=ROOT, env=env, capture_output=True, timeout=60)
            self.assertEqual(migrated.returncode, 0, 'isolated forward migration failed')

            def sql(query):
                return subprocess.run(base + ['exec', '-T', 'postgres', 'psql', '-X', '-U', 'datamesh_owner', '-d', name, '-v', 'ON_ERROR_STOP=1', '-At'], input=query, cwd=ROOT, env=env, capture_output=True, text=True, timeout=30)

            seeded = sql("""
                INSERT INTO users(id,username,password_hash,department_id,active)
                VALUES('guard-user','guard-user','not-used','guard-dept',true);
                INSERT INTO business_records(kind,id,owner_id,department_id,state,body) VALUES
                ('source','guard-source','guard-user','guard-dept','available','{}'),
                ('task','guard-task','guard-user','guard-dept','draft','{}'),
                ('etl_run','guard-run','guard-user','guard-dept','succeeded','{}');
                INSERT INTO cdc_bindings(task_id,source_id,object_id,kind,connector_name,topic_prefix,topic,schema,"table",source_fingerprint,source_version,config_version)
                VALUES('guard-task','guard-source','guard-object','postgres','guard-connector','guard-prefix','guard-topic','public','guard-table','guard-instance',1,1);
                INSERT INTO archive_objects(key,object_id,state) VALUES('guard-object-key','guard-object','referenced');
                INSERT INTO etl_delivery_inputs(run_id,state,wrapped_key,target_kind,target_version,target_columns,expires_at)
                VALUES('guard-run','ready',decode('00','hex'),'postgres',1,'[]',now()+interval '7 days');
                INSERT INTO governance_definitions(id,kind,department_id,latest_version)
                VALUES('guard-model','model','guard-dept',1);
                INSERT INTO governance_versions(definition_id,version,state,definition,created_by)
                VALUES('guard-model',1,'draft','{}','guard-user');
                INSERT INTO metadata_observations(id,object_id,version,source_id,source_version,object_version,schema,"table",fields,changes,matches_active,observed_by)
                VALUES('guard-observation','guard-object',1,'guard-source',1,1,'public','test','[]','[]',true,'guard-user');
                UPDATE metadata_observations SET resource_type='file_resource',observation_kind='registered_contract' WHERE id='guard-observation';
                INSERT INTO governance_runs(id,kind,department_id,actor_id,definition,state) VALUES('guard-governance-run','profile','guard-dept','guard-user','{}','succeeded');
                INSERT INTO governance_flow_rows(run_id,node_id,ordinal,encrypted_body) VALUES('guard-governance-run','output',1,decode('00','hex'));
                INSERT INTO governance_outputs(run_id,dataset_id,dataset_body) VALUES('guard-governance-run','guard-output','{"governance_run_id":"guard-governance-run"}');
                INSERT INTO governance_workflow_tasks(parent_run_id,task_id,state) VALUES('guard-governance-run','guard-task','succeeded');
                INSERT INTO governance_quality_issues(id,department_id,actor_id,assignee_id,run_id,flow_id,flow_version,due_at,state)
                VALUES('guard-issue','guard-dept','guard-user','guard-user','guard-governance-run','guard-model',1,now()+interval '1 day','open');
            """)
            self.assertEqual(seeded.returncode, 0, 'isolated rollback fixture failed')
            for migration, table, message in [
                ('000009_cdc_bindings', 'cdc_bindings', 'CDC tasks exist'),
                ('000010_resource_snapshots', 'archive_objects', 'Snapshot or archive objects exist'),
                ('000011_delivery_reconciliation', 'etl_delivery_inputs', 'Delivery evidence exists'),
                ('000012_governance_definitions', 'governance_versions', 'Governance definition evidence exists'),
                ('000013_governance_metadata', 'metadata_observations', 'Metadata evidence exists'),
                ('000014_governance_metadata_resources', 'metadata_observations', 'metadata resource evidence exists'),
                ('000015_governance_runs', 'governance_runs', 'governance execution evidence exists'),
                ('000016_governance_dataflows', 'governance_flow_rows', 'dataflow execution evidence exists'),
                ('000017_governance_outputs', 'governance_outputs', 'governance output evidence exists'),
                ('000018_governance_workflows', 'governance_workflow_tasks', 'workflow or schedule evidence exists'),
                ('000019_governance_quality_issues', 'governance_quality_issues', 'quality remediation evidence exists'),
            ]:
                with self.subTest(migration=migration):
                    down = (ROOT / 'migrations' / (migration + '.down.sql')).read_text()
                    result = sql('BEGIN;\n' + down + '\nROLLBACK;')
                    self.assertNotEqual(result.returncode, 0, 'populated evidence rollback unexpectedly succeeded')
                    self.assertIn(message, result.stderr)
                    preserved = sql('SELECT count(*) FROM ' + table)
                    self.assertEqual(preserved.returncode, 0, 'evidence table was dropped')
                    self.assertEqual(preserved.stdout.strip(), '1', 'evidence record was lost')
            version = sql('SELECT version,dirty FROM schema_migrations')
            latest = max(int(path.name.split('_', 1)[0]) for path in (ROOT / 'migrations').glob('*.up.sql'))
            self.assertEqual(version.stdout.strip(), f'{latest}|f')
        finally:
            compose('exec', '-T', 'postgres', 'dropdb', '--force', '-U', 'datamesh_owner', name, capture=True)


if __name__ == '__main__':
    unittest.main()
