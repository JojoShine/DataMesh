ALTER TABLE business_records DROP CONSTRAINT business_records_kind_check;
ALTER TABLE business_records ADD CONSTRAINT business_records_kind_check CHECK (kind IN ('source','object','task','rule','dataset','application','grant','archive','rule_version','directory','directory_revision','mount','mount_revision','review','assignment','processing_authorization','directory_dictionary'));
CREATE UNIQUE INDEX directory_code_idx ON business_records ((body->>'resource_code')) WHERE kind='directory' AND body->>'resource_code' <> '';
CREATE UNIQUE INDEX active_mount_directory_idx ON business_records (parent_id) WHERE kind='mount' AND state='active';
CREATE UNIQUE INDEX active_mount_resource_idx ON business_records ((body->>'resource_key')) WHERE kind='mount' AND state='active';
CREATE UNIQUE INDEX pending_review_idx ON business_records (parent_id,(body->>'stage')) WHERE kind='review' AND state='pending';
CREATE INDEX assignment_actor_idx ON business_records ((body->>'user_id'),department_id) WHERE kind='assignment' AND state='active';
