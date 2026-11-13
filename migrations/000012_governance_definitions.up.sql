CREATE TABLE governance_definitions (
 id text PRIMARY KEY,
 kind text NOT NULL CHECK (kind IN ('domain','term','standard','model')),
 department_id text NOT NULL,
 latest_version integer NOT NULL CHECK(latest_version>0),
 created_at timestamptz NOT NULL DEFAULT now()
);
CREATE INDEX governance_definitions_department_idx ON governance_definitions(department_id,kind,created_at,id);
CREATE TABLE governance_versions (
 definition_id text NOT NULL REFERENCES governance_definitions(id),
 version integer NOT NULL CHECK(version>0),
 state text NOT NULL CHECK(state IN ('draft','published','retired')),
 definition jsonb NOT NULL,
 change_note text NOT NULL DEFAULT '',
 migration_note text NOT NULL DEFAULT '',
 created_by text NOT NULL REFERENCES users(id),
 created_at timestamptz NOT NULL DEFAULT now(),
 published_at timestamptz,
 PRIMARY KEY(definition_id,version)
);
CREATE INDEX governance_versions_published_idx ON governance_versions(definition_id,version) WHERE state='published';
GRANT SELECT,INSERT,UPDATE ON governance_definitions,governance_versions TO datamesh_app;
