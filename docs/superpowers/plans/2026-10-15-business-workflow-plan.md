# 业务页面与协同流程实施计划

> 使用 superpowers:executing-plans 在当前会话顺序实施。用户已要求直接推进，不使用子代理，不逐项红绿循环；集中验证，不自动提交。

**Goal:** 在既有共享身份和 Docker 基线上交付可使用的六组业务页面，并贯通真实来源、治理检查、发布、跨部门申请审批、受控交付、撤权和审计。

**Architecture:** 延续已确认的四平台分工与模块化 Go 后台。新增 workflow 按来源、任务、治理、目录和交换拆文件；业务状态写入 PostgreSQL。前端 React/antd/Zustand/Vite/TypeScript，通过同源 HTTPS API 使用现有 Cookie。复用已验证的本地处理模板，禁止 UI 伪造组件执行成功。

**Spec:** `docs/superpowers/specs/2026-10-15-datamesh-phase1-design.md` 与原 V0.4。以下是本次落实的操作边界，不修改一期 P0。

## 约束与边界

- 增量迁移，不覆盖身份数据；新增角色能力 admin/operator/governor/provider/consumer/auditor，平台能力和部门检查仍生效。
- 数据源凭据 AES-GCM 加密，不返回前端；网络目标限于运维允许的本地来源。来源连接与 CDC 条件分别核实。
- 当前计算模板为已实测 PostgreSQL customers→TRIM/decimal→Doris，不接受任意 SQL。MySQL/Oracle 可登记、检查、绑定已有采集器；未配置结果模板时明确阻断结果发布。
- 操作号、请求散列和幂等键持久化；并发审批和授权同事务，自批拒绝。外部结果未知时保留 unknown。
- 有界原始归档真实读取 Kafka、写 S3 和清单；质量、结果和归档进度独立。当前最多 10000 条样例范围，超限阻断，持续归档仍是后续专项。
- 发布和治理交付核对真实结果、规则和归档截止水位； raw_object 单独授权，不依赖治理发布。
- 仅本机使用，不发布云网站；Go 同源提供 Vite 构建结果，不引入额外线上身份/网关。

## Review Focus

1. 自批、跨部门和字段越权、到期/撤权/下架后请求被拒，含直接 API。
2. 相同幂等键不同请求体冲突；审批并发至多一份授权，超时不盲目新建。
3. Doris/归档/质量失败不伪装通过，旧游标不能跨资源、授权和新水位复用。
4. 来源密码、会话和敏感字段不泄露；用户不能指定任意运行 SQL 或组件地址。
5. 前端失败和空列表区分，切换身份清缓存，过期响应不能覆盖新选择，轮询有界且可见时运行。

## Task 1 业务状态与授权

Files: migrations/000002_workflow.*, internal/modules/identity, internal/modules/workflow/{models,http,operations}.go。
- [ ] 新增角色、业务表及索引，扩展身份 DTO；保持已建账号有效。
- [ ] 实现部门/角色检查、严谨输入、服务端分页白名单、幂等事务和操作查询。
- [ ] 集成测试覆盖角色边界、幂等冲突、竞争审批。

## Task 2 接入与运行

Files: workflow/{sources,tasks,runtime,archive}.go，scripts/bootstrap_business.py，compose.yaml。
- [ ] 数据源登记/修改、对象元数据、真实连接与 CDC 条件检查；加密凭据。
- [ ] 绑定现有采集作业，启动暂停恢复查询真实 Connect/Flink 状态；未支持重放策略明确拒绝。
- [ ] Kafka 有界归档、S3 校验与清单；保存来源和两路状态。

## Task 3 治理、目录与交换

Files: workflow/{governance,catalog,exchange,delivery}.go。
- [ ] 规则版本、实际质量评估、表级血缘、源/结果核对与发布门禁。
- [ ] 目录登记、发布下架；原始资源元数据独立提供申请入口。
- [ ] 申请、批准驳回撤回、授权到期撤销；字段投影脱敏、签名游标与每请求复核。
- [ ] 审计筛选和真实接口闭环验证。

## Task 4 业务页面

Files: web/{package.json,package-lock.json,vite.config.ts,src/...}，Go 静态服务。
- [ ] 登录、身份及平台导航；六组列表/表单/详情与错误、权限、空态。
- [ ] API 类型、统一请求、取消旧请求、有界操作查询与刷新；无浏览器秘密持久化。
- [ ] 检查、测试与构建，实际浏览器验证提供方和使用方业务流。

## Task 5 交付验证

Files: tests/integration/workflow_test.go，scripts/smoke/business.py，README.md，docs/validation/business/report.md。
- [ ] 迁移、Go vet/build/race、前端类型/请求生命周期测试/build。
- [ ] 真正跨部门账号完成预检→任务→质量→归档→发布→申请→审批→交付→撤权，核对拒绝边界和审计。
- [ ] 更新运行说明与未覆盖的全量一期验收，不用本批闭环代替全部 AT 用例。

## 用户确认的页面结构

- 默认进入公开首页：顶部四个模块入口，下方资产卡片、搜索和筛选，不直接进入后台。
- 登录按钮进入独立登录页，登录成功统一返回首页，用户再选择平台。
- 平台内部采用左侧菜单、右侧内容，提供返回首页和平台切换。
- 未登录仅公开已发布资产摘要；业务操作和数据交付使用共享身份及独立资源授权。
