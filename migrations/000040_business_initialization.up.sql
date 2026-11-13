-- Durable initialization progress travels with platform backups; business writes still use audited APIs.
CREATE TABLE platform_initializations (
 name text PRIMARY KEY,
 version text NOT NULL,
 state jsonb NOT NULL,
 updated_at timestamptz NOT NULL DEFAULT now()
);
