#!/bin/bash
set -euo pipefail
[[ "$DORIS_READER_PASSWORD" =~ ^[0-9a-f]{48}$ ]] || { echo 'Invalid generated reader secret' >&2; exit 1; }
[[ "$DORIS_ROOT_PASSWORD" =~ ^[0-9a-f]{48}$ && "$DORIS_WRITER_PASSWORD" =~ ^[0-9a-f]{48}$ ]] || { echo 'Invalid generated fixture secret' >&2; exit 1; }
conf=/opt/apache-doris/fe/conf/fe.conf
sed -i 's/-Xmx8192m/-Xmx512m/g; s/-Xms8192m/-Xms512m/g' "$conf"
# The upstream image can allow root on loopback without checking its password.
# Require real authentication so readiness cannot skip credential initialization.
# Preserve the persisted FE address when the container also joins the app network.
sed -i '/^priority_networks[[:space:]]*=/d' "$conf"
priority_networks="${DORIS_PRIORITY_NETWORKS:-172.29.238.0/24}"
[[ "$priority_networks" =~ ^([0-9]{1,3}\.){3}[0-9]{1,3}/24$ ]] || { echo 'Invalid Doris priority subnet' >&2; exit 1; }
IFS=./ read -r octet_a octet_b octet_c octet_d prefix <<< "$priority_networks"
for octet in "$octet_a" "$octet_b" "$octet_c" "$octet_d"; do
  ((10#$octet <= 255)) || { echo 'Invalid Doris priority subnet' >&2; exit 1; }
done
((10#$octet_d == 0)) || { echo 'Doris priority subnet must be a network address' >&2; exit 1; }
printf '\npriority_networks = %s\n' "$priority_networks" >> "$conf"
sed -i '/^skip_localhost_auth_check[[:space:]]*=/d' "$conf"
printf '\nskip_localhost_auth_check = false\n' >> "$conf"
IFS=: read -r _ address _ <<< "${FE_SERVERS%%,*}"
bash /usr/local/bin/init_fe.sh &
child=$!
trap 'kill -TERM "$child" 2>/dev/null || true; wait "$child" || true' TERM INT
ready=false
for ((attempt=0; attempt<120; attempt++)); do
  if MYSQL_PWD="$DORIS_ROOT_PASSWORD" mysql -h"$address" -P9030 -uroot -e 'SHOW FRONTENDS' >/dev/null 2>&1; then
    ready=true
    break
  fi
  if MYSQL_PWD='' mysql -h"$address" -P9030 -uroot -e 'SHOW FRONTENDS' >/dev/null 2>&1; then
    if ! MYSQL_PWD='' mysql -h"$address" -P9030 -uroot >/dev/null 2>&1 <<SQL
SET PASSWORD = PASSWORD('${DORIS_ROOT_PASSWORD}');
SQL
    then
      echo 'Doris credential initialization failed' >&2
      kill -TERM "$child"
      exit 1
    fi
  fi
  kill -0 "$child" 2>/dev/null || { wait "$child"; exit 1; }
  sleep 1
done
if [ "$ready" != true ]; then
  echo 'Doris FE did not become ready' >&2
  kill -TERM "$child"
  exit 1
fi
if ! MYSQL_PWD="$DORIS_ROOT_PASSWORD" mysql -h"$address" -P9030 -uroot >/dev/null 2>&1 <<SQL
CREATE DATABASE IF NOT EXISTS datamesh_smoke;
SET PASSWORD FOR 'admin' = PASSWORD('${DORIS_ROOT_PASSWORD}');
CREATE USER IF NOT EXISTS 'datamesh_writer' IDENTIFIED BY '${DORIS_WRITER_PASSWORD}';
GRANT SELECT_PRIV, LOAD_PRIV ON datamesh_smoke.* TO 'datamesh_writer';
CREATE USER IF NOT EXISTS 'datamesh_reader' IDENTIFIED BY '${DORIS_READER_PASSWORD}';
SQL
then
  echo 'Doris smoke user initialization failed' >&2
  kill -TERM "$child"
  exit 1
fi
# Governance namespaces are explicit; reader cannot inspect ODS, staging or quarantine.
for layer in ods dwd dim dws ads internal quality; do
  MYSQL_PWD="$DORIS_ROOT_PASSWORD" mysql -h"$address" -P9030 -uroot >/dev/null <<SQL
CREATE DATABASE IF NOT EXISTS datamesh_${layer};
GRANT SELECT_PRIV, LOAD_PRIV, CREATE_PRIV, DROP_PRIV, ALTER_PRIV ON datamesh_${layer}.* TO 'datamesh_writer';
SQL
  case "$layer" in
    dwd|dim|dws|ads) MYSQL_PWD="$DORIS_ROOT_PASSWORD" mysql -h"$address" -P9030 -uroot -e "GRANT SELECT_PRIV ON datamesh_${layer}.* TO 'datamesh_reader'" >/dev/null ;;
  esac
done
# The production sandbox uses its own namespace and narrower service identities.
if [[ -n "${SANDBOX_DORIS_READER_PASSWORD:-}${SANDBOX_DORIS_WRITER_PASSWORD:-}" ]]; then
  [[ "${SANDBOX_DORIS_READER_PASSWORD:-}" =~ ^[0-9a-f]{48}$ && "${SANDBOX_DORIS_WRITER_PASSWORD:-}" =~ ^[0-9a-f]{48}$ ]] || { echo 'Invalid sandbox generated credentials' >&2; exit 1; }
  MYSQL_PWD="$DORIS_ROOT_PASSWORD" mysql -h"$address" -P9030 -uroot >/dev/null <<SQL
CREATE DATABASE IF NOT EXISTS datamesh_sandbox;
CREATE USER IF NOT EXISTS 'datamesh_sandbox_reader' IDENTIFIED BY '${SANDBOX_DORIS_READER_PASSWORD}';
CREATE USER IF NOT EXISTS 'datamesh_sandbox_writer' IDENTIFIED BY '${SANDBOX_DORIS_WRITER_PASSWORD}';
GRANT SELECT_PRIV ON datamesh_sandbox.* TO 'datamesh_sandbox_reader';
GRANT SELECT_PRIV, LOAD_PRIV, CREATE_PRIV, DROP_PRIV ON datamesh_sandbox.* TO 'datamesh_sandbox_writer';
SET PROPERTY FOR 'datamesh_sandbox_reader' 'max_user_connections'='4';
SET PROPERTY FOR 'datamesh_sandbox_writer' 'max_user_connections'='4';
SQL
fi
# Doris requires a table to exist before granting table-level privileges.
# A fresh environment creates the fixture table in processing.py after FE is ready.
# Metadata-only lookup: BE starts after FE readiness and may still be offline.
fixture_table=$(MYSQL_PWD="$DORIS_ROOT_PASSWORD" mysql -h"$address" -P9030 -uroot -N -B -e "SHOW TABLES FROM datamesh_smoke LIKE 'customers'")
if [ "$fixture_table" = "customers" ]; then
  MYSQL_PWD="$DORIS_ROOT_PASSWORD" mysql -h"$address" -P9030 -uroot -e "GRANT SELECT_PRIV ON datamesh_smoke.customers TO 'datamesh_reader'" >/dev/null
fi
wait "$child"
