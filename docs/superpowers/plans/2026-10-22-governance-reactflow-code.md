# 数据流拖拽编排与 Python / JavaScript 实施计划

> **执行方式：** 按用户已指定的方式，由当前助手在本会话逐项执行，使用 superpowers:executing-plans。不得使用子代理；不得采用红绿测试。步骤遵循先完成实现、再针对性验证；未请求提交 Git commit。

**Goal:** 交付可拖拽、可连线的数据流画布，以及能真实执行 Python / JavaScript 的代码节点，并部署主环境。

**Architecture:** React Flow 替换现有自绘画布，保留原节点配置、版本和发布流程。新增受认证的代码执行代理，每次代码节点在独立、限额且无网络的容器中运行；结果回到既有数据流中间区，继续质量及模型核验。布局单独保存，不修改已发布的业务定义。

**Tech Stack:** React / TypeScript / Ant Design、@xyflow/react 12.12.0、按需加载的 CodeMirror 6 Python/JavaScript 编辑能力、Go 标准库、现有 PostgreSQL / GORM / Docker；JavaScript 固定使用 decimal.js 10.6.0，Python 使用标准库 Decimal。

**Spec:** [已确认方案](../specs/2026-10-22-governance-reactflow-code-design.md)

## Global Constraints

- 真实治理流程、部门和资源权限、固定版本引用及原发布门禁全部保留。
- 第一阶段为 `transform(row, context)` 逐行一进一出；完整语言语法，非公式替代。筛选和汇总继续使用既有节点。
- 单层 dialog，字段配置在同层；保存、发布、运行、试运行和返回操作固定右下角。保留批准的字号、控件尺寸和24px分区间距。
- 代码、输出契约和运行环境固定到数据流版本；重试不使用最新草稿。
- 不挂载宿主目录、平台凭据或 Docker socket 给脚本；主业务服务不执行用户代码。
- 无子代理、无红绿测试、不丢弃现有改动、不新增提交要求、不升级无关依赖。

## Review Focus

- 旧版本没有布局或脚本字段：能直接浏览和运行，单独移动不形成业务修订；由任务1、3验证。
- 脚本返回未知字段、缺字段或错误精度：节点级拒绝，未提交结果不得成为成果；由任务3、4验证。
- 脚本永不结束、输出爆炸或产生子进程：取消及超时销毁整个任务容器，不仅关闭 HTTP 等待；由任务2、4验证。
- 金额、大整数、空值、Unicode及日期边界：跨语言结果无隐式精度损失，错误定位不带敏感值；由任务2、3、5验证。
- 撤销、重连、切换节点和运行中权限失效：字段配置不丢失，无效分支不能发布，已撤权运行不提交结果；由任务1、4、6验证。

---

### Task 1: 可独立使用的拖拽画布与布局保存

**Files:** 修改 `web/src/pages/GovernanceFlowGraph.tsx`、`GovernanceDataflows.tsx`、`GovernanceDataflows.css`、`web/src/api/governance.ts`、`web/package.json` 及锁文件；新增 `web/src/pages/governanceFlowEditing.ts`、`internal/modules/workflow/governance_flow_layout.go`、`migrations/000026_governance_flow_layout.up.sql` 及 down；同步 `api/openapi/platform.yaml`。

**Interfaces:**
- `GovernanceFlowLayout {revision:number;positions:Record<string,{x:number;y:number}>;viewport:{x:number;y:number;zoom:number}}`。
- `GET/PUT /api/v1/governance/dataflows/:id/versions/:version/layout`，布局按当前用户、定义及版本保存；PUT 携带基准 revision，冲突返回409，读取与更新均重新检查定义可见性及治理角色。
- `validateFlowConnection(graph, connection): string | null`，输入含来源节点/分支和目标节点/输入端口；有错误返回白话原因。
- `FlowGraph` 受控接收 graph、selected、editable、layout，输出选择、图修改及布局修改事件，兼容原只读使用方。

- [x] 安装精确版本 React Flow，保留现有 React / Zustand / Ant Design；实现可拖动节点、连接端口、删除和重连、缩放与适应画布。
- [x] 普通输入、关联左右输入和质量双分支对应原业务结构；拒绝自连、环、重复占用输入及不合格分支进入成果。无效草稿允许继续编辑，保存/发布仍由服务端阻止。
- [x] 新增节点默认不改写已有连线；保留明确的“插入选中连线”操作。实现最多50次撤销/重做，图、节点配置及布局一起恢复。
- [x] 实现仅保存布局的独立表及接口；坐标为有限数且绝对值不超过100000，zoom在0.1–4之间，只接受当前定义节点，禁止跨用户布局覆盖。无布局时复用现有分层排列算法。
- [x] 修改后运行 `web/tests/governance-dataflow.test.tsx` 与新增 `web/tests/governance-flow-editing.test.ts`；覆盖循环、分支、重连、已有配置与撤销。新增隔离集成 `TestGovernanceFlowLayouts` 验证纯布局更新不改变定义指纹、版本或发布状态以及越权/并发冲突。

### Task 2: 独立代码执行环境与任务生命周期

**Files:** 新增 `cmd/governance-code-runner/main.go`、`internal/platform/coderunner/{protocol,server,docker,limits}.go`、对应检查文件、`deploy/docker/governance-code/{python.py,javascript.cjs,python.Dockerfile,javascript.Dockerfile,package.json,package-lock.json}`；修改 `compose.yaml` 和 `deploy/versions.lock.json`。

**Interfaces:**
- 代理使用内部网络且不映射公网端口，通过独立0600令牌文件认证；只有代理可连接容器管理服务；Compose新增仅业务服务与代理加入的内部 `code_control` 网络，代理不对外发布端口。主业务服务只连接代理，脚本容器没有任何网络或宿主挂载。
- `POST /v1/jobs`：`CreateJob{language,runtime_version,source,context,timeout_ms}` → `{job_id}`。
- `POST /v1/jobs/:id/batches`：`{sequence,rows:[{ordinal,values}]}` → `{sequence,rows:[{ordinal,values}],error?:{code,line,column}}`；同任务串行，sequence严格递增，重复或乱序拒绝。
- `DELETE /v1/jobs/:id`：销毁该代理拥有的任务容器；`GET /v1/runtimes`：返回可用环境、语言、不可变镜像标识及固定库列表。

- [x] 从官方 Python 3.14 / Node 24 当前维护镜像解析具体版本与不可变摘要，记录锁文件；构建固定包装器及 decimal.js 10.6.0，运行时禁止拉取镜像或安装包。
- [x] 实现 Docker 标准 HTTP 接口客户端，不增加通用代理接口；镜像、启动命令、挂载和网络参数由服务端固定，用户只能提交语言、代码和允许的参数。脚本通过 stdin 交付，禁止 shell 拼接。
- [x] 每任务独立容器：非root用户、只读根文件系统、network=none、cap-drop ALL、no-new-privileges、默认seccomp、256MiB内存、1 CPU、32进程、32MiB临时空间、无swap额度。并发最多2任务，代码最多64KiB，每批最多250行/2MiB，每行最多256KiB，累计结果仍受现有 ETL 配额限制。
- [x] 任务最长120秒，空闲30秒清理；取消、断连后客户端明确清理、超时和代理重启恢复均销毁代理自身标签标识的容器。所有清理只作用本代理任务，不操作其他业务容器。
- [x] 两种包装器都暴露 `transform(row, context)`。输入整数和精确小数统一传十进制字符串，Decimal工具固定到环境；禁止将金额默认转float/Number。脚本print/console输出不进入结果协议，异常仅回传类型及用户代码行列，服务日志不保存行值或完整异常消息。
- [x] 修改后做真实容器检查：正常清洗、Decimal计算、超大整数、Unicode、异常行号、无限循环、分配内存、超大输出、派生进程、网络/凭据/宿主目录/跨任务访问、取消及重启回收；验证限制不支持时拒绝启动任务，不使用无隔离降级。

### Task 3: 代码节点契约、类型与血缘

**Files:** 修改 `internal/modules/workflow/governance_dataflow.go`、`governance_dataflow_test.go`、`governance_flow_execute.go`、`governance_flow_parameters.go`、`web/src/api/governance.ts`、`api/openapi/platform.yaml`；新增 `internal/modules/workflow/governance_flow_script.go`。

**Interfaces:**
- 新增节点 kind=`script`，可选属性 `script:{language:"python"|"javascript",runtime_version:string,source:string,fields:GovernanceField[]}`；旧节点不增加必填项。
- `scriptOutputFields(node, input): []governanceFlowField`：严格验证输出字段名、类型、精度和可空性，直接标准约束继续由输出模型承担。
- `normalizeScriptRow(row, fields): (DataRow,error)`：拒绝未知/缺失字段及非有限数，按现有数值和日期规则归一化，错误只包含节点和字段标识，不包含记录值。
- `GET /api/v1/governance/code-runtimes`：按原治理角色返回运行环境；不可用环境不可排队为成功任务。

- [x] 实现新节点结构与编译检查，要求一个输入、1–100输出字段、支持的固定环境版本及非空代码；发布和运行各自复核环境可用性及来源加工权限。
- [x] 固定语言、代码和环境进入已发布执行定义与重试证据。旧定义解码不变；新节点属性不用于其他节点，未知语言和错误组合明确拒绝。
- [x] 代码输出保守关联所有代码输入血缘，标记保守追踪；敏感性至少为输入敏感性的并集，用户不能通过声明false降低敏感标记。已有非管理员脱敏范围保持，不授予新字段读取能力。
- [x] 修改后执行代码节点契约检查，覆盖少字段、多字段、空值、类型/精度边界、非法标识、错误环境、敏感性与保守血缘；原 `TestGovernanceDataflow*` 编译和类型检查继续通过。

### Task 4: 真实执行与限量试运行

**Files:** 修改 `internal/modules/workflow/governance_flow_execute.go`、`governance_runs.go`、`governance_dataflow_http.go`、`governance_dashboard.go`、`governance_operations.go`、`web/src/pages/GovernanceDashboard.tsx`、`web/src/pages/GovernanceOperations.tsx`、`api/openapi/platform.yaml`；新增 `internal/modules/workflow/governance_code_client.go`、`governance_flow_preview.go`、`migrations/000027_governance_code_preview.up.sql` 及 down、`tests/integration/governance_script_test.go`。

**Interfaces:**
- `executeScriptNode(ctx, tx, node, plan, parameters, guard): (inputCount,outputCount,error)`：按ordinal游标每250行发送，验证后批量写入现有临时中间表。
- `POST /api/v1/governance/dataflows/code-preview`：`{definition,node_id,sample_limit:20}` → 原治理运行结构；新增 kind=`dataflow_preview`，其执行定义固定目标代码节点及仅该节点的上游前缀。
- 试运行复用原运行读取、取消和重试入口；加密保存最多20条输入/输出对比，`GET /api/v1/governance/runs/:id/code-preview-records` 返回样本，不提供成果登记端点。

- [x] 实现代理客户端与服务配置，绑定上下文取消；每批前后复用guard，累计输入/输出预算沿用ETL限制，返回序号必须与输入逐项一致，任何批次失败整体回滚。
- [x] 在既有执行循环中加入script分支，其他算子继续现有SQL路径；新节点仍记录节点统计，后续继续质量和模型核验，只有完整成功才登记成果。
- [x] 实现 `compileGovernancePreviewPrefix`，只编译目标代码节点及可达上游，复用原节点与来源验证，不要求尚未配置的下游模型；每个来源最多读取20条，明确为来源顺序样本。预览结果不登记governance_outputs、不进入成果发布，不参与完整运行成功指标。
- [x] 迁移27扩展原 `governance_runs_kind_check` 约束及API运行类型联合，允许 `dataflow_preview`；回退迁移如已有预览记录则明确拒绝，不删除历史记录。更新运行筛选、工作台和运维统计，预览单独显示，不计入完整数据流成功率。
- [x] 预览复用现有运行租约、撤权复核、取消及加密行存储；样本读取权限与该运行一致，禁止从其他人的预览记录读取数据。
- [x] 修改后以真实PostgreSQL及两种实际运行环境执行客户清洗、精确金额、质量分流、模型核验、成果发布；验证失败无成果、固定版本重试、撤权/取消回滚、预览不可发布及跨部门拒绝。隔离数据库完成后清理。

### Task 5: 同层代码编辑器、字段声明与运行反馈

**Files:** 修改 `web/src/pages/GovernanceDataflows.tsx`、`GovernanceDataflows.css`、`GovernanceFlowGraph.tsx`、`web/src/components/governanceJourney.ts`、`GovernanceFieldHelp.tsx`、`GovernanceGuide.tsx`；新增 `web/src/components/GovernanceCodeEditor.tsx` 和 `GovernanceScriptConfig.tsx`；同步新运行类型的工作台统计与说明。

**Interfaces:**
- `GovernanceScriptConfig({node,inputFields,runtimes,editable,onChange,onPreview})`，完整配置回写原数据流草稿。
- `GovernanceCodeEditor({language,value,inputFields,errorLocation,onChange})`，按需加载CodeMirror 6，Python和JavaScript语言包只用于当前编辑器。
- `flowEditorFields` 支持代码节点声明输出，继续传播到原投影、清洗、关联、质量和模型节点。

- [x] 增加“自定义代码”节点及分类算子库，语言选择、固定环境/库说明、代码示例、输入字段补全、输出契约编辑、错误行列跳转均在同一dialog右侧配置区。
- [x] 输出字段支持带入上一步、增加、改名和移除；默认示例保留全部字段，语言切换有草稿保留，不静默覆盖用户代码。明确整数/小数为字符串及Decimal用法。
- [x] 试运行和完整运行在同一弹窗切换结果区，明确样本范围、行数、输入输出差异、节点错误与取消状态。试运行按钮放固定右下操作栏，完成试运行不改变已发布状态。
- [x] 指南说明代码适用业务场景、上下游关系和客户/商品实例；陌生字段解释放数据项右侧悬浮icon，不添加纯按钮教程。
- [x] 修改后运行数据流与成果相关前端检查、TypeScript和生产构建；浏览器检查1920、1440和540宽的拖拽/重连、编辑切换、样本及错误展示、单层dialog和右下角按钮。

### Task 6: 主环境部署与完整交付核验

**Files:** 修改 `scripts/dev_env.py`、`scripts/check.sh` 和既有部署辅助脚本所需的schema/新增服务检查；新增 `scripts/smoke/governance_code.py`；更新 `docs/validation/governance/` 记录及运行环境说明。

- [x] 自行逐项审查规格覆盖、类型/接口一致性、权限和资源上限；修复实际问题，不借用子代理或外部审查线程。
- [x] 完成隔离环境完整检查后，按现有方式备份主环境、隔离恢复并校验旧表数量和受保护业务定义指纹。
- [x] 构建固定运行镜像、执行代理及前后端，完成迁移26、27再更新所需服务。只新增本功能服务，不重建无关数据组件；代理不可用不阻止原SQL数据流启动和运行；令牌不进入仓库或日志。
- [x] 主环境使用固定证书严格验证健康、运行环境、布局权限、新前端资源与旧接口；保留用户既有数据。实际代码流程先在隔离数据上验收，生产若需新增演示作业先明确其用途，不能覆盖用户已有作业。
- [x] 验证代码执行器失效时旧SQL数据流仍可运行，代码节点显示明确原因；保存旧镜像/服务配置及数据库恢复信息，新增布局表不破坏旧版读取。
- [x] 交付实际部署状态、两种语言执行证据、交互截图及未解决限制；只有全部要求完成才报告完成。

## 自审结果

规格中的画布、Python/JavaScript、精度、运行隔离、试运行、权限、血缘、旧版兼容、指南和主环境交付均对应以上任务。执行方法沿用用户“不要子代理”，检查方式沿用“不要红绿测试”。本计划已执行，主环境已更新；实际检查与范围说明见 `docs/validation/governance/2026-10-22-code-nodes.md`。
