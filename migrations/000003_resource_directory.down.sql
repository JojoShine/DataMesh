DO $$ BEGIN
 IF EXISTS (SELECT 1 FROM business_records WHERE kind IN ('directory','directory_revision','mount','mount_revision','review','assignment','processing_authorization','directory_dictionary')) THEN
  RAISE EXCEPTION 'Resource directory records exist; export and migrate them before rollback';
 END IF;
END $$;
DROP INDEX assignment_actor_idx;
DROP INDEX pending_review_idx;
DROP INDEX active_mount_resource_idx;
DROP INDEX active_mount_directory_idx;
DROP INDEX directory_code_idx;
ALTER TABLE business_records DROP CONSTRAINT business_records_kind_check;
ALTER TABLE business_records ADD CONSTRAINT business_records_kind_check CHECK (kind IN ('source','object','task','rule','dataset','application','grant','archive','rule_version'));
