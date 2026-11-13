DO $$ BEGIN
 IF EXISTS(SELECT 1 FROM governance_quality_issues WHERE state='superseded' OR replacement_run_id IS NOT NULL OR replacement_issue_id IS NOT NULL) THEN
  RAISE EXCEPTION '替代记录属于不可丢弃的治理证据，请保留46版结构并回滚应用或从升级前备份恢复';
 END IF;
END $$;
DROP INDEX governance_issue_replacement_run;
ALTER TABLE governance_quality_issues DROP CONSTRAINT governance_issue_replacement,
 DROP COLUMN replacement_run_id, DROP COLUMN replacement_issue_id;
ALTER TABLE governance_quality_issues DROP CONSTRAINT governance_quality_issues_state_check;
ALTER TABLE governance_quality_issues ADD CONSTRAINT governance_quality_issues_state_check CHECK(state IN ('open','in_progress','retesting','closed'));
