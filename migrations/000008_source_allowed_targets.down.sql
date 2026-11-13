DO $$ BEGIN
 IF EXISTS(SELECT 1 FROM source_allowed_targets) THEN RAISE EXCEPTION 'Source allowlist history exists; preserve it before rollback'; END IF;
END $$;
DROP TABLE source_allowed_targets;
