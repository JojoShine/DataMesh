DO $$ BEGIN
 IF EXISTS(SELECT 1 FROM governance_model_deployments) THEN
  RAISE EXCEPTION 'Physical model deployment evidence exists; refusing destructive rollback';
 END IF;
END $$;
DROP TABLE governance_model_deployments;
