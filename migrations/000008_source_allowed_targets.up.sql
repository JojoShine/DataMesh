CREATE TABLE source_allowed_targets (
 id text PRIMARY KEY,
 kind text NOT NULL CHECK (kind IN ('postgres','mysql','oracle')),
 host text NOT NULL CHECK (length(host) BETWEEN 1 AND 253),
 port integer NOT NULL CHECK (port BETWEEN 1 AND 65535),
 database text NOT NULL CHECK (length(database) BETWEEN 1 AND 128),
 created_by text NOT NULL REFERENCES users(id),
 created_at timestamptz NOT NULL DEFAULT now(),
 revoked_at timestamptz
);
CREATE UNIQUE INDEX source_allowed_targets_active ON source_allowed_targets(kind,host,port,database) WHERE revoked_at IS NULL;
GRANT SELECT,INSERT,UPDATE ON source_allowed_targets TO datamesh_app;
