DO $$ BEGIN IF EXISTS(SELECT 1 FROM business_records WHERE kind='mount' AND state='active' AND body->>'service_variant'='true') THEN
 RAISE EXCEPTION 'Active service representations must be unmounted before downgrading'; END IF; END $$;
DROP INDEX active_mount_directory_idx;
CREATE UNIQUE INDEX active_mount_directory_idx ON business_records(parent_id)
 WHERE kind='mount' AND state='active' AND COALESCE(body->>'governance_variant','false')<>'true';
REVOKE SELECT ON platform_initializations FROM datamesh_app;
