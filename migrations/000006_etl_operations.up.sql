CREATE TABLE etl_run_controls (
 run_id text PRIMARY KEY, run_kind text NOT NULL DEFAULT 'etl_run' CHECK(run_kind='etl_run'),
 cancel_requested boolean NOT NULL DEFAULT false, active boolean NOT NULL DEFAULT false,
 heartbeat_at timestamptz NOT NULL DEFAULT now(), stage_started_at timestamptz NOT NULL DEFAULT now(),
 FOREIGN KEY(run_kind,run_id) REFERENCES business_records(kind,id)
);
CREATE TABLE etl_events (
 id bigint GENERATED ALWAYS AS IDENTITY PRIMARY KEY, run_id text NOT NULL,
 run_kind text NOT NULL DEFAULT 'etl_run' CHECK(run_kind='etl_run'), task_id text NOT NULL,
 level text NOT NULL CHECK(level IN ('info','warning','error')), stage text NOT NULL, code text NOT NULL,
 message text NOT NULL, actor_id text NOT NULL, trace_id text NOT NULL DEFAULT '', created_at timestamptz NOT NULL DEFAULT now(),
 FOREIGN KEY(run_kind,run_id) REFERENCES business_records(kind,id)
);
CREATE INDEX etl_events_run ON etl_events(run_id,id);
CREATE INDEX etl_events_retention ON etl_events(created_at);
CREATE TABLE etl_alerts (
 id text PRIMARY KEY, run_id text NOT NULL, run_kind text NOT NULL DEFAULT 'etl_run' CHECK(run_kind='etl_run'),
 code text NOT NULL, severity text NOT NULL CHECK(severity IN ('warning','error')), message text NOT NULL,
 state text NOT NULL DEFAULT 'open' CHECK(state IN ('open','acknowledged','resolved')),
 handled_by text NOT NULL DEFAULT '', reason text NOT NULL DEFAULT '', created_at timestamptz NOT NULL DEFAULT now(), updated_at timestamptz NOT NULL DEFAULT now(),
 UNIQUE(run_id,code), FOREIGN KEY(run_kind,run_id) REFERENCES business_records(kind,id)
);
CREATE INDEX etl_alerts_run ON etl_alerts(run_id,created_at);
GRANT SELECT,INSERT,UPDATE ON etl_run_controls,etl_alerts TO datamesh_app;
GRANT SELECT,INSERT,DELETE ON etl_events TO datamesh_app;
GRANT USAGE,SELECT ON SEQUENCE etl_events_id_seq TO datamesh_app;
