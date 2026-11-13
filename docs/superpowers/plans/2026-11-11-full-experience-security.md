# Full Experience Security Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development or superpowers:executing-plans to implement task-by-task.

**Goal:** Provide real isolated four-platform experience with implemented PDF/content tracing and row filtering.
**Architecture:** Lightweight separate application/metadata/object store with scoped shared Doris compute; heavy runtime services enabled as needed. Existing main public_test boundaries remain enforced. Security controls apply within normal delivery paths, before evidence is signed.
**Tech Stack:** Go/Gin/GORM, React/Ant Design, Docker Compose, Python deployment tooling.
**Spec:** docs/superpowers/specs/2026-11-11-full-experience-security.md

## Global Constraints
- Preserve current business data and all existing authorization checks.
- No simulated success, no public admin credentials from main instance.
- Independent review remains required for controls and releases.
- Fail closed when protected format or delivery path cannot apply enforcement.

## Review Focus
- Cross-instance cookie collision and shared Docker networks/volumes/secrets.
- Filtering after pagination, masking or aggregation leaking unauthorized records.
- Unsupported PDF/encryption/signatures and watermark escaping.
- Async ETL jobs executing before policy or after revocation.
- Memory limits exceeding machine capacity; report actual unsupported capabilities.

### Task 1: PDF and implicit content watermark helpers
- [x] Implement bounded file transformation/extraction with tamper-aware proof binding; verify adversarial format and round trips.
### Task 2: Row-level policy enforcement
- [x] Trace all delivery paths; implement validated predicates and fail-closed enforcement; regression tests.
### Task 3: Isolated full backend environment
- [x] Generate independent keys, storage/network/project, source fixtures, scoped runner, cookie names and resource checks.
- [x] Verify lightweight core using shared compute with independent database/user privileges and bounded workloads.
- [x] Provision separate role accounts using real identity and business interfaces.
### Task 4: Integrate and deploy
- [x] Connect security controls UI to actual backend capability; meaningful tests and build.
- [x] Verify isolation and full business lifecycle; only expose experience entry when environment is ready.

Validation: docs/validation/governance/2026-11-11-lightweight-experience-security.md. Optional heavy profiles and uncovered adapter combinations are explicitly outside completed runtime acceptance.
