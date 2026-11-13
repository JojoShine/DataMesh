DO $$ BEGIN
 IF EXISTS(SELECT 1 FROM business_records WHERE kind='mount' AND body->>'governance_variant'='true') THEN
  RAISE EXCEPTION 'Governance catalog variants exist; preserve their sharing evidence and use a forward repair';
 END IF;
END $$;
DROP INDEX active_mount_directory_idx;
CREATE UNIQUE INDEX active_mount_directory_idx ON business_records (parent_id) WHERE kind='mount' AND state='active';
