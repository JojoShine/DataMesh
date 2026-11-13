DO $$ BEGIN
 IF EXISTS(SELECT 1 FROM metadata_observations WHERE resource_type<>'raw_object') THEN
  RAISE EXCEPTION 'metadata resource evidence exists; preserve it before rollback';
 END IF;
END $$;
ALTER TABLE metadata_observations DROP COLUMN observation_kind;
ALTER TABLE metadata_observations DROP COLUMN resource_type;
