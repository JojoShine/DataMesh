CREATE TABLE governance_streams (
    id text PRIMARY KEY,
    workspace_id text NOT NULL REFERENCES governance_workspaces(id),
    department_id text NOT NULL,
    actor_id text NOT NULL REFERENCES users(id),
    flow_id text NOT NULL,
    flow_version integer NOT NULL CHECK(flow_version > 0),
    definition jsonb NOT NULL,
    sources jsonb NOT NULL,
    positions jsonb NOT NULL DEFAULT '{}',
    state text NOT NULL CHECK(state IN ('starting','running','pausing','paused','failed','unknown')),
    desired_state text NOT NULL CHECK(desired_state IN ('running','paused','failed')),
    generation integer NOT NULL DEFAULT 1,
    job_id text NOT NULL DEFAULT '',
    session_id text NOT NULL DEFAULT '',
    operation_id text NOT NULL DEFAULT '',
    submission_started boolean NOT NULL DEFAULT false,
    stop_trigger text NOT NULL DEFAULT '',
    savepoint text NOT NULL DEFAULT '',
    error_code text NOT NULL DEFAULT '',
    input_events bigint NOT NULL DEFAULT 0,
    output_rows bigint NOT NULL DEFAULT 0,
    rejected_rows bigint NOT NULL DEFAULT 0,
    output_offset bigint NOT NULL DEFAULT 0,
    caught_up boolean NOT NULL DEFAULT false,
    lease_owner text NOT NULL DEFAULT '',
    lease_expires_at timestamptz NOT NULL DEFAULT 'epoch',
    created_at timestamptz NOT NULL DEFAULT now(),
    updated_at timestamptz NOT NULL DEFAULT now()
);
CREATE UNIQUE INDEX governance_stream_active_flow ON governance_streams(workspace_id,actor_id,flow_id)
    WHERE state <> 'failed' OR desired_state <> 'failed';
GRANT SELECT,INSERT,UPDATE ON governance_streams TO datamesh_app;
