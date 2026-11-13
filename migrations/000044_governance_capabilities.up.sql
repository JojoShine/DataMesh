ALTER TABLE governance_definitions DROP CONSTRAINT governance_definitions_kind_check;
ALTER TABLE governance_definitions ADD CONSTRAINT governance_definitions_kind_check CHECK(kind IN ('domain','term','standard','model','metadata','dataflow','workflow','quality_template','master','scenario'));
ALTER TABLE governance_definitions DROP CONSTRAINT governance_definition_workspace_kind;
ALTER TABLE governance_definitions ADD CONSTRAINT governance_definition_workspace_kind CHECK((kind IN ('model','dataflow','workflow','quality_template','master','scenario'))=(workspace_id IS NOT NULL));
CREATE OR REPLACE FUNCTION governance_definition_workspace_default() RETURNS trigger LANGUAGE plpgsql SET search_path=pg_catalog,public AS $$
BEGIN
 IF NEW.kind IN ('model','dataflow','workflow','quality_template','master','scenario') AND (NEW.workspace_id IS NULL OR NEW.workspace_id='') THEN NEW.workspace_id:=public.governance_legacy_workspace(NEW.department_id); END IF;
 RETURN NEW;
END $$;
ALTER TABLE governance_runs DROP CONSTRAINT governance_runs_kind_check;
ALTER TABLE governance_runs ADD CONSTRAINT governance_runs_kind_check CHECK(kind IN ('profile','dataflow','workflow','dataflow_preview','master'));
ALTER TABLE business_records DROP CONSTRAINT business_records_kind_check;
ALTER TABLE business_records ADD CONSTRAINT business_records_kind_check CHECK(kind IN ('source','object','task','rule','dataset','application','grant','archive','rule_version','directory','directory_revision','mount','mount_revision','review','assignment','processing_authorization','directory_dictionary','table_resource','file_resource','api_resource','resource_import','resource_upload','etl_task','etl_run','lightweight_upload','lightweight_unit','governance_release','scenario_instance'));
CREATE INDEX governance_release_candidates ON business_records((body->'candidate'->>'id'),state) WHERE kind='governance_release';
CREATE TABLE governance_master_candidates (
 id text PRIMARY KEY, run_id text NOT NULL REFERENCES governance_runs(id), entity_id text NOT NULL,
 payload jsonb NOT NULL, conflicts jsonb NOT NULL, created_at timestamptz NOT NULL DEFAULT now(),
 UNIQUE(run_id,entity_id)
);
CREATE TABLE governance_master_entities (
 entity_id text PRIMARY KEY, workspace_id text NOT NULL REFERENCES governance_workspaces(id), master_id text NOT NULL REFERENCES governance_definitions(id),
 latest_version integer NOT NULL CHECK(latest_version>0), updated_at timestamptz NOT NULL DEFAULT now()
);
CREATE TABLE governance_master_revisions (
 entity_id text NOT NULL REFERENCES governance_master_entities(entity_id), version integer NOT NULL CHECK(version>0),
 candidate_id text NOT NULL REFERENCES governance_master_candidates(id), choices jsonb NOT NULL, payload jsonb NOT NULL,
 actor_id text NOT NULL REFERENCES users(id), reason text NOT NULL, created_at timestamptz NOT NULL DEFAULT now(),
 PRIMARY KEY(entity_id,version)
);
GRANT SELECT,INSERT ON governance_master_candidates,governance_master_revisions TO datamesh_app;
GRANT SELECT,INSERT,UPDATE ON governance_master_entities TO datamesh_app;
ALTER TABLE governance_monitor_observations ADD COLUMN resolution_state text NOT NULL DEFAULT 'open' CHECK(resolution_state IN ('open','closed')),
 ADD COLUMN resolution_note text NOT NULL DEFAULT '', ADD COLUMN retest_observation_id text REFERENCES governance_monitor_observations(id), ADD COLUMN version integer NOT NULL DEFAULT 1;
