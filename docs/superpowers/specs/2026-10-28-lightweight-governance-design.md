# Independent lightweight governance

Approved direction: upload CSV files, directly join/clean/check quality, run and inspect results without creating or selecting a governance workspace. Add a distinct entry under 处理数据. Deploy it together with the already implemented compact platform switch button.

## Behavior and boundaries

- A privately owned saved unit contains a name, existing GovernanceDataflow graph, immutable uploaded file references, and independent run history. Raw CSV objects are stored in RustFS; PostgreSQL stores metadata only. No hidden/default workspace, source directory registration, publication or model is required.
- Reuse existing graph/compiler, transform/join/quality components, worker leases/cancellation, Doris SQL executor and private result storage. Runtime batches static CSV input. Results remain in Doris; existing optional RustFS archive remains available. No new execution engine or client-side data processing shortcut.
- CSV: UTF-8/BOM, comma separated, first row headers, 20 MiB/file, at most 100 columns and 100,000 data rows; one to eight sources per unit. Preserve strings and leading zeros. Generate safe column identifiers for nonidentifier headers, retain original labels. Reject duplicate/empty headers and malformed row widths; do not silently discard data rows. Show inferred field names, row count and actual errors. Text typing is explicit; support required, uniqueness, length/enum and compatible existing inline checks.
- Private files/units are owner-scoped with existing governance platform/role checks. Selecting an existing shared resource is not part of this upload-only entry. Unit graph references cannot be used to read another person's upload or a registered protected source. Referenced uploads remain available to historical runs.
- Lightweight runs reuse kind=dataflow and an immutable execution marker, with NULL workspace_id. Migration 33 relaxes only these run rows; normal run/schedule defaults and existing workspace constraints remain intact. No governance definition/model/output publication is automatically created.
- UI uses existing business steps and node configuration where applicable; input file choice and output preview are specific to this independent entry. No workspace provider, bar, implicit selection, or workspace_id query propagation. Saved unit recipes can be reopened, edited with optimistic version checks, and run repeatedly. Each run pins actual graph/inputs.
- Unit page shows real status, selected input/fields/rules, result and rejected rows; allow CSV download and cancellation. Keep failure history and prevent stale run results from appearing under a changed selection.
- Existing full governance, authorizations and directory/publication workflows stay unchanged. No unsolicited role registration/switching changes.

## API contract

Base: `/api/v1/lightweight-governance` (no workspace scoping).

- GET `/files`: Page of `{file_id,name,size,rows,fields:Field[],field_labels:Record<string,string>,version:1}`. Optional bounded `sample` rows may be returned.
- POST `/files`: multipart `file`; same file DTO. Private object details/keys are never returned.
- GET `/units`: Page of unit DTOs.
- POST `/units`: `{name,graph:GovernanceDataflow}` -> unit DTO.
- GET `/units/:id`: unit DTO `{id,name,version,graph,files:FileDTO[],created_at,updated_at}`.
- POST `/units/:id`: `{expected_version,name,graph}` -> updated unit DTO.
- GET `/units/:id/runs`: Page of existing GovernanceFlowRun DTOs (immutable definition/results; NULL workspace serialized as empty string is acceptable).
- POST `/units/:id/run`: `{expected_version}` -> GovernanceFlowRun.
- GET `/runs/:id`: GovernanceFlowRun.
- GET `/runs/:id/records`: Page of result data rows.
- GET `/runs/:id/rejections`: Page of `{node_id,ordinal,data:Record<string,unknown>,violations:string[]}`.
- GET `/runs/:id/download`: authenticated CSV download of full successful output, not current page only.
- POST `/runs/:id/cancel`: `{}` -> updated run.

Graph source refs use existing `file_resource` shape with private uploaded file IDs and contract version 1; they resolve only through lightweight execution. `governanceFlowExecution.lightweight = {unit_id,version}` identifies this immutable execution. Input snapshot adds `upload_id`, `source_name`, `field_labels` for input resolution/labels. Other source types and external references/scripts are rejected for lightweight units. Output uses infer_model=true for schema inference only; executor must skip automatic model registration for lightweight runs.

## Review focus

Cross-user files/runs, spoofed normal source IDs, workspace query injection, stale unit versions/historical graph drift, orphan upload cleanup races, CSV BOM/Unicode/quotes/newlines/empty headers/ragged rows/leading zeros, malformed graph/cycles/joins producing excess rows, cancellation/lease loss, unavailable Doris/RustFS and downloads of failed/other-owner runs.
