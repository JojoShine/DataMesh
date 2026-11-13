DO $$ BEGIN
 IF EXISTS(SELECT 1 FROM governance_workflow_tasks WHERE state='waiting_approval' OR result<>'null'::jsonb OR reused_from_run IS NOT NULL)
 OR EXISTS(SELECT child_run_id FROM governance_workflow_tasks WHERE child_run_id IS NOT NULL GROUP BY child_run_id HAVING count(*)>1)
 OR EXISTS(SELECT 1 FROM governance_schedules WHERE parameters<>'{}'::jsonb)
 OR EXISTS(SELECT 1 FROM governance_versions WHERE jsonb_path_exists(definition, '$.** ? (exists(@.parameter) || exists(@.parameter_bindings) || @.kind == "quality_check" || @.kind == "publish_approval" || exists(@.parameters) || exists(@.input_task))'))
 OR EXISTS(SELECT 1 FROM governance_runs WHERE definition ? 'parameters') THEN
  RAISE EXCEPTION 'Extended workflow evidence exists; refusing destructive rollback';
 END IF;
END $$;
ALTER TABLE governance_schedules DROP COLUMN parameters;
DROP INDEX governance_workflow_tasks_child;
ALTER TABLE governance_workflow_tasks ADD CONSTRAINT governance_workflow_tasks_child_run_id_key UNIQUE(child_run_id);
ALTER TABLE governance_workflow_tasks DROP COLUMN reused_from_run;
ALTER TABLE governance_workflow_tasks DROP COLUMN result;
ALTER TABLE governance_workflow_tasks DROP CONSTRAINT governance_workflow_tasks_state_check;
ALTER TABLE governance_workflow_tasks ADD CONSTRAINT governance_workflow_tasks_state_check CHECK(state IN ('pending','queued','running','succeeded','failed','cancelled','interrupted','skipped'));
