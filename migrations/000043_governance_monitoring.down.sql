DO $$ BEGIN IF EXISTS(SELECT 1 FROM governance_monitors) THEN RAISE EXCEPTION 'Monitoring records exist; preserve them before downgrade';END IF;END $$;
ALTER TABLE governance_monitors DROP CONSTRAINT monitor_baseline_ref;
ALTER TABLE governance_monitors DROP CONSTRAINT monitor_observation_ref;
DROP TABLE governance_monitor_observations;
DROP TABLE governance_monitors;
