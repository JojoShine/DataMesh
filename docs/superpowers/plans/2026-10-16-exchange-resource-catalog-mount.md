# 共享交换资源目录、挂载与治理成果交付 Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:executing-plans to implement this plan task-by-task. 用户已选择当前会话直接执行，禁止子代理；无需重新选择执行方式。

**Goal:** 让原始数据和治理成果经编目审核、挂载及责任部门授权后统一交付，并隔离管理、加工和使用权限。

**Architecture:** 扩展现有 Go 工作流和 business_records，继续使用现有事务、幂等、会话及审计；原始对象与治理数据集仍是唯一物理资源引用。目录、挂载和授权分别保存版本快照，通过同一服务端门禁保护申请、审批和交付；React 页面复用当前 Ant Design 工作台。

**Tech Stack:** Go / Gin / GORM / PostgreSQL，现有 Kafka、Flink、Doris、对象存储适配器；React / TypeScript / Ant Design / Zustand / Vite；本地 Docker。

**Spec:** [已确认设计及权限补充](../specs/2026-10-16-exchange-resource-catalog-mount-design.md)

## Global Constraints

- 不使用子代理，不做频繁红绿循环；按业务批次实现并集中验证有意义的权限、事务及流程测试。
- 不新增依赖、中间件、身份系统或通用审批引擎；沿用现有架构及契约路径，不重命名 `/datasets`。
- 一期每个目录只有一个生效挂载，每个资源契约只有一个生效共享目录。
- 管理权限、加工授权、数据使用授权分别授予；角色和部门本身不授予业务内容读取权。
- 无条件共享仍经申请和答复；不予共享不能申请；跨部门加工及转供没有相应依据时阻断。
- 当前只开放真实已验证的原始对象与治理成果查询交付，不模拟 API、文件、订阅、多源融合或行级过滤能力。
- 搜索和表单用 Ant Design 官方 large，表格、标签和行操作使用默认尺寸；区块间距 24px；名称用蓝色链接，命令用标准实体按钮。
- 保留用户数据及修改；Git 已按用户后续要求初始化在 codex/resource-directory，不自动提交、不丢弃现有文件。
- 业务代码需经书面计划审阅后实施；本文件是计划，不表示功能已完成。

## Review Focus

1. 审批人与提交人可能兼任多个角色：无论 admin 或角色组合，都不能自批；资源创建人不能被当作唯一审批人。任务 2、3、5 的集成用例覆盖。
2. 校验后资源、水位或策略发生变化：不能凭过期报告启用；并发挂载及申请审批只能形成一个有效结果。任务 4、5 覆盖版本、并发和回滚。
3. 禁用账号或撤销岗位后会话仍存活：下一次管理、加工和交付必须拒绝；旧游标不获得额外字段或新版本范围。任务 2、5 覆盖。
4. 存量无目录资源及旧授权：不能自动编造合规信息，也不能留下旧 API 绕过通道；失败切换不能开放半套服务。任务 7 覆盖迁移报告和门禁状态。
5. 管理详情、失败诊断和匿名门户可能泄露内容：管理者不自动获得样例，内部共享目录不会变成公开首页资产；真实结果读取不使用数据库写权限。任务 2、5、6 覆盖。

---

## 文件职责

| 范围 | 文件 |
| --- | --- |
| 新模型、迁移和契约 | `internal/modules/workflow/directory_models.go`；`migrations/000003_resource_directory.up.sql`、`.down.sql`；`contracts/states.yaml`；新增 `contracts/resources/directory.schema.json`、`mount.schema.json`；`api/openapi/platform.yaml` |
| 资源职责与权限 | 新增 `internal/modules/workflow/resource_access.go`；修改 `http.go`、`sources.go`、`tasks.go`、`governance.go`、`exchange.go` 的相关入口 |
| 目录与审核 | 新增 `internal/modules/workflow/resource_directory.go`、`directory_review.go` |
| 挂载与共享门禁 | 新增 `internal/modules/workflow/resource_mount.go`、`sharing_policy.go`；修改 `delivery.go`、`runtime.go`、`compose.yaml`、`deploy/docker/doris/fe-start.sh`、`scripts/dev_env.py` |
| 页面与账号配置 | 新增 `web/src/pages/ResourceDirectory.tsx`、`DirectoryWorkspace.tsx`、`DirectoryReviews.tsx`；修改 `App.tsx`、`api/types.ts`、`Catalog.tsx`、`Exchange.tsx`、`Security.tsx`、`common.tsx` |
| 数据切换与验收 | 新增 `scripts/migrate_directory.py`；修改 `bootstrap_business.py`、`smoke/business.py`、`smoke/contracts_http.py`、`scripts/test_identity.py` 和现有 Doris 初始化路径；新增集成、契约及页面测试 |

不拆分现有无关模块，不重做门户、登录或侧栏视觉设计。新文件路径均相对项目根目录。

### Task 1：数据模型、状态与向前兼容迁移

**Files:** 文件职责表第一行；新增 `internal/modules/workflow/directory_models_test.go`、`tests/integration/directory_test.go`，扩充 `tests/contracts/test_contracts.py`。

**Interfaces:**

- 继续使用 `Record`、`Resource`、`Field`，不改变两种 Resource.Type。
- 新建 `DirectoryDefinition`：业务基本信息、分类编码、数据项、共享规则及材料引用；`DirectoryData`：当前生效修订和状态；`DirectoryRevisionData`：不可变提交内容、修订号、提交人及审核结果。
- 新建 `MountData`、`MountRevisionData`：目录及修订引用、资源契约引用、字段映射、策略修订、校验证据、生效状态。
- 新建 `ResourceAssignment`：用户、部门、资源或部门审核范围、能力、期限、授予人及理由；`ProcessingAuthorization`：来源、字段、执行人、目的、期限、派生成果与共享依据。
- 新建 `SharingSnapshot`：目录 ID/修订、挂载 ID/修订、策略修订；作为 `ApplicationData`、`GrantData` 新的可选字段，不移除现有响应字段。

分批集成验证统一通过 `python3 scripts/test_identity.py --run '<测试名正则>'`，由现有入口建立并清理隔离数据库；不向终端打印连接串或凭据。工作流包单元测试沿用 `scripts/check.sh go` 的工具容器。前端检查通过已配置的 Node PATH 执行，不安装新版本。

- [ ] 扩展 business_records 类型约束，加入 `directory`、`directory_revision`、`mount`、`mount_revision`、`review`、`assignment`、`processing_authorization`、`directory_dictionary`；沿用应用角色最小写权限。
- [ ] 目录生效编码加唯一索引；挂载生效目录和资源契约分别加部分唯一索引；审核请求按目标修订、阶段和待审状态限制重复。
- [ ] 明确定义目录修订状态 `draft/department_pending/authority_pending/approved/rejected/withdrawn`、目录状态 `draft/published/withdrawn`；挂载修订状态 `draft/validated/pending/approved/rejected/withdrawn`、挂载状态 `draft/active/suspended/unmounted`。审核通过不自动启用。
- [ ] 实现业务校验：三种共享属性；条件共享须列条件，不予共享须理由与依据；数据项技术名不重复、编码唯一、正式字典来源及版本可核对。无正式字典时可保存草稿、不可正式发码发布；测试编码明确标为本地测试。
- [ ] 更新 JSON Schema、状态契约和 OpenAPI 新增结构；兼容旧请求缺少快照时由服务端解析，不能接受客户端伪造审核快照。
- [ ] 测试 `TestDirectoryDefinitionValidation` 覆盖缺依据、错误编码、重复字段、非法状态；集成测试迁移保留旧记录及索引并发约束；迁移回退发现新增业务记录时明确拒绝，不能删除它们。
- [ ] 检查：`scripts/check.sh contracts`，并通过现有隔离 PostgreSQL 测试入口验证新迁移。记录实际输出，不把未运行测试标为通过。

### Task 2：责任人、岗位范围和处理授权

**Files:** `resource_access.go`；相关 `http.go/sources.go/tasks.go/governance.go`；`Security.tsx`；新增 `tests/integration/resource_access_test.go`。

**Interfaces:**

- `requireResourceCapability(tx *gorm.DB, p identity.Principal, ref Resource, capability string) error`：角色、部门及有效 assignment 同时满足；不能只用 admin 放行。
- `requireProcessingAuthorization(tx *gorm.DB, p identity.Principal, ref Resource, fields []string, purpose string) error`：执行人、字段、来源、目的、期限及派生范围匹配。
- `GET/POST /api/v1/resource-assignments`，`POST /api/v1/resource-assignments/:id/revoke`；`GET/POST /api/v1/processing-authorizations`，`POST /api/v1/processing-authorizations/:id/revoke`，全部复用现有认证、CSRF 和幂等写入口。
- Assignment 能力固定为 `manage_resource/operate_resource/govern_resource/department_review/mount_review/authority_review/share_approve`，不接受任意权限字符串。系统管理员配置岗位；处理授权由来源责任部门指定审批人批准，禁止自批。

- [ ] 将资源责任人和创建人分开，负责人变更只调整审核过的 assignment 并记录理由，不篡改创建审计；处理授权不从消费 grant 推导。
- [ ] 账号管理提供授权范围、期限、部门审核岗位和目录主管审核岗位配置；列表只暴露当前操作者有权查看的配置，普通编目人不能自行配置审批身份。
- [ ] 对涉及目录、资源管理、治理、审批的入口逐个替换宽泛权限判断，保留系统账号管理的 admin 权限；同部门但未获派人员不能管理全部资源。
- [ ] 内容预览与加工作业使用独立处理/使用授权；质量报告返回字段名、失败计数及脱敏诊断，不能返回未授权原始样例；禁用账号或撤销岗位在下一请求生效。
- [ ] `TestResourceCapabilityIsolation` 覆盖仅 admin/operator/governor/provider/auditor、同部门第二账号、过期 assignment、撤销和角色兼任；`TestProcessingAuthorizationDoesNotGrantSharing` 验证加工不授予转供和共享审批权。
- [ ] 通过隔离数据库集成测试；检查已有身份与会话测试，失败时区分预期的明确权限变化和真实回归，不放宽断言来保留旧 admin 越权行为。

### Task 3：目录编制、双级审核与修订发布

**Files:** `resource_directory.go`、`directory_review.go`、`http.go`；`tests/integration/directory_test.go`；OpenAPI。

**Interfaces:**

- `registerResourceDirectory(api, write *gin.RouterGroup)` 注册独立目录 API。
- `GET/POST /api/v1/resource-directories`；`GET /api/v1/resource-directories/:id`；`POST /:id/revisions`、`/:id/submit`、`/:id/withdraw-submission`、`/:id/withdraw`。
- `GET /api/v1/directory-reviews`；`POST /api/v1/directory-reviews/:id/approve`、`/:id/reject`。审核目标引用确切修订与阶段，接收 `expected_version`，退回必填理由。
- `GET /api/v1/directory-dictionaries` 只提供核验过的分类、编码规则及来源；配置由指定目录主管管理并纳入审计。

- [ ] 草稿只对获派本部门编制人员可见；已发布目录向具备内部发现权限的账号提供可公开的业务元数据，排除连接、内部存储和审批材料内容。
- [ ] 部门审核、主管审核逐级推进；提交人不能自批，主管审核人与部门审核人不同；校验角色兼任仍不能绕过。缺负责人依据、评估材料引用或有效编码时不发布。
- [ ] 首次批准原子设置生效修订；变更期间保留旧生效内容，批准后切换并保留差异；下架阻断后续共享但保留历史和变更原因。
- [ ] 已发布未挂载显示待挂载；记录目录状态与服务可用状态，不伪造“可申请”。
- [ ] `TestDirectoryReviewLifecycle` 覆盖退回、撤回、修订失败不覆盖旧版本、缺审核人；`TestDirectoryReviewSeparation` 覆盖自批、多角色、跨部门及并发审核只产生一个结果。
- [ ] 检查新目录及权限集成用例和 OpenAPI 契约；验证匿名调用不能获得内部目录与材料。

### Task 4：挂载、真实校验与原子启用

**Files:** `resource_mount.go`、`directory_review.go`、相关 runtime/归档/质量接口；新增 `tests/integration/mount_test.go`。

**Interfaces:**

- `registerResourceMount(api, write *gin.RouterGroup)`。
- `GET/POST /api/v1/resource-mounts`，`GET /api/v1/resource-mounts/:id`；`POST /:id/revisions`、`/:id/validate`、`/:id/submit`、`/:id/withdraw-submission`、`/:id/activate`、`/:id/suspend`、`/:id/unmount`；审核复用任务 3 的 review 路径。
- `validateMount(ctx context.Context, tx *gorm.DB, revision MountRevisionData) (MountValidation, error)`；证据记录目录、资源及策略版本、检查时间、水位和逐项结果。
- `resolveActiveSharing(tx *gorm.DB, ref Resource, lock bool) (SharingSnapshot, error)`：只有唯一已发布目录和生效挂载才返回，其他情况有可理解的错误码。

- [ ] 选择已有对象或数据集并映射数据项，拒绝重复、缺失、额外字段、跨部门无处理依据、原始/治理资源类型混淆；一期 unsupported 载体只能编目。
- [ ] 原始目标检查真实归档可读与版本，治理目标检查发布、质量、完整性及实际结果路由；不因数据库连接成功而通过业务校验。
- [ ] 校验最长有效期固定为 15 分钟；提交与启用重新核对所有版本，启用再次执行真实可交付性检查。超时、证据过期或状态变化不启用、不部分切换。
- [ ] 将外部运行检查放在短事务外；在写入结果与启用事务内重新锁定和校验修订，版本变化时丢弃旧检查结果。锁顺序统一为资源 → 目录 → 挂载 → 申请/授权。
- [ ] 挂载审批与启用分别执行；同资源生效唯一索引和事务共同防止并发重复；暂停和卸载保存原因，不能绕过授权影响检查。
- [ ] `TestMountValidationAndActivation` 覆盖成功、质量失败、归档断连、unsupported、过期及版本变化；`TestConcurrentMountActivation` 断言仅一个生效挂载及失败事务不覆盖原挂载。
- [ ] 检查集成测试；真实链路测试使用现有本地模板，不以 fixture 的 published 标记代替可交付性证据。

### Task 5：申请、补正、批准与统一交付门禁

**Files:** `sharing_policy.go`、`exchange.go`、`delivery.go`、`governance.go`、`runtime.go`、`catalog.go`；相关 Doris 初始化和 Compose 服务环境配置；`tests/integration/workflow_test.go`、新增 `sharing_policy_test.go`；grant schema/OpenAPI。

**Interfaces:**

- `requireSharingSnapshot(tx *gorm.DB, ref Resource, expected *SharingSnapshot) (SharingSnapshot, error)`：解析并核对任务 4 生效关联，不接收用户构造的生效状态。
- 扩充 `GrantRequest` 可选的申请材料字段：使用依据、场景、范围、共享方式、需求部门负责人同意引用；业务必填检查明确报错，旧协议字段不改名。
- 申请状态新增 `needs_supplement`；`POST /api/v1/applications/:id/request-supplement` 和 `/:id/resubmit`，补正要求和补交记录保留，不静默覆盖历史申请。
- 管理审批通过 `share_approve` 岗位，而非 `OwnerID == actor`；取数继续按实际用户、部门、资源版本、字段、用途和期限验证 grant。

- [ ] 新建申请与批准均检查生效挂载及快照；无条件共享沿用申请答复、不额外增加准入条件，不予共享由服务端拒绝；补正申请人不能更换资源或扩展范围，需变更时重新申请。
- [ ] 审核批准前重新核对账号、岗位、目录、挂载、材料及期限，禁止自批；事务和现有唯一授权索引确保重复或并发批准只形成一个 grant。
- [ ] 两种 `/delivery/v1` 入口及相关预览统一检查快照、字段和有效性；普通数据更新沿用有效授权，原始与治理授权互不继承；旧游标不能跨账号、grant、字段或版本使用。
- [ ] 明确资产发布不代表匿名公开：新增受控的开放审核状态，仅显式通过的资产摘要进入公共门户；现有已发布资产不自动推断为政府公开数据，迁移报告列出影响。
- [ ] 治理结果只读身份单独配置并授权所需表 SELECT；读取缺配置则明确失败，不回退 writer。源数据核对及处理继续沿用其独立服务职责，日志和 HTTP 响应均不输出凭据。
- [ ] 分离停止新授权与阻断后续交付，不删除过去交付记录；处理授权与使用授权撤销分别按其依赖关系执行，未实现的行级过滤明确拒绝挂载。
- [ ] `TestSharingGateCannotBeBypassed` 覆盖旧 API、无挂载、过期快照、暂停、下架、仅 admin、同部门其他账号、字段扩权、原始/治理互访、账号禁用；`TestSupplementAndConcurrentApproval` 验证材料补正、重提与一次授权。
- [ ] 运行相关隔离 PostgreSQL 集成及契约检查，再运行 Go 静态检查和构建；对敏感泄露与只读身份使用真实失败请求验证。

### Task 6：三个工作台页面及资产平台衔接

**Files:** 新增三页；修改 `App.tsx/api/types.ts/Catalog.tsx/Exchange.tsx/Security.tsx/common.tsx`；新增 `web/tests/directory.test.tsx`，必要时扩充 `navigation.test.tsx`。

**Interfaces:**

- `ResourceDirectory`：内部发现、分类树、查询、业务详情及带目录/挂载快照的申请。
- `DirectoryWorkspace`：本部门目录编制与资源挂载页签、修订表单、校验报告及生命周期操作。
- `DirectoryReviews`：按有效岗位展示部门、主管和挂载审核队列，不仅依赖角色字符串判断可点击性；服务端始终最终校验。
- 路由 `/platform/exchange/directory`、`/platform/exchange/workspace`、`/platform/exchange/reviews`；支持 `?dataset_id=` 的资产发起共享预填，缺目标或无权限时显示错误。

- [ ] 读取 `web/AGENTS.md` 与现有设计规范；新类型使用明确 union，复用 `useQuery/useAction/ApplyModal`，不新建请求或状态框架。
- [ ] 实现目录分类检索、共享/可用状态和详情；共享规则与字段用业务化展示，不让使用方阅读内部 JSON 或数据库连接。
- [ ] 实现编目、挂载分步表单，提交前列出缺项；校验失败逐项说明原因，审批通过后仍明确待启用。
- [ ] 实现审核快照与差异、材料引用、补正和退回理由；防止旧页面操作对新修订生效，冲突时刷新并提示。
- [ ] 资产详情展示关联资源编码及共享状态；“发起共享”仅生成或关联草稿，不复制结果数据；原始页和资产页已有申请按钮统一走生效目录检查。
- [ ] 账号范围配置及消费申请/授权详情展示责任部门、批准范围、期限和版本；权限不足、后台错误、空状态及 loading 完整，不在界面制造假成功。
- [ ] `directory.test.tsx` 验证无权限无操作入口、已发布未挂载不可申请、治理资产发起共享不重复采集、补正表单、版本冲突不显示成功；网络请求 mock 不作为真实业务验收。
- [ ] 在已配置 Node 环境运行 `scripts/check.sh web`；通过浏览器检查 1920×1080 和窄屏，核对字号、输入高度、实体操作按钮、蓝色名称链接及长字段横向滚动。

### Task 7：存量补编、启动门禁及本地切换

**Files:** `scripts/migrate_directory.py`、`bootstrap_business.py`、`scripts/test_identity.py`、现有业务 smoke；新增 `docs/validation/directory-migration.md`。

**Interfaces:**

- `scripts/migrate_directory.py --report` 只读列出未编目资源、缺审核岗位、缺字典、缺挂载、旧授权和匿名门户受影响项。
- `scripts/migrate_directory.py --stage-drafts` 幂等创建待补编草稿和责任人待确认记录，不能生成正式编码、通过审核或发放授权。
- `scripts/migrate_directory.py --verify-cutover` 检查可交付资源均具备有效目录、挂载与授权快照，仅返回检查结果，不以“启用开关”跳过审核。

- [ ] 隔离测试数据库先执行新迁移；fixture 为新流程创建明确的岗位、审核和挂载证据，调整测试准备而非降低旧权限断言。
- [ ] 本地切换前生成影响报告并备份元数据；只在本机该项目维护窗口暂停业务入口，验证迁移和补编结果，统一部署已具备服务端门禁的新版本。
- [ ] 本地测试演示编码、审核依据均标为测试。未取得正式字典或材料时保留待补编，不声称完成国标正式发布；用明确隔离的测试配置验收流程。
- [ ] 历史 grant 留存可查，缺新快照时拒绝交付并提示重新申请；迁移失败保留维护状态和可恢复记录，不能退回开放旧绕过路径。
- [ ] `TestDirectoryCutoverReadiness` 验证重复 staging 不重复建记录、没有假审批、旧授权被拒绝、未审核资源不进入可交付清单；补充脚本参数校验与只读报告测试。
- [ ] 执行 `scripts/check.sh tooling`、隔离迁移测试及影响报告检查；禁止直接在用户本地业务数据上运行测试清库或回退删除。

### Task 8：双链路验证及交付记录

**Files:** `scripts/smoke/business.py`、`contracts_http.py`；新增 `docs/validation/resource-directory/report.md`；必要更新接口能力说明。

**Interfaces:** 使用前七任务最终 HTTP 接口与本地真实运行时，不新增产品接口。

- [ ] 原始链路：真实接入与归档 → 目录双级审核 → 挂载校验审核启用 → 第二部门账号申请 → 责任部门答复 → 授权取数，记录操作/资源/授权 ID 与状态，不记录凭据。
- [ ] 治理链路：处理授权 → 已验证模板治理及质量通过 → 资产发布 → 发起共享与挂载 → 跨部门申请答复 → 读取真实治理结果，核对脱敏字段与水位，并确认没有重复采集任务。
- [ ] 反向验证：仅运维、仅治理、仅审核、仅 admin 的账号不能直接取数；创建人与指定审批人不同仍可依法定流程处理；同部门另一账号、原始/治理交叉引用、伪造快照均被拒绝。
- [ ] 验证暂停、资产撤回、授权撤销与账号禁用后的下一次调用失败；验证质量失败及运行断连不产生成功交付，稳定数据更新保持契约授权。
- [ ] 按需运行 `scripts/check.sh contracts`、`go`、`integration`、`web`、`business`。检查脚本若缺运行时或因已知无关错误失败，记录具体阻断，不将全部检查笼统标为通过，也不重写无关业务。
- [ ] 完成浏览器视觉与流程检查；如受 HTTPS 证书或工具环境限制，保留限制说明，不关闭认证或 TLS 验证冒充浏览器通过。
- [ ] 将实际通过项、未支持的资源类型、标准全文核验范围、存量迁移结果和剩余限制写入报告，更新接口 implementation 标记；只向用户报告已验证的完成范围。

## 自查与执行状态

计划覆盖目录和挂载独立状态、单部门原始及治理交付、岗位与数据授权分离、材料补正、只读结果身份、存量迁移和页面规范。多源融合、通用行级引擎、批量下载/推送订阅及法定时限督办仍保持设计中明确的后续范围。

计划已于 2026-10-16 获用户批准，现按任务执行；实际实现、验证与限制以 docs/validation/resource-directory/report.md 为准。执行方式已确定：本会话由主代理直接实现，不启动子代理，不自动提交 Git。
