ALTER TABLE governance_quality_issues ALTER COLUMN flow_id DROP NOT NULL, ALTER COLUMN flow_version DROP NOT NULL;
ALTER TABLE governance_quality_issues ADD COLUMN origin_kind text NOT NULL DEFAULT 'dataflow' CHECK(origin_kind IN ('dataflow','monitor','standard')),
 ADD COLUMN evidence jsonb NOT NULL DEFAULT '{}', ADD COLUMN monitor_id text REFERENCES governance_monitors(id), ADD COLUMN observation_id text REFERENCES governance_monitor_observations(id);
ALTER TABLE governance_quality_issues ADD CONSTRAINT governance_issue_origin CHECK((origin_kind='dataflow' AND flow_id IS NOT NULL AND flow_version IS NOT NULL AND monitor_id IS NULL AND observation_id IS NULL) OR (origin_kind='standard' AND flow_id IS NULL AND flow_version IS NULL AND monitor_id IS NULL AND observation_id IS NULL) OR (origin_kind='monitor' AND flow_id IS NULL AND flow_version IS NULL AND monitor_id IS NOT NULL AND observation_id IS NOT NULL));
CREATE TABLE governance_issue_notices (
 id text PRIMARY KEY, issue_id text NOT NULL REFERENCES governance_quality_issues(id), user_id text NOT NULL REFERENCES users(id), code text NOT NULL,
 issue_version integer NOT NULL, created_at timestamptz NOT NULL DEFAULT now(), seen_at timestamptz,
 UNIQUE(issue_id,user_id,issue_version,code)
);
CREATE INDEX governance_issue_notices_user ON governance_issue_notices(user_id,created_at DESC,id);
GRANT SELECT,INSERT,UPDATE ON governance_issue_notices TO datamesh_app;
ALTER TABLE governance_workspaces ADD COLUMN required_release_kinds jsonb NOT NULL DEFAULT '[]' CHECK(jsonb_typeof(required_release_kinds)='array');
ALTER TABLE business_records DROP CONSTRAINT business_records_kind_check;
ALTER TABLE business_records ADD CONSTRAINT business_records_kind_check CHECK(kind IN ('source','object','task','rule','dataset','application','grant','archive','rule_version','directory','directory_revision','mount','mount_revision','review','assignment','processing_authorization','directory_dictionary','table_resource','file_resource','api_resource','resource_import','resource_upload','etl_task','etl_run','lightweight_upload','lightweight_unit','governance_release','scenario_instance','master_snapshot'));
