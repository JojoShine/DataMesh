# Stream Script Completion Implementation Plan

> **For agentic workers:** Use superpowers:subagent-driven-development task-by-task.

**Goal:** Complete sandboxed Python/JavaScript continuous processing and prove the real CDC lifecycle.
**Architecture:** Existing Flink chain plus signed private scalar bridge to bounded existing sandboxes; encrypted immutable memo objects preserve replay/retraction identity.
**Tech Stack:** Go/Gin, Flink1.20/Java17, Kafka/Connect, Doris, RustFS, Python tooling, React.
**Spec:** docs/superpowers/specs/2026-11-11-stream-script-completion-design.md

## Global constraints
- No scripts in trusted Flink JVM; no network/host credentials inside sandbox.
- No silent batch substitute, weakened authentication, guessed unknown POST replay or main-data reset.
- Exact wire/HMAC/env contracts are in the spec. Preserve caches across generations.
- Real runtime capacity preflight remains enforced; all unrelated apps preserved unless user authorizes a resource change.

## Review focus
- Nondeterministic retractions/update→old input, lost response and concurrent cache writers return same durable value.
- Authorization on cache hits, revoke/stop during sandbox work, stale generation/forged signature.
- Typed nulls/large integers/decimal/date/Unicode roundtrip; malformed outputs fail explicitly.
- Actual live I/U/D, quality correction, savepoint resume, worker restart and safe confirmed termination.
- No credentials in generated SQL/JAR diagnostics; CA + hostname checks; closed HTTP bounds.

### Tasks
- [x] Root: private signed bridge, encrypted immutable memo, runner reuse, cancellation and focused regression tests.
- [x] Adapter/compiler: Java adapter and build, typed Flink SQL script nodes, worker JAR loading and tests.
- [x] Tooling: isolated tokens/JAR/certs/env mounts and deployment readiness; unit tests.
- [x] E2E: extend existing PostgreSQL CDC actual harness and integration runner timeout/provision flags; real script/lifecycle/security matrix.
- [x] Review all changes, focused tests then broader build/vet/contracts, obtain runtime resources, run real complete chain and fix observed failures.
- [x] Deploy validated application/adapter/config, verify preserved data and reusable real stream examples, report actual scope/limits.
