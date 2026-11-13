DO $$ BEGIN
 IF EXISTS(SELECT 1 FROM governance_runs) THEN
  RAISE EXCEPTION 'governance execution evidence exists; preserve it before rollback';
 END IF;
END $$;
DROP TABLE governance_run_events;
DROP TABLE governance_runs;
