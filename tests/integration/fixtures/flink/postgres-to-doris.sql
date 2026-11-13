SET 'execution.runtime-mode' = 'streaming';
SET 'execution.attached' = 'false';
SET 'table.local-time-zone' = 'UTC';
SET 'restart-strategy.type' = 'fixed-delay';
SET 'restart-strategy.fixed-delay.attempts' = '3';
SET 'restart-strategy.fixed-delay.delay' = '5 s';
CREATE TABLE source_customers (
  id BIGINT,
  name STRING,
  amount DECIMAL(20,6),
  PRIMARY KEY (id) NOT ENFORCED
) WITH (
  'connector' = 'kafka',
  'topic' = 'datamesh_postgres.public.customers',
  'properties.bootstrap.servers' = 'kafka:9092',
  'properties.group.id' = 'datamesh-flink-smoke',
  'scan.startup.mode' = 'earliest-offset',
  'format' = 'debezium-json',
  'debezium-json.schema-include' = 'false'
);
CREATE TABLE governed_customers (
  source_id STRING,
  object_id STRING,
  id BIGINT,
  name STRING,
  amount DECIMAL(20,6),
  PRIMARY KEY (source_id, object_id, id) NOT ENFORCED
) WITH (
  'connector' = 'doris',
  'fenodes' = 'doris-fe:8030',
  'table.identifier' = 'datamesh_smoke.customers',
  'username' = 'datamesh_writer',
  'password' = '${DORIS_WRITER_PASSWORD}',
  'sink.label-prefix' = '${LABEL_PREFIX}',
  'sink.enable-delete' = 'true',
  'sink.properties.format' = 'json',
  'sink.properties.read_json_by_line' = 'true'
);
INSERT INTO governed_customers
SELECT 'source-postgres-demo', 'public.customers', id, TRIM(name), amount
FROM source_customers;
