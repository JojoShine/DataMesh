# 业务化数据流 Implementation Plan

**Goal:** 默认用业务步骤完成常见数据整理，高级图编排按需打开。
**Architecture:** 复用 GovernanceDataflows 和 NodeEditor；在 governanceFlowEditing 中确认单链并安全接线，增加轻量业务步骤视图。
**Tech Stack:** React / TypeScript / Ant Design，保留 React Flow。
**Spec:** docs/superpowers/specs/2026-10-26-business-dataflow-design.md

## Constraints and review focus

保持接口、权限、版本、实际运行与字体规范；不覆盖现有用户文件。复杂图、断线图、质量分支、多个来源及切换视图时的未保存配置不能被静默改写。

## Tasks

- [x] 在 governance-flow-editing.test.ts 加单链识别、插入/移除、复杂图拒绝测试，观察失败；扩展 governanceFlowEditing.ts 实现最小接线逻辑。
- [x] 在 governance-dataflow.test.tsx 加默认业务操作与实际保存连接测试；实现 GovernanceFlowSteps.tsx，复用表单并切换高级画布；保持高级视图现有测试。
- [x] 跑针对性与完整前端测试、类型检查、构建；检查1920和窄屏，审阅改动并记录结果。

本次按用户“仅在阻塞时提问、常规修改直接完成”的要求在当前工作区执行；工作区现有内容均未跟踪，不移动或提交用户文件。

验证记录见 docs/validation/governance/2026-10-26-business-dataflow.md。代码复核未发现需要修复的问题；高级画布的撤销历史仍属于画布本地状态，切换视图不改变当前处理方案或草稿。
