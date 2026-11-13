# 授权资源批量交换 Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [x]`) syntax for tracking.

**Goal:** 库表、原始归档、治理成果三类平台资源在授权和质量约束下完成百万行级交换。

**Architecture:** 保留现有 ETL reader→转换→批次写入→整轮提交结构，将资源一次性读取替换为一致性快照的有界消费。原始事件分块归档、按主键持久重建快照；治理结果使用显式绑定及批量质量门禁。前置A计划的任务绑定和快照边界。

**Tech Stack:** 现有 Go/sql/GORM、PostgreSQL、RustFS、PG/MySQL/Doris；不新增流处理框架或ORM。

**Spec:** ../specs/2026-10-21-sharing-exchange-completion-design.md

**执行状态：** 本子计划已完成；实际文件、测试名称及验证边界以[本批验收](../../validation/sharing-exchange/2026-10-21-completion.md)为准。复用现有集成测试入口，不为计划中的示例文件名创建重复实现。

## Global Constraints

继承总计划。每类读取默认目标200万行/1GiB/30分钟，批次500行/4MiB；不提高普通查询API的单页限制。授权脱敏先于转换；转换仍检查加工授权。中途撤权或质量失败回滚。超限不截断成功；不把原始值记入日志。

## Review Focus

- 途中撤权、目录下架与身份停用：批次/提交检查阻断，不继续交付（B1）。
- 未授权主键、脱敏碰撞和大整数：不能回退到未脱敏主键或失去精度（B1）。
- 归档损坏、分区缺段与删除事件：快照完整性和当前值准确（B2）。
- 同时存在新旧治理结果、规则版本不符：不能选择错误物化结果（B3）。
- 高事件量低存量、超宽单行、异常退出：容量按实际消耗限制，暂存不会无限增长（B2/B4）。

### B1：统一批量读取契约与库表资源

**Files:** 修改 `etl_resources.go`、`etl_batch.go`、`etl.go`、`source_tables.go`；新增 `etl_resource_reader.go`、`tests/integration/etl_resource_bulk_test.go`。

**Interfaces:**
- `ETLInputSnapshot`：Kind/ResourceID/SnapshotID string、ContractVersion int、Fields []string、Partitions []CDCPartitionRange（A3）；仅保存范围与版本，不保存凭据。
- `(*Server).etlResourceReader(ctx context.Context, p identity.Principal, run *ETLRun) (func(func([]interface{}) error) error, func(), error)`：与现有 etlReader 回调/清理契约一致，异常释放源事务和临时快照；设置run.InputSnapshot新增可选字段。
- 复用 `etlResourceSchema`、`authorize`、`rawProjection`、`transformETL`；内部服务读取物理库所需凭据不意味着消费者获得数据源运维权限。

- [x] 写 `TestETLResourceBulkTable`：普通跨部门使用授权账号交换10001行成功、只能读取授权字段、敏感字段按既有规则脱敏；无授权403；变换无加工授权403。
- [x] 写 `TestETLResourceBulkRevocation`：第二批撤权/下架/停用后目标回滚，written=0；提交结果不确定保持unknown。
- [x] 写 `TestETLResourceBulkKeys`：未授权主键与脱敏碰撞明确拒绝，超大整数/小数/空值不改变语义。
- [x] 执行 `python3 scripts/test_identity.py --run TestETLResourceBulk`，确认失败点为原10000行限制或缺少流式检查。
- [x] 为PG/MySQL库表资源打开只读一致性事务、检查真实结构和主键，按既有字段顺序读取；每批检查授权和范围。Oracle资源不自动套用PG SQL；采用Oracle只读一致性读取并独立验证后才提高该适配器能力。
- [x] 接入etlReader，移除库表资源模式全量[][]分配；沿用既有目标事务、租约和占用。对尚未完成的归档/治理适配器维持原限制，不统一宣布扩容。
- [x] 执行定向测试、`scripts/check.sh go`，确认原有直接库表和资源授权测试通过。

### B2：分块归档与原始快照重建

**Files:** 修改 `archive.go`、`models.go`、`delivery.go`；新增 `archive_stream.go`、`raw_snapshot.go`、`tests/integration/archive_stream_test.go`；新增 `migrations/000010_resource_snapshots.{up,down}.sql`。

**Interfaces:**
- `ArchiveChunk`：Key/SHA256 string、Partition int、Start/End/Bytes int64、Count int；扩展ArchiveData增加ManifestVersion int、Chunks []ArchiveChunk、SnapshotBoundary CDCSnapshotBoundary，可读旧单文件清单。
- `(*Server).streamArchive(ctx context.Context, meta ArchiveData, consume func(RawRecord) error) error`：逐块校验压缩摘要、解压上限、位点连续性；不返回全量事件数组。
- `(*Server).buildRawSnapshot(ctx context.Context, task Record, meta ArchiveData) (string, error)`：返回不可变快照ID；`resource_snapshot_rows`按snapshot_id/key_digest唯一，内容和原主键加密，key_digest使用快照绑定密钥HMAC。

- [x] 写 `TestArchiveStreamIntegrity`：旧清单兼容、10001事件、多分区、空快照、坏摘要、解压炸弹、断续位点及保留期缺段；不完整时快照不得ready。
- [x] 写 `TestRawSnapshotReplay`：r/c/u/d顺序重建、重复消费幂等、主键更新、相同主键分区冲突拒绝；未完成快照不供交换。
- [x] 执行 `python3 scripts/test_identity.py --run 'TestArchiveStream|TestRawSnapshot'`，确认失败。
- [x] 实现块大小目标4MiB压缩前数据，单事件上限4MiB；归档读取上限分别配置总事件数/总字节数，默认1000万事件/10GiB，不以最终存量行数冒充事件消耗。所有输入上限做严格解析并公开能力说明。
- [x] 流式gzip写对象存储，逐块校验成功后原子发布完整清单；部分写入无可见ready清单，记录受控孤儿对象清理。边界来自A3，不再假定单分区或所有任务从Kafka当前0位点开始。
- [x] 按500条事件批次加密暂存到平台数据库，以实际分区位点推进重建；同主键跨分区无法证明顺序时阻断。暂存受容量/有效期约束，成功ready后快照不可修改。
- [x] 将原始资源reader绑定固定ready快照，按主键摘要顺序批量解密→授权投影→转换；并发新事件不改变当前运行输入。不同账号不得绕过授权读取快照表。
- [x] 增加周期清理，仅清理无活动引用的过期临时快照和孤儿块；长任务引用租约避免被清理。保留审计/清单元信息，清理加密正文。无权限进程不可访问底层对象。
- [x] 测试故障恢复/重复归档/容量超限，执行原有归档交付及撤权回归。对真正不支持的Debezium事件类型明确阻断，不静默丢弃。

### B3：治理结果绑定与批量质量门禁

**Files:** 修改 `governance.go`、`catalog.go`、`models.go`、`etl_resources.go`、`runtime.go`；新增 `governed_reader.go`、`tests/integration/governed_bulk_test.go`；扩展000010迁移。

**Interfaces:**
- `GovernedResultBinding`：DatasetID/SourceID/Schema/Table/InputSnapshotID string、ContractVersion/RuleVersion int；保存物化结果来源/字段契约和精确输入范围。
- `(*Server).governedResourceReader(ctx context.Context, p identity.Principal, run *ETLRun) (func(func([]interface{}) error) error, func(), error)`。
- 质量检查汇总沿用现有Quality字段，CheckedRange记录绑定版本及快照ID；结果绑定由具备现有资源/治理职责的操作者配置并经过原发布门禁。

- [x] 写 `TestGovernedBulkBinding`：两个结果库表隔离；绑定来源、规则、契约版本不符拒绝；普通账号不得修改绑定或选择任意表绕过门禁。
- [x] 写 `TestGovernedBulkQuality`：第10001行违规也阻断；归档不完整、来源范围变化、物化结果与批准规则不一致均拒绝；旧样例质量检查结论不回退。
- [x] 定向集成测试确认失败。
- [x] 给现有固定结果增加兼容解析，新增显式结果绑定；不引入通用治理计算或从输入临时冒充治理结果。只有能够核对输入快照、已支持规则和实际输出的绑定可发布。
- [x] 将现有支持的质量规则评估改为流式汇总，整份一致性结果参与检查；跨行约束使用受控暂存或数据库聚合，不能只检首批。目标提交前得到完整passed结论。
- [x] 接入治理资源reader，在固定结果快照中检查并输出；保持每批授权/脱敏。不存在可验证物化结果时明确阻断，不读取旧public.customers替代。
- [x] 执行 `python3 scripts/test_identity.py --run TestGovernedBulk`、旧治理/质量/发布回归，验证大表时源目标事务与暂存资源全部释放。

### B4：容量呈现、百万行验收及前端

**Files:** 修改 `etl.go` capabilities响应、`web/src/pages/ETL.tsx`、`web/src/api/types.ts`、`web/tests/etl.test.tsx`、`api/openapi/platform.yaml`；新增 `tests/integration/etl_resource_benchmark_test.go`；扩展 `scripts/test_identity.py` 的规模测试超时和隔离配置。

- [x] 写 `TestETLResourceCapabilities`：响应区分table/raw/governed实际能力，未验证适配器仍显示受限；前端随所选资源显示准确容量。
- [x] 新增 `TestETLResourceMillionRows`：分别为库表、原始快照、治理结果建立普通账号授权和真实100万行；验证脱敏后的全部目标字段、唯一键数及无越权字段，不只核对row count。
- [x] 执行每类至少一次全量正确性与内存检查，PG库表资源三次空目标和一次已存在主键更新计时；记录耗时、环境、行宽、读取/转换/写入/核对时间分开统计。规模测试启动前验证环境配置，禁止缺夹具后skip并宣称通过。
- [x] 测试4MiB以上单行、真实2000001行，以及源网络中断和取消；字节上限和超时使用20000字节/5秒配置实际越界，确认与默认1GiB/30分钟相同机制回滚。未进行物理1GiB或等待30分钟的压力测试，须如实记录。
- [x] 更新页面动态容量、错误指引、进度与输入快照摘要；不增加嵌套弹窗或新字号。
- [x] 执行前端定向测试/类型/构建和契约校验；写 `docs/validation/etl/authorized-million-row.md`，明确本批实测与旧直接连接百万行成绩不同。
