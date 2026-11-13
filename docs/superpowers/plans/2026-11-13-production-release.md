# DataMesh Production Release Plan

**Goal:** Prepare and release DataMesh to 121.196.245.95 using the user-specified deploy-standards, preserving existing projects and local18443.
**Reference:** https://github.com/JojoShine/deploy-standards/blob/main/DEPLOYMENT.md (read2026-11-13).
**Execution:** Inline implementation; no unrelated commits, server changes, service stops or data cleanup. Deployment choices requiring user decisions remain pending rather than inferred.

## Confirmed and pending decisions

- PostgreSQL: bundled, explicitly selected2026-11-13. Use independent roles and durable storage.
- Frontend/backend: combined business image, following2026-11-13 discussion; React source/API boundaries remain separate, database and compute remain independent services.
- Source repository: JojoShine/DataMesh exists, public and empty. User explicitly authorized keeping it public and committing/publishing source2026-11-13. Re-run final credential scan before initial commit; exclude runtime data/secrets/screenshots.
- Public entrance: `https://tbtparent.me/datamesh`, explicitly selected2026-11-13. Data migration scope: user explicitly selected migrating local18443 current business data and configuration; do not migrate old sessions or private CSV sandboxes. Preserve identities, permissions, history, encryption/signing keys and formal object bindings. Local18443 data must not be copied or reset implicitly; a fresh production administrator must not reuse a disclosed local password.
- Compute allocation: pending. Target x86_64, approximately7.1GiB total/2.3GiB available memory; root5GiB and data11GiB free,18 existing containers. Current validated local full chain consumes approximately7GiB at the observed instant. Need capacity or independently reachable compute before claiming complete production support. Do not reduce functionality or stop other services to fit.

## Tasks

- [x] Read deployment standard and inspect target via verified SSH host key; read only.
- [x] Correct release metadata to derive schema from actual migration files rather than stale47; regression-check fail closed on incomplete pairs.
- [ ] Confirm remaining topology, entrance and resources. Record explicit decisions in project DEPLOYMENT.md.
- [ ] Implement runtime interfaces healthcheck/migrate/bootstrap in one backend digest, non-root runtime8080 and /healthz; preserve localTLS8443 behavior. Validate repeat initialization, actual role connection and clean migrations using isolated database.
- [ ] Add confirmed-mode complete compose.prod.yaml, environment example and isolated Nginx location fragment; localhost31000–31999, no source uploads/builds, private database, stable projectname and preserved secrets.
- [ ] Add locked CI build/check and candidate verification, GHCR publication and promotion-only release workflow; linux/amd64 and every required compute/runtime dependency must be validated, not reused from unverified ARM64 artifacts.
- [ ] Review source publication manifest and exclude runtime files, backups, credentials and local data before authorized initial commit/push. Pin third-party Actions to full SHA.
- [ ] Prepare immutable release manifest, rollback files and verified backup/restore procedure. Before remote writes, verify capacity, Nginx conflicts and all selected versions.
- [ ] Pull exact artifacts, prepare roles and migrations separately, explicitly bootstrap, launch and verify HTTPS/static/API/auth/real CSV SQL/lightweight/stream chain. On failed migration keep old app; on failed release restore compatible artifacts without deleting data.

## Verification boundaries

Read-only target inspection is not deployment. LocalARM64 validation is not x86_64 acceptance. No Docker pruning, unrelated service changes, migration on shared databases or public source publication is authorized by this preparation step alone. User instruction to release authorizes the selected deployment once required decisions and artifacts are concrete; do not add a redundant confirmation gate for routine work.

## Capacity optimization follow-up

User requests minimizing resource use before increasing server configuration, and explicitly authorizes cleaning data-disk backups. Existing app image has accumulated replacement layers (~1.55GB); measured final server binary87.66MiB, code-runner8.64MiB, ingress8.60MiB and web assets8.20MiB. A fresh multistage final image can eliminate historical layers; its final size and runtime dependencies still require validation. Artifact compression does not reduce the resident memory of Doris/Flink/Kafka. Do not claim the complete real chain fits the currently available2.3GiB without an isolated capacity test.

Selected cleanup target is only the verified retired `/data/backups/docker-systemdisk-20261018-135052/docker` copy (12,244,787,200 allocated bytes). Dry-run confirmed activeDockerRootDir `/data/docker`, no container/bind/overlay references or nested mounts, and18 existing running containers. Keep project backups and historical migration evidence. Cleanup result recorded privately under `.local/production-release/`.

## Prefix and restart decisions

- User selected `tbtparent.me/datamesh`; productionBASE_PATH=/datamesh and APP_ORIGIN=https://tbtparent.me. Do not allocate another root /api, /auth or /static location on the shared domain.
- Implement a frontend path helper from ViteBASE_URL for API calls, history navigation, actual links/downloads and asset references. Keep internal route/endpoint names logical so local18443 and root-mode tests remain unchanged. Test encoded query/fragment, already-prefixed paths and sibling/external paths.
- Backend mounts routes below the validatedprefix and binds8080; preserve existing local8443TLS behavior. Keep credentialed internal streaming bridge HTTPS and token verification; internal-only paths are not public Nginx locations. Session cookies must be scoped to the application prefix.
- Health must test real database readiness, migration cleanliness and latest schema; SPA fallback cannot hide API/health404s. Application, migration and idempotent bootstrap use one digest. No admin password in persistent appenv.
- Record reboot baseline `.local/production-release/server-before-reboot.json`: all18 containers have always/unless-stopped, Docker/Nginx/minio/Redis and supporting services enabled, PostgreSQLcluster enabled-runtime. User may execute the cloud reboot; do not trigger a second reboot while cloud scheduling is unresolved. Recovery checker compares bootID, expandedCPU+RAM, originalcontainer images/health and hostservice states.
- Backup cleanup completed: reclaimed12,244,533,248bytes;18containers not stopped/restarted by cleanup. Project backups retained. Evidence remote `/data/maintenance/datamesh-backup-cleanup-20261113T013354Z` and local `.local/production-release/backup-cleanup-result.json`.
- Compact packaging experiment PASS: actual imageinspect size506,990,628→88,979,714bytes (82.45% reduction),111runtime files and startup config identical. ARM64 size probe only, no production deployment or functional/amd64 claim. Production still needs clean source build plus ops tooling and compute dependency images; do not publish the experimental repack as the production release.

## Production preparation checkpoint (2026-11-13)

- User retained cloud reboot appointment10:20Beijing; no agent-triggered reboot. Actual recovery verified4CPU/14.71GiB,18 original containers same images healthy/running,9 hostservices recovered; evidence `.local/production-release/server-recovery.json`. Data free21.21GiB.
- Files implemented: frontend basePath helper +45focusedtests/TS/prefixbuild; actuallink/download/assets allprefix; GoMountBasePath + validated sharedprefix utility; cookiePath config; exact trustedproxyIPs; PORT env(default8443TLS retained). New production `/healthz` verifies embedded migrationversion and cleanliness. Rootimage runtime8080TLS with nonrootCMDsupports commandoverride.
- Root Dockerfile, `.env.prod.example`, complete `compose.prod.yaml`, Nginx location fragment, PostgreSQL and source-role init configs; Dockerfiles allow native-version base overrides for source-reader, Python/JS and Flink plugin. No production app deployed yet.
- `cmd/runtime-tools` exposes migrate/bootstrap/healthcheck via symlinks. Golang-migratev4.19.1 justified; it requires lz4patch4.1.15→4.1.16; no other existingdependency upgrade. TiDBparser moved indirect→direct withoutversionchange. Allcmd/internal/migrations tests +govetPASS;60toolingPASS.
- Actual isolatedPG non-supermigrator + realappbootstrap role repeatmigration/passwordpreservation/disabledidentityrefusalPASS. New realimageverifier (`scripts/verify_production_image.py`) testsfreshroles, migrationtwice, bootstraptwice, appTLS/prefix/static/API/cookie and PostgreSQLcontainer recreatewithsamevolumePASS on localARM64. Proof `.local/production-release/local-image-verification.json`. Do not claimnativeAMD64 yet.
- Debugging evidence: localprivatebuild omittedCGO_ENABLED=0, causedELFinterpretermissing inSCRATCH; fixedprivatebuild, rootDockerfilealwayscorrect. Go compiler required2GiB/singlejob rather than1GiB. AlpinePG uid70 discovered dynamically; initialscriptmust0755 (Mac bind-mounted0644 caused Bash access check to chooseexec and kernel deny). Productionconfigexecmodefixed. Earlier failedlogs keptprivate, ownedtestcontainers/volumes cleaned.
- CI files `.github/workflows/{ci,build,release}.yml` + `scripts/{ci_production,promote_production}.py` implemented but not executed/static-reviewed fully yet. Mainpublishes onlyafter nativeimage/protocol/pluginchecks; formalpromotesverifieddigests/no rebuild. PinnedActionSHAs verifiedprimaryGitHub: checkoutd23441a48e516b6c34aea4fa41551a30e30af803/setup-node249970729cb0ef3589644e2896645e5dc5ba9c38/upload-artifactea165f8d65b6e75b540449e92b4886f43607fa02.
- `DEPLOYMENT.md` added; finaltests, workflowcontractchecks, schema/OpenAPIchecks, pubmanifestupdate and actualcommit/push/CI/version/deploy STILLREQUIRED. No local gitidentity/remote configured; repo unborn/untracked. User now authorized commit/push; do not add `.local`, artifacts, rawbusinessdata or private screenshots. Reviewed808files~40.6MB/noactualcredentialmatches before latestCI additions; regeneratefinalreview.
- Localcandidate staticimage `datamesh-server:production-prefix-local-20261113` derivedfrom freshlycompiledcurrentGo+prefixweb, ARM64 validationonly. Currentmain18443 remains oldverifiedimage unaffected; rootnewsourcehasnotbeen deployedthere.
- Current readonlymigrationinventory: schema48;31sessions;4closed sandboxes;0activequeries;2privatevisitorlightweightuploads/1unit;566business_records;0activegovernance/streams/ETL. Need snapshotquiescence beforecross-storeexport; preservemainserviceafterboundedwindow.
- Full migration must use logical PG dump/restore and Doris supported BACKUP/RESTORE via private S3 repository (docs verified https://doris.apache.org/docs/4.x/admin-manual/data-admin/backup-restore/backup/ and restore). No cross-architecture rawDoris/PGdirectorycopy. Need inspectKafka/Connect/Flink/sourceCDCcurrentstate; PGlogicalrestoredoesnotcarryreplicationslots/WAL, so handle sourcepositions explicitly/failclosed. Do not fabricate resumedcontinuoushistory.
- Privacy filtering must occur in owned staging clone, not source: clear sessions; exclude all5sandboxtables (reinsertworkerlockid1); exclude visitor-scoped lightweight uploads/units/runs and theircachedoperations/privateobjects. Audit record schema contains IDs/actions only, preservehistory. Inspect FKs before filtering run-related tables. Never copy S3 blindly if it includes excluded privateobjects.
- Preserve CREDENTIAL_KEY, allwatermarkkeys, service-sourcecredentials and formalIDs. GeneratefreshcloudinternalTLScert(longervalid than oldlocal30days) and consistently mount newpublicCA intoGateway/Taskmanager/hostNG; keepbridgeTLS/token checks. Namespaceprefix and x86scriptimageConfigIDs need reconcile via realruntimevalidation/new normalversions rather than silently rewriting immutablepublishedbindings.
