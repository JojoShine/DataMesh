#!/bin/sh
set -eu
psql --username "$POSTGRES_USER" --dbname "$POSTGRES_DB" --set=ON_ERROR_STOP=1 --set=app_password="$APP_DB_PASSWORD" <<'SQL'
CREATE ROLE datamesh_app LOGIN PASSWORD :'app_password';
GRANT CONNECT ON DATABASE datamesh TO datamesh_app;
GRANT USAGE ON SCHEMA public TO datamesh_app;
SQL
