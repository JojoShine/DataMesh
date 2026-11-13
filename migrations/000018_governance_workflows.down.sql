DO $$ BEGIN
 IF EXISTS(SELECT 1 FROM governance_workflow_tasks) OR EXISTS(SELECT 1 FROM governance_schedules) OR EXISTS(SELECT 1 FROM governance_definitions WHERE kind='workflow') THEN
  RAISE EXCEPTION 'workflow or schedule evidence exists; preserve it before rollback';
 END IF;
END $$;
DROP TABLE governance_schedule_triggers;
DROP TABLE governance_schedules;
DROP TABLE governance_workflow_tasks;
ALTER TABLE governance_definitions DROP CONSTRAINT governance_definitions_kind_check;
ALTER TABLE governance_definitions ADD CONSTRAINT governance_definitions_kind_check CHECK(kind IN ('domain','term','standard','model','metadata','dataflow'));
