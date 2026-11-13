CREATE TABLE data_service_keys (
 id text PRIMARY KEY,
 service_id text NOT NULL,
 service_kind text NOT NULL DEFAULT 'api_resource' CHECK(service_kind='api_resource'),
 user_id text NOT NULL REFERENCES users(id),
 department_id text NOT NULL,
 grant_id text NOT NULL,
 secret_hash text NOT NULL UNIQUE,
 expires_at timestamptz NOT NULL,
 revoked boolean NOT NULL DEFAULT false,
 created_at timestamptz NOT NULL DEFAULT now(),
 FOREIGN KEY(service_kind,service_id) REFERENCES business_records(kind,id)
);
CREATE INDEX data_service_key_owner ON data_service_keys(user_id,service_id,created_at);
GRANT SELECT,INSERT,UPDATE ON data_service_keys TO datamesh_app;
