# 治理业务闭环 Implementation Plan

> 当前会话自行执行；使用executing-plans。用户已批准需求与顺序，不使用子代理、不增加确认，不创建提交。

**Goal:** 补齐真实业务配置、统一整改、主数据成果、必审策略及看板。
**Architecture:** Go/GORM/PostgreSQL版本与运行、Doris处理、React/Ant Design；新增迁移45，保留既有接口与权限。
**Spec:** docs/superpowers/specs/2026-11-09-governance-closure-design.md

## Global Constraints
原数据和6资产保留；初始化不含demo/test业务名称；不新增账号/权限，不扩跨部门发布，不伪造审批/运行。只读内部主数据快照保留原授权撤销检查，不可挂原始目录。

## Review Focus
原监测基线或标准被换用；复测实际未完成却关闭；内部快照绕过加工权限/敏感范围；新审核策略被旧发布入口绕过；初始化重跑重复或覆盖业务记录。

### Task 1 统一整改
- [x] 迁移45扩展governance_quality_issues可空流引用、证据类型及监测引用；修改governance_quality_issues.go，新增governance_quality_evidence.go。
- [x] 共用登记/派单/事件/复测/关闭，监测复测核对相同版本固定基线，落标复测固定全部标准指纹及来源；关联监测闭环同步。
- [x] 扩展GovernanceQualityIssueCreate、GovernanceQualityIssues及Monitoring/StandardCoverage入口，通知关联原运行与工单，保留原数据流接口。
- [x] 隔离验证三类证据、并发、旧复测、撤权和越权。

### Task 2 主数据成果
- [x] 新增governance_master_snapshot.go：快照冻结确认版本，内部源仅本人可见；governance_input.go/metadata/workspace支持受控快照，回溯原权限。
- [x] 通过普通数据流生成成果；原始字段血缘在计划中展开；主数据页创建成果草稿并链接正常核验与目录流程。
- [x] 验证确认后变动不改旧快照、撤权阻断、敏感标记、标准模型及正常成果读取。

### Task 3 发布策略与工作流试跑
- [x] 空间增加required_release_kinds及设置；governanceReleaseGate无申请时也执行已启用策略，保留现有版本读取。
- [x] 工作流候选完整试跑固定子定义，阻止试跑发布审批产生真实发布，独立审核只接受同候选成功证据；前端说明运行范围。
- [x] 验证直接发布阻断、独立审核、工作流试跑及无资产发布副作用。

### Task 4 看板
- [x] governance_dashboard.go及GovernanceDashboard.tsx新增空间范围能力统计、未闭环异常与待审事项及入口。
- [x] 验证空值、未授权空间和口径不混算。

### Task 5 六场景初始化与交付
- [x] scripts/initialize_governance_scenarios.py复用已有初始化版本、资源/流程/空间；幂等建立监测、落标、指标、方案和基础主数据，真实执行，保存检查点与迁移说明。
- [x] 更新OpenAPI、指南；集中后端/前端/隔离验证，作者复核与桌面窄屏检查。
- [x] 备份恢复及迁移演练，升级本机、运行初始化、核对原数据资产及新增配置与运行。

Ruling: 用户已授权直接实施并明确不用子代理，技能中的重复审批、子代理和强制红绿/提交步骤不适用。

已验证记录见 docs/validation/governance/2026-11-09-closure.md；最终本机版本及六空间实际配置、来源摘要已核对；本轮交付完成。
