DO $$ BEGIN
 IF EXISTS(SELECT 1 FROM governance_snapshot_purges) OR EXISTS(SELECT 1 FROM governance_migration_plans) OR EXISTS(SELECT 1 FROM governance_retention_policies) OR EXISTS(SELECT 1 FROM governance_alerts) OR EXISTS(SELECT 1 FROM governance_alert_rules) THEN
 RAISE EXCEPTION 'Governance operations evidence exists; refusing destructive rollback';
 END IF;
END $$;
DROP TABLE governance_migration_plans,governance_snapshot_purges,governance_retention_policies,governance_alerts,governance_alert_rules;
