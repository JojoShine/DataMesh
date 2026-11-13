#!/bin/bash
set -euo pipefail
classpath='/build/classes:/build/tests:/opt/flink/lib/*'
java -cp "$classpath" datamesh.governance.StreamScriptFunctionTest
keytool -genkeypair -alias bridge -keyalg EC -dname CN=localhost -ext SAN=dns:localhost -validity 1 -storetype PKCS12 -keystore /tmp/bridge.p12 -storepass test-password -keypass test-password >/dev/null 2>&1
keytool -exportcert -rfc -alias bridge -keystore /tmp/bridge.p12 -storepass test-password -file /tmp/bridge.crt >/dev/null 2>&1
printf '%064d' 0 | tr '0' 'a' > /tmp/token
export GOVERNANCE_STREAM_SCRIPT_ORIGIN=https://127.0.0.1:18443
export GOVERNANCE_STREAM_SCRIPT_CA_FILE=/tmp/bridge.crt
export GOVERNANCE_STREAM_SCRIPT_TOKEN_FILE=/tmp/token
GOVERNANCE_STREAM_SCRIPT_TLS_NAME=localhost java -cp "$classpath" datamesh.governance.StreamScriptFunctionTest /tmp/bridge.p12
GOVERNANCE_STREAM_SCRIPT_TLS_NAME=wrong.invalid java -cp "$classpath" datamesh.governance.StreamScriptFunctionTest /tmp/bridge.p12 reject-peer
