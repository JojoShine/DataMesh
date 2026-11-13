# 治理能力扩展 Implementation Plan

> 执行方式：superpowers:executing-plans；用户明确要求不用子代理、减少重复红绿测试，不创建提交或改写已有工作。

**Goal:** 完成持续监测、落标、发布、主数据、指标、场景六项真实能力及现有平台联动。
**Architecture:** Go既有授权/空间/版本/运行架构；React/Ant Design既有界面；增量PostgreSQL迁移。
**Spec:** docs/superpowers/specs/2026-11-09-governance-expansion.md

## Global Constraints
- 不使用子代理，不放宽授权，不执行任意用户SQL；保留业务数据和既有接口。
- 先阅读相关实现，分模块集中验证；只在新变更或失败时重跑。
- 复用现有分类、控件、分页、版本和工作流；审批不冒充已运行证据。

## Review Focus
权限撤销后的定时任务；空间/部门越权；采样被误标全量；并发修改基线或版本；敏感数据和凭据随模板/报告泄漏。

### Task 1 持续数据监测
- [x] 新增 governance_monitor.go、governance_monitor_worker.go 与迁移43，复用 enqueueGovernanceProfile / ProfileReport，定时和手动共用有界入口；状态含实际运行和固定基线。
- [x] 新增前端 GovernanceMonitoring.tsx，配置来源与阈值、周期、基线、历史异常；沿用告警入口分类。
- [x] 测试：实际剖析、行数/空值/重复/时效变化、未知时效、并发与权限撤销。

### Task 2 标准落标
- [x] governance_standard_coverage.go 读取既有模型与元数据标准引用、授权可见字段；复用现有标准校验执行生成差异报告。
- [x] 前端在标准与规范内展示覆盖、未覆盖、约束差异，确认映射后建立新版本。
- [x] 测试：固定标准版本、码值差异、敏感值与不可见对象不泄漏。

### Task 3 变更发布
- [x] governance_release.go 固定差异、影响、试运行及候选版本；复用既有发布动作，独立审核与恢复记录。
- [x] 前端检查与交付下的发布办理；测试陈旧证据、本人审核、历史引用和恢复边界。

### Task 4 主数据
- [x] governance_master.go / governance_master_execution.go：实体定义、精确匹配、冲突候选、人工确认、编号映射及版本。
- [x] 前端准备数据下的实体与候选处理，复用授权输入及治理成果；测试跨来源冲突、并发确认、撤权及历史追溯。

### Task 5 指标口径
- [x] governance_metrics.go：指标定义转换为既有聚合数据流，固定口径及执行版本。
- [x] 前端标准与规范中维护来源、维度、过滤、统计及有效范围，查看实际结果；测试精确数值、空集、权限、口径版本。

### Task 6 场景方案
- [x] governance_scenarios.go：固定流程与输入契约，重新绑定授权来源并实例化正常数据流/工作流。
- [x] 前端处理数据内维护方案、实例化与验收要求；测试输入不兼容、原授权不继承、引用失效。

### 集成与交付
- [x] 更新OpenAPI、操作指南；后端/前端/隔离集成检查，桌面与窄屏检查。
- [x] 作者独立复核六项需求、数据授权和版本边界；备份后升级本机并核对原数据、资产与服务。

实现说明：主数据执行集中在 governance_master.go；指标与场景复用逻辑集中在 governance_capabilities.go，不另建平行执行引擎。主数据确认保存实体历史，不自动写回来源或公开共享。监测异常在观察记录中复核关闭；既有运行失败通知继续沿用治理告警。
