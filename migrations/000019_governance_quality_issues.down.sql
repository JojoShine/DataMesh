DO $$ BEGIN
 IF EXISTS(SELECT 1 FROM governance_quality_issues) THEN
  RAISE EXCEPTION 'quality remediation evidence exists; preserve it before rollback';
 END IF;
END $$;
DROP TABLE governance_quality_issue_events;
DROP TABLE governance_quality_issues;
