CREATE TABLE governance_model_deployments (
 id text PRIMARY KEY,
 department_id text NOT NULL,
 actor_id text NOT NULL REFERENCES users(id),
 model_id text NOT NULL,
 model_version integer NOT NULL,
 source_id text NOT NULL,
 source_version integer NOT NULL,
 source_fingerprint text NOT NULL,
 target_schema text NOT NULL,
 target_table text NOT NULL,
 ddl text NOT NULL,
 state text NOT NULL CHECK(state IN ('queued','running','succeeded','failed','unknown')),
 error_code text NOT NULL DEFAULT '',
 lease_owner text NOT NULL DEFAULT '',
 lease_expires_at timestamptz NOT NULL DEFAULT '1970-01-01Z',
 created_at timestamptz NOT NULL DEFAULT now(),
 updated_at timestamptz NOT NULL DEFAULT now(),
 FOREIGN KEY(model_id,model_version) REFERENCES governance_versions(definition_id,version)
);
CREATE INDEX governance_model_deployments_owner ON governance_model_deployments(department_id,actor_id,created_at DESC);
CREATE UNIQUE INDEX governance_model_deployments_active ON governance_model_deployments(source_id,target_schema,target_table) WHERE state IN ('queued','running','unknown');
GRANT SELECT,INSERT,UPDATE ON governance_model_deployments TO datamesh_app;
