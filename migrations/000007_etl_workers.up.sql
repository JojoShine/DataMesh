ALTER TABLE etl_run_controls ADD COLUMN lease_owner text NOT NULL DEFAULT '', ADD COLUMN lease_expires_at timestamptz NOT NULL DEFAULT '1970-01-01 00:00:00+00', ADD COLUMN target_key text NOT NULL DEFAULT '';
CREATE INDEX etl_control_leases ON etl_run_controls(lease_expires_at) WHERE active=true;
CREATE TABLE etl_target_locks (
 target_key text PRIMARY KEY, run_id text NOT NULL UNIQUE, run_kind text NOT NULL DEFAULT 'etl_run' CHECK(run_kind='etl_run'),
 FOREIGN KEY(run_kind,run_id) REFERENCES business_records(kind,id)
);
CREATE INDEX business_etl_queue ON business_records(created_at,id) WHERE kind='etl_run' AND state='queued';
GRANT SELECT,INSERT,UPDATE,DELETE ON etl_target_locks TO datamesh_app;
