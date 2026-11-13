ALTER TABLE governance_definitions DROP CONSTRAINT governance_definitions_kind_check;
ALTER TABLE governance_definitions ADD CONSTRAINT governance_definitions_kind_check CHECK(kind IN ('domain','term','standard','model','metadata','dataflow'));
CREATE TABLE governance_flow_rows (
 run_id text NOT NULL REFERENCES governance_runs(id),
 node_id text NOT NULL,
 port text NOT NULL DEFAULT '',
 ordinal bigint NOT NULL CHECK(ordinal>0),
 encrypted_body bytea NOT NULL,
 PRIMARY KEY(run_id,node_id,port,ordinal)
);
GRANT SELECT,INSERT,DELETE ON governance_flow_rows TO datamesh_app;
ALTER TABLE governance_run_events ADD COLUMN input_count bigint NOT NULL DEFAULT 0;
ALTER TABLE governance_run_events ADD COLUMN output_count bigint NOT NULL DEFAULT 0;
ALTER TABLE governance_run_events ADD COLUMN duration_ms bigint NOT NULL DEFAULT 0;
