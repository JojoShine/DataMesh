ALTER TABLE governance_runs DROP CONSTRAINT governance_runs_kind_check;
ALTER TABLE governance_runs ADD CONSTRAINT governance_runs_kind_check CHECK (kind IN ('profile','dataflow','workflow','dataflow_preview'));
ALTER TABLE governance_run_events ADD COLUMN error_line integer NOT NULL DEFAULT 0, ADD COLUMN error_column integer NOT NULL DEFAULT 0, ADD COLUMN error_field text NOT NULL DEFAULT '';
