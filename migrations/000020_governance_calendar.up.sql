ALTER TABLE governance_schedules ADD COLUMN cron text NOT NULL DEFAULT '' CHECK(length(cron)<=100);
ALTER TABLE governance_schedules DROP CONSTRAINT governance_schedules_interval_seconds_check;
ALTER TABLE governance_schedules ADD CONSTRAINT governance_schedules_timing_check CHECK(
 (cron='' AND interval_seconds BETWEEN 60 AND 31536000) OR (cron<>'' AND interval_seconds=0)
);
