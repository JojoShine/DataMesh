#!/usr/bin/env python3
"""Run draft-staging assertions against an isolated real PostgreSQL database."""
import json
import subprocess
import sys
import unittest
import uuid
from unittest.mock import patch
from urllib.parse import urlsplit, urlunsplit

sys.path.insert(0,str(__import__('pathlib').Path(__file__).resolve().parents[2]/'scripts'))
import migrate_directory
from dev_env import ROOT, ENV_FILE, compose, load_env


class DirectoryMigration(unittest.TestCase):
    def test_staging_is_idempotent_and_does_not_approve(self):
        name='datamesh_test_'+uuid.uuid4().hex
        compose('exec','-T','postgres','createdb','-U','datamesh_owner',name,capture=True)
        try:
            env=load_env();parts=urlsplit(env['MIGRATION_DSN']);env['MIGRATION_DSN']=urlunsplit(parts._replace(path='/'+name))
            r=subprocess.run(['docker','compose','--project-name','datamesh','--env-file',str(ENV_FILE),'run','--rm','migrate'],env=env,cwd=ROOT,capture_output=True,timeout=60)
            self.assertEqual(r.returncode,0,'isolated schema migration failed')
            def sql(query):
                return compose('exec','-T','postgres','psql','-U','datamesh_owner','-d',name,'-v','ON_ERROR_STOP=1','-At','-c',query,capture=True).stdout.strip()
            sql("""INSERT INTO users(id,username,password_hash,department_id,active) VALUES('migration-user','migration-user','not-used','migration-dept',true);
            INSERT INTO business_records(kind,id,department_id,owner_id,state,body) VALUES
            ('object','migration-object','migration-dept','migration-user','available','{"qualified_name":"public.customers","fields":[{"name":"id","logical_type":"integer","nullable":false,"primary_key":true,"sensitive":false,"precision":null,"scale":null}]}'),
            ('dataset','migration-dataset','migration-dept','migration-user','draft','{"definition":{"name":"待补编成果","fields":[{"name":"id","logical_type":"integer","nullable":false,"primary_key":true,"sensitive":false,"precision":null,"scale":null}]}}');""")
            # Only reroute the connection; the production staging SQL and real DB run unchanged.
            with patch.object(migrate_directory,'sql',side_effect=sql):
                migrate_directory.stage_drafts()
                first=sql("SELECT count(*) FROM business_records WHERE kind IN ('directory','directory_revision')")
                self.assertEqual(first,'4')
                migrate_directory.stage_drafts()
                self.assertEqual(sql("SELECT count(*) FROM business_records WHERE kind IN ('directory','directory_revision')"),first)
                self.assertEqual(sql("SELECT count(*) FROM business_records WHERE kind IN ('directory','directory_revision') AND state<>'draft'"),'0')
                self.assertEqual(sql("SELECT count(*) FROM business_records WHERE kind='directory' AND COALESCE(body->>'resource_code','')<>''"),'0')
                self.assertEqual(sql("SELECT count(*) FROM business_records WHERE kind IN ('review','grant','mount')"),'0')
                self.assertEqual(sql("SELECT count(*) FROM business_records WHERE kind='directory_revision' AND body->'definition'->'items'->0->>'business_name'='id'"),'2')
                result=migrate_directory.report()
                self.assertEqual(result['resources'],2)
                self.assertEqual(len(result['unmounted_resources']),2)
        finally:
            compose('exec','-T','postgres','dropdb','--force','-U','datamesh_owner',name,capture=True)


if __name__=='__main__':
    unittest.main()
