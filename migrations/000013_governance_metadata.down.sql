DO $$ BEGIN
 IF EXISTS(SELECT 1 FROM metadata_observations) OR EXISTS(SELECT 1 FROM governance_definitions WHERE kind='metadata') THEN
  RAISE EXCEPTION 'Metadata evidence exists; export and migrate it before rollback';
 END IF;
END $$;
DROP TABLE metadata_observations;
ALTER TABLE governance_definitions DROP CONSTRAINT governance_definitions_kind_check;
ALTER TABLE governance_definitions ADD CONSTRAINT governance_definitions_kind_check CHECK(kind IN ('domain','term','standard','model'));
