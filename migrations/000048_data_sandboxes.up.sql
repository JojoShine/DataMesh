CREATE TABLE data_sandboxes (
 id text PRIMARY KEY, owner_id text NOT NULL REFERENCES users(id), department_id text NOT NULL,
 owner_version integer NOT NULL, visitor_scope text NOT NULL DEFAULT '', name text NOT NULL,
 state text NOT NULL DEFAULT 'active' CHECK (state IN ('active','closing','closed','expired')),
 version integer NOT NULL DEFAULT 1 CHECK (version > 0),
 expires_at timestamptz NOT NULL, created_at timestamptz NOT NULL DEFAULT now(), updated_at timestamptz NOT NULL DEFAULT now()
);
CREATE INDEX data_sandboxes_owner ON data_sandboxes(owner_id,department_id,visitor_scope,state);
CREATE TABLE data_sandbox_tables (
 id text PRIMARY KEY, sandbox_id text NOT NULL REFERENCES data_sandboxes(id), name text NOT NULL,
 state text NOT NULL CHECK (state IN ('queued','running','ready','failed','canceled','unknown','removing','removed')),
 fields jsonb NOT NULL DEFAULT '[]', field_labels jsonb NOT NULL DEFAULT '{}',
 rows bigint NOT NULL DEFAULT 0 CHECK(rows>=0), bytes bigint NOT NULL DEFAULT 0 CHECK(bytes>=0),
 query_id text, error_code text, source_object jsonb, result_object jsonb,
 attempt_id text, load_label text, source_digest text, sandbox_version integer,
 lease_owner text, lease_until timestamptz, cancel_requested boolean NOT NULL DEFAULT false,
 physical_table text NOT NULL UNIQUE CHECK(physical_table ~ '^t_[0-9a-f]{32}$'),
 created_at timestamptz NOT NULL DEFAULT now(), updated_at timestamptz NOT NULL DEFAULT now()
);
CREATE UNIQUE INDEX data_sandbox_tables_name ON data_sandbox_tables(sandbox_id,lower(name)) WHERE state NOT IN ('removed');
CREATE TABLE data_sandbox_queries (
 id text PRIMARY KEY, sandbox_id text NOT NULL REFERENCES data_sandboxes(id), sql text NOT NULL,
 reserved_bytes bigint NOT NULL DEFAULT 0 CHECK(reserved_bytes>=0), input_tables jsonb, mode text NOT NULL CHECK(mode IN ('preview','materialize')), table_name text,
 state text NOT NULL CHECK(state IN ('queued','running','succeeded','failed','canceled','unknown')),
 fields jsonb NOT NULL DEFAULT '[]', row_count bigint NOT NULL DEFAULT 0 CHECK(row_count>=0),
 truncated boolean NOT NULL DEFAULT false, result_table_id text REFERENCES data_sandbox_tables(id),
 error_code text, result_object jsonb, attempt_id text, sandbox_version integer, lease_owner text, lease_until timestamptz,
 cancel_requested boolean NOT NULL DEFAULT false,
 created_at timestamptz NOT NULL DEFAULT now(), updated_at timestamptz NOT NULL DEFAULT now()
);
CREATE INDEX data_sandbox_tables_queue ON data_sandbox_tables(created_at,id) WHERE state IN ('queued','running');
CREATE INDEX data_sandbox_queries_queue ON data_sandbox_queries(created_at,id) WHERE state IN ('queued','running');

ALTER TABLE data_sandbox_tables ADD CONSTRAINT data_sandbox_table_query_fk FOREIGN KEY(query_id) REFERENCES data_sandbox_queries(id);
CREATE TABLE data_sandbox_worker_lock (id integer PRIMARY KEY CHECK(id=1), lease_owner text, lease_until timestamptz);
INSERT INTO data_sandbox_worker_lock(id) VALUES(1);
GRANT SELECT,INSERT,UPDATE,DELETE ON data_sandboxes,data_sandbox_tables,data_sandbox_queries,data_sandbox_worker_lock TO datamesh_app;

CREATE TABLE data_sandbox_objects (key text PRIMARY KEY, sandbox_id text NOT NULL, object jsonb NOT NULL, created_at timestamptz NOT NULL DEFAULT now());
GRANT SELECT,INSERT,UPDATE,DELETE ON data_sandbox_objects TO datamesh_app;
