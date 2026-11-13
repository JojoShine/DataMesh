DO $$ BEGIN
 IF EXISTS(SELECT 1 FROM governance_runs WHERE (kind='workflow' AND definition ? 'trial') OR definition ? 'workflow_trial') OR EXISTS(SELECT 1 FROM governance_quality_issues WHERE origin_kind<>'dataflow') OR EXISTS(SELECT 1 FROM governance_issue_notices) OR EXISTS(SELECT 1 FROM governance_workspaces WHERE required_release_kinds<>'[]'::jsonb) OR EXISTS(SELECT 1 FROM business_records WHERE kind='master_snapshot') THEN RAISE EXCEPTION 'Preserve governance closure records before downgrade'; END IF;
END $$;
DROP TABLE governance_issue_notices;
ALTER TABLE governance_quality_issues DROP CONSTRAINT governance_issue_origin,DROP COLUMN origin_kind,DROP COLUMN evidence,DROP COLUMN monitor_id,DROP COLUMN observation_id;
ALTER TABLE governance_quality_issues ALTER COLUMN flow_id SET NOT NULL,ALTER COLUMN flow_version SET NOT NULL;
ALTER TABLE governance_workspaces DROP COLUMN required_release_kinds;
ALTER TABLE business_records DROP CONSTRAINT business_records_kind_check;
ALTER TABLE business_records ADD CONSTRAINT business_records_kind_check CHECK(kind IN ('source','object','task','rule','dataset','application','grant','archive','rule_version','directory','directory_revision','mount','mount_revision','review','assignment','processing_authorization','directory_dictionary','table_resource','file_resource','api_resource','resource_import','resource_upload','etl_task','etl_run','lightweight_upload','lightweight_unit','governance_release','scenario_instance'));
