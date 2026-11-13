CREATE TABLE governance_alert_rules (
 id text PRIMARY KEY, department_id text NOT NULL, actor_id text NOT NULL REFERENCES users(id),
 name text NOT NULL, enabled boolean NOT NULL DEFAULT false, failure boolean NOT NULL DEFAULT true,
 quality_rate double precision NOT NULL CHECK(quality_rate>=0 AND quality_rate<=1), webhook text NOT NULL DEFAULT '',
 cursor_at timestamptz NOT NULL DEFAULT now(), cursor_id text NOT NULL DEFAULT '', version integer NOT NULL DEFAULT 1,
 created_at timestamptz NOT NULL DEFAULT now(), updated_at timestamptz NOT NULL DEFAULT now()
);
CREATE TABLE governance_alerts (
 id text PRIMARY KEY, rule_id text NOT NULL REFERENCES governance_alert_rules(id), run_id text NOT NULL REFERENCES governance_runs(id),
 code text NOT NULL, state text NOT NULL CHECK(state IN ('in_app','pending','delivering','delivered','failed','cancelled')),
 attempts integer NOT NULL DEFAULT 0, next_at timestamptz NOT NULL DEFAULT now(), lease_until timestamptz NOT NULL DEFAULT '1970-01-01Z',
 error_code text NOT NULL DEFAULT '', created_at timestamptz NOT NULL DEFAULT now(), delivered_at timestamptz,
 UNIQUE(rule_id,run_id,code)
);
CREATE INDEX governance_alerts_pending ON governance_alerts(next_at) WHERE state IN ('pending','delivering');
CREATE TABLE governance_retention_policies (
 id text PRIMARY KEY, department_id text NOT NULL, actor_id text NOT NULL REFERENCES users(id), flow_id text NOT NULL REFERENCES governance_definitions(id),
 days integer NOT NULL CHECK(days BETWEEN 1 AND 3650), keep_last integer NOT NULL CHECK(keep_last BETWEEN 1 AND 1000), enabled boolean NOT NULL DEFAULT false,
 version integer NOT NULL DEFAULT 1, created_at timestamptz NOT NULL DEFAULT now(), updated_at timestamptz NOT NULL DEFAULT now(), UNIQUE(actor_id,flow_id)
);
CREATE TABLE governance_snapshot_purges (
 run_id text PRIMARY KEY REFERENCES governance_outputs(run_id), actor_id text NOT NULL REFERENCES users(id),
 deleted_rows bigint NOT NULL CHECK(deleted_rows>=0), evidence_hash text NOT NULL, policy_id text REFERENCES governance_retention_policies(id), created_at timestamptz NOT NULL DEFAULT now()
);
CREATE TABLE governance_migration_plans (
 id text PRIMARY KEY, department_id text NOT NULL, actor_id text NOT NULL REFERENCES users(id), source_id text NOT NULL REFERENCES governance_outputs(dataset_id),
 target_id text NOT NULL REFERENCES governance_outputs(dataset_id), state text NOT NULL CHECK(state IN ('open','completed','cancelled')),
 drafts jsonb NOT NULL DEFAULT '[]', created_at timestamptz NOT NULL DEFAULT now(), updated_at timestamptz NOT NULL DEFAULT now(), CHECK(source_id<>target_id)
);
GRANT SELECT,INSERT,UPDATE ON governance_alert_rules,governance_alerts,governance_retention_policies,governance_migration_plans TO datamesh_app;
GRANT SELECT,INSERT ON governance_snapshot_purges TO datamesh_app;
