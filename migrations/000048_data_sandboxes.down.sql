DO $$ BEGIN
 IF EXISTS(SELECT 1 FROM data_sandboxes) OR EXISTS(SELECT 1 FROM data_sandbox_tables) OR EXISTS(SELECT 1 FROM data_sandbox_queries) OR EXISTS(SELECT 1 FROM data_sandbox_objects) THEN
  RAISE EXCEPTION 'refusing to remove populated data sandbox metadata';
 END IF;
END $$;
ALTER TABLE data_sandbox_tables DROP CONSTRAINT data_sandbox_table_query_fk;
DROP TABLE data_sandbox_worker_lock;
DROP TABLE data_sandbox_objects;
DROP TABLE data_sandbox_queries;
DROP TABLE data_sandbox_tables;
DROP TABLE data_sandboxes;
