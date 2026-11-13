ALTER TABLE governance_definitions DROP CONSTRAINT governance_definitions_kind_check;
ALTER TABLE governance_definitions ADD CONSTRAINT governance_definitions_kind_check CHECK(kind IN ('domain','term','standard','model','metadata','dataflow','workflow'));
CREATE TABLE governance_workflow_tasks (
 parent_run_id text NOT NULL REFERENCES governance_runs(id),
 task_id text NOT NULL,
 child_run_id text UNIQUE REFERENCES governance_runs(id),
 state text NOT NULL CHECK(state IN ('pending','queued','running','succeeded','failed','cancelled','interrupted','skipped')),
 error_code text NOT NULL DEFAULT '',
 updated_at timestamptz NOT NULL DEFAULT now(),
 PRIMARY KEY(parent_run_id,task_id)
);
CREATE TABLE governance_schedules (
 id text PRIMARY KEY,
 department_id text NOT NULL,
 actor_id text NOT NULL REFERENCES users(id),
 workflow_id text NOT NULL,
 workflow_version integer NOT NULL CHECK(workflow_version>0),
 timezone text NOT NULL,
 interval_seconds integer NOT NULL CHECK(interval_seconds BETWEEN 60 AND 31536000),
 concurrency_policy text NOT NULL CHECK(concurrency_policy='forbid'),
 missed_policy text NOT NULL CHECK(missed_policy IN ('latest','skip')),
 next_run_at timestamptz NOT NULL,
 state text NOT NULL CHECK(state IN ('active','paused')),
 version integer NOT NULL DEFAULT 1,
 last_run_id text REFERENCES governance_runs(id),
 last_error_code text NOT NULL DEFAULT '',
 created_at timestamptz NOT NULL DEFAULT now(),
 updated_at timestamptz NOT NULL DEFAULT now(),
 FOREIGN KEY(workflow_id,workflow_version) REFERENCES governance_versions(definition_id,version)
);
CREATE INDEX governance_schedules_due ON governance_schedules(next_run_at,id) WHERE state='active';
CREATE TABLE governance_schedule_triggers (
 schedule_id text NOT NULL REFERENCES governance_schedules(id),
 scheduled_at timestamptz NOT NULL,
 run_id text REFERENCES governance_runs(id),
 code text NOT NULL,
 PRIMARY KEY(schedule_id,scheduled_at)
);
GRANT SELECT,INSERT,UPDATE ON governance_workflow_tasks,governance_schedules TO datamesh_app;
GRANT SELECT,INSERT ON governance_schedule_triggers TO datamesh_app;
