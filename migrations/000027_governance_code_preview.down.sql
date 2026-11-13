DO $$ BEGIN IF EXISTS (SELECT 1 FROM governance_runs WHERE kind='dataflow_preview') THEN RAISE EXCEPTION 'Preview history exists; preserve history before reverting'; END IF; END $$;
ALTER TABLE governance_run_events DROP COLUMN error_line, DROP COLUMN error_column, DROP COLUMN error_field;
ALTER TABLE governance_runs DROP CONSTRAINT governance_runs_kind_check;
ALTER TABLE governance_runs ADD CONSTRAINT governance_runs_kind_check CHECK (kind IN ('profile','dataflow','workflow'));
