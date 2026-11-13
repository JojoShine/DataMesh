# Continuous CDC and sandbox script completion

User authorization: complete both real streaming acceptance and Python/JavaScript continuous script capability. Existing AGENTS asks us to choose routine implementation details and proceed without approval questions. Work remains in the existing unborn `codex/resource-directory` checkout; no commits/worktree manipulation or user changes discarded.

## Approach

Reuse the existing CDC → Kafka → Flink → transactional Kafka → controlled Doris materialization chain. Extend Flink through a small Java17 scalar adapter that calls a private authenticated platform endpoint; actual user code remains in the existing networkless, nonroot sandbox. Do not execute user scripts inside the trusted Flink JVM or silently replace streaming with batch runs.

A fresh one-row sandbox on each unseen input gives explicitly row-local semantics: interpreter globals do not form stream state. Durably memoize normalized output by frozen stream/node/logical row key/canonical input hash. Keep memoization across generations/savepoint restore, so nondeterministic output remains identical on retractions, old-value replay and lost responses. Use encrypted immutable objects in existing RustFS, not business rows in the PostgreSQL metadata store. Conditional create (`If-None-Match: *`) chooses a single durable winner; all responses read that winner. The storage SDK may retry exactly the same encrypted candidate with that create-only condition after a lost response: this is idempotent and cannot overwrite the winner or rerun code. An acknowledged write or conditional conflict resolves only through an authoritative GET; a final unresolved transport failure returns 503 and the next request starts with GET. No cache TTL while savepoints/paused streams remain usable. Fail closed on store errors/tampering; no empty/null substitutes for execution failure.

Alternatives rejected: direct uncached scalar calls break retractions and replay; Kafka feedback/multiple job segmentation adds drain/checkpoint coordination and capacity without fitting the existing lifecycle. This choice prioritizes correct bounded execution over throughput; measure cold sandbox cost honestly.

## Private wire contract

- POST `/internal/governance/stream-script`, JSON: `{stream_id, generation, node_id, row_key, payload}`. `payload` is the exact declared input-field object. Caller cannot supply code, runtime, parameters, actor, or output schema.
- `X-Datamesh-Script-Time`: Unix seconds, ±60 seconds. `X-Datamesh-Script-Signature`: hex HMAC-SHA256 over `time + "\nPOST\n/internal/governance/stream-script\n" + hex(SHA256(raw request body))`.
- Token: random private ASCII64 hex in `GOVERNANCE_STREAM_SCRIPT_TOKEN_FILE`, separate from runner token. Never pass secrets in SQL, data payload, diagnostics or sandbox mounts.
- Success: normalized output row as raw JSON, explicit Content-Length; failure: existing sanitized problem format, also explicit Content-Length. No redirects. Request/response bounded (one 256KiB row plus metadata).
- Adapter configured by `GOVERNANCE_STREAM_SCRIPT_ORIGIN=https://server:8443`, CA file, TLS peer name `localhost` and token file. Raw TLS socket connects only to configured server, verifies the configured peer name and pinned CA; no verification bypass.
- Verify active generation/state and frozen owner authorization BEFORE cache access and AFTER execution/cache loading. Cancel in-flight execution when desired stop/revocation/state change is observed. Use pinned runtime, script, parameters and strict normalization already implemented for batch nodes.
- UDF signature `eval(String streamID, Integer generation, String nodeID, String rowKey, String payload) -> String`. Compiler retains `_dm_key`; projects typed output through existing casts. Stream ID/generation derived from validated job name; compiler tests may use non-executable plan placeholders.

## Real acceptance

Cover actual source changes after running (insert/update/delete), quality invalid→valid, capture pause/resume, governed savepoint pause/resume, immutable snapshot, application worker restart, checkpoint recovery, one active job/no duplicate data, scope/CSRF/authorization revocation, safe termination. Add Python→JavaScript nodes in real streamed graph; test stable nondeterminism, unchanged input replay, updates/retractions/delete, errors, timeout/cancel, exact numeric/null/Unicode values, forged adapter requests, revoked access even on cache hit. Keep production metadata untouched, use isolated test data and owned topics/connectors/slots/jobs.

## Resources

Measured Docker VM7.748GiB, non-experience containers~4.29GiB. Existing lightweight code+complete stream service limits6.125GiB plus1GiB reserve =>~11.42GiB required. No full Doris clone. Existing8GiB cannot safely fit full live suite while retaining all other services; runtime acceptance needs a user-approved resource arrangement. Code/unit/bridge/JAR tests continue independently.

## Isolated Connect startup

The pinned Debezium 3.6.3 image contains Kafka Connect 4.3.0. Read-only ZIP inspection verified all three configured source connector JARs contain `META-INF/services/org.apache.kafka.connect.source.SourceConnector`, naming their existing PostgreSQL, MySQL and Oracle connector classes; the configured JSON converter and standard header converters also have service manifests. This is the correct source SPI, rather than a manifest named after the generic Connector class. The image entrypoint maps `CONNECT_PLUGIN_DISCOVERY` to `plugin.discovery`, consistent with the [Debezium 3.6 container instructions](https://github.com/debezium/container-images/blob/main/connect/3.6/README.md#others).

Only the isolated rendered Connect service uses `CONNECT_PLUGIN_DISCOVERY=service_load`. The primary Compose environment keeps its default discovery mode. [Kafka 4.3 documentation](https://kafka.apache.org/43/kafka-connect/user-guide/#plugin-discovery) explains that this mode skips reflection scanning and requires compatible service manifests. This evidence covers the pinned bundled adapters used here, and does not establish compatibility for arbitrary added plugins. Actual startup timing and the required three classes in `/connector-plugins` must still be checked during isolated runtime acceptance. Health checks, cold-start grace periods, and capacity checks remain enforced.
