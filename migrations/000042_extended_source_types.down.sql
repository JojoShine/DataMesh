DO $$ BEGIN
IF EXISTS(SELECT 1 FROM source_allowed_targets WHERE kind NOT IN ('postgres','mysql','oracle')) OR EXISTS(SELECT 1 FROM business_records WHERE kind='source' AND body->>'kind' NOT IN ('postgres','mysql','oracle')) THEN
 RAISE EXCEPTION 'Extended data sources exist; preserve records and remove them through normal administration before downgrade';
END IF;
END $$;
ALTER TABLE source_allowed_targets DROP CONSTRAINT source_allowed_targets_kind_check;
ALTER TABLE source_allowed_targets ADD CONSTRAINT source_allowed_targets_kind_check CHECK (kind IN ('postgres','mysql','oracle'));
