DO $$ BEGIN
 IF EXISTS(SELECT 1 FROM etl_events) OR EXISTS(SELECT 1 FROM etl_alerts) THEN
 RAISE EXCEPTION 'ETL operational history exists; export before rollback';
 END IF;
END $$;
DROP TABLE etl_alerts,etl_events,etl_run_controls;
