ALTER TABLE governance_definitions DROP CONSTRAINT governance_definitions_kind_check;
ALTER TABLE governance_definitions ADD CONSTRAINT governance_definitions_kind_check CHECK(kind IN ('domain','term','standard','model','metadata'));
CREATE TABLE metadata_observations (
 id text PRIMARY KEY,
 object_id text NOT NULL,
 version integer NOT NULL CHECK(version>0),
 source_id text NOT NULL,
 source_version integer NOT NULL CHECK(source_version>0),
 object_version integer NOT NULL CHECK(object_version>0),
 schema text NOT NULL,
 "table" text NOT NULL,
 fields jsonb NOT NULL,
 columns jsonb NOT NULL DEFAULT '[]',
 changes jsonb NOT NULL,
 matches_active boolean NOT NULL,
 observed_by text NOT NULL REFERENCES users(id),
 created_at timestamptz NOT NULL DEFAULT now(),
 UNIQUE(object_id,version)
);
GRANT SELECT,INSERT ON metadata_observations TO datamesh_app;
