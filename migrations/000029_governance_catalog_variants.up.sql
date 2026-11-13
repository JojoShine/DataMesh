DROP INDEX active_mount_directory_idx;
CREATE UNIQUE INDEX active_mount_directory_idx ON business_records (parent_id)
 WHERE kind='mount' AND state='active' AND COALESCE(body->>'governance_variant','false')<>'true';
