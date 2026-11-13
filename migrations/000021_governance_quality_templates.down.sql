DO $$ BEGIN
 IF EXISTS(SELECT 1 FROM governance_definitions WHERE kind='quality_template') THEN
  RAISE EXCEPTION 'Quality template versions exist; refusing destructive rollback';
 END IF;
END $$;
ALTER TABLE governance_definitions DROP CONSTRAINT governance_definitions_kind_check;
ALTER TABLE governance_definitions ADD CONSTRAINT governance_definitions_kind_check CHECK(kind IN ('domain','term','standard','model','metadata','dataflow','workflow'));
