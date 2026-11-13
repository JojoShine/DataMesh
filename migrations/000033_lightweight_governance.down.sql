-- A downgrade must not silently attach private runs to a workspace or delete uploads.
DO $$ BEGIN
 IF EXISTS(SELECT 1 FROM governance_runs WHERE workspace_id IS NULL) OR EXISTS(SELECT 1 FROM business_records WHERE kind IN ('lightweight_upload','lightweight_unit')) THEN
  RAISE EXCEPTION 'remove or archive lightweight governance data before downgrade';
 END IF;
END $$;
DROP INDEX governance_lightweight_unit_runs;
DROP TRIGGER governance_run_workspace_default ON governance_runs;
DROP FUNCTION governance_lightweight_run_workspace();
ALTER TABLE governance_runs DROP CONSTRAINT governance_run_lightweight_workspace;
ALTER TABLE governance_runs ALTER COLUMN workspace_id SET NOT NULL;
CREATE TRIGGER governance_run_workspace_default BEFORE INSERT ON governance_runs FOR EACH ROW EXECUTE FUNCTION governance_task_workspace_default();
ALTER TABLE business_records DROP CONSTRAINT business_records_kind_check;
ALTER TABLE business_records ADD CONSTRAINT business_records_kind_check CHECK (kind IN ('source','object','task','rule','dataset','application','grant','archive','rule_version','directory','directory_revision','mount','mount_revision','review','assignment','processing_authorization','directory_dictionary','table_resource','file_resource','api_resource','resource_import','resource_upload','etl_task','etl_run'));
