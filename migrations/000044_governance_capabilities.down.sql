DO $$ BEGIN
 IF EXISTS(SELECT 1 FROM governance_monitor_observations WHERE resolution_state='closed' OR resolution_note<>'') OR EXISTS(SELECT 1 FROM governance_definitions WHERE kind IN ('master','scenario')) OR EXISTS(SELECT 1 FROM business_records WHERE kind IN ('governance_release','scenario_instance')) OR EXISTS(SELECT 1 FROM governance_runs WHERE kind='master') THEN RAISE EXCEPTION 'Governance capability history must be preserved'; END IF;
END $$;
ALTER TABLE governance_monitor_observations DROP COLUMN resolution_state,DROP COLUMN resolution_note,DROP COLUMN retest_observation_id,DROP COLUMN version;
DROP TABLE governance_master_revisions,governance_master_entities,governance_master_candidates;
DROP INDEX governance_release_candidates;
ALTER TABLE business_records DROP CONSTRAINT business_records_kind_check;
ALTER TABLE business_records ADD CONSTRAINT business_records_kind_check CHECK(kind IN ('source','object','task','rule','dataset','application','grant','archive','rule_version','directory','directory_revision','mount','mount_revision','review','assignment','processing_authorization','directory_dictionary','table_resource','file_resource','api_resource','resource_import','resource_upload','etl_task','etl_run','lightweight_upload','lightweight_unit'));
ALTER TABLE governance_runs DROP CONSTRAINT governance_runs_kind_check;
ALTER TABLE governance_runs ADD CONSTRAINT governance_runs_kind_check CHECK(kind IN ('profile','dataflow','workflow','dataflow_preview'));
ALTER TABLE governance_definitions DROP CONSTRAINT governance_definition_workspace_kind;
ALTER TABLE governance_definitions ADD CONSTRAINT governance_definition_workspace_kind CHECK((kind IN ('model','dataflow','workflow','quality_template'))=(workspace_id IS NOT NULL));
ALTER TABLE governance_definitions DROP CONSTRAINT governance_definitions_kind_check;
ALTER TABLE governance_definitions ADD CONSTRAINT governance_definitions_kind_check CHECK(kind IN ('domain','term','standard','model','metadata','dataflow','workflow','quality_template'));
CREATE OR REPLACE FUNCTION governance_definition_workspace_default() RETURNS trigger LANGUAGE plpgsql SET search_path=pg_catalog,public AS $$ BEGIN
 IF NEW.kind IN ('model','dataflow','workflow','quality_template') AND (NEW.workspace_id IS NULL OR NEW.workspace_id='') THEN NEW.workspace_id:=public.governance_legacy_workspace(NEW.department_id); END IF; RETURN NEW;
END $$;
