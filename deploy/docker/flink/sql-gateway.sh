#!/bin/bash
set -euo pipefail
# JVM options must be exported before Flink launches Java; -D CLI arguments
# configure the already running gateway and cannot set its heap.
export FLINK_ENV_JAVA_OPTS_SQL_GATEWAY="${FLINK_ENV_JAVA_OPTS_SQL_GATEWAY:-} -Xms128m -Xmx384m"
# Keep the connector available to the gateway planner and uploaded job classpath.
exec /docker-entrypoint.sh /opt/flink/bin/sql-gateway.sh start-foreground -Dsql-gateway.endpoint.rest.address=0.0.0.0 -Dsql-gateway.endpoint.rest.port=8083 '-Dsql-gateway.session.idle-timeout=5 min' '-Dsql-gateway.session.check-interval=30 s' -Dsql-gateway.session.max-num=32
