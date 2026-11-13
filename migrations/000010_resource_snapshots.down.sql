DO $$ BEGIN
 IF EXISTS(SELECT 1 FROM governed_checks) OR EXISTS(SELECT 1 FROM resource_snapshots) OR EXISTS(SELECT 1 FROM archive_objects) THEN
  RAISE EXCEPTION 'Snapshot or archive objects exist; preserve their evidence before rollback';
 END IF;
END $$;
DROP TABLE governed_checks;
DROP TABLE archive_objects;
DROP TABLE resource_snapshot_refs;
DROP TABLE resource_snapshot_rows;
DROP TABLE resource_snapshots;
