DO $$ BEGIN
 IF EXISTS(SELECT 1 FROM governance_versions) THEN
  RAISE EXCEPTION 'Governance definition evidence exists; export and migrate it before rollback';
 END IF;
END $$;
DROP TABLE governance_versions;
DROP TABLE governance_definitions;
