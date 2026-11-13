# Lightweight Governance Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Provide a standalone CSV governance entry and deploy it with the compact platform button.
**Architecture:** Reuse existing dataflow AST/compiler/executor and durable run infrastructure; private upload/unit records and an immutable lightweight marker supply CSV inputs. Only lightweight run rows may omit workspace_id; existing workspace defaults and shared-resource authorization remain enforced.
**Tech Stack:** Existing Go/Gin/GORM/PostgreSQL, Doris, RustFS; React/TypeScript/Ant Design. No new dependencies.
**Spec:** `docs/superpowers/specs/2026-10-28-lightweight-governance-design.md`

## Global Constraints
- No hidden/default workspace for lightweight units, no automatic source-directory/model registration.
- Preserve text/leading zeros, existing field/precision semantics and node selection information.
- UI follows approved 14/15/18/24px typography, 36px inputs, 32px buttons, 24px sections and fixed bottom-right dialog commands.
- Existing untracked project and deployment configuration stay in place; do not commit/reset/create a worktree that omits intentional untracked code. No broad refactors or package additions.
- Only introduce API contracts listed in the spec, and deploy with backup and an isolated-database/Doris/RustFS end-to-end test.

## Review Focus
- Deny cross-owner access, spoofed resource IDs and workspace injection.
- Pin immutable files/graph while units are edited or retried.
- Bound malformed and oversized CSV, preserve header labels/leading zeros.
- Keep cancellation, leases, Doris cleanup, failed/unknown commit evidence intact.
- No regressions to full-space workers, quality, exports, menu permissions or compact switching.

## Tasks
- [x] Backend: migration 33, upload/unit endpoints, dedicated run endpoints, existing worker/executor integration, strict authorization and result/rejection/download paths. Files: new `governance_lightweight*.go`, existing `governance_runs.go`, `governance_dataflow_http.go`, `governance_input.go`, `governance_flow_execute.go`, `governance_field_labels.go`, registration; migrations; Go unit/integration tests. Follow spec DTOs exactly. Prove no workspace/definition/model/assignment/grant is created.
- [x] Frontend: independent `GovernanceLightweight.tsx` and API types, reuse node editor/business steps/field summary; App route/menu bypasses provider/bar, navigation excludes workspace inheritance; run/history/results/rejections/download and optional existing archive. Add meaningful component/routing tests, keep compact button. Follow spec APIs; do not edit backend.
- [x] Integration/review: inspect diffs and contracts, run targeted Go tests on isolated services and frontend tests/build; test ordinary governor without exchange permissions and another user. Fix true failures and review ownership/limits/old worker behavior.
- [x] Deployment: build full backend+frontend image, backup and restore-check DB, apply migration 33, restart only server, validate health/routes/schema/data fingerprints; use disposable isolated smoke resources for real two-CSV run. Verify live compact menu and standalone entry; clean private QA sessions and retain recovery image.

## Ledger
- Ruling: CSV values default to text to preserve identifiers/leading zeros; original nonidentifier headers are retained as field labels with safe generated keys. Explicit numeric type inference is deferred, rather than silently changing values.
- Ruling: Keep the project in its existing checkout because all implementation is intentionally untracked and runtime deployment paths are coupled to it. No Git commits are requested.

## Verification (2026-10-28)
- Workflow unit tests, vet, integration compilation passed.
- Real isolated LightweightGovernance integration passed with Doris/RustFS (51.461 s): join, trim, required quality, rejection reads, full CSV, owner boundaries, private history, and no workspace creation.
- Ordinary run/default-workspace compatibility integration passed (28.353 s).
- Frontend: 36 test files passed, one skipped; 220 tests passed, one skipped. TypeScript and production build passed.
- Database backup restored successfully; migration 33 on the restored copy preserved all 53 table counts and protected business fingerprints.
- Browser acceptance passed at 1440/1920/540: ordinary governor uploaded two real CSVs, joined region_code, trimmed customer_name, checked required order_id, downloaded the complete CSV; 2 good and 1 rejected. The isolated DB contained zero workspaces, workspace sources, definitions, versions, outputs and schedules; no workspace/metadata requests occurred.
- Deployment lightweight-governance-20261028-151003 completed: clean schema 33; 67 HTTPS checks and 14 static assets verified; protected business data preserved.
