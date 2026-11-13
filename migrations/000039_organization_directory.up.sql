CREATE TABLE organization_units (
 id text PRIMARY KEY,
 parent_id text REFERENCES organization_units(id),
 kind text NOT NULL CHECK (kind IN ('organization','department')),
 name text NOT NULL CHECK (length(btrim(name)) BETWEEN 1 AND 200),
 code text NOT NULL DEFAULT '' CHECK (length(code)<=128),
 active boolean NOT NULL DEFAULT true,
 version integer NOT NULL DEFAULT 1 CHECK(version>0),
 CHECK ((kind='organization' AND parent_id IS NULL) OR (kind='department' AND parent_id IS NOT NULL))
);
CREATE UNIQUE INDEX organization_unit_code_idx ON organization_units(code) WHERE code<>'';
CREATE INDEX organization_unit_parent_idx ON organization_units(parent_id);
-- Preserve historical ownership identifiers; real names and codes require administrator confirmation.
INSERT INTO organization_units(id,kind,name)
SELECT department_id,'organization',department_id FROM (
 SELECT department_id FROM users UNION SELECT department_id FROM business_records
) d WHERE department_id<>'' AND department_id<>'*';
ALTER TABLE users ADD COLUMN display_name text NOT NULL DEFAULT '' CHECK(length(display_name)<=200);
ALTER TABLE users ADD COLUMN version integer NOT NULL DEFAULT 1;
CREATE TABLE directory_code_counters (
 dictionary_id text PRIMARY KEY,
 last_value bigint NOT NULL CHECK(last_value>0)
);
CREATE TABLE directory_code_reservations (
 code text PRIMARY KEY,
 directory_id text NOT NULL,
 dictionary_id text NOT NULL
);
INSERT INTO directory_code_reservations(code,directory_id,dictionary_id)
SELECT DISTINCT ON (code) code,id,dictionary_id FROM (
 SELECT r.id,v.body->'definition'->>'resource_code' AS code,
 COALESCE(v.body->'definition'->>'dictionary_id','') AS dictionary_id
 FROM business_records r JOIN business_records v ON v.kind='directory_revision' AND v.parent_id=r.id
 WHERE r.kind='directory'
) codes WHERE code IS NOT NULL AND code<>'' ORDER BY code,id;
GRANT SELECT,INSERT,UPDATE ON organization_units,directory_code_counters,directory_code_reservations TO datamesh_app;
GRANT DELETE ON user_roles,user_platform_access TO datamesh_app;

CREATE TABLE identity_change_records (
 id bigint GENERATED ALWAYS AS IDENTITY PRIMARY KEY,
 actor_id text NOT NULL,
 subject_kind text NOT NULL,
 subject_id text NOT NULL,
 reason text NOT NULL,
 before_value jsonb NOT NULL,
 after_value jsonb NOT NULL,
 created_at timestamptz NOT NULL DEFAULT now()
);
CREATE INDEX identity_change_subject_idx ON identity_change_records(subject_kind,subject_id,created_at);
GRANT SELECT,INSERT ON identity_change_records TO datamesh_app;
GRANT USAGE,SELECT ON SEQUENCE identity_change_records_id_seq TO datamesh_app;
