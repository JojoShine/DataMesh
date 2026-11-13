DO $$ BEGIN
 IF EXISTS(SELECT 1 FROM governance_workspaces WHERE NOT is_legacy) THEN
  RAISE EXCEPTION 'Non-legacy governance workspaces exist; preserve their data and use a forward repair';
 END IF;
END $$;
DROP TRIGGER governance_legacy_dataset_workspace ON business_records;
DROP FUNCTION governance_legacy_dataset_workspace();
DROP TABLE governance_workspace_legacy_datasets;
DROP TABLE governance_workspace_sources;
ALTER TABLE governance_alert_rules DROP COLUMN workspace_id;
DROP TRIGGER governance_schedule_workspace_default ON governance_schedules;
DROP TRIGGER governance_run_workspace_default ON governance_runs;
DROP FUNCTION governance_task_workspace_default();
ALTER TABLE governance_schedules DROP COLUMN workspace_id;
ALTER TABLE governance_runs DROP COLUMN workspace_id;
DROP TRIGGER governance_definition_workspace_default ON governance_definitions;
DROP FUNCTION governance_definition_workspace_default();
ALTER TABLE governance_definitions DROP CONSTRAINT governance_definition_workspace_kind;
ALTER TABLE governance_definitions DROP COLUMN workspace_id;
DROP FUNCTION governance_legacy_workspace(text);
DROP TABLE governance_workspaces;
