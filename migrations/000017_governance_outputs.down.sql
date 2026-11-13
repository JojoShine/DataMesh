DO $$ BEGIN
 IF EXISTS(SELECT 1 FROM governance_outputs) THEN
  RAISE EXCEPTION 'governance output evidence exists; preserve it before rollback';
 END IF;
END $$;
DROP TABLE governance_outputs;
