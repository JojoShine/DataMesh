# 交付对账与回执 Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [x]`) syntax for tracking.

**Goal:** 为本次ETL交付生成可信技术对账报告，并记录独立的业务接收或驳回。

**Architecture:** 转换后生成按目标主键归一化的加密核对索引与带密钥摘要；写入事务内先校验，提交后独立核对。复用持久worker控制，核对重试不再次写入目标；接收状态与ETL执行结果分离。

**Tech Stack:** 现有Go/sql/GORM、PostgreSQL及crypto标准库、React/Ant Design；不加第三方哈希/任务框架。

**Spec:** ../specs/2026-10-21-sharing-exchange-completion-design.md

**执行状态：** 本子计划已完成；实际文件、测试名称及验证边界以[本批验收](../../validation/sharing-exchange/2026-10-21-completion.md)为准。复用现有集成测试入口，不为计划中的示例文件名创建重复实现。

## Global Constraints

继承总计划。只核对本次最终主键集合及映射字段，不比较整表总量。未知提交结果先核实；对账不改变原执行终态，不新增使用授权。历史运行未做对账时显示未核对。暂存数据不出现在日志/报告。

## Review Focus

- 同键多次写入及数据库char/大小写等价键：以实际最终upsert语义核对（C1）。
- 触发器、精确小数、NULL、时间精度/时区：类型规范化不能掩盖真实差异（C1/C2）。
- 事务已提交但记录保存失败：等待核实，不再次交付或虚假回滚（C2）。
- 管理员代办、重复/过期回执、撤权：权限和记录版本检查不可缺失（C3）。
- 核对索引已清理或目标表变更：明确无法重新核对，不补造通过结论（C2/C3）。

### C1：规范化与安全期望索引

**Files:** 新增 `etl_reconciliation_models.go`、`etl_reconciliation_digest.go`、`etl_reconciliation_test.go`、`migrations/000011_delivery_reconciliation.{up,down}.sql`；修改 `etl.go`、`etl_batch.go`。

**Interfaces:**
- `ETLReconciliation`：ID/RunID/State/InputSnapshotID/ErrorCode string、Version int、ReadRows/ProcessedRows/KeyCount/CheckedRows/MismatchCount int64、CheckedAt *time.Time。State取pending/checking/passed/mismatched/failed；旧运行无记录代表未核对。
- `canonicalETLValue(field Field, value interface{}) ([]byte, error)`：带类型、长度及NULL标识规范化；无法无损规范化则拒绝。
- `etlRowDigest(key []byte, runID string, fields []Field, values []interface{}) ([]byte, error)`：HMAC-SHA256，消息包含runID、列顺序、类型和值边界。
- `etl_delivery_keys`：run_id/key_digest唯一、encrypted_key、expected_digest、sequence；不保存原始字段值。每运行随机数据密钥以现有密钥加密封装，避免直接复用凭据加密AAD。

- [x] 写 `TestETLDigestCanonicalization`：NULL区别空字符串，整数大于2^53精确，小数1.0与目标同尺度等价、不同值不等价，时间按目标声明精度处理，二进制与文本分离，非有限数明确拒绝。
- [x] 写 `TestETLExpectedFinalKeys`：同主键多次更新只保留最后期望值；目标数据库归一化主键等价时合并，不能仅按Go字符串去重。
- [x] 运行 `scripts/check.sh go`确认测试失败；实现类型规范化与带运行隔离的摘要。
- [x] 加密记录核对主键，数据密钥仅保存在受控封装字段；沿用ETL容量约束，对密文额外存储量设置上限。元数据写失败在提交前阻断目标提交。
- [x] 用PG/MySQL真实主键语义测试重复键/char/排序规则；以目标数据库解析出的最终主键身份形成索引，不能用应用端简单lowercase模拟所有数据库规则。
- [x] 增加 `tests/integration/etl_reconciliation_test.go`，运行 `python3 scripts/test_identity.py --run TestETLReconciliation`验证索引不含明文，越权查询403，迁移回退不删已有记录。

### C2：事务内核对与提交后报告

**Files:** 新增 `etl_reconciliation.go`、`etl_reconciliation_worker.go`；修改 `etl.go`、`etl_target.go`、`etl_worker.go`、`etl_operations.go`、`cmd/server/main.go`；扩展000011控制字段。

**Interfaces:**
- `(*Server).checkETLDelivery(ctx context.Context, runID string, targetTx *sql.Tx) (ETLReconciliation, error)`：按索引批量读取本次键的映射字段，与期望摘要比对，targetTx由调用者持有。
- `(*Server).reconcileETLDelivery(ctx context.Context, runID string) error`：确认提交后独立一致性只读事务核对；持久状态、租约和generation防重复结果覆盖。
- `POST /api/v1/etl-runs/:id/reconciliation` 请求重核；GET同路径查询最新报告；每次结果追加历史，不覆写旧报告。

- [x] 写 `TestETLReconciliationBeforeCommit`：目标trigger改值导致核对不一致，整轮rollback；目标额外历史行及未映射字段不造成误报；目标缺行必报错。
- [x] 写 `TestETLReconciliationAfterCommit`：已提交目标被外部修改，执行仍succeeded但对账mismatched；核对网络失败为failed，重核不调用写入；未知结果不允许核对/接收直到核实。
- [x] 定向集成测试确认失败。
- [x] 在ETL提交前、现有最后一次权限/租约检查之前调用事务内核对，所有差异阻断提交；保留已有committing/unknown边界。
- [x] 在本轮执行创建时登记待核对状态。确认提交后worker领取；若提交后平台保存失败，unknown经管理员核实为已提交后再领取，核实为未提交则不核对。
- [x] 每次核对重查账号、平台、岗位/授权和目标版本；使用输入期望索引，不重读变化的源库。索引已过期、字段类型变化或目标不可读时保存明确failed原因，禁止当成通过。
- [x] 运行 `python3 scripts/test_identity.py --run 'TestETLReconciliation|TestETLWorkerRecovery|TestETLLifecycleAPI'`，验证取消、租约、unknown原语义不改变。

### C3：人工验收、报告导出与保留

**Files:** 新增 `delivery_receipt.go`、`tests/integration/delivery_receipt_test.go`；修改 `http.go`、`resource_access.go`、`etl_operations.go`、`api/openapi/platform.yaml`。

**Interfaces:**
- `DeliveryReceiptInput`：Decision string（accepted/rejected）、Reason string（1–1000字符）、ExpectedVersion int、ReconciliationID string；管理员代办追加OnBehalfReason string（1–1000字符）。
- `POST /api/v1/etl-runs/:id/receipts`；GET同路径返回追加记录。接收人确定为当前运行实际数据使用账号；管理员可代办并记录实际操作者和原因，不能自行授予新接收身份。
- `GET /api/v1/etl-runs/:id/reconciliation?download=1` 导出安全JSON报告，复用当前查询授权与审计。

- [x] 写 `TestDeliveryReceiptAuthorization`：他人403，已撤权403，unknown/核对未通过不能accepted；管理员无代办原因拒绝；旧报告版本409；重复幂等请求仅一条回执。
- [x] 写 `TestDeliveryReceiptHistory`：接收/驳回不改变ETL状态，不写入目标、不产生授权；更正另增记录并要求当前版本，保留完整历史。
- [x] 运行 `python3 scripts/test_identity.py --run TestDeliveryReceipt`确认失败；实现回执事务、幂等、审计和版本检查。
- [x] 保留核对索引默认7天，报告/回执元数据保留；增加受服务生命周期控制的周期清理，活动核对/unknown持有引用不得清理。超过保留期明确无法重核，既有报告仍可查看。
- [x] 写 `TestDeliveryReconciliationRetention`：清理过期密文及封装密钥、保留报告、活动核对不受影响，导出无主键/值/密码。
- [x] 运行定向测试、Go检查、OpenAPI契约校验；用普通账号验证下载和分页历史，不仅使用admin。

### C4：运行明细闭环与性能验收

**Files:** 修改 `web/src/pages/ETL.tsx`、`web/src/pages/ETLOperations.tsx`、`web/src/api/types.ts`、`web/tests/etl.test.tsx`；新增 `docs/validation/etl/reconciliation.md`。

- [x] 写前端测试：执行succeeded但对账failed分开显示；unknown不能接收；历史运行显示未核对；重核按钮不发run请求；驳回/接收在原窗口完成，取消返回保留上下文。
- [x] 增加运行明细“交换结果/对账/验收”内容，显示去重交付键数与处理行数的区别、核对范围/时间/差异数量、安全错误和回执历史；角色与状态决定按钮是否可操作。
- [x] 核对自动刷新时保留输入的验收原因；引用旧报告时后台409，提示刷新后再确认，不静默改用最新结果。
- [x] 执行前端定向测试/类型/构建、桌面/窄屏检查；遵循冻结字号及单层交互规范。
- [x] 在B阶段百万行数据上复测完整链路：分别记录同步耗时、事务内核对开销、提交后核对耗时；全量核对通过且跨部门普通账号回执完成。旧16.04秒成绩不作为加入核对后的新成绩。
- [x] 完成总计划最终验收及独立审查，再发布本地服务；记录已验证数据库/资源组合和剩余限制。
