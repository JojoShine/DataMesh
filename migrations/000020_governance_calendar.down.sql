DO $$ BEGIN
 IF EXISTS(SELECT 1 FROM governance_schedules WHERE cron<>'') THEN
  RAISE EXCEPTION 'Calendar schedule evidence exists; refusing destructive rollback';
 END IF;
END $$;
ALTER TABLE governance_schedules DROP CONSTRAINT governance_schedules_timing_check;
ALTER TABLE governance_schedules DROP COLUMN cron;
ALTER TABLE governance_schedules ADD CONSTRAINT governance_schedules_interval_seconds_check CHECK(interval_seconds BETWEEN 60 AND 31536000);
