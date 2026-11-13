-- Preserve the one-per-kind rule only for historical template tasks.
DROP INDEX business_task_binding_idx;
CREATE UNIQUE INDEX business_task_binding_idx ON business_records((body->>'binding')) WHERE kind='task' AND body->'capture_binding' IS NULL;
CREATE TABLE cdc_bindings (
 task_id text PRIMARY KEY, task_kind text NOT NULL DEFAULT 'task' CHECK(task_kind='task'),
 source_id text NOT NULL, source_kind text NOT NULL DEFAULT 'source' CHECK(source_kind='source'), object_id text NOT NULL,
 kind text NOT NULL CHECK(kind IN ('postgres','mysql','oracle')),
 connector_name text NOT NULL UNIQUE, topic_prefix text NOT NULL UNIQUE, topic text NOT NULL UNIQUE,
 schema_history_topic text NOT NULL DEFAULT '', schema text NOT NULL, "table" text NOT NULL,
 slot text NOT NULL DEFAULT '', publication text NOT NULL DEFAULT '', pdb text NOT NULL DEFAULT '',
 source_fingerprint text NOT NULL, source_version integer NOT NULL CHECK(source_version>0),
 config_version integer NOT NULL CHECK(config_version>0), server_id bigint NOT NULL DEFAULT 0,
 FOREIGN KEY(task_kind,task_id) REFERENCES business_records(kind,id),
 FOREIGN KEY(source_kind,source_id) REFERENCES business_records(kind,id),
 UNIQUE(source_fingerprint,schema,"table")
);
CREATE UNIQUE INDEX cdc_server_ids ON cdc_bindings(server_id) WHERE server_id<>0;
CREATE UNIQUE INDEX cdc_slots ON cdc_bindings(source_fingerprint,slot) WHERE slot<>'';
CREATE TABLE cdc_controls (
 task_id text PRIMARY KEY REFERENCES cdc_bindings(task_id),
 desired_state text NOT NULL DEFAULT 'draft' CHECK(desired_state IN ('draft','running','paused')),
 config_owned boolean NOT NULL DEFAULT false,
 generation integer NOT NULL DEFAULT 1, observed_generation integer NOT NULL DEFAULT 0,
 lease_owner text NOT NULL DEFAULT '', lease_expires_at timestamptz NOT NULL DEFAULT '1970-01-01 00:00:00+00',
 last_error_code text NOT NULL DEFAULT '', updated_at timestamptz NOT NULL DEFAULT now()
);
GRANT SELECT,INSERT,UPDATE ON cdc_bindings,cdc_controls TO datamesh_app;
CREATE TABLE cdc_snapshot_progress (
 task_id text PRIMARY KEY REFERENCES cdc_bindings(task_id), state text NOT NULL DEFAULT 'pending',
 expected_rows bigint NOT NULL DEFAULT -1, observed_rows bigint NOT NULL DEFAULT 0,
 completed boolean NOT NULL DEFAULT false, error_code text NOT NULL DEFAULT '',
 notification_offset bigint NOT NULL DEFAULT 0, partitions jsonb NOT NULL DEFAULT '[]',
 lease_owner text NOT NULL DEFAULT '', lease_expires_at timestamptz NOT NULL DEFAULT '1970-01-01 00:00:00+00',
 updated_at timestamptz NOT NULL DEFAULT now()
);
GRANT SELECT,INSERT,UPDATE ON cdc_snapshot_progress TO datamesh_app;
