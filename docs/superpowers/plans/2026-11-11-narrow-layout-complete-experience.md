# Narrow Layout and Complete Experience Implementation Plan

> **For agentic workers:** Use superpowers:subagent-driven-development task-by-task.

**Goal:** Eliminate document-level narrow table overflow and expand normal isolated experience without blanket feature restrictions.
**Architecture:** Diagnose actual layout constraints, contain scrolling within tables and wrap controls. Keep real business interfaces/roles; enable additional lightweight real fixtures where practical. Add opt-in bounded abnormal-request controls to isolated experience while preserving main identity isolation and existing authorization.
**Tech Stack:** React/Ant Design, Go/Gin, Docker Compose/Python.
**Spec:** User request in this chat, plus docs/design/interface-standard-v1.md and docs/isolated-experience.md.

## Global Constraints
- Preserve typography, real data, table controls and per-table horizontal scrolling.
- Do not hide document overflow to conceal inaccessible content.
- Preserve authentication, grants, independent review, network and compute boundaries.
- No unmeasured full-stack clone or new always-on heavy engines.

## Review Focus
- Minimum-content flex/grid sizes and long account/toolbars at320/390/540.
- Table pagination/size selector and last operation columns remain reachable.
- Normal page-loading/polling must not be treated as abuse.
- Blocking occurs before protected side effects; recovery is temporary and auditable.
- Unsupported/unconnected integrations must not be advertised as successfully usable.

### Tasks
- [x] Root: reproduce and fix narrow document overflow; verify desktop/narrow views and internal table scrolling.
- [x] Backend: inspect/implement opt-in abnormal-request protection without narrowing ordinary features.
- [x] Experience: inspect actual functional blockers and complete practical lightweight fixtures/availability.
- [x] Integrate, run focused/broader checks, deploy18443/19443 and verify actual normal+blocked behavior.


Verification: browser acceptance passed 5 real isolated table routes at320/390/540/1920, startup frames, sidebar transitions, table ends and toolbar controls; actual ETL6rows+physical equality+reconciliation, native API independent review+consumer invocation, and blocked mutation cooldown/recovery all passed. Main18443 deployment protected hashes matched; own19443 enabled guard. Targeted Go race/router/vet checks, frontend19tests+build and37toolingtests passed. External API/AI credentials and full streaming/Oracle remain configuration/resource dependent and are not claimed accepted.
