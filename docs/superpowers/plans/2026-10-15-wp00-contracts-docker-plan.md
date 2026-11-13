# DataMesh WP-00 契约与本地 Docker 基线实施计划

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking. 用户已选择在当前会话顺序实施，禁止子代理；按功能批次集中验证，不逐个小改动执行红绿循环。

**Goal:** 建立四平台共享契约、可复现的本地 Docker 环境和最小真实数据链路，为后续业务实现提供可验证基线。

**Architecture:** 共享交换负责前道原始接入及后道治理成果交付，治理负责加工质量，资产负责目录发布，安全贯穿全程。用户进一步要求轻量统一登录，共享账号与会话直接由 Go/平台 PostgreSQL 维护；本批次实现身份接口，不提前实现所有业务模块。三库采集优先验证 Debezium + Kafka Connect，处理使用 Flink，结果使用 Doris，归档优先验证 RustFS。

**Tech Stack:** Docker Compose、PostgreSQL、Kafka KRaft、Kafka Connect/Debezium、Flink、Doris、S3 对象存储；OpenAPI 3.1、JSON Schema 2020-12；后续应用保持 Go/Gin/GORM、React/antd/Zustand/Vite。采用独立 golang-migrate 迁移；前端尚未创建，TypeScript 留待前端批次落实。具体版本为实现选择，不标成用户指定。

**Spec:** `docs/superpowers/specs/2026-10-15-datamesh-phase1-design.md`，同时读取原始 V0.4 需求；用户确认的共享交换职责优先于原文的产品归属表述。

## Global Constraints

- 中间件当前全部使用本地 Docker；Compose 项目名 `datamesh`，独立网络和命名卷，宿主端口只绑定 `127.0.0.1`。
- 现有其他项目的容器、网络和数据卷不改动；不默认执行清卷操作，不擅自修改 Docker Desktop 全局设置。
- 平台 PostgreSQL 与源 PostgreSQL 独立容器、账号和卷；配置样例、事件样例及证据中无真实秘密。
- 原始交换不要求先经过治理；两类资源授权分开，治理成果权限不自动授予原始数据权限。
- Kafka 使用单节点 KRaft；Flink 恢复状态和 Doris 元数据、结果存储持久化。
- 精确镜像 tag、digest、运行架构及插件校验值锁定；不使用 `latest`，不以组件启动证明 CDC 或 exactly-once。
- 三库 P0 不削减；未验证的源、版本、权限及恢复场景标记为阻塞，不以 mock 或 SQL 轮询替代。
- 不自动提交 Git；用户未要求提交，且当前目录尚无 Git 仓库。本计划文件中的路径均相对项目根目录。

## Review Focus

1. 原始与治理资源同名或同源时，申请和授权仍区分层次，禁止跨层继承权限：由任务 1 的负面契约样例覆盖。
2. 已有端口占用、错误架构、镜像不存在或资源不足时，环境预检明确失败，不覆盖已有服务：由任务 2 的预检验证覆盖。
3. 登录后切换平台、退出、禁用和权限回收时，同一会话必须按最新权限判断：由任务 4 的真实 PostgreSQL 集成测试覆盖。
4. 源日志条件不足或快照期间有增删改时，连接成功不代表 CDC 可用：由任务 5 的三库采集验证覆盖。
5. 容器删除重建、归档失败或处理停止时，接入与处理状态分别保留：由任务 6 的重建和故障记录覆盖。

## 文件与责任映射

| 路径 | 责任 |
| --- | --- |
| `docs/architecture/platform-boundaries.md` | 四平台职责、实体归属、原始与治理交接及权限矩阵 |
| `api/openapi/platform.yaml` | 管理、接入、交付、共享身份和异步操作契约 |
| `contracts/events/*.schema.json`、`contracts/examples/` | 标准逻辑事件、原生 CDC 映射及正反例 |
| `contracts/states.yaml`、`contracts/adapters.md` | 状态转换和组件适配接口 |
| `deploy/versions.lock.json` | 版本、来源、digest、架构、插件校验值及验证状态 |
| `compose.yaml`、`deploy/docker/` | 本地组件、配置、初始化及必要的固定构建文件 |
| `configs/local.env.example`、`.gitignore` | 无秘密配置模板和本地敏感文件排除 |
| `scripts/dev-env.sh`、`scripts/check.sh` | 唯一环境操作和分层检查入口 |
| `tools/contracts/`、`tests/contracts/` | 锁定的开发校验工具与契约正反例测试 |
| `tests/integration/fixtures/`、`scripts/smoke/` | 三库固定数据及真实链路验证入口 |
| `docs/operations/local-development.md`、`docs/validation/wp00/` | 操作说明与实际验证证据 |

## Task 1 四平台共享契约

**Files:** 创建责任映射中的架构文档、OpenAPI、事件、状态、适配器及契约校验文件。

**Interfaces:**
- 输入：已确认的架构方案、V0.4 P0 和新增 FLOW/SSO 用例。
- 输出：`scripts/check.sh contracts`，退出码 0 表示所有正例通过且所有反例按预期拒绝；契约校验工具作为开发依赖精确锁定，不加入业务运行依赖。
- 统一资源引用字段：`resource_type` 为 `raw_object` 或 `governed_dataset`，并包含 `resource_id`、`contract_version`；授权另含字段范围、部门、用途、有效期。不允许仅用数据集名称或来源名称定位授权。
- 统一事件字段：`event_id`、`source_id`、`object_id`、`run_id`、`op`、`key`、`source_position`、`source_order`、`schema_version`、`captured_at`、`before`、`after`、`trace_id`。`op` 为 `snapshot/create/update/delete`；`source_order` 无法可靠提供时为 null，必须附能力声明，不使用时间戳假装严格版本。
- 异步操作：`operation_id`、`accepted/running/succeeded/failed/unknown`，写操作带 `Idempotency-Key`，相同键但不同请求体返回冲突。
- 任务状态：`draft/preflight_passed/initializing/streaming/pausing/paused/error/recovering/manual_required/finished`；仅按显式转换表接受命令，`unknown` 只描述控制操作的结果，不伪装成已成功的作业状态。
- 交付判定分别读取 `publication_status`、`quality_status`、`completeness_status` 和有效授权；原始交付不读取治理发布门禁。公开响应与错误枚举在同一 OpenAPI 定义，原始接收成功只表示进入可靠接入层。

- [x] 编写正反例，验证原始与治理申请类型不混用、删除事件不要求普通业务必填字段、缺少主键或模式版本被拒、未定义能力的无版本事件被拒；正例必须实际通过，避免“全部拒绝”误通过。
- [x] 按用户要求集中运行契约正反例及 OpenAPI 校验，不执行逐步骤红绿循环。
- [x] 编写契约及责任矩阵：接入归共享交换，治理/目录/授权各有唯一所有者；将 CDC 原生封套映射与逻辑事件分开，不丢源位点。
- [x] 定义 `/api/v1` 管理路由、`/ingest/v1/events` 推送、`/delivery/v1/raw/{resource_id}/records` 与 `/delivery/v1/datasets/{dataset_id}/records` 查询，以及 `/api/v1/operations/{operation_id}`；列表默认 20、最大 100，筛选及排序白名单，错误包含 `code/message/trace_id`。
- [x] 冻结原始交付首个样例为受授权的有限范围事件分页查询，返回来源、操作、主键、模式及位点上下文。游标绑定资源、授权范围及截止水位；失败不伪装空页。授权之外不允许下载原始归档或直连 Kafka。治理成果仍使用 Doris 受控分页查询。
- [x] 定义适配器能力和 `unknown`：提交、查询、暂停、恢复、按范围重放、查询结果、归档清单和核对；提交超时后按操作编号查询，不盲目新建。
- [x] 运行 `scripts/check.sh contracts`；保存工具版本、命令和结果。该任务只证明契约一致，不代表业务接口已经实现。

## Task 2 Docker 配置与基础依赖

**Files:** 创建 `compose.yaml`、`deploy/versions.lock.json`、`configs/local.env.example`、`.gitignore`、`scripts/dev-env.sh`、`deploy/docker/postgres/`；增补本地运行文档。

**Interfaces:**
- `scripts/dev-env.sh preflight <profile>`：检查 Docker、架构、镜像、端口和磁盘；失败非零退出，不打印秘密。
- `scripts/dev-env.sh init`：只在本项目忽略目录 `.local/` 生成本地随机凭据，不覆盖既有值。
- `scripts/dev-env.sh up <profile>`、`status <profile>`、`stop <profile>`：固定项目名 `datamesh`；无清卷子命令。
- profiles：`base`、`pipeline`、`mysql-source`、`postgres-source`、`oracle-source`、`app`；`base` 启动平台 PostgreSQL 和已验证对象存储。

- [x] 依据官方版本资料选择精确候选，先检查 ARM64 manifest，再将镜像 digest 和来源写入锁文件；插件记录 SHA-256。无兼容镜像时停止该组件冻结流程，记录阻塞，不默认跨架构仿真。
- [x] 实现配置：平台 PostgreSQL 绑定 `15432`，对象存储绑定 `19000/19001`；拒绝与本次实际监听端口冲突的配置。数据卷、权限账号和健康检查显式配置。
- [x] 实现环境脚本，分别用错误镜像、占用端口和 Docker 不可达条件检查预检非零退出；正常 `preflight base` 成功。资源不足只报告缺口。
- [x] 运行 `docker compose --env-file .local/runtime.env --profile base config --quiet`；需要可读配置时使用不会输出秘密的检查方式。
- [x] 启动 `base`，实际连接平台 PostgreSQL；对象存储执行写入、读取、列举和校验。已有 MinIO 不复用；RustFS 不通过必要验证时记录原因后评估备选，不同时运行两套归档后端。
- [x] 执行 `stop base` 后重新启动，核对数据库样例和对象校验值仍在；记录镜像、端口及结果，不保存凭据。

## Task 3 Kafka Connect Flink Doris 运行基线

**Files:** 扩展 `compose.yaml`、`deploy/versions.lock.json`、`deploy/docker/kafka/`、`deploy/docker/connect/`、`deploy/docker/flink/`、`deploy/docker/doris/` 和环境脚本。

**Interfaces:**
- `scripts/dev-env.sh up pipeline` 启动任务 2 基础依赖和处理链组件；`status pipeline` 输出各组件实际就绪状态。
- Kafka 外部端口 `19092`，内部 listener 使用服务 DNS；Connect `18083`，Flink `18081`，Doris FE `18030/19030`；BE `18040` 仅按实际宿主访问需求开放。

- [x] 对 Kafka、Connect/Debezium、Flink 和 Doris connector 选择相容的精确版本，保留官方矩阵与本机 manifest 证据。候选对不上则先修正组合，不混装多个冲突 JAR。
- [x] 设置 Kafka 单节点 KRaft、Connect 内部主题单副本、独立持久化和原始主题；Connect REST 只作后台适配依赖。
- [x] 设置 Flink JobManager/TaskManager、检查点路径与 JAR；Doris FE/BE 及节点注册幂等，数据目录持久化。内存限额分别记录，不将总额配置为超过当前 Docker 可用资源还声称全栈可运行。
- [x] 启动 `pipeline`，生产并消费一条 Kafka 消息；检查 Connect 三库插件是否加载、Flink 可用 task slots、Doris FE/BE Alive，并实际执行建表、写入和查询。
- [x] 保存逐组件检查结果；只把检查完成项标记为已验证。未提交实际作业时，不声称 Flink 数据处理已通过。

## Task 4 轻量共享账号与会话基线

**用户变更:** 没有外部统一登录系统，只做一套账号和会话供四平台共用，不引入 Keycloak/OIDC 身份中间件。

**Files:** `cmd/server`、`cmd/manage`、`internal/modules/identity`、`internal/platform`、`migrations/000001_identity.*.sql`、`tests/integration/identity_test.go`、`docs/architecture/identity-contract.md`。

**Interfaces:**
- `POST /auth/login`、`POST /auth/logout`、`GET /api/v1/me`、`GET /api/v1/platforms/{platform}/access`。
- `manage create-user` 从标准输入读取私密配置；`manage disable-user` 禁用并删除所有会话，不公开注册。
- bcrypt 密码散列，32 字节随机会话，数据库只存 SHA-256；HttpOnly/Secure/SameSite Cookie；CSRF；8 小时绝对、30 分钟空闲期限。

- [x] 实现 Gin/GORM/PostgreSQL 基线，数据库迁移独立执行，应用启动不自动改表。
- [x] 实现登录、当前身份、四平台能力及退出；每请求查询有效账号和当前权限，默认无平台权限。
- [x] 登录、退出和账号操作追加审计，应用数据库账号无修改/删除审计记录权限。
- [x] 首轮集中运行真实 PostgreSQL 竞争测试，覆盖四平台会话复用、权限回收、停用、过期、退出、CSRF、重复账号与审计防修改。
- [x] 新增永久失效禁用操作后运行最终批量回归，并验证实际 HTTPS 服务与镜像。

## Task 5 三库 CDC 与最小处理链验证

**Files:** 创建 `tests/integration/fixtures/{mysql,postgres,oracle}/`、`deploy/docker/sources/`、`configs/connectors/`、`scripts/smoke/{cdc,fresh_cdc,oracle,processing,object_store}.py`、`tests/integration/fixtures/flink/`。

**Interfaces:**
- `python3 scripts/smoke/cdc.py mysql|postgres`、`python3 scripts/smoke/oracle.py`：实际源库初始化及日志预检、提交/查询固定 Connect 连接器、执行快照后增删改并消费核对；快照并发变更另列未完成验收；不能仅测试 SQL 连通性。
- `python3 scripts/smoke/processing.py`：选择已通过采集验证的源，部署固定 Flink SQL 样例，核对 Doris 结果。
- `python3 scripts/smoke/object_store.py --input <export>`：导出明确 Kafka 分区和 offset 截止范围的原始样例，JSONL/gzip 写入对象存储，读取后校验清单；只用于联调，不代替 WP-05 持续归档消费者。

- [ ] 为每库建立稳定主键样例，包含中文、null、精确小数、时间及边界字符串；记录源版本、日志与权限预检。源 PostgreSQL 绑定 `25432`，MySQL `13306`，Oracle `11521`。
- [x] 优先验证 Oracle 原生 ARM64、镜像/安装介质可用性及 LogMiner 条件。归档、补充日志和权限不足时给出具体阻塞；不替用户接受商业条款，不跳过 CDC 改用定时 SQL。
- [ ] 对三库分别执行首次快照与持续 insert/update/delete，快照期间注入变更，记录快照结束标记、源位点和 Kafka 范围；在固定截止点核对最终主键和值。记录该样例覆盖的类型与不支持项。
- [x] 任选一个已通过源跑 Flink → Doris，演示字段转换、更新和删除，查询结果与样例一致。原始事件同时走归档样例导出并校验；记录结果路径和归档路径各自范围，不把抽样导出宣称为可靠持续归档。
- [ ] 停止一个下游，确认原始事件仍能按配置缓冲且水位证据不丢；达到保护阈值时明示暂停/阻塞。恢复后核对指定范围，不将积压隐藏成完成。
- [x] 三库分别保存 passed/failed/blocked 及查询证据；任一未完成则本批次对应出口不通过。严格源序列、旧版本重放保护、DDL、故障恢复及全量 P0 留给 WP-02—WP-05 专项实现和验收，不能用本任务烟测替代。

## Task 6 统一检查 运维和交接

**Files:** 完成 `scripts/check.sh`、`README.md`、`docs/operations/local-development.md`、`docs/validation/wp00/report.md`。

**Interfaces:**
- `scripts/check.sh contracts|environment|integration` 分开执行；`scripts/check.sh all` 依次执行。依赖缺失导致对应检查非零退出，不能静默跳过。
- 证据统一记录：需求/用例、精确版本、架构、输入、步骤、预期、实际、时间、结论、限制；不写真实密码、令牌和完整敏感载荷。

- [x] 汇总前五任务的真实命令与结果，生成准确的启动、停止、检查、备份、恢复说明；README 明确共享登录后台已实现，四平台页面和业务尚未完成。
- [ ] 实际重建本项目非源服务容器且保留命名卷，核对 PostgreSQL 样例、Kafka 范围、Connect 已提交位点、Flink 样例恢复状态、Doris 样例及对象校验值。
- [x] 导出平台 PostgreSQL 备份到受控本地路径，恢复到本项目临时隔离实例并核对，不覆盖正常库。无业务迁移时明确只验证数据库级恢复。
- [x] 执行 `scripts/check.sh all`，将所有失败或阻塞逐项列出；前五任务不可用的能力不降级为“通过”。
- [x] 对照规格进行自查，列出后续 WP-01、WP-02—WP-05、WP-06/WP-08 的输入、依赖和验收缺口。代码业务逻辑、六组页面、长期归档、跨层授权执行、全量 FLOW/SSO/AT 用例进入各自后续计划。

## 计划自查和执行边界

本计划覆盖规格第 8 节的首批 WP-00 与 Docker 兼容性验证，以及第 4.1/4.2 节的共享契约。规格中的完整业务、安全执行、页面及恢复语义由后续工作包交付；本批不生成假成功业务 API，不用空模块或 mock 代替真实链路。

当前无代码仓库历史，无需为计划创建工作树。进入实施时先检查目录变化与可用工具，保留用户已有文件。按任务顺序实施；只有用户明确选择多 Agent 执行时再启用对应工作流。

任务 4 已按用户要求改为轻量共享账号，无外部身份依赖。原始查询交付在本批次冻结契约，实际服务由后续 exchange 实现；契约通过不等于接口可调用。

## 技术参考

- [Debezium 版本与变更记录](https://debezium.io/releases/)：使用被选定系列的官方支持矩阵和精确版本，不能仅凭最新标记判断兼容。
- 其他组件依据随规格第 10 节，最终锁文件和实测证据共同决定兼容性结论。

## 2026-10-15 执行范围核对

- 三库已完成最终版本的真实快照与快照后增删改，保存点恢复、有界原始归档、隔离身份测试和备份恢复已有证据。完整结果见 `docs/validation/wp00/report.md`。
- Task 5 原列的快照期间并发变更、下游保护阈值故障，以及 Task 6 的全体非源服务统一删除重建矩阵仍未验收，保持未勾选。已验证停止/重启、保存点恢复和部分组件重建，不扩称完整故障恢复。
- 上述缺口保留在 WP-02—WP-05/最终集成验收，本批仅作为开发底座交付，P0 范围不因此削减。

- 类型覆盖核对：三库主键、中文、小数和增删改已核对；MySQL/Oracle 有 null 样例，PostgreSQL/MySQL 有时间字段。三库时间映射及全部边界字符串矩阵未完成，因此该综合条目保持未勾选。
