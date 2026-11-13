DO $$ BEGIN
 IF EXISTS(SELECT 1 FROM cdc_bindings) THEN RAISE EXCEPTION 'CDC tasks exist; preserve their bindings and offsets before rollback'; END IF;
END $$;
DROP TABLE cdc_snapshot_progress;
DROP TABLE cdc_controls;
DROP TABLE cdc_bindings;
DROP INDEX business_task_binding_idx;
CREATE UNIQUE INDEX business_task_binding_idx ON business_records((body->>'binding')) WHERE kind='task';
