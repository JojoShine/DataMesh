ALTER TABLE governance_workflow_tasks DROP CONSTRAINT governance_workflow_tasks_state_check;
ALTER TABLE governance_workflow_tasks ADD CONSTRAINT governance_workflow_tasks_state_check CHECK(state IN ('pending','queued','running','waiting_approval','succeeded','failed','cancelled','interrupted','skipped'));
ALTER TABLE governance_workflow_tasks ADD COLUMN result jsonb NOT NULL DEFAULT 'null';
ALTER TABLE governance_workflow_tasks ADD COLUMN reused_from_run text REFERENCES governance_runs(id);
ALTER TABLE governance_workflow_tasks DROP CONSTRAINT governance_workflow_tasks_child_run_id_key;
CREATE INDEX governance_workflow_tasks_child ON governance_workflow_tasks(child_run_id) WHERE child_run_id IS NOT NULL;
ALTER TABLE governance_schedules ADD COLUMN parameters jsonb NOT NULL DEFAULT '{}' CHECK(jsonb_typeof(parameters)='object');
