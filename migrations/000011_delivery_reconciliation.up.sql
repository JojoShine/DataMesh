CREATE TABLE etl_delivery_inputs (
 run_id text PRIMARY KEY,run_kind text NOT NULL DEFAULT 'etl_run' CHECK(run_kind='etl_run'),
 state text NOT NULL CHECK(state IN ('building','ready','expired')),wrapped_key bytea NOT NULL,
 target_kind text NOT NULL CHECK(target_kind IN ('postgres','mysql')),target_version integer NOT NULL,
 target_columns jsonb NOT NULL,key_count bigint NOT NULL DEFAULT 0,stored_bytes bigint NOT NULL DEFAULT 0,
 receipt_version integer NOT NULL DEFAULT 1,expires_at timestamptz NOT NULL,
 created_at timestamptz NOT NULL DEFAULT now(),updated_at timestamptz NOT NULL DEFAULT now(),
 FOREIGN KEY(run_kind,run_id) REFERENCES business_records(kind,id)
);
CREATE TABLE etl_delivery_keys (
 run_id text NOT NULL REFERENCES etl_delivery_inputs(run_id),key_digest bytea NOT NULL,
 encrypted_key bytea NOT NULL,expected_digest bytea NOT NULL,sequence bigint NOT NULL,
 PRIMARY KEY(run_id,key_digest)
);
CREATE TABLE etl_reconciliations (
 id text PRIMARY KEY,run_id text NOT NULL,run_kind text NOT NULL DEFAULT 'etl_run' CHECK(run_kind='etl_run'),
 version integer NOT NULL CHECK(version>0),state text NOT NULL CHECK(state IN ('pending','checking','passed','mismatched','failed')),
 input_snapshot_id text NOT NULL DEFAULT '',input_scope jsonb NOT NULL DEFAULT 'null',task_version integer NOT NULL,
 read_rows bigint NOT NULL DEFAULT 0,processed_rows bigint NOT NULL DEFAULT 0,key_count bigint NOT NULL DEFAULT 0,
 duration_ms bigint NOT NULL DEFAULT 0,checked_rows bigint NOT NULL DEFAULT 0,mismatch_count bigint NOT NULL DEFAULT 0,error_code text NOT NULL DEFAULT '',checked_at timestamptz,
 lease_owner text NOT NULL DEFAULT '',lease_expires_at timestamptz NOT NULL DEFAULT '1970-01-01 00:00:00+00',
 created_at timestamptz NOT NULL DEFAULT now(),updated_at timestamptz NOT NULL DEFAULT now(),
 FOREIGN KEY(run_kind,run_id) REFERENCES business_records(kind,id),UNIQUE(run_id,version)
);
CREATE UNIQUE INDEX etl_reconciliations_active ON etl_reconciliations(run_id) WHERE state IN ('pending','checking');
GRANT SELECT,INSERT,UPDATE,DELETE ON etl_delivery_inputs,etl_delivery_keys TO datamesh_app;
GRANT SELECT,INSERT,UPDATE ON etl_reconciliations TO datamesh_app;
CREATE TABLE delivery_receipts (
 id text PRIMARY KEY,run_id text NOT NULL REFERENCES etl_delivery_inputs(run_id),version integer NOT NULL CHECK(version>1),
 reconciliation_id text NOT NULL REFERENCES etl_reconciliations(id),decision text NOT NULL CHECK(decision IN ('accepted','rejected')),
 reason text NOT NULL,actor_id text NOT NULL REFERENCES users(id),recipient_id text NOT NULL REFERENCES users(id),
 on_behalf_reason text NOT NULL DEFAULT '',created_at timestamptz NOT NULL DEFAULT now(),UNIQUE(run_id,version)
);
GRANT SELECT,INSERT ON delivery_receipts TO datamesh_app;
