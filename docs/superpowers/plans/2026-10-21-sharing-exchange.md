# 共享交换第一批补齐 Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [x]`) syntax for tracking.

**Goal:** 完成远程 CDC、授权资源批量交换和交付对账三个已批准目标。

**Architecture:** 延续现有 Go 平台、Kafka Connect、Kafka 和对象存储。三个子计划顺序实施，已有接口兼容；每阶段独立验收，但只有三阶段全部通过才完成本批目标。

**Tech Stack:** Go 1.25.5、Gin/GORM/PostgreSQL、Debezium 3.6.3.Final、Kafka 3.9.1、现有 RustFS/Flink/Doris、React 19/Ant Design 6。沿用 deploy/versions.lock.json，不升级依赖。

**Spec:** ../specs/2026-10-21-sharing-exchange-completion-design.md（2026-10-21 用户已批准）

## Global Constraints

- 不得绕过目录发布、使用授权、加工授权、脱敏、质量门禁或可信连接范围。
- 沿用既定字号；业务操作单层展示，返回保留选择及表单。
- 默认容量目标：200 万行、1 GiB、30 分钟；每批最多 500 行、4 MiB。分资源类型验收后才公布对应能力。
- 目标仍为整轮单事务；不增加分段提交或断点续传；unknown 仍要求人工核实。
- 测试只使用隔离库。客户业务库日志配置、外部日志对象创建和持续采集不属于测试副作用。
- 保留现有未提交工作；未授权提交，不执行 git commit，不升级依赖、不重写无关代码。
- 不承诺调度、增量 ETL、复杂转换、Oracle 直接 ETL 写入、字段血缘或平台高可用。

## Review Focus

1. 同一真实数据源以不同名称登记，不能产生覆盖已有位点的采集任务（A1/A2）。
2. 快照未完成、Kafka 多分区或事件过期，不能产生完整原始快照（A3/B2）。
3. 中途撤权、敏感主键脱敏碰撞或结构变更，不能继续错误交付（B1/B4）。
4. 同一主键重复写入、数据库类型归一化及触发器改值，不能生成虚假对账通过（C1/C2）。
5. 目标已提交而平台结果未保存，不能直接重试交换或签发接收回执（C2/C3）。

## 子计划与顺序

- [x] A：[远程 CDC](2026-10-21-remote-cdc.md)：独立任务绑定、受控配置和持久控制、数据库预检、真实状态和前端流程。完成后可采集和核查，不宣称任意治理计算已实现。
- [x] B：[授权资源批量交换](2026-10-21-authorized-bulk-exchange.md)：库表流式读取、分块归档与原始快照、治理结果绑定与质量门禁、按资源类型百万行验证。
- [x] C：[交付对账与回执](2026-10-21-delivery-reconciliation.md)：期望结果索引、事务内核对、提交后对账、单独核对重试、人工接收/驳回及审计。

## 执行方式

已按用户确认在当前会话顺序执行，跨阶段接口由同一实现者维护；独立只读审查确认的6项问题已修复并回归。实际实现、测试策略调整、测量边界及发布证据见[本批验收](../../validation/sharing-exchange/2026-10-21-completion.md)。

## 最终验收及发布

- [x] 执行各子计划的测试，确认真实测试没有因缺少环境变量跳过关键数据库场景；记录所有未验证边界。
- [x] 运行 `scripts/check.sh contracts`、`scripts/check.sh go`、`scripts/check.sh tooling`；前端类型、全部测试和生产构建。
- [x] 对最终改动做独立审查，重点核对 Review Focus 五项；修复后只重跑受影响测试及必要的全量检查。
- [x] 在隔离数据库验证新增迁移、旧样例任务和历史运行兼容；回退迁移必须拒绝删除已有业务记录。
- [x] 发布前检查活动 ETL/CDC 控制动作。存在活动任务时避免直接重启，先保留待发布构建和明确停机影响。
- [x] 在本地服务执行向后兼容迁移及构建更新；核对 HTTPS 健康、接口能力、实际静态资源指纹。对客户库不自动启动新 CDC。
- [x] 更新 `docs/validation/sharing-exchange/2026-10-21-completion.md`，分别列出实现、真实验证、容量和待办，不把局部通过扩大成全平台能力。

## 计划自审

覆盖检查：设计 A 对应 A1–A4 与 B2；设计 B 对应 B1–B4；设计 C 对应 C1–C4。密码/索引加密、权限撤销、旧绑定、部分外部成功、快照边界、质量失败、规模测试和单层交互均有归属任务。

实现需以批准设计和本计划共同为准；如果发现需要新增框架或改变事务/授权契约，记录具体矛盾后再调整设计，不静默缩小目标或绕过约束。
