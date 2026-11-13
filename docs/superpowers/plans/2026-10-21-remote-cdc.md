# 远程 CDC Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [x]`) syntax for tracking.

**Goal:** 为 PostgreSQL/MySQL/Oracle 已登记库表建立可隔离、可核查、可恢复控制动作的采集任务。

**Architecture:** 新任务持久保存显式绑定，继续通过现有 Connect 内部服务执行。旧任务保留样例解析；创建配置与采集运行分离，外部调用由持久控制记录驱动，避免 HTTP 事务内执行网络请求。

**Tech Stack:** 当前 Go/GORM、Kafka Connect/Debezium 与 React/Ant Design；版本保持锁定。

**Spec:** ../specs/2026-10-21-sharing-exchange-completion-design.md

**执行状态：** 本子计划已完成；实际文件、测试名称及验证边界以[本批验收](../../validation/sharing-exchange/2026-10-21-completion.md)为准。复用现有集成测试入口，不为计划中的示例文件名创建重复实现。

## Global Constraints

继承总计划全部约束。一次任务只采集一个表；新任务名称、主题前缀和 schema history topic 均按任务隔离。日志及响应不返回凭据；不自动删除 slot/publication 或重置位点。不把 RUNNING 等同于快照完成或治理结果可用。

## Review Focus

- 同名表、带正则元字符的表名：include-list 必须精确匹配，不扩大采集范围（A1）。
- 同源别名、旧样例任务：新绑定不得接管别人的连接器、slot 或 server-id（A1/A2）。
- 外部创建已成功但平台超时：核查后恢复同一作业，不重复创建（A2）。
- 缺少日志、驱动、PDB 信息或实际 Connect 网络不通：明确阻断，不用平台 SQL 连接成功代替采集预检（A3）。
- 多分区、空表首次快照、DDL 或日志过期：准确区分完成、空快照、待核查和范围缺失（A3）。

### A1：显式绑定、对象登记与兼容

**Files:** 新增 `internal/modules/workflow/cdc_models.go`、`cdc_models_test.go`；修改 `models.go`、`tasks.go`、`source_tables.go`、`runtime.go`、`archive.go`；新增 `migrations/000009_cdc_bindings.{up,down}.sql`。

**Interfaces:**
- `CDCBinding`：TaskID/SourceID/ObjectID/ConnectorName/TopicPrefix/SchemaHistoryTopic/Schema/Table/Slot/Publication/PDB/SourceFingerprint 为 string；SourceVersion/ConfigVersion 为 int；ServerID 为 int64。
- `resolveCDCBinding(task Record, data TaskData) (CDCBinding, error)`：新任务读取显式绑定；旧 Binding 解析为原 datamesh-{kind} 和原主题，不重新配置旧连接器。
- `CDCConfigInput`：SourceID/Schema/Table/Publication/PDB 为 string；SourceVersion 为 int。输入不接受任意 connector.class、主题名、密码或连接地址。
- 新建 `POST /api/v1/cdc-tasks` 接受 CDCConfigInput；保留 `POST /tasks` 旧契约。新接口在服务端重新读取表结构并登记 Object，不能信任客户端字段。

- [x] 写 `TestCDCBindingIsolation`：不同任务同表生成不同 connector/topic/history；数据库表名含 `.`、`+` 时精确匹配；绑定序列化不含密码。
- [x] 写 `TestCDCLegacyBinding`：旧 PG/MySQL/Oracle 任务解析结果与现有样例一致。
- [x] 运行 `scripts/check.sh go`，确认新增测试因缺少接口而失败。
- [x] 实现绑定及迁移：数据库唯一约束保护 connector/topic/server-id 和可识别来源指纹+表范围；连接别名需结合数据库标识核对，无法确认时不声称已去重。用户提供的 PG publication 必须属于所选库且包含该表；平台 slot 按任务生成且禁止覆盖外来 slot。
- [x] 实现新建接口的权限、白名单、配置版本、数据库真实主键与表类型检查；事务内登记对象及任务，失败不留下可运行半成品。
- [x] 增加 `tests/integration/cdc_task_test.go` 的 `TestCDCTaskRegistration`：无权限403、版本过期409、并发重复创建仅一份、旧任务兼容、回退拒删已有任务。
- [x] 运行 `python3 scripts/test_identity.py --run TestCDCTaskRegistration` 与 `scripts/check.sh go`，全部通过。

### A2：配置生成、持久控制和状态核查

**Files:** 新增 `cdc_config.go`、`cdc_control.go`、`cdc_config_test.go`、`tests/integration/cdc_control_test.go`；修改 `runtime.go`、`tasks.go`、`http.go`、`cmd/server/main.go`。

**Interfaces:**
- `buildCDCConfig(binding CDCBinding, source SourceSecret, credential Credential) (map[string]string, error)`：只产生现有固定版本支持的白名单配置；内部使用，不序列化给用户。
- `(*Runtime).connectorByName(ctx context.Context, name string) (ConnectorStatus, error)`。
- `(*Server).reconcileCDCControl(ctx context.Context, taskID string) error`：持久期望状态到实际状态的核查；由受服务生命周期管理的 worker 调用。
- `cdc_controls`（包含在000009）：task_id主键、desired_state、generation、observed_generation、lease_owner、lease_expires_at、last_error_code、updated_at；控制错误不保存原始 Connect 响应。

- [x] 写 `TestCDCConfig`：三库配置按真实选表生成，PG禁自动创建publication、快照initial、独立slot；MySQL唯一server-id/history；Oracle必需PDB/LogMiner配置；非法附加配置拒绝。
- [x] 写 `TestCDCControlRecovery`：模拟 PUT 已成功但响应丢失，第二次核查不改位点；异名/配置不符作业拒绝接管；并发worker仅一人控制；有效租约不被重启重置。
- [x] 运行定向 Go/集成测试，确认预期失败。
- [x] 实现持久期望状态及租约领取，网络调用放在数据库事务之外，保存 observed_generation 前检查租约及 generation；超时记待核查，重启重新核对实际作业。
- [x] 复用现有任务启动/暂停/恢复入口；新任务按显式绑定控制，旧任务保持原行为。启动/恢复重查权限与白名单；后台检查失效时仅暂停平台拥有的新作业并记录原因。不得调用删除或offset重置接口。
- [x] 成功/失败/待核查追加安全审计；Connect错误只输出允许字段，禁止trace/config中凭据外泄。
- [x] 运行 `python3 scripts/test_identity.py --run TestCDCControl` 及 `scripts/check.sh go`，验证断点恢复及权限回归。

### A3：真实预检、进度与三库集成

**Files:** 修改 `cdc_preflight.go`、`sources.go`、`tasks.go`、`archive.go`、`scripts/test_identity.py`；新增 `cdc_progress.go`、`tests/integration/cdc_remote_test.go` 和 `scripts/smoke/cdc_remote.py`。

**Interfaces:**
- `CDCSnapshotBoundary`：State string、ObjectVersion int、Partitions []CDCPartitionRange；`CDCPartitionRange` 为 Topic string、Partition int、Start/End int64。
- `(*Server).cdcSnapshotBoundary(ctx context.Context, task Record) (CDCSnapshotBoundary, error)`：只有首次快照已完成且输入范围可验证才返回完整边界；不能以消费者读到末尾推断快照完成。
- 任务响应新增 `capture_binding`（安全摘要）、`snapshot` 和错误指引；现有 ingestion/processing/result 字段保留。

- [x] 写 `TestCDCProgressBoundaries`：RUNNING但snapshot未完成仍pending；空表快照有可验证完成依据；分区位点分别记录；日志过期与DDL不兼容阻断完整归档。
- [x] 确认锁定版 Debezium 提供的快照完成/位点观测方式；使用受控通知主题或明确快照指标，不能依赖样例字段值猜测完成。将实际配置和验证证据写入验收记录。
- [x] 实现按选表和任务配置的预检，删除“所有远程源均unsupported”的固定判断；缺少驱动/平台通路/数据库参数仍返回明确未满足，外部条件不能验证时标unknown。
- [x] 实现进度及边界观察，选表变化使原预检失效；新远程任务不绑定固定 Flink/Doris 样例，也不显示虚假治理成功。
- [x] 扩展隔离测试夹具及清理：测试前记录唯一资源名，创建PG复制账号/publication、MySQL测试库、Oracle专用schema及受控日志条件；只清理由本次测试创建的对象，保持原样例位点。
- [x] 复用 `scripts/test_identity.py --run TestCDCRemoteCapturePostgres`、`TestCDCRemoteCaptureMySQL`、`TestCDCRemoteCaptureOracle`：每库两表真实验证snapshot、insert/update/delete和归档。持久控制的暂停/恢复、并发与平台重启恢复另用真实PG元数据及受控Connect故障响应测试；不声称逐库重启了真实生产Connect。输出不含凭据，缺环境不得跳过后标通过。
- [x] 原有 `TestCDCRemoteDiagnostics`、`TestCDCMySQLDiagnostics`、`TestCDCLocalScopeStatus` 按新能力调整准确断言，保留全部负例。

### A4：平台创建流程与契约

**Files:** 修改 `web/src/pages/Tasks.tsx`、`web/src/components/SourceCheckReport.tsx`、`web/src/api/types.ts`、`web/tests/tasks.test.tsx`、`api/openapi/platform.yaml`、`docs/validation/cdc-readiness-guidance.md`。

- [x] 写前端测试：未绑定远程表也能进入配置；预检缺项阻断启动；查看诊断只显示一层；返回保留源/表/配置；源切换清空旧预检；真实错误原样以安全文案呈现。
- [x] 实现同窗“选表→条件与配置→确认创建”，展示新外部日志对象及负责人；创建成功不等于运行成功，明细显示控制状态和实际状态。
- [x] 写并验证新增API契约、旧任务响应兼容；不暴露原始Connect配置。
- [x] 执行 `npm --prefix web test -- tests/tasks.test.tsx tests/sources.test.tsx --maxWorkers=1`、前端类型/构建、`scripts/check.sh contracts`；1920×1080与390px检查。
- [x] 更新CDC验收记录：逐数据库列出已验证场景、未验证条件、操作指引及归档依赖。进入B计划前保留完整的A阶段可运行构建。
