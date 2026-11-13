# 治理空间与工作入口 Implementation Plan

> **For agentic workers:** 使用 `superpowers:executing-plans`，由当前会话逐项直接实施。用户明确禁止子代理和红绿测试；先实施，再进行针对性验证。不得自动提交 Git。

**Goal:** 提供有真实后端边界的治理空间，让现有来源、模型、处理、质量、成果及血缘围绕一次任务使用，并兼容历史数据。

**Architecture:** 在现有定义、运行和调度上增加空间归属，沿用现有授权、版本、执行和成果证据。增加空间服务和前端空间上下文，按空间过滤并在写入、执行、依赖引用及归档时核实边界。公共标准、元数据及原始来源保持现有共享范围，不建立第二套执行引擎。

**Tech Stack:** Go/Gin/GORM/PostgreSQL；React/TypeScript/Ant Design/React Flow/CodeMirror；无新增依赖。

**Spec:** `docs/superpowers/specs/2026-10-23-governance-workspaces-design.md`，用户已于 2026-10-23 确认。

## Global Constraints

- 不使用子代理、不采用红绿测试流程、不新增框架或绘图库。
- 沿用当前部门、角色和数据授权，不另建一套必须配置的成员审批体系。
- 保存原有定义编号、版本、运行、成果内容、证据及共享引用；不覆盖存量配置。
- 默认页面展示业务对象、状态、结果和动作；学习内容主动查看，可跳过、可重看。
- 数据流保留独立页面和面包屑，不恢复重复页面大标题；简单操作使用单层 dialog，命令统一右下角。
- 本轮不实现内部“保存并运行”合并事务、数据流直接定时或成果报告复用优化；它们属于第二、三轮，原有发布和运行约束继续生效。
- 验证先针对本次功能，再按受影响范围扩大；主环境部署使用可恢复备份、严格证书校验和业务数据对比。

## Review Focus

1. 同部门两个空间的对象混用、遗漏空间参数或伪造参数：选择器及后台同时校验，不能通过旧接口跨空间修改。对应任务 2、3。
2. 历史调用、旧链接及新部门：旧记录归历史空间，新部门首次旧式调用也能安全创建历史空间，历史证据保持。对应任务 1、2、6。
3. 归档与运行/调度并发：不能出现已归档空间又启动新任务；有在途运行时归档明确受阻。对应任务 4。
4. 共享成果或来源权限撤销：空间成员身份及统计入口不扩大数据访问范围，旧运行的结果读取继续复核。对应任务 3、5。
5. 空间切换时的未保存编辑及跨账号缓存：不得混入前一空间的配置、列表或请求结果。对应任务 6、7。

---

## 第一轮产品范围

平台入口：治理空间、元数据、公共标准、全部成果。空间内：概览、来源、模型、开发与编排、数据质量、成果、血缘记录。开发与编排仍通过明确子入口打开数据流、工作流及现有调度；运行记录就地可达。原有主题域、术语、质量模板、整改、告警、物理建表、生命周期等保留在有关页面的高级入口及兼容链接中，不删除业务能力。

当前按 actor 限制的运行、日志、剖析及质量记录继续按 actor 限制；空间概览不能把本部门其他人的运行细节意外公开。全局汇总和全部成果入口只聚合当前账号原本可见的内容。

## Task 1: 空间结构与历史迁移

**Files:** 新增 `migrations/000028_governance_workspaces.up.sql`、`migrations/000028_governance_workspaces.down.sql`；修改 `internal/modules/workflow/governance_catalog_http.go`、`governance_runs.go`、`governance_schedule.go`；新增 `tests/integration/governance_workspace_migration_test.go`。

**Interfaces:** 新建 `governance_workspaces`、`governance_workspace_sources`。定义头、运行、调度新增 `workspace_id`；自动成果归属从 `governance_outputs.run_id → governance_runs.workspace_id` 得出，不改不可变证据。历史外部成果经 `governance_workspace_legacy_datasets(workspace_id,dataset_id)` 映射，保留业务记录原内容。

- [ ] 实施空间表：`id text` 主键、`department_id text`、`name` 1–200 字、`description` 最长 4000 字、`owner_id` 可空用户外键（历史空间可无人）、`state` 仅 active/archived、`is_legacy boolean`、`max_concurrent_runs` 默认 2 且限 1–10、`version integer`、创建/更新时间。每部门历史空间有部分唯一索引；运行索引覆盖空间、状态和时间。
- [ ] 对已有用户、治理定义、运行、调度和数据集涉及的部门建立历史空间，ID 使用 `md5('governance-workspace:' || department_id)`，名为“历史治理”。模型、数据流、工作流和质量模板归历史空间；domain/term/standard/metadata 定义保持 `workspace_id IS NULL`，维持既有共享规则。所有历史运行和调度按原部门归入历史空间。新增数据库约束核对空间部门一致及种类归属，不改 `governance_versions` 的 JSON、状态或编号。
- [ ] 为旧应用省略归属的 INSERT 提供仅指向本部门历史空间的兼容默认处理；历史空间创建函数限制搜索路径、使用数据库内置 md5，不授予应用越部门写入或删除权限。新空间禁止通过省略归属“继承最近空间”。来源关联主键为 `(workspace_id, resource_type, resource_id, contract_version)`，固定来源引用并记录关联人员和时间，增加 `active boolean DEFAULT true`。remove 仅标记 inactive 并保留审计，再次关联恢复 active；不复制连接或凭据。
- [ ] 实施降级保护：有非历史空间或其数据时 down migration 拒绝破坏性撤销；只有全部仍为历史数据时才能移除新增结构。保留旧应用对原字段的读写兼容，但正式回退期间停止新空间写入和调度，防止旧服务遗漏隔离及归档判断。
- [ ] 代码完成后添加 `TestGovernanceWorkspaceMigrationPreservesEvidence`、`TestGovernanceWorkspaceLegacyInsertCompatibility`、`TestGovernanceWorkspaceDownRejectsNewSpaces`：比较迁移前后版本 JSON、数据集 body、成果 proof 和已有授权；验证新增部门默认历史空间、重复准备无重复空间、应用角色无删除授权。
- [ ] 在独立数据库验证：`python3 scripts/test_identity.py --run 'TestGovernanceWorkspace(Migration|LegacyInsert|Down)'`。必须实际执行且无跳过，主环境不用于迁移试验。

## Task 2: 空间接口与授权边界

**Files:** 新增 `internal/modules/workflow/governance_workspace.go`、`governance_workspace_scope.go`、`tests/integration/governance_workspace_test.go`；修改 `internal/modules/workflow/governance_catalog_http.go`、`api/openapi/platform.yaml`、`tests/contracts/test_governance.py`。

**Interfaces:** `GovernanceWorkspace`；`governanceWorkspaceRead(tx *gorm.DB,p identity.Principal,id string,forWrite bool)(GovernanceWorkspace,error)`；`governanceLegacyWorkspace(tx *gorm.DB,department string)(GovernanceWorkspace,error)`；`registerGovernanceWorkspaces(api,write *gin.RouterGroup)`；将既有依赖验证函数扩为 `governanceValidateReferences(tx *gorm.DB,p identity.Principal,department string,workspaceID string,v GovernanceDefinition) error`，所有调用者传对象真实空间，公共定义传空字符串。新增 GET/POST `/governance/workspaces`、GET `/governance/workspaces/:id`、POST `/:id/update`、POST `/:id/archive`；沿用 mutate、CSRF、审计、幂等键和版本冲突模式。

- [ ] 实施分页、搜索和按 state 过滤；创建只必填名称，默认部门/责任人为调用者。修改与归档需原 owner 或 admin；普通同部门 governor 可读取并沿既有规则操作空间对象。其他部门不得读取空间，非法格式返回 400，越权维持现有 403 语义。空间管理请求不接受任意 department_id。
- [ ] update/archive 使用 `base_version` 做 CAS，错误码分别为 `GOVERNANCE_WORKSPACE_VERSION_CONFLICT`、`GOVERNANCE_WORKSPACE_ARCHIVED`、`GOVERNANCE_WORKSPACE_BUSY`；错误说明给出刷新、返回活动空间或等待任务完成的下一步。archive 的事务和运行互斥逻辑由任务 4 补全后才开放写路由。
- [ ] 明确请求归属：现有创建类 JSON 增加可选 `workspace_id`；GET 列表增加可选 `workspace_id`，普通旧式列表默认历史空间，公共定义仍按原规则查询。已有对象的 detail/history/diff/impact 可按 ID 读取并返回所属空间，但不因此增加权限；所有修改对新空间对象必须显式匹配 workspace_id。历史对象旧式请求继续兼容；不允许通过省略参数修改新空间对象。布局等已有 payload 同样校验请求上下文，排队和后台调用以对象真实归属为准。
- [ ] 将空间归属加入定义头与版本响应，公共定义返回 `workspace_id: null`；调用者不能通过新建版本改对象归属。后台私有对象引用必须同空间；公共标准/domain/term/metadata 引用保留既有权限与版本要求。保存新空间对象拒绝引用历史或其他空间的私有模型、模板、数据流和工作流。首轮不新增“整图复制”功能，选择器与错误说明引导用户建立本空间定义。
- [ ] 添加完成后回归：`TestGovernanceWorkspacePermissions`（角色、部门、owner 管理、admin 仍必须匹配作用域）、`TestGovernanceWorkspaceDefinitionScope`（跨空间引用、伪造、缺失、不可迁移归属、公共固定标准引用）、`TestGovernanceWorkspaceLegacyLinks`（历史 detail/publish 与原地址继续有效）。
- [ ] 验证：`python3 scripts/test_identity.py --run 'TestGovernanceWorkspace(Permissions|DefinitionScope|LegacyLinks)'`，并执行 `scripts/check.sh contracts`，新增字段可选且现有接口保持兼容。

## Task 3: 来源、执行、预览及成果归属

**Files:** 修改 `internal/modules/workflow/governance_dataflow_http.go`、`governance_flow_preview.go`、`governance_flow_parameters.go`、`governance_flow_layout.go`、`governance_profile.go`、`governance_workflow.go`、`governance_workflow_extensions.go`、`governance_model_deployments.go`、`governance_runs.go`、`governance_outputs.go`；新增 `internal/modules/workflow/governance_workspace_sources.go`、`tests/integration/governance_workspace_execution_test.go`。

**Interfaces:** GET/POST `/governance/workspaces/:id/sources`、POST `/:id/sources/remove`；关联输入 `{resource: Resource}`。`governanceWorkspaceReference(tx *gorm.DB,p identity.Principal,workspaceID string,resource Resource) error` 检查关联及现有元数据/来源授权。正式 run/profile/preview 的输入增加可选 workspace_id；恢复与重试继承原空间，前端显式传递。

- [ ] 实施来源关联：只有当前有权见到的实际资源版本可关联，参数引用不包含秘密；取消关联需先确认本空间定义依赖，存在引用返回 409。其他空间成果只能通过显式选择具体 `governed_dataset` 资源加入，再复核现有读取/加工权限。未发布或不可用的他空间成果按现有资源规则拒绝，不能把别人的运行编号伪造成可读来源。
- [ ] 在保存、发布、排队及执行前校验本空间依赖、来源关联、活动状态及现有权限。新流程/工作流子任务继承空间；工作流 artifact 绑定核对父子真实空间。预览没有保存定义也必须有明确空间；旧式预览和剖析走历史空间。试运行仍限 20 行且不注册正式成果。
- [ ] 覆盖取消、重试、恢复、布局读写、物理建表及结果记录/样本接口；保留 actor 校验、敏感处理、当前来源权限重查与代码沙箱，不用空间成员身份代替。物理目标写入仍需既有目标授权，空间不会自动获得外部数据库写入权。
- [ ] 运行以 `GovernanceRun.WorkspaceID` 保存归属，队列和结果保存不依赖浏览器当前空间；自动成果从真实运行导出归属，不修改 `governance_outputs.dataset_body`。单次运行 staging 继续使用运行标识隔离。旧运行定义和证据不重写 workspace 字段。
- [ ] 实施后添加 `TestGovernanceWorkspaceExecutionScope`（两个空间各跑真实流，结果与临时行不交叉）、`TestGovernanceWorkspacePreviewScope`（预览归属、样本保护、不发布）、`TestGovernanceWorkspaceCrossSpaceSource`（显式授权成果引用、撤权后拒绝）、`TestGovernanceWorkspaceRetryAndLayoutScope`（旧式绕过/伪造、重试继承及布局隔离）。
- [ ] 先跑针对性隔离数据库验证，再覆盖原有 `TestGovernance.*(Dataflow|Workflow|Profile|Layout|Script)`；使用 `python3 scripts/test_identity.py --run '<对应正则>'`。代码运行器验证使用现有隔离 QA 入口，不能在主环境试写虚构来源。

## Task 4: 调度、空间并发和归档互斥

**Files:** 修改 `internal/modules/workflow/governance_schedule.go`、`governance_runs.go`、`governance_workflow.go`、`governance_workspace.go`；新增 `tests/integration/governance_workspace_lifecycle_test.go`。

**Interfaces:** `governanceWorkspaceLock(tx *gorm.DB,workspaceID string)(GovernanceWorkspace,error)`；`governanceWorkspaceRunSlot(tx *gorm.DB,workspaceID string) (bool,error)`。空间行锁在排队、worker claim、调度触发和 archive 中采用一致锁序，跨空间顺序按 ID 排序；现有全局锁、用户限额与租约不取消。

- [ ] 调度归属从被引用工作流确定并校验；新空间创建/修改调度显式核对 workspace_id。首轮仍引用工作流，页面说明只改变归属和就地入口。列表、触发历史、暂停/恢复和参数修改均保持作用域及 actor 限制。
- [ ] 默认每空间同时执行的非 workflow 实际运行最多 2 个（可在空间管理中调整到 1–10）；workflow 控制实例不占这个执行名额，子数据流、profile、preview 占用。先按活动空间/未满额筛选再取 queued 候选，避免一个空间满额让其他空间的队列饥饿；失效租约仍按现有恢复流程处理。用户/全局已有上限继续共同生效。
- [ ] 归档事务锁定空间后检查 queued/running 的所有根/子运行；非零返回 409 和活动数量，不变更状态。没有在途运行时原子暂停本空间调度并标记 archived。持相同锁的排队/调度在归档后拒绝新任务。取消自己的在途运行仍可用； archived 对象可读，不能编辑/发布/布局写入/启动/恢复调度。成果外部已获准使用不因归档自动撤权，不执行来源或成果删除。
- [ ] 添加完成后的真实并发验证：`TestGovernanceWorkspaceArchiveRace`（归档与新运行/调度同时提交）、`TestGovernanceWorkspaceConcurrencyFairness`（A 满额时 B 被 claim）、`TestGovernanceWorkspaceArchivePreservesOutputs`（调度只停本空间、证据/来源/他空间不变）。
- [ ] 验证：`python3 scripts/test_identity.py --run 'TestGovernanceWorkspace(Archive|Concurrency)'`；必须启用现有 race 检查且没有因缺库跳过。

## Task 5: 空间概览与所有关联视图范围

**Files:** 新增 `internal/modules/workflow/governance_workspace_overview.go`；修改 `internal/modules/workflow/governance_operations.go`、`governance_dashboard.go`、`governance_quality_templates.go`、`governance_quality_issues.go`、`governance_lifecycle.go`、`governance_alerts.go`、`governance_impact.go`、`governance.go`、`catalog.go`；新增 `tests/integration/governance_workspace_overview_test.go`。

**Interfaces:** GET `/governance/workspaces/:id/overview` 返回 `{workspace,source_count,model_count,dataflow_count,workflow_count,run_states,recent_runs,recent_results,quality_summary,observed_at}`。最近项最多各 10，列表分页仍走原接口。统计仅按原权限范围计算；没有检查时质量比例为 null，无数据与加载失败分开。

- [ ] 实施聚合，以已核实的空间 ID 为查询条件；定义计对象不计历史版本，成果计独立运行产物，报告范围明确。概览只返回用户本来有权见到的记录和安全摘要，结果 payload 仍由详情接口复核后读取。
- [ ] 对空间下运行、成果、质量报告/趋势/问题、血缘/影响、模型建表、生命周期和调度列表增加统一的后端范围核对。成果列表/详情返回真实 workspace_id；全平台“全部成果”按现有授权聚合，而非任意绕过。共享交换及资产平台现有流程继续按原权限办理。
- [ ] 告警规则增加可空 workspace_id：存量 NULL 仍表示原来的部门/actor 范围，新规则可绑定空间；规则扫描/事件归属按真实 run 校验，不能影响他空间。保留策略按真实 flow 归属查询，迁移计划不得借用当前空间修改他空间挂接；跨空间成果引用仍明确展示下游并保护清理。
- [ ] 添加 `TestGovernanceWorkspaceOverviewScope`（全部列表/分页/计数不混空间）、`TestGovernanceWorkspaceActorPrivacy`（同部门他人日志和剖析不泄漏）、`TestGovernanceWorkspaceSourceRevocation`（撤权后报告/成果读取受阻）、`TestGovernanceWorkspaceMaintenanceScope`（告警、保留、迁移及跨空间依赖保护）。
- [ ] 验证：`python3 scripts/test_identity.py --run 'TestGovernanceWorkspace(Overview|ActorPrivacy|SourceRevocation|Maintenance)'`，再跑既有治理看板、运维和生命周期回归。

## Task 6: 空间上下文、导航和入口

**Files:** 新增 `web/src/api/governanceWorkspace.ts`、`web/src/components/GovernanceWorkspaceContext.tsx`、`web/src/pages/GovernanceWorkspaces.tsx`、`web/src/pages/GovernanceWorkspaces.css`；修改 `web/src/app/App.tsx`、`web/src/api/governance.ts`、`web/src/app/navigation.ts`（仅需要的导航工具）、`web/tests/navigation.test.tsx`；新增 `web/tests/governance-workspaces.test.tsx`。

**Interfaces:** `GovernanceWorkspace`、`GovernanceWorkspaceOverview`、`GovernanceWorkspaceSource` 严格 TS 类型匹配任务 1/5；`useGovernanceWorkspace()` 返回当前已验证空间、加载/错误状态与 `navigateWithinWorkspace(path:string):void`。复用现有 URL/query 路由：`/platform/governance/workspaces` 为空间列表，`?workspace_id=<id>` 用于现有模型/数据流等页面；空间首页 `/platform/governance/workspaces?workspace_id=<id>`，空间概览为默认内容。

- [ ] 顶部导航使用四个主要入口；空间内保留七项清楚分工的导航。现有高级页面保留可达链接，平台切换、白底 Logo、权限过滤、面包屑和浏览器前进后退正常；不创建额外的路由框架。面包屑在空间内表达“数据治理 / 空间名 / 当前功能”。
- [ ] 实施空间列表及单层新建 dialog，只必填名称，选填用途；保留查询、状态和错误反馈。空间详情/归档通过同一 dialog 内容切换，不叠加确认框；有在途运行时提供相应记录入口。旧 definition_id/run_id 深链接先核实对象归属再进入空间，不先加载错误空间的列表。
- [ ] 上下文按 session epoch 和 workspace_id 重新加载，切换空间清除本地结果和选择状态；草稿未保存时提供继续编辑/保存后切换/明确放弃，复用现有数据流保护逻辑，不额外叠 dialog。浏览器后退同样保护，取消时恢复原位置。异步返回不能覆盖新空间，使用已有 abort/请求序列模式。
- [ ] 完成后检查 `governance-workspaces.test.tsx`：新建默认项、空状态、API 失败、跨空间切换未保存保护、晚到请求、账号切换、旧深链接及归档反馈；`navigation.test.tsx` 覆盖四个入口和三级面包屑。
- [ ] 验证：`npm --prefix web run check` 与 `npm --prefix web test -- tests/governance-workspaces.test.tsx tests/navigation.test.tsx`。

## Task 7: 业务页面接入与主动帮助

**Files:** 修改 `web/src/pages/GovernanceCatalog.tsx`、`GovernanceDataflows.tsx`、`GovernanceWorkflows.tsx`、`GovernanceResults.tsx`、`GovernanceOperations.tsx`、`GovernanceQualityIssues.tsx`、`GovernanceQualityTrends.tsx`、`GovernanceLineage.tsx`、`GovernanceAlerts.tsx`、`GovernanceLifecycle.tsx`、`Governance.tsx`；修改 `web/src/pages/GovernanceProfiles.tsx`；修改 `web/src/components/GovernanceReferenceSelect.tsx`、`GovernanceModelDeployment.tsx`、`GovernanceLearning.tsx`、`GovernanceContext.tsx`。

**Interfaces:** 选择器从 context 获取空间；私有 kind 的列表/详情引用与保存请求显式传递 workspace_id，公共 kind 保持 global。新增空间来源视图可复用 `GovernanceWorkspaces.tsx` 下独立来源组件，关联资源后再在数据流中选择。导航跳转持续携带空间上下文。

- [ ] 空间概览默认展示来源、最近运行、质量摘要和成果；按真实状态给出继续编辑/查看问题/查看成果入口。来源选择同时展示资源名称、类型和版本，同名资源不混淆。首次进入不自动打开教学弹窗。
- [ ] 为保存、发布、运行、代码预览、剖析、重试/恢复、布局、调度及管理请求加空间归属；列表、选择器和关联跳转同样带上下文。数据流仍为左右独立工作页；新空间只提供本空间模型/模板/数据流。只读 archived 状态和后台拒绝一致，不能仅禁用部分按钮。
- [ ] 移除默认页面的常驻 `GovernanceGuide`、大段 `GovernancePurpose` 和重复教学提示；由明确“帮助”入口在同一单层 dialog 中主动查看。字段业务说明、用途、质量结果、权限限制和实际错误继续就地展示。首次学习不再成为操作前置，已学习状态不作为权限或发布凭据。
- [ ] 质量、血缘和成果在空间内可直接进入，数据流运行结果链接到本次成果。高级能力放到对应对象/页面的管理入口，不删规则、模板、整改、建表、告警、生命周期、主题域和术语的原有能力。首轮保留原有保存/发布/运行命令及真实文案，第二轮再减少这些操作。
- [ ] 实施后更新既有数据流/模型/成果等交互用例，新增 `web/tests/governance-workspace-journey.test.tsx`：无教学引导完成“空间→来源→模型→数据流→运行→成果”；只有学习入口能打开教学；条件不满足时有明确处理入口；来源/模型选择器不会混入另一空间。
- [ ] 验证 `npm --prefix web test -- tests/governance-workspace-journey.test.tsx tests/governance-dataflow.test.tsx tests/governance-catalog.test.tsx tests/governance-journey-production.test.tsx` 后执行 `npm --prefix web run build`，并按范围执行完整前端回归。核对 1920×1080、1440 宽及 540×960，底栏右对齐、内部滚动与无重复页面大标题。

## Task 8: 第一轮端到端、迁移演练与主环境部署

**Files:** 新增 `scripts/smoke/governance_workspaces.py`、`docs/validation/governance/2026-10-23-workspaces.md`；在 `.local/` 新建本轮部署辅助脚本，复用既有备份恢复、镜像和固定证书校验逻辑，显式升级 schema 至 28。更新 `docs/design/interface-standard-v1.md` 与必要接口说明。

**Interfaces:** smoke 接受独立环境 origin/证书/安全认证配置，打印非敏感验收摘要和真实运行编号；不把密码、完整数据或敏感样例输出到日志。主环境只执行已授权操作，隔离环境创建客户/商品示例并验证跨空间写入拒绝。

- [ ] 在独立迁移库建立客户/商品空间，跑通各自的真实模型、图形/代码处理、质量和成果；验证取消、归档、重试、定时并发和跨空间显式成果引用。记录任何沿用的发布约束，不把后续轮次能力标记成已交付。
- [ ] 跑 `scripts/check.sh go`、`scripts/check.sh contracts`、治理相关隔离集成检查及前端检查；记录实际通过/跳过项目，失败必须定位并修复，不能禁用权限或校验使其通过。完成功能自审，不使用独立代理。
- [ ] 主环境部署前确认没有正在执行的治理任务，备份全部数据并隔离恢复验证。对定义头、运行新增空间列允许预期变化；对 `governance_versions`、业务记录 body、输出 proof、授权与挂接逐项核对内容不变。记录迁移报告、历史空间数、存量归属数及歧义记录，不凭名称自动拆业务场景。
- [ ] 先迁移再重建应用，代码运行器镜像不变；严格验证健康、schema=28、新接口、旧链接、当前客户治理流和静态资源。失败时保持新空间写入关闭并使用已演练回退路径；有新空间数据后不能直接破坏性降库。浏览器证书提示不代为越过，不再建立被拒绝的认证代理。
- [ ] 清理本轮临时服务/测试库，保留验收证据和恢复资料。最终报告明确第一轮已交付能力、主环境状态和第二/三轮尚未实施的简化项，不宣称整体融合已经全部完成。

## 自审结论

本计划覆盖已确认设计的首轮“空间基础与工作入口”，为第二轮的保存并运行、模型便捷生成、直接数据流定时，以及第三轮的成果交付整合保留现有接口基础。本轮有真实边界、业务入口和迁移验证，可独立部署；后三项不通过临时隐藏后台限制来提前交付。
