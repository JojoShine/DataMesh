CREATE TABLE resource_snapshots (
 id text PRIMARY KEY, archive_id text NOT NULL, archive_kind text NOT NULL DEFAULT 'archive' CHECK(archive_kind='archive'),
 scope text NOT NULL DEFAULT '',
 object_id text NOT NULL, object_version integer NOT NULL CHECK(object_version>0),
 state text NOT NULL DEFAULT 'building' CHECK(state IN ('building','ready','failed')),
 stored_bytes bigint NOT NULL DEFAULT 0 CHECK(stored_bytes>=0),
 row_count bigint NOT NULL DEFAULT 0 CHECK(row_count>=0), event_count bigint NOT NULL DEFAULT 0,
 lease_owner text NOT NULL DEFAULT '', lease_expires_at timestamptz NOT NULL DEFAULT '1970-01-01 00:00:00+00',
 error_code text NOT NULL DEFAULT '', created_at timestamptz NOT NULL DEFAULT now(), updated_at timestamptz NOT NULL DEFAULT now(),
 FOREIGN KEY(archive_kind,archive_id) REFERENCES business_records(kind,id), UNIQUE(archive_id,object_version,scope)
);
CREATE TABLE resource_snapshot_rows (
 snapshot_id text NOT NULL REFERENCES resource_snapshots(id) ON DELETE CASCADE, key_digest bytea NOT NULL,
 partition integer NOT NULL, source_offset bigint NOT NULL, deleted boolean NOT NULL, encrypted_body bytea NOT NULL,
 PRIMARY KEY(snapshot_id,key_digest)
);
CREATE INDEX resource_snapshot_live_rows ON resource_snapshot_rows(snapshot_id,key_digest) WHERE deleted=false;
CREATE TABLE resource_snapshot_refs (
 snapshot_id text NOT NULL REFERENCES resource_snapshots(id) ON DELETE CASCADE, run_id text NOT NULL,
 run_kind text NOT NULL DEFAULT 'etl_run' CHECK(run_kind='etl_run'),
 FOREIGN KEY(run_kind,run_id) REFERENCES business_records(kind,id), PRIMARY KEY(snapshot_id,run_id)
);
CREATE TABLE archive_objects (
 key text PRIMARY KEY, object_id text NOT NULL,
 state text NOT NULL CHECK(state IN ('staging','referenced','deleting')),
 updated_at timestamptz NOT NULL DEFAULT now()
);
GRANT SELECT,INSERT,UPDATE,DELETE ON resource_snapshots,resource_snapshot_rows,resource_snapshot_refs,archive_objects TO datamesh_app;

CREATE TABLE governed_checks (
 id text PRIMARY KEY,dataset_id text NOT NULL,dataset_kind text NOT NULL DEFAULT 'dataset' CHECK(dataset_kind='dataset'),
 actor_id text NOT NULL REFERENCES users(id),definition jsonb NOT NULL,state text NOT NULL CHECK(state IN ('queued','checking','passed','blocked')),
 result_snapshot_id text NOT NULL,processed bigint NOT NULL DEFAULT 0,error_code text NOT NULL DEFAULT '',
 lease_owner text NOT NULL DEFAULT '',lease_expires_at timestamptz NOT NULL DEFAULT '1970-01-01 00:00:00+00',
 created_at timestamptz NOT NULL DEFAULT now(),updated_at timestamptz NOT NULL DEFAULT now(),
 FOREIGN KEY(dataset_kind,dataset_id) REFERENCES business_records(kind,id)
);
CREATE UNIQUE INDEX governed_checks_active ON governed_checks(dataset_id) WHERE state IN ('queued','checking');
GRANT SELECT,INSERT,UPDATE ON governed_checks TO datamesh_app;
