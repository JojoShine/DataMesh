ALTER TABLE business_records DROP CONSTRAINT business_records_kind_check;
ALTER TABLE business_records ADD CONSTRAINT business_records_kind_check CHECK (kind IN ('source','object','task','rule','dataset','application','grant','archive','rule_version','directory','directory_revision','mount','mount_revision','review','assignment','processing_authorization','directory_dictionary','table_resource','file_resource','api_resource','resource_import','resource_upload','etl_task','etl_run','lightweight_upload','lightweight_unit'));
ALTER TABLE governance_runs ALTER COLUMN workspace_id DROP NOT NULL;
ALTER TABLE governance_runs ADD CONSTRAINT governance_run_lightweight_workspace CHECK (
 workspace_id IS NOT NULL OR (
  kind='dataflow' AND jsonb_typeof(definition->'lightweight')='object'
  AND COALESCE(length(definition->'lightweight'->>'unit_id'),0)>0
  AND COALESCE(definition->'lightweight'->>'version','') ~ '^[1-9][0-9]*$'
  AND definition->'flow'->>'id'=definition->'lightweight'->>'unit_id'
  AND definition->'flow'->>'version'=definition->'lightweight'->>'version'
 ) IS TRUE
);
CREATE FUNCTION governance_lightweight_run_workspace() RETURNS trigger LANGUAGE plpgsql SET search_path=pg_catalog,public AS $$
BEGIN
 IF NEW.kind='dataflow' AND jsonb_typeof(NEW.definition->'lightweight')='object'
  AND COALESCE(length(NEW.definition->'lightweight'->>'unit_id'),0)>0
  AND COALESCE(NEW.definition->'lightweight'->>'version','') ~ '^[1-9][0-9]*$'
  AND NEW.definition->'flow'->>'id'=NEW.definition->'lightweight'->>'unit_id'
  AND NEW.definition->'flow'->>'version'=NEW.definition->'lightweight'->>'version' THEN
  IF NEW.workspace_id IS NOT NULL AND NEW.workspace_id<>'' THEN RAISE EXCEPTION 'lightweight runs cannot have workspace'; END IF;
  NEW.workspace_id:=NULL;
 ELSIF TG_OP='INSERT' AND (NEW.workspace_id IS NULL OR NEW.workspace_id='') THEN
  NEW.workspace_id:=public.governance_legacy_workspace(NEW.department_id);
 END IF;
 RETURN NEW;
END $$;
DROP TRIGGER governance_run_workspace_default ON governance_runs;
CREATE TRIGGER governance_run_workspace_default BEFORE INSERT OR UPDATE ON governance_runs FOR EACH ROW EXECUTE FUNCTION governance_lightweight_run_workspace();
CREATE INDEX governance_lightweight_unit_runs ON governance_runs(actor_id,(definition->'lightweight'->>'unit_id'),created_at DESC,id) WHERE workspace_id IS NULL;
