ALTER TABLE business_records DROP CONSTRAINT business_records_kind_check;
ALTER TABLE business_records ADD CONSTRAINT business_records_kind_check CHECK (kind IN ('source','object','task','rule','dataset','application','grant','archive','rule_version','directory','directory_revision','mount','mount_revision','review','assignment','processing_authorization','directory_dictionary','table_resource','file_resource','api_resource','resource_import','resource_upload'));
CREATE TABLE resource_import_rows (batch_id text NOT NULL,row_number integer NOT NULL,data jsonb NOT NULL,PRIMARY KEY(batch_id,row_number));
GRANT SELECT,INSERT,DELETE ON resource_import_rows TO datamesh_app;

-- Multiple read-only table connections may use the same database driver.
DROP INDEX business_local_source_kind_idx;
