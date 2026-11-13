CREATE TABLE users (
    id text PRIMARY KEY,
    username text NOT NULL UNIQUE,
    password_hash text NOT NULL,
    department_id text NOT NULL,
    active boolean NOT NULL DEFAULT true,
    created_at timestamptz NOT NULL DEFAULT now(),
    CHECK (username ~ '^[a-z][a-z0-9_.-]{2,63}$'),
    CHECK (length(department_id) BETWEEN 1 AND 128)
);
CREATE TABLE user_platform_access (
    user_id text NOT NULL REFERENCES users(id),
    platform text NOT NULL CHECK (platform IN ('exchange', 'governance', 'assets', 'security')),
    PRIMARY KEY (user_id, platform)
);
CREATE TABLE sessions (
    token_hash text PRIMARY KEY,
    user_id text NOT NULL REFERENCES users(id),
    created_at timestamptz NOT NULL DEFAULT now(),
    expires_at timestamptz NOT NULL,
    last_seen_at timestamptz NOT NULL DEFAULT now()
);
CREATE INDEX sessions_user_id_idx ON sessions(user_id);
CREATE INDEX sessions_expiry_idx ON sessions(expires_at);
CREATE TABLE audit_records (
    id bigint GENERATED ALWAYS AS IDENTITY PRIMARY KEY,
    actor_id text,
    action text NOT NULL,
    object_id text NOT NULL,
    result text NOT NULL,
    trace_id text NOT NULL,
    created_at timestamptz NOT NULL DEFAULT now()
);
CREATE INDEX audit_records_created_at_idx ON audit_records(created_at);
GRANT SELECT, INSERT, UPDATE ON users, user_platform_access, sessions TO datamesh_app;
GRANT SELECT, INSERT ON audit_records TO datamesh_app;
GRANT USAGE, SELECT ON SEQUENCE audit_records_id_seq TO datamesh_app;
GRANT DELETE ON sessions TO datamesh_app;
