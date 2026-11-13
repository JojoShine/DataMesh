DO $$ BEGIN
 IF EXISTS(SELECT 1 FROM governance_schedules WHERE target_kind='dataflow') THEN
  RAISE EXCEPTION 'Direct dataflow schedules must be migrated before rollback';
 END IF;
END $$;
ALTER TABLE governance_schedules DROP COLUMN target_kind;
ALTER TABLE governance_versions DROP COLUMN execution_mode;
