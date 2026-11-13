CREATE TABLE governance_outputs (
 run_id text PRIMARY KEY REFERENCES governance_runs(id),
 dataset_id text NOT NULL UNIQUE,
 dataset_body jsonb NOT NULL,
 created_at timestamptz NOT NULL DEFAULT now(),
 CHECK(dataset_body->>'governance_run_id'=run_id)
);
GRANT SELECT,INSERT ON governance_outputs TO datamesh_app;
