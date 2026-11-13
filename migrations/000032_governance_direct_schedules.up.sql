-- Legacy versions could serve both engines. Never silently break a published workflow.
DO $$ BEGIN
 IF EXISTS(
  SELECT 1 FROM governance_streams s
  JOIN governance_versions v ON v.state='published'
  JOIN governance_definitions d ON d.id=v.definition_id AND d.kind='workflow'
  WHERE v.definition->'workflow'->'tasks' @> jsonb_build_array(jsonb_build_object('dataflow',jsonb_build_object('id',s.flow_id,'version',s.flow_version)))
 ) THEN
  RAISE EXCEPTION 'GOVERNANCE_MODE_MIGRATION_CONFLICT: split legacy mixed-mode versions before migration';
 END IF;
END $$;
-- Mode belongs to the immutable version; preserve definition/run JSON fingerprints.
ALTER TABLE governance_versions ADD COLUMN execution_mode text NOT NULL DEFAULT 'batch'
 CHECK(execution_mode IN ('batch','continuous'));
UPDATE governance_versions v SET execution_mode='continuous'
 WHERE EXISTS(SELECT 1 FROM governance_streams s WHERE s.flow_id=v.definition_id AND s.flow_version=v.version);
-- Keep the legacy reference columns and FK for existing clients and plans.
ALTER TABLE governance_schedules ADD COLUMN target_kind text NOT NULL DEFAULT 'workflow'
 CHECK(target_kind IN ('workflow','dataflow'));
