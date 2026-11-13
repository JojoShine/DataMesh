#!/bin/sh
set -eu
case "$SOURCE_CDC_PASSWORD" in *[!0-9a-f]*|'') echo 'Invalid generated fixture secret' >&2; exit 1;; esac
[ "${#SOURCE_CDC_PASSWORD}" -eq 48 ] || { echo 'Invalid fixture secret length' >&2; exit 1; }
MYSQL_PWD="$MYSQL_ROOT_PASSWORD" mysql --default-character-set=utf8mb4 -uroot <<SQL
CREATE DATABASE IF NOT EXISTS sample CHARACTER SET utf8mb4;
CREATE USER 'datamesh_cdc'@'%' IDENTIFIED BY '${SOURCE_CDC_PASSWORD}';
GRANT RELOAD, SHOW DATABASES, REPLICATION SLAVE, REPLICATION CLIENT ON *.* TO 'datamesh_cdc'@'%';
GRANT SELECT, LOCK TABLES ON sample.* TO 'datamesh_cdc'@'%';
CREATE TABLE sample.customers (id bigint PRIMARY KEY, name varchar(100), amount decimal(20,6), updated_at timestamp(3) NOT NULL);
INSERT INTO sample.customers (id,name,amount,updated_at) VALUES (1,'样例客户1',1234.567890,'2026-10-15 00:00:00'),(2,NULL,0.000001,'2026-10-15 00:00:00'),(3,'删除样例',NULL,'2026-10-15 00:00:00');
SQL
