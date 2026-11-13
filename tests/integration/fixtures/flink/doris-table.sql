CREATE TABLE IF NOT EXISTS datamesh_smoke.customers (
  source_id VARCHAR(64) NOT NULL,
  object_id VARCHAR(128) NOT NULL,
  id BIGINT NOT NULL,
  name VARCHAR(100) NULL,
  amount DECIMAL(20,6) NULL
)
UNIQUE KEY(source_id, object_id, id)
DISTRIBUTED BY HASH(id) BUCKETS 1
PROPERTIES ('replication_num'='1', 'enable_unique_key_merge_on_write'='true');
