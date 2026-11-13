DROP TABLE identity_change_records;
REVOKE DELETE ON user_roles,user_platform_access FROM datamesh_app;
DROP TABLE directory_code_reservations;
DROP TABLE directory_code_counters;
ALTER TABLE users DROP COLUMN version;
ALTER TABLE users DROP COLUMN display_name;
DROP TABLE organization_units;
