DO $$ BEGIN
 IF EXISTS(SELECT 1 FROM governance_definitions WHERE kind='dataflow') OR EXISTS(SELECT 1 FROM governance_flow_rows) THEN
  RAISE EXCEPTION 'dataflow execution evidence exists; preserve it before rollback';
 END IF;
END $$;
ALTER TABLE governance_run_events DROP COLUMN duration_ms;
ALTER TABLE governance_run_events DROP COLUMN output_count;
ALTER TABLE governance_run_events DROP COLUMN input_count;
DROP TABLE governance_flow_rows;
ALTER TABLE governance_definitions DROP CONSTRAINT governance_definitions_kind_check;
ALTER TABLE governance_definitions ADD CONSTRAINT governance_definitions_kind_check CHECK(kind IN ('domain','term','standard','model','metadata'));
