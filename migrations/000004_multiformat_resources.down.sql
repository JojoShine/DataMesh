-- Refuse rollback while new resource records exist; never silently delete resources.
ALTER TABLE business_records DROP CONSTRAINT business_records_kind_check;
ALTER TABLE business_records ADD CONSTRAINT business_records_kind_check CHECK (kind IN ('source','object','task','rule','dataset','application','grant','archive','rule_version','directory','directory_revision','mount','mount_revision','review','assignment','processing_authorization','directory_dictionary'));
DROP TABLE resource_import_rows;

CREATE UNIQUE INDEX business_local_source_kind_idx ON business_records((body->>'kind')) WHERE kind='source';
