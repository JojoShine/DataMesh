ALTER TABLE source_allowed_targets DROP CONSTRAINT source_allowed_targets_kind_check;
ALTER TABLE source_allowed_targets ADD CONSTRAINT source_allowed_targets_kind_check CHECK (kind IN ('postgres','mysql','oracle','sqlserver','dm','kingbase','highgo','vastbase','doris','clickhouse','mongodb','elasticsearch','redis','hbase'));
