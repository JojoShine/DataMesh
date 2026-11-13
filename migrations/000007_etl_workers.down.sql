DO $$ BEGIN
 IF EXISTS(SELECT 1 FROM etl_run_controls WHERE active=true) OR EXISTS(SELECT 1 FROM etl_target_locks) THEN RAISE EXCEPTION 'ETL workers or unresolved target locks exist'; END IF;
END $$;
DROP TABLE etl_target_locks;
DROP INDEX business_etl_queue;
DROP INDEX etl_control_leases;
ALTER TABLE etl_run_controls DROP COLUMN lease_owner,DROP COLUMN lease_expires_at,DROP COLUMN target_key;
