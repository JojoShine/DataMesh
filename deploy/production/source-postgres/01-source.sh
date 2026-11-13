#!/bin/sh
set -eu
psql --username source_owner --dbname sample --set=ON_ERROR_STOP=1 <<'SQL'
\getenv cdc_password SOURCE_CDC_PASSWORD
CREATE ROLE datamesh_cdc LOGIN REPLICATION PASSWORD :'cdc_password';
GRANT CONNECT ON DATABASE sample TO datamesh_cdc;
GRANT USAGE ON SCHEMA public TO datamesh_cdc;
CREATE TABLE customers (id bigint PRIMARY KEY, name varchar(100), amount numeric(20,6), updated_at timestamp(3) NOT NULL);
ALTER TABLE customers REPLICA IDENTITY FULL;
INSERT INTO customers SELECT i, '样例客户' || i, 1234.567890, timestamp '2026-10-15 00:00:00' FROM generate_series(1,1000) AS i;
GRANT SELECT ON customers TO datamesh_cdc;
CREATE PUBLICATION datamesh_publication FOR TABLE customers;
SQL
