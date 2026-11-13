CREATE TABLE governance_monitors (
 id text PRIMARY KEY, workspace_id text NOT NULL REFERENCES governance_workspaces(id),
 department_id text NOT NULL, actor_id text NOT NULL REFERENCES users(id), name text NOT NULL,
 policy jsonb NOT NULL, enabled boolean NOT NULL DEFAULT false, interval_minutes integer NOT NULL CHECK(interval_minutes BETWEEN 15 AND 43200),
 version integer NOT NULL DEFAULT 1 CHECK(version>0), next_at timestamptz NOT NULL DEFAULT now(),
 pending_run_id text REFERENCES governance_runs(id), baseline_id text, last_observation_id text,
 created_at timestamptz NOT NULL DEFAULT now(), updated_at timestamptz NOT NULL DEFAULT now()
);
CREATE INDEX governance_monitors_due ON governance_monitors(next_at) WHERE enabled;
CREATE TABLE governance_monitor_observations (
 id text PRIMARY KEY, monitor_id text NOT NULL REFERENCES governance_monitors(id), run_id text UNIQUE REFERENCES governance_runs(id),
 policy_version integer NOT NULL, policy jsonb NOT NULL, state text NOT NULL CHECK(state IN ('queued','passed','alert','failed','superseded')),
 report jsonb NOT NULL DEFAULT '{}', error_code text NOT NULL DEFAULT '', created_at timestamptz NOT NULL DEFAULT now(), finished_at timestamptz
);
CREATE INDEX governance_monitor_history ON governance_monitor_observations(monitor_id,created_at DESC,id);
ALTER TABLE governance_monitors ADD CONSTRAINT monitor_baseline_ref FOREIGN KEY(baseline_id) REFERENCES governance_monitor_observations(id);
ALTER TABLE governance_monitors ADD CONSTRAINT monitor_observation_ref FOREIGN KEY(last_observation_id) REFERENCES governance_monitor_observations(id);
GRANT SELECT,INSERT,UPDATE ON governance_monitors,governance_monitor_observations TO datamesh_app;
