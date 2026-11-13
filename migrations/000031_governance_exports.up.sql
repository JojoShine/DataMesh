CREATE TABLE governance_exports (
 id text PRIMARY KEY,
 run_id text NOT NULL REFERENCES governance_runs(id),
 actor_id text NOT NULL REFERENCES users(id),
 state text NOT NULL CHECK(state IN ('queued','running','ready','failed')),
 manifest jsonb NOT NULL DEFAULT '{}',
 error_code text NOT NULL DEFAULT '',
 lease_owner text NOT NULL DEFAULT '',
 lease_expires_at timestamptz NOT NULL DEFAULT 'epoch',
 created_at timestamptz NOT NULL DEFAULT now(),
 updated_at timestamptz NOT NULL DEFAULT now(),
 UNIQUE(run_id,actor_id)
);
GRANT SELECT,INSERT,UPDATE ON governance_exports TO datamesh_app;
ALTER TABLE governance_snapshot_purges ADD COLUMN storage_cleaned_at timestamptz;
