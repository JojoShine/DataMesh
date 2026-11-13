# CSV Data Sandbox Implementation Plan

> For agentic workers: REQUIRED SUB-SKILL: superpowers:subagent-driven-development; execute continuously under existing user authorization.

**Goal:** Independent real CSV SQL sandbox integrated with lightweight governance at18443.
**Architecture:** Existing Go service, PostgreSQL metadata/leased jobs, existing Doris restricted namespace, encrypted RustFS originals/results, existing React/Antd UI. No additional runtime process.
**Tech Stack:** Go1.25.5, MySQL AST parser pinned, Doris4, PostgreSQL, RustFS, React/Antd.
**Spec:** docs/superpowers/specs/2026-11-12-csv-data-sandbox-design.md

## Global Constraints

- Preserve all existing uncommitted files/data; no commits or worktree resets. Existing unborn checkout is required to represent current user environment.
- Main entry18443, Docker12GiB, no second compute cluster; all new privileges only dedicated sandbox database.
- Keep approved web design sizes and normal role, CSRF, audit and public-abuse controls.

## Review Focus

- Shared-account different-login isolation, including exported lightweight data and worker frozen scope.
- SQL AST resolution through CTE/subquery/aliases, functions, hidden hints and cross-schema access.
- Unknown writes/restart, expired lease workers, close/cancel/revocation before publishing.
- Exact integers/decimals/leadingzero/emptyCSV and schema labels across SQL+download+lightweight.
- Quotas under concurrent imports/materialization and cleanup restricted to proven new resources.

### Task1 SQL compiler

- [x] Pin justified parser dependency; regression tests for allowedSELECT/CTE/JOIN/CAST/aggregates and malicious/boundary inputs (initial fail-first artifact was not captured).
- [x] Implement compileSandboxSQL+safe AST rewrite/function allowlist, resource/depth/size bounds.
- [x] Owned tests/vet and independent review; no raw SQL pass-through.

### Task2 Backend sandbox lifecycle and lightweight adapter

- [x] Add migration48/models/HTTP contracts from spec; authorization/session/quota/state tests, including captured vulnerable-baseline regressions.
- [x] Implement bounded leased import/query/materialization, dedicated reader/writer, cancellation, history/results and exact typed values; robust cleanup/expiry and restart.
- [x] Implement file import/export and isolated lightweight handoff using existingCSV/storage; preserve ordinary users and frozen background authorization.
- [x] Real opt-in integration tests against restricted Doris/RustFS+Postgres; independent review.

### Task3 UI

- [x] Add typed API/page/nav independent of formal governance workspace, SQL editor, catalog/upload/schema, results/history/save/download/handoff; focused component tests.
- [x] Integrate lightweight entry and file_id preselection; existing design tokens/Dialog actions and narrow scrolling.
- [x] Focused frontend tests, type/build, actual 1920/390 acceptance; independent review.

### Task4 Provision and deploy

- [x] Prepare private token/credentials/limited namespace provisioning and configuration; no root credential in app.
- [x] Backup/restore metadata before migration, preserve original row hashes; build/deploy18443 without active user jobs.
- [x] Real main full CSV/SQL/materialization/lightweight lifecycle + isolation/negative tests and resource evidence; docs/OpenAPI accurate.
- [x] Whole-feature review and final verification, close owned test sandboxes and leave no active sandbox jobs; private exported lightweight fixtures remain session-isolated.

## Final evidence

Implemented and deployed at18443/schema48. See `docs/validation/experience/2026-11-12-csv-data-sandbox.md` for executed validation and boundaries. Actual main cancellation/expiry were not forced; isolated PostgreSQL integration covers them. No claim that all external integration fixtures or initial red-phase artifacts are complete.
