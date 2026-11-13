# DataMesh

数据治理项目的一期基础底座。共享交换负责前道原始数据接入、统一共享目录与挂载，也负责治理成果的授权交付；数据治理负责加工和质量，数据资产负责治理成果登记与资产发布，数据安全贯穿全程。四个平台共用轻量账号和会话。

当前已有门户、登录和四平台业务页面，以及数据源/任务管理、质量检查、资产发布、目录双级审核、资源挂载、材料补正、授权交付与审计。原始数据和治理成果分别授权，管理岗位不自动获得业务取数权限。实际交付限于已验证的本地模板；通用计算、持续归档补偿、文件/API 挂载及正式国家编码字典仍有独立接入边界，详见[本次实现与验证记录](docs/validation/resource-directory/report.md)。

共享交换已支持远程 PostgreSQL/MySQL/Oracle 的独立 CDC 采集任务、分块原始归档、授权资源批量 ETL 和交付对账。库表、完整原始快照及已核验治理结果的批量适配器默认上限 200 万行 / 1 GiB / 30 分钟；Oracle 查询、Excel 和旧归档等兼容适配器仍保留 10,000 行 / 20 MiB 限制。整轮目标事务、使用/加工授权、敏感字段脱敏和运行中撤权检查保持有效。交换后单独核对最终目标主键与映射字段，再由收方填写接收或驳回回执；执行成功、技术核对及业务验收分别记录。共享交换的迁移基线为 000011。详见[ETL 使用与验证说明](docs/validation/etl/README.md)。

## 数据治理补齐进度

已有主题域、术语标准、版本化建模、统一元数据及数据剖析。本次代码补充了可编辑数据流、自动成果登记与现有资产发布/交付衔接、工作流依赖和固定间隔调度、质量整改复测、字段血缘、治理总览与运行运维。新成果通过实际执行与质量门禁产生，不再以手工结果绑定作为新建治理成果入口。

2026-10-22 已继续补齐声明式参数与前置成果传递、独立质量检查及发布审批、断点恢复、日历调度、质量模板/跨字段跨表检查及真实趋势、PostgreSQL/MySQL 物理建表、受权影响图与运行告警、成果保留清理及下游迁移工作台，以及结构化文件/API/可信治理成果输入。新增配置、编辑和详情采用单层居中 Dialog。

2026-10-22 主环境已更新至迁移 000024，备份恢复、原数据保留及线上核验通过。侧栏参照共享交换分为治理基础、开发与编排、数据质量、血缘与运维，治理总览单独保留；各入口沿用原有角色权限。自动清理策略默认关闭，外部通知使用已批准的 API 目标。功能边界与验收证据见[最新治理补齐记录](docs/validation/governance/2026-10-22-remaining.md)。

## 当前安全模块与部署基线

2026-11-02 安全模块当时升级至迁移000038；当前18443主环境为迁移000048，新增独立 CSV 数据沙箱，支持 SQL 查询、关联、汇总和结果库化，并与轻量治理双向衔接，见[沙箱使用与验收记录](docs/validation/experience/2026-11-12-csv-data-sandbox.md)。完整流式运行与故障修复见[主环境核验记录](docs/validation/experience/2026-11-12-main-18443.md)。数据安全按安全工作台、分类分级、身份与权限、策略与防护、风险监测与处置、审计与核查分类，并与共享资源、资产详情和数据产品成员联动。首批支持独立复核的标签/策略/水印模板、策略预检、资源冻结、页面与Excel标记、API/ETL签名溯源；未支持的强制防护会明确阻止执行。

备份恢复、原业务记录核对及真实实例页面检查已完成。支持范围和本次部署证据见[安全实施记录](docs/superpowers/plans/2026-11-02-data-security.md)及[数据安全操作指南](web/src/guides/security.md)。历史治理批次的迁移号仅表示当时的阶段，不是当前运行版本。

## 当前运行方式

生产发布采用随栈 PostgreSQL、合并业务镜像，目标入口为 `https://tbtparent.me/datamesh`；GitHub/GHCR、同摘要迁移初始化及备份回滚流程见[生产部署说明](DEPLOYMENT.md)。下文保留本地开发环境的操作方式。

本地地址为 `https://localhost:18443`，提供门户、工作台与 API。共享账号信息在 `.local/bootstrap-admin.json`，数据库和组件凭据在 `.local/runtime.env`；这些文件被排除在 Git 与镜像构建上下文之外，不要复制到公开配置或前端。

本地证书在 `.local/certs/server.crt`。验证脚本显式信任此证书；浏览器访问时需信任本地证书。证书有效期 30 天，初始化不会覆盖已有证书。

| 接口 | 已实现能力 |
| --- | --- |
| `POST /auth/login` | 共享账号登录，JSON 请求及来源检查 |
| `GET /api/v1/me` | 当前身份、部门、平台能力与 CSRF 值 |
| `GET /api/v1/platforms/{platform}/access` | exchange、governance、assets、security 分别鉴权 |
| `POST /auth/logout` | 校验 CSRF 并使四平台共用的会话失效 |
| `GET /health/live`、`GET /health/ready` | 区分进程存活与数据库/表就绪 |

账号使用 bcrypt；会话随机生成，数据库只保存散列。绝对期限 8 小时，空闲期限 30 分钟。每次请求读取有效账号和当前权限。平台访问能力不等于数据资源授权。

## 初始化和启动

当前锁定环境已在 Apple Silicon / Docker Linux ARM64 验证，需要 Docker Compose、Python 3.11+、OpenSSL 3。Go 使用固定容器构建，无须在宿主安装。

```sh
scripts/dev-env.sh init
python3 -m venv .local/tools
.local/tools/bin/python -m pip install -r tools/contracts/requirements.lock.txt
npm --prefix web ci
npm --prefix web run build
scripts/dev-env.sh fetch base
scripts/dev-env.sh up base
scripts/dev-env.sh migrate
docker compose --project-name datamesh --env-file .local/runtime.env build server go-tools
scripts/dev-env.sh up app
scripts/dev-env.sh create-user < .local/bootstrap-admin.json
```

首次创建账号执行一次；重复初始化保留已有随机凭据，重复迁移不会重建业务表。新增账号使用私密 JSON 文件，字段见 `identity.NewUser`，从标准输入提交；不提供公开注册。禁用账号时使用只含 `username` 的私密 JSON：

```sh
scripts/dev-env.sh disable-user < .local/user-to-disable.json
```

禁用会在同一事务中删除该账号的所有会话并记录审计。重新启用账号也不会复活旧会话。

业务初始化在应用、来源 PostgreSQL 与 Doris 启动后执行：

```sh
scripts/dev-env.sh initialize-business
```

该入口先执行结构迁移，再通过正常编目、治理、审核及挂载流程初始化六个业务场景。共21张表、75,300行虚构业务样本；姓名、机构、地址和业务编号使用自然业务表达，不使用 test/demo 前缀，也不使用真实自然人资料。

| 场景 | 来源表 | 行数范围 | 治理资产 |
| --- | ---: | ---: | --- |
| 招生报名 | 7 | 每表3,000 | 招生报名综合信息 |
| 人口基础库 | 3 | 3,000–9,000 | 人口基础信息库 |
| 法人基础库 | 3 | 3,000–6,000 | 法人主体综合信息库 |
| 死亡人口专项库 | 2 | 每表1,200 | 死亡人口核验专项库 |
| 矛盾调解 | 3 | 2,400–4,800 | 矛盾纠纷调解办理信息 |
| 不动产数据应用 | 3 | 1,800–4,000 | 不动产权属综合信息 |

初始化还在“招生报名综合信息”的同一目录下登记“招生报名信息查询”SQL 服务；库表和 API 分别挂载审核、分别授权，不重复创建目录。新资产的目录摘要可在首页浏览，具体记录继续受授权保护。内部编码字典不冒充国家资源编码规则。

版本及内容摘要保存在来源库 `platform_initialization.table_versions`，平台创建进度保存在 `platform_initializations`，随数据库迁移；本地备份、验证结果和迁移包位于 `.local/business-initialization/2026.11.1/`。重复执行不重复插入或创建对象，人工维护过的来源表会停止覆盖。既有招生初始化表在确认内容与原清单一致后升级；未知同名表不会被替换。前期客户及招生条目退出在用展示，历史审计与运行证据保留。

导出不含连接凭据的初始化数据包：

```sh
python3 scripts/initialize_business_scenarios.py --export /path/to/business-initialization
```

迁移已有环境时，备份恢复平台 PostgreSQL、来源 PostgreSQL、Doris、对象存储以及私密运行配置，再执行同一初始化入口核对或续执行；干净环境可重建相同版本基础业务记录。不能用重新初始化替代真实业务数据迁移。旧 `seed_demo_assets.py --initialize` 入口兼容转入当前六场景初始化，不再创建前期客户演示资源。

## 治理批流执行与分层

新数据流的业务计算和结果存储使用 Doris，平台 PostgreSQL 保留定义、权限、运行证据及少量代码诊断样本。历史成果保持原读取兼容。批量任务读取授权来源到 ODS 后执行 Doris SQL；持续更新复用共享交换 CDC，经 Kafka、Flink 处理，通过受控服务把已提交事务写入 Doris。写入凭据不会进入 Flink SQL。

启动治理计算环境：`scripts/dev-env.sh up pipeline`，其中包含 SQL Gateway。Doris 不可用时明确报错，不自动退回平台数据库。普通批量／定时工作流使用 Doris；持续任务在已发布数据流的“持续更新”页启停和恢复。持续任务首次启动要求完整 CDC 快照与单分区历史；本地并发容量为2个任务。

Doris 使用 `datamesh_ods`、`datamesh_dwd`、`datamesh_dim`、`datamesh_dws`、`datamesh_ads`。`datamesh_internal` 与 `datamesh_quality` 保存内部计算和质量证据，普通结果读取身份无权读取这些区域。数据层级在模型、输出、元数据、成果及血缘展示，与原部门和业务目录独立。

Doris 持续表展示最新已提交数据；暂停并保存恢复点后可生成固定成果，经核验再共享。成功成果可按需存为 RustFS 加密快照，包含行数、时间和校验值，下载前重新授权和验证完整性。存档不双向同步，也不覆盖 Doris 最新结果。保留策略先提交退役记录，再异步清理对应物理存储。

## 数据环境和验证

共享交换新增“资源目录”“编目与挂载”“审核工作台”；数据资产可发起共享编目，安全平台可配置资源岗位范围。目录发布、资产发布和挂载启用是独立状态。匿名门户只展示主管审核明确允许公开的目录摘要，不直接暴露内部资产说明。

本地示例环境就绪后，`python3 scripts/bootstrap_business.py` 保留原凭据并补齐审核账号；`scripts/check.sh business` 用分离的账号验证真实编目、挂载和交付。示例编码以 `TEST-` 开头，明确启用 `DIRECTORY_TEST_MODE=1` 才可使用，不能充当正式国家资源编码。正式字典由指定目录主管核验后通过目录字典接口配置，见 OpenAPI。

`python3 scripts/smoke/directory_permissions.py` 验证管理员可直接取数、普通运维及审核人仍需授权，以及加工授权撤销后的阻断；`python3 scripts/migrate_directory.py --report` 查看存量影响，`--stage-drafts` 仅生成待补编草稿，不自动发码或批准。

```sh
scripts/dev-env.sh fetch pipeline
scripts/dev-env.sh fetch postgres-source
scripts/dev-env.sh fetch mysql-source
scripts/dev-env.sh fetch oracle-source
scripts/check.sh data
```

固定镜像和 JAR 地址、SHA-256 见 `deploy/versions.lock.json`；fetch 不使用 latest。当前 Docker 约 7.75 GiB 内存，数据检查分组运行：PostgreSQL 到 Doris/归档、MySQL CDC、Oracle CDC，再从 Flink 保存点恢复。结束后 PostgreSQL 样例链路继续运行，MySQL/Oracle 连接器暂停且对应源容器停止，避免三库与全部计算组件同时占用资源。

原始事件样例保留 topic、partition、offset 和 Debezium 源位点；归档同时写入 gzip 数据和清单，核对连续范围、事件数、SHA-256、列举与读取。它是**有界联调导出**，不是持续归档消费者或完整处理水位服务。

| 检查入口 | 覆盖范围 |
| --- | --- |
| `scripts/check.sh contracts` | 事件/资源正反例、状态及 OpenAPI 校验 |
| `scripts/check.sh tooling` | 连接器状态传播、配置冲突和等待上限；脚本语法 |
| `scripts/check.sh environment` | 原生镜像、端口、内存与 Compose 配置 |
| `scripts/check.sh go` | Go 格式、静态检查和构建 |
| `scripts/check.sh integration` | 临时真实 PostgreSQL 库中的身份及竞争测试，结束后删除测试库 |
| `scripts/check.sh data` | 三库真实快照及增删改、Flink/Doris、归档和保存点恢复 |
| `scripts/check.sh all` | 以上检查，加实际 HTTPS 身份检查及备份恢复；会启动和暂停本项目样例组件 |

单项验证可以独立运行。`python3 scripts/smoke/identity.py` 使用初始账号检查实际 HTTPS 登录和退出；`python3 scripts/smoke/restore.py` 将平台备份恢复到无网络的临时容器，不覆盖运行库。

本项目端口采用 15432、19000/19001、19092、18083、18081、18030/19030、18443，以及样例源 25432、13306、11521，均仅绑定本机。未接管其他项目的 PostgreSQL、Redis 或 MinIO。

## 停止与恢复

`scripts/dev-env.sh stop base|app|pipeline|postgres-source|mysql-source|oracle-source` 只停止本项目对应服务，保留命名卷。避免在需要保留进度时执行清卷命令。重新启动计算前，先检查已保存的作业/保存点元数据，结果未知时核对实际作业，不盲目重新提交。

备份、恢复、操作状态和限制详见 [本地运行说明](docs/operations/local-development.md)。实测结果及剩余验收边界见 [首批验证记录](docs/validation/wp00/report.md)。

### 远程数据库与可信数据源白名单

管理员可在「共享交换 → 数据源管理 → 可信数据源」查看有效白名单、增加和删除平台配置；保存后新连接立即生效，无需重启。平台配置保存在数据库，变更经 CSRF、幂等及管理员权限校验，并写入操作审计；撤销条目保留历史，不删除已登记的数据源，也不强制中断已建立的外部连接。若目标同时被部署配置放行，删除平台条目后仍允许连接。

原有部署方式继续兼容：本地在 `.local/runtime.env` 配置 `SOURCE_ALLOWED_TARGETS`，Compose 将其传入 server；线上在服务进程环境变量配置同名值。环境配置和内置样例在页面只读展示；环境条目通过部署配置维护，内置样例固定保留兼容。按数据库类型、主机名/IP、端口、数据库名（Oracle 为 Service Name）精确匹配，不支持通配符。数据库账号还需具有相应库表访问权限；地址必须能从 DataMesh 服务所在网络访问。

```dotenv
SOURCE_ALLOWED_TARGETS=[{"kind":"postgres","host":"pg.example.internal","port":5432,"database":"business"},{"kind":"mysql","host":"mysql.example.internal","port":3306,"database":"business"},{"kind":"oracle","host":"oracle.example.internal","port":1521,"database":"ORCLPDB1"}]
```

以上仅为示例，请替换为实际目标，不填写密码。保存后执行以下命令重新创建服务容器，单纯刷新网页或 restart 不会更新容器环境变量：

```sh
docker compose --project-name datamesh --env-file .local/runtime.env up -d --no-deps --force-recreate server
```

新增数据源表单可先“测试连接”再保存；测试不创建数据源、不保存凭据，也不会启动同步。PostgreSQL、MySQL 与 Oracle 的连接检查均使用填写的地址和凭据。Oracle 使用 Service Name；库表发现与查询使用账号实际可见范围。现有内置本地样例地址仍保留兼容。白名单仅允许 SQL 连接，不会自动授予资源权限或配置 CDC；CDC 在“CDC 接入任务”选择真实库表并配置独立作业；保存草稿不会启动采集。PostgreSQL 填写已有发布名称，Oracle 登记 CDB 数据源并另填目标 PDB（两者均须处于可信范围）。先检查具体表的日志、权限、采集驱动及配置条件，再从任务列表启动；暂停、恢复和核对保留已有位点。旧样例采集绑定继续兼容。远程实例的网络、TLS 要求及实际权限需在对应部署环境验证。

数据源「CDC 预检」及建立任务中的「检查 CDC 条件」会返回逐项诊断：当前值、要求、处理负责人、操作步骤和可复制的只读验证 SQL。PostgreSQL 检查逻辑日志、复制权限/容量及表复制标识；MySQL 检查 binlog、账号权限、主键和所选表的真实 SELECT 权限；Oracle 检查归档及补充日志，完整 LogMiner 权限和逐表日志仍需 DBA 核对。连接失败时其余数据库项标为等待检查，不能读取配置时标为无法检测；采集驱动、网络或配置不满足时单独提示平台管理员处理，不误报为数据库参数错误。处理后可重新检查；预检不会修改外部数据库配置或读取业务记录。

ETL 数据交换统一选择“原始数据 / 治理数据”。平台资源来源绑定资源及契约版本，按有效目录挂载、数据使用授权和字段脱敏规则读取；治理数据继续执行质量门禁。缺少授权可在任务编辑器内提交数据使用申请。直接映射按使用授权交付，字段转换还需允许派生成果的加工授权。原始归档按主键合并到当前状态后写入目标（目标仍为更新或插入，不传播删除）。原有直接连接 PostgreSQL/MySQL 库表的任务保留；目标支持 PostgreSQL/MySQL。文件和 API 不作为本期 ETL 库表来源。独立原始数据申请菜单已移除，资源目录仍保留统一申请入口。

历史治理成果保留 PostgreSQL/MySQL 结果库表、原始归档及字段转换的显式绑定与核验记录。新建治理成果改由受控数据流执行、质量分流与模型契约自动登记，发布和交换继续走现有目录与授权流程。原始快照和交付期望索引正文加密存放，默认保留期分别为24小时和7天；活动或提交结果未知的运行保留其证据，技术报告及业务回执历史单独保留。

操作指南统一从全局顶栏“操作指南”进入（`/guides`），分别阅读 [共享交换操作指南](web/src/guides/exchange.md) 和 [数据治理操作指南](web/src/guides/governance.md)，支持章节导航与 Markdown 下载。门户首页和治理看板不再展示流程指引正文，各业务页面的按需帮助继续保留。

主环境现有客户表已完成模型、数据流、1000行质量核验及成果发布，包含真实前端与浏览器验证。完整可复用步骤见 [主环境完整治理实跑](docs/validation/governance/2026-10-22-main-flow.md)。
