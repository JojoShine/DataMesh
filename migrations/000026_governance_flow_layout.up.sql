CREATE TABLE governance_flow_layouts (
 user_id text NOT NULL REFERENCES users(id),
 definition_id text NOT NULL REFERENCES governance_definitions(id),
 definition_version integer NOT NULL,
 revision integer NOT NULL CHECK(revision>0),
 positions jsonb NOT NULL,
 viewport jsonb NOT NULL,
 updated_at timestamptz NOT NULL DEFAULT CURRENT_TIMESTAMP,
 PRIMARY KEY(user_id,definition_id,definition_version),
 FOREIGN KEY(definition_id,definition_version) REFERENCES governance_versions(definition_id,version)
);
GRANT SELECT,INSERT,UPDATE,DELETE ON governance_flow_layouts TO datamesh_app;
