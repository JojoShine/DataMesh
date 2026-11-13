ALTER TABLE governance_quality_issues DROP CONSTRAINT governance_quality_issues_state_check;
ALTER TABLE governance_quality_issues ADD CONSTRAINT governance_quality_issues_state_check CHECK(state IN ('open','in_progress','retesting','closed','superseded'));
ALTER TABLE governance_quality_issues ADD COLUMN replacement_run_id text REFERENCES governance_runs(id),
 ADD COLUMN replacement_issue_id text REFERENCES governance_quality_issues(id);
ALTER TABLE governance_quality_issues ADD CONSTRAINT governance_issue_replacement CHECK(
 (state='superseded' AND replacement_run_id IS NOT NULL AND replacement_run_id<>run_id AND closed_at IS NOT NULL AND (replacement_issue_id IS NULL OR replacement_issue_id<>id))
 OR (state<>'superseded' AND replacement_run_id IS NULL AND replacement_issue_id IS NULL));
CREATE INDEX governance_issue_replacement_run ON governance_quality_issues(replacement_run_id) WHERE replacement_run_id IS NOT NULL;
