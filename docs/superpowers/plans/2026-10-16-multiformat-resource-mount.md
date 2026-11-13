# 多类型资源挂载与真实交付 Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:executing-plans to implement this plan task-by-task. 用户已要求当前会话直接实施，不使用子代理，不执行频繁红绿循环，不自动提交 Git。

**Goal:** 在目录行内完成数据库、Excel 数据、文件、API 的挂载、审核及真实授权交付。

**Architecture:** 延续 Go/Gin 单体、现有 Record/挂载修订/审核机制及 Docker 部署。新增类型适配及私有存储，统一授权门禁；保留既有 CDC 和治理成果查询。网络和文件 IO 在短事务之外执行，确认写入时再次检查版本和权限。

**Tech Stack:** Go 1.25.5、Gin、GORM/PostgreSQL、现有 PostgreSQL/MySQL 驱动、RustFS/S3、React/TypeScript/Ant Design 6；Excel 使用 Go excelize/v2，安装前核对官方支持的 Go 版本并锁定兼容版本，不自行实现 OOXML 解析器。

**Spec:** `docs/superpowers/specs/2026-10-16-multiformat-resource-mount-design.md`（用户已确认：Excel 仅导入实际数据，目录结构为唯一依据）。

## Global Constraints

- 无新平台，无独立挂载菜单，不重写现有治理/CDC 流程；数据库通用适配首批 PostgreSQL、MySQL。
- 不改变既有 raw_object / governed_dataset 引用及旧授权的 query 语义；迁移只增不删。
- Excel 首批 .xlsx，20 MiB、100 列、5 万数据行；不执行公式、外部链接或宏。
- 文件默认 50 MiB，显式允许类型；私有存储，下载不暴露永久对象地址或凭据。
- API 首批只读 GET/查询 POST 和 JSON 响应；明确允许的网络目标，无任意 URL 转发。
- 普通用户依部门/资源/交付动作授权；admin 保留已批准的全部业务权限，状态和数据有效性仍检查。
- 页面使用现有蓝色 AntD、居中弹窗、固定步骤/操作栏、语义化 placeholder；输入遵循官方 large 尺寸，行操作为实体按钮。
- 不重新生成设计图，不派子代理，不自动提交 Git。每个交付单元做一次相关验证，失败时定位修复，不为过程反复制造失败。

## Review Focus

1. 并发确认、重试和服务重启不能重复导入或留下可用的半成品（任务 3、4）。
2. 同名表不同 schema、引号标识符及 schema 漂移不能查询错表或扩宽字段（任务 2）。
3. Excel 前导零、超长数字、空值、日期、重复表头和解压膨胀不能被静默改写（任务 3）。
4. DNS 变化、重定向和参数构造不能使 API 代理访问未批准目标（任务 5）。
5. 已发起下载/调用与撤销授权并发时，交付前重新检查，审计区分成功、拒绝和中断，不承诺收回已发送字节（任务 4、5、6）。

## 文件与接口划分

保留 `resource_mount.go` 管理状态，`directory_review.go` 管理审核，`sharing_policy.go` 和 `delivery.go` 管理授权。新增文件只承载新类型实际行为：

- `resource_adapters.go`：新资源元数据解析与挂载适配；不引入通用插件框架。
- `source_tables.go`：真实库表发现、字段读取、稳定分页。
- `excel_import.go`：上传解析、映射校验、确认入库与批次清理。
- `file_resources.go`：文件验证、对象保存和下载。
- `api_resources.go`：受控 API 配置、测试和代理。
- `multiformat_models.go`：上述资源的请求/持久化类型及交付动作。
- `web/src/pages/ResourceMountEditor.tsx`：从现有 DirectoryWorkspace 中提取并扩展挂载表单；保留目录上下文、已有成果入口。

新增 API 均在现有 `/api/v1` 身份、CSRF 和权限中间件内；交付在 `/delivery/v1`。新类型使用 `table_resource`、`file_resource`、`api_resource`，旧两类引用不变。`Resource` 三元组仍为 type/id/contract_version。目录 data_layer 单独保留 raw_object/governed_dataset；新资源定义保存对应 data_layer，不从引用类型猜测。

## Task 1：新资源契约与统一门禁

**Files:** 修改 `internal/modules/workflow/{models.go,directory_models.go,exchange.go,resource_access.go,resource_directory.go,resource_mount.go,sharing_policy.go,delivery.go,http.go}`；新增 `multiformat_models.go`、`resource_adapters.go`；修改 `contracts/resources/{resource,mount,grant,directory}.schema.json`、`web/src/api/types.ts`。

**Interfaces:**
- 新增 `DeliveryMethod`，值 query/download/invoke；旧授权无 method 时只按 query 解释。
- 新资源定义至少包含不可变版本、责任部门、data_layer、字段与真实载体配置；未就绪资源不得被 resource() 解析为可交付。
- 新增 `authorizeDelivery(tx *gorm.DB, p identity.Principal, ref Resource, method DeliveryMethod, requested []string) (Record, GrantData, []Field, []string, error)`；保留现有 authorize() 为 query 包装，旧调用不改签名。
- 新增 `resourceLayer(tx *gorm.DB, ref Resource) (string,error)`，用于挂载数据层检查；文件不伪造字段，以 download 权限约束完整文件版本。

- [ ] 扩展契约及解析分支，同步类型；文件 mapping 允许空对象，库表/API 保留完整且唯一映射校验。
- [ ] 扩展申请材料的 method 验证、挂载格式/交付动作匹配；查询授权不得用于下载/调用。
- [ ] 保留 admin 业务权限，并在新交付端显式检查有效目录/挂载/资源版本，普通用户复用有效授权和部门校验。
- [ ] 新增 `tests/integration/multiformat_test.go`：`TestDeliveryMethodIsolation`（query 授权下载返回 403）、`TestLegacyQueryCompatibility`（旧授权不变）、`TestMultiformatCrossDepartment`（普通用户拒绝跨部门维护，admin 允许）。
- [ ] 在隔离 PostgreSQL 运行 `python3 scripts/test_identity.py --run 'TestDeliveryMethodIsolation|TestLegacyQueryCompatibility|TestMultiformatCrossDepartment'`；预期 PASS。同步旧挂载测试，空文件映射仅允许文件路径。

## Task 2：真实数据库选表与挂载

**Files:** 新增 `internal/modules/workflow/source_tables.go`、`source_tables_test.go`；修改 `sources.go`、`runtime.go`、`resource_mount.go`；新增 `web/src/pages/ResourceMountEditor.tsx`；修改 `DirectoryWorkspace.tsx`、`web/src/api/types.ts`、`compose.yaml`、`scripts/dev_env.py`；新增 `tests/integration/source_tables_test.go`。

**Interfaces:**
- GET `/sources/:id/tables?schema=...` 返回 `{items:[{schema,name,kind}]}`；GET `/sources/:id/table-schema?schema=...&table=...` 返回字段、唯一键。
- POST `/table-resources` 输入 `{directory_id,source_id,schema,table,expected_directory_version}`，服务端重读元数据，保存版本化资源引用；不接受客户端 SQL 或任意物理表字段声明。
- GET `/table-resources/:id/preview` 限 20 行，要求维护权限并审计；GET `/delivery/v1/tables/:id/records` 仅按授权字段查询，复用有签名的游标。
- 网络许可通过环境配置明确的 host/port/database 清单；现有样例默认项保留，不把任意内网当许可。

- [ ] 用现有驱动列出 PostgreSQL/MySQL schema、表/视图、字段与唯一键；严格引用已发现的标识符，绑定数据参数。
- [ ] 实现目录内“数据库选表”步骤和真实有限预览，自动建议字段映射并由用户确认；不再仅显示 customers。
- [ ] 无唯一键时允许保存草稿，校验启用阶段提示配置稳定排序键；数据库实时查询显示实时读取语义，不冒充固定快照。
- [ ] 通用查询使用唯一键 keyset 分页，新游标绑定资源版本、授权、字段和排序键；不修改旧归档游标含义。
- [ ] `TestSourceTableDiscovery` 使用两张不同结构表、两个 schema 同名表、含引号标识符；`TestSourceSchemaDrift` 验证类型/敏感契约变化阻止旧挂载扩大交付；`TestSourceReadOnly` 验证无法提交 SQL 或修改源表。
- [ ] 运行上述集成测试及 `npm --prefix web run build`；数据来自本地真实 PostgreSQL/MySQL，不能只以 mock 结果验收。

## Task 3：Excel 数据导入与版本化查询

**Files:** 新增 `migrations/000004_multiformat_resources.up.sql`、对应 `.down.sql`，`internal/modules/workflow/excel_import.go`、`excel_import_test.go`；修改 `go.mod/go.sum`、`ResourceMountEditor.tsx`、`web/src/api/client.ts`；新增 `tests/integration/excel_import_test.go`、`web/tests/resource-mount.test.tsx`。

**Interfaces:**
- POST `/resource-imports`：multipart .xlsx + directory_id；返回 batch_id、工作表列表，不创建可用资源。
- POST `/resource-imports/:id/preview`：sheet/header_row/mapping；返回目录版本、有限样例、错误明细与计数。
- POST `/resource-imports/:id/confirm`：expected_directory_version、sheet、header_row、mapping、幂等键；完整校验后原子转为 ready，返回 table_resource 引用。
- 导入表：批次记录 owner/department/directory_revision/status/object_key/checksum/mapping/row_count/error；行表主键 `(batch_id,row_number)`、数据 JSONB；数字使用保精度字符串按类型校验，禁止 float64 中转。

- [ ] 锁定兼容 excelize 版本；限制文件字节、工作表有效维度、解压字节、列数及行数，拒绝公式/宏/外链。原文件私有存储，临时批次 24 小时过期，仅回收无引用对象。
- [ ] 匹配目录技术字段名/业务名称；重复表头与歧义要求确认；缺必填列阻止导入，多余列提示不导入，可空未映射列填空，不修改目录结构。
- [ ] 适配前端 multipart 请求：FormData 不设置 JSON Content-Type，保留 CSRF/会话/错误行为；上传和解析显示阶段状态，长操作可查询批次状态，禁止虚构百分比。
- [ ] 确认时再次读文件、目录版本和权限，逐行完整校验；失败批次不生成可查询资源。重试幂等，处理中重启显示失败/可重试，不自动重复确认。
- [ ] 库表查询分派至导入行表，按不可变批次及 row_number 分页，应用授权投影及敏感策略。
- [ ] `TestExcelImportMapping` 验证已有目录不变、表头重复/缺列/多余列；`TestExcelImportPrecision` 验证字符串前导零、超大整数、精度/scale/空值/日期；`TestExcelImportAtomicity` 验证错误行零可用资源、并发确认同一批次唯一结果；`TestExcelArchiveLimits` 验证解压膨胀和公式拒绝。
- [ ] 运行 `python3 scripts/test_identity.py --run 'TestExcelImport'`、Excel 单元测试及 `npm --prefix web test -- tests/resource-mount.test.tsx`，预期 PASS；真实上传后查询与输入值一致。

## Task 4：文件资源及授权下载

**Files:** 新增 `internal/modules/workflow/file_resources.go`、`file_resources_test.go`、`tests/integration/file_resources_test.go`；修改 `ResourceMountEditor.tsx`、`web/src/pages/{Catalog,Exchange,ResourceDirectory}.tsx`、`delivery.go`。

**Interfaces:**
- POST `/file-resources/uploads` multipart 上传至暂存对象；POST `/file-resources` 将暂存对象绑定目录并生成不可变版本。
- GET `/delivery/v1/files/:id/content?contract_version=...` 返回附件流；执行 download 授权；安全 Content-Disposition 和 nosniff，禁止内联执行。
- 文件资源保存原名称、MIME、size、SHA-256、生成的对象键和版本，不接受外部任意对象路径。

- [ ] 显式允许 PDF/TXT/CSV/DOCX/XLSX/PPTX；Office ZIP 仅按其容器格式验证，普通 ZIP、宏、脚本和可执行内容拒绝；限制 50 MiB 及压缩展开元数据，不承诺文档病毒扫描。
- [ ] 实现上传、确认、存储检查、挂载、重新上传新版本及受控下载；失败回收无引用暂存，不删除旧生效版本。
- [ ] 新增文件申请/授权展示，不要求伪造字段映射；字段受限授权不得换成完整文件下载。
- [ ] 下载前重新验证授权、目录/挂载状态及对象版本；审计流开始、完成和中断，不把部分下载记作完整成功。
- [ ] `TestFileResourceRoundTrip` 验证真实 RustFS 下载摘要一致；`TestFileResourceDenied` 覆盖过期/撤销/暂停/其他部门；`TestFileUploadFailure` 覆盖重复确认、截断、伪扩展名及暂存清理。
- [ ] 运行相关单元/隔离集成测试，模拟对象存储不可用时不得显示挂载校验通过。

## Task 5：API 配置、测试及受控调用

**Files:** 新增 `internal/modules/workflow/api_resources.go`、`api_resources_test.go`、`tests/integration/api_resources_test.go`；修改 `ResourceMountEditor.tsx`、`web/src/pages/Exchange.tsx`、`runtime.go`、`compose.yaml`；新增 `tests/integration/fixtures/api/server.py` 本地只读联调服务。

**Interfaces:**
- POST `/api-resources`：directory_id、URL、GET/POST、query_only、参数 schema、鉴权配置、响应字段路径；密钥加密持久化、返回仅 configured 标志。
- POST `/api-resources/:id/test`：维护权限、受限测试参数；返回状态/时延/结构检查和已脱敏样例，无凭据原文。
- POST `/delivery/v1/apis/:id/invoke`：contract_version、parameters、fields；先执行 invoke 授权再服务端请求，不能传目标地址或鉴权头。
- 首批 JSON 单对象或对象数组；配置固定数据路径，复杂嵌套字段显式映射；不支持的结构阻止启用。

- [ ] 实施允许的 scheme/host/port/地址配置、解析后的地址检查、固定拨号目标和重定向拒绝；HTTP 仅用于明确批准的本地联调目标，其他使用 HTTPS。
- [ ] 超时 8 秒、响应上限 2 MiB；每用户/资源每分钟 60 次（当前单实例进程内限流，重启重置），429 返回重试提示；凭据 header 禁止用户参数覆盖。
- [ ] GET/查询 POST 参数按声明绑定，拒绝未知参数、类型和路径注入；状态非成功、超限或 schema 不符时拒绝交付；按授权字段映射输出并脱敏。
- [ ] 更新鉴权目标/字段/操作产生新修订并重新校验审核；不改已启用版本，测试和调用写审计但不写完整响应或密钥。
- [ ] `TestAPIResourceRoundTrip` 覆盖真实本地 GET/POST 和有效 Bearer；`TestAPIProxyBoundary` 覆盖重定向、DNS/地址变化、参数头覆盖；`TestAPIResponseLimits` 覆盖超时、2 MiB 限制、结构变化、限流；`TestAPIRevocation` 验证撤销后不向上游发请求。
- [ ] 运行上述测试与真实 Docker API 联调，预期返回实际请求对应的结果，不使用固定“测试成功”。

## Task 6：整体验收、文档与本地部署

**Files:** 修改 `docs/design/interface-standard-v1.md`、`contracts/adapters.md`、`contracts/states.yaml`；新增 `scripts/smoke/multiformat.py`、`docs/validation/multiformat/report.md`；补充 `web/tests/resource-mount.test.tsx`。

- [ ] 贯通目录载体与共享方式选项、挂载记录和详情、审核详情、申请材料、我的授权和真实使用入口；文件/API 不再落入原有固定库表预览分支。
- [ ] 验证固定导航/底栏、当前步骤必填校验、文件类型限制提示、无横向表单溢出、1920×1080 和窄屏；列表详情依资源类型显示真实内容。
- [ ] 验证暂停/卸载、更新版本、跨部门和撤销授权；补测审计失败、上传取消/客户端超时重试、服务重启状态恢复；不要求“收回”已经发送的数据。
- [ ] 执行 Go 相关包测试与构建、完整前端测试及构建、`python3 scripts/test_identity.py --run 'Test.*(Mount|Sharing|Delivery|SourceTable|SourceSchema|SourceReadOnly|Excel|FileResource|FileUpload|API|Multiformat|LegacyQuery)'`，失败定位后再针对性复验。
- [ ] 按现有 Docker 命令迁移、构建和更新 server；运行新增 smoke 在隔离测试资源中完成三种真实交付，检查旧原始数据与治理数据查询未回退。
- [ ] 报告明确实际支持的数据库/文件/API 形态、实测容量、失败限制与证据，不将未实现数据库驱动或通用治理任务宣称完成。任务复选框仅在有验证证据后勾选。

## 计划自检

- Excel 数据项定义导入已排除；目录字段为唯一依据。
- 三类资源均包含注册/读取真实内容、版本挂载、审核启用、申请授权和交付；无只做表单的阶段性“完成”。
- 老资源及授权兼容、admin 约定、普通用户隔离、API SSRF 和上传边界均有对应任务及验收。
- API 参数边界、文件完整性、Excel 精度与幂等、源表变化五类重点均已分配具体测试。

## 执行结果（2026-10-16）

核心操作已实现并部署；实际验证证据与计划中的未验证专项见 `docs/validation/multiformat/report.md`。未逐项验证的压力、故障注入、浏览器尺寸验收仍保留未勾选状态，不作为已通过项目。
