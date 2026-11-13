CREATE TABLE security_controls (
 id text PRIMARY KEY, kind text NOT NULL CHECK(kind IN ('classification','policy','watermark','recognition','risk_rule')),
 owner_id text NOT NULL REFERENCES users(id), department_id text NOT NULL,
 generation integer NOT NULL DEFAULT 1, latest_version integer NOT NULL DEFAULT 1,
 published_version integer NOT NULL DEFAULT 0, created_at timestamptz NOT NULL DEFAULT now(), updated_at timestamptz NOT NULL DEFAULT now()
);
CREATE TABLE security_control_versions (
 control_id text NOT NULL REFERENCES security_controls(id), version integer NOT NULL,
 state text NOT NULL CHECK(state IN ('draft','submitted','published','rejected','retired')),
 definition jsonb NOT NULL, created_by text NOT NULL REFERENCES users(id), reviewer_id text NOT NULL DEFAULT '', reason text NOT NULL DEFAULT '',
 created_at timestamptz NOT NULL DEFAULT now(), PRIMARY KEY(control_id,version)
);
CREATE INDEX security_controls_kind ON security_controls(kind,updated_at DESC,id);
CREATE INDEX security_control_resource ON security_control_versions((definition->'resource'->>'resource_id')) WHERE state='published';
CREATE TABLE security_scans (
 id text PRIMARY KEY, actor_id text NOT NULL REFERENCES users(id), resource jsonb NOT NULL,
 candidates jsonb NOT NULL, created_at timestamptz NOT NULL DEFAULT now()
);
CREATE TABLE security_incidents (
 id text PRIMARY KEY, resource jsonb NOT NULL, resource_id text NOT NULL,
 title text NOT NULL, reason text NOT NULL, state text NOT NULL CHECK(state IN ('open','frozen','resolved')),
 owner_id text NOT NULL REFERENCES users(id), frozen_by text NOT NULL DEFAULT '', reviewed_by text NOT NULL DEFAULT '',
 generation integer NOT NULL DEFAULT 1, evidence jsonb NOT NULL DEFAULT '{}', dedupe_key text UNIQUE,
 created_at timestamptz NOT NULL DEFAULT now(), updated_at timestamptz NOT NULL DEFAULT now()
);
CREATE INDEX security_incidents_resource ON security_incidents(resource_id,state);
CREATE TABLE security_delivery_proofs (
 id text PRIMARY KEY, actor_id text NOT NULL REFERENCES users(id), department_id text NOT NULL,
 resource jsonb NOT NULL, action text NOT NULL, grant_id text NOT NULL,
 fields jsonb NOT NULL, controls jsonb NOT NULL, manifest jsonb NOT NULL DEFAULT '{}', text text NOT NULL, sha256 text NOT NULL,
 key_id text NOT NULL, signature text NOT NULL, state text NOT NULL CHECK(state IN ('prepared','completed','interrupted','unknown')),
 size_bytes bigint NOT NULL, run_id text NOT NULL DEFAULT '', created_at timestamptz NOT NULL DEFAULT now(), updated_at timestamptz NOT NULL DEFAULT now()
);
CREATE INDEX security_proofs_actor ON security_delivery_proofs(actor_id,created_at DESC,id);
CREATE INDEX security_proofs_run ON security_delivery_proofs(run_id) WHERE run_id<>'';
GRANT SELECT,INSERT,UPDATE ON security_controls,security_control_versions,security_incidents,security_delivery_proofs TO datamesh_app;
GRANT SELECT,INSERT ON security_scans TO datamesh_app;
