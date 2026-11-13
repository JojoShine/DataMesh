CREATE TABLE user_roles (
 user_id text NOT NULL REFERENCES users(id),
 role text NOT NULL CHECK (role IN ('admin','operator','governor','provider','consumer','auditor')),
 PRIMARY KEY(user_id,role)
);
CREATE TABLE business_records (
 kind text NOT NULL CHECK (kind IN ('source','object','task','rule','dataset','application','grant','archive','rule_version')),
 id text NOT NULL,
 department_id text NOT NULL,
 owner_id text NOT NULL REFERENCES users(id),
 parent_id text NOT NULL DEFAULT '',
 state text NOT NULL,
 version integer NOT NULL DEFAULT 1 CHECK(version>0),
 body jsonb NOT NULL,
 created_at timestamptz NOT NULL DEFAULT now(),
 updated_at timestamptz NOT NULL DEFAULT now(),
 PRIMARY KEY(kind,id)
);
CREATE INDEX business_department_idx ON business_records(kind,department_id,created_at,id);
CREATE INDEX business_parent_idx ON business_records(kind,parent_id);
CREATE UNIQUE INDEX grant_application_once_idx ON business_records(parent_id) WHERE kind='grant';
CREATE TABLE business_operations (
 id text PRIMARY KEY,
 actor_id text NOT NULL REFERENCES users(id),
 idempotency_key text NOT NULL,
 request_hash text NOT NULL,
 status text NOT NULL CHECK(status IN ('accepted','running','succeeded','failed','unknown')),
 resource_id text NOT NULL DEFAULT '',
 response_status integer NOT NULL DEFAULT 202,
 response jsonb NOT NULL,
 created_at timestamptz NOT NULL DEFAULT now(),
 updated_at timestamptz NOT NULL DEFAULT now(),
 UNIQUE(actor_id,idempotency_key)
);
GRANT SELECT,INSERT ON user_roles TO datamesh_app;
GRANT SELECT,INSERT,UPDATE ON business_records,business_operations TO datamesh_app;
CREATE UNIQUE INDEX business_local_source_kind_idx ON business_records((body->>'kind')) WHERE kind='source';
CREATE UNIQUE INDEX business_task_binding_idx ON business_records((body->>'binding')) WHERE kind='task';
