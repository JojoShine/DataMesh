# 独立体验环境

2026-11-12 起，本机统一从 `https://localhost:18443` 体验已实现功能，完整流式链路运行在主环境。原19443独立验证服务已停止并保留全部数据卷，不与主链路并行占用资源；下文保留独立环境工具的部署说明。当前部署与验收见[主环境核验记录](validation/experience/2026-11-12-main-18443.md)。

默认是适用于现有 16GiB 主机、约 8GiB Docker VM 的轻量模式：独立应用、元数据库、样例数据库、对象存储和凭据，复用现有 Doris 计算进程，但只允许访问新建的 `experience_` 数据库。不会复制整套重型后端，也不会读取主实例的业务数据或复用其服务账号。

## 默认配置与资源

`python3 scripts/experience_env.py prepare` 生成私有 `.local/experience/`，不启动容器、不读取主实例 `runtime.env`、业务账号、Flink 绑定或 smoke 数据。唯一宿主端口为 `127.0.0.1:19443`，正式入口 `18443` 保持原样。

| 默认服务 | 内存上限 |
| --- | ---: |
| 应用 server | 512MiB |
| 独立 PostgreSQL | 256MiB |
| 独立 RustFS | 384MiB |
| source-reader | 640MiB |
| 临时数据库迁移 | 128MiB |
| 固定上游 HTTPS 入口代理 | 64MiB |
| 合计 | **1.9375GiB** |

容量检查在上述服务上限之外保留 **1GiB** 余量，并实测所有其他应用的当前内存用量。启动时再次检查，不提供跳过选项，不停止其他项目腾空间。现有应用约 4.26GiB、Docker 总内存约 7.75GiB 时，默认服务的预留预算在容量内；这不是压力测试已经通过的保证。

可选代码执行增加 128MiB runner 和一个 256MiB 任务，合计预留 **2.3125GiB**（另加 1GiB 余量）。轻量模式使用 `CODE_RUNNER_MAX_JOBS=1`，任务继续无网络、只读根文件系统，并限制 CPU、内存和进程数。默认服务不会启动 runner。

元数据位于独立 PostgreSQL 的 `datamesh` 库，来源样例位于同一独立 PostgreSQL 的 `sample` 库，使用不同数据库和账号，不再复制一个 PostgreSQL 进程。对象存储使用独立 RustFS 容器、卷和新凭据，因此相同逻辑桶名也不会指向主实例对象。应用、数据和任务网络均为内部网络，仅复用只读镜像、JAR 制品和初始化脚本。Docker Desktop 不为全内部网络容器实际发布端口，因此只有无数据库凭据的固定上游 HTTPS 代理接入专属入口桥接网络并发布本机端口。代理只转发至内部应用，验证其 TLS 证书，禁止开放代理方法，并过滤双方会话 cookie；应用本身仍没有外部网络出口。

## 共享计算的前置隔离

操作者通过进程环境临时提供 `COMPUTE_ADMIN_PASSWORD` 后，运行：

```sh
python3 scripts/experience_env.py attach-compute
```

该命令只通过 Docker 标签发现现有 FE，不会自行读取主实例的密钥文件，也不把操作者凭据写入任何体验配置。它会：

1. 在任何变更前保存私有意向记录；若目标数据库、用户或工作负载组已经存在而没有匹配的归属记录，拒绝接管。
2. 创建专属内部桥接网络 `datamesh-experience_compute`，仅将现有 Doris FE 以 `doris-fe` 别名附加进去；不重启主服务，不断开主网络，不让体验应用加入主网络。
3. 新建 `experience_internal/ods/quality/dwd/dim/dws/ads/smoke` 八个数据库，使用重新生成的 `experience_writer`、`experience_reader`。写账号权限仅覆盖这些数据库；读账号仅覆盖成果层及指定的 legacy 样例表，不读取 ODS、内部暂存或质量隔离区。
4. 配置并核验专属 `experience_compute` 工作负载组：硬内存上限 10%、并发 2、队列 20、等待最多 5 秒；两个用户绑定该组，连接数上限设为 8（引擎支持时）。应用连接另有每查询 128MiB、30 秒限制。
5. 通过真实写入、读取、两账号访问主数据库被拒绝的反向测试，以及四个有界查询的排队/FE 响应测试后，才写 `compute-ready.json`。配置不支持、实际排队无法观察或主 FE 响应退化时拒绝共享计算就绪。

资源组的属性及用户绑定依据 [Doris 4.x 工作负载组文档](https://doris.apache.org/docs/4.x/admin-manual/workload-management/workload-group/)。代码先读取实际引擎能力，使用硬内存和排队控制；不修改全局开关，也不为 CPU 控制改动主实例的 cgroup 或重启 BE。共享进程的缓存、后台工作和故障仍会影响双方，不能称为物理隔离；CPU 硬限制依赖现有 cgroup 能力，不能仅凭属性配置声称已经生效。

启动预检还会重新检查计算网络为内部网络、专属身份只可见授权数据库，以及实际工作负载组属性。所有密钥、cookie（`datamesh_experience_session`）和代码容器命名空间仍与主实例独立。

## 启动与实际业务初始化

先核对应用镜像包含当前后端、命名空间和代码执行并发设置，再执行：

```sh
python3 scripts/experience_env.py preflight
python3 scripts/experience_env.py up
python3 scripts/experience_env.py status
```

`up` 按真实顺序启动独立依赖、迁移数据库、创建身份、启动内部应用和入口代理，等待受信任 HTTPS `/health/ready` 实际就绪，然后初始化和核验真实业务。轻量初始化复用已有模型/流程接口的请求结构，所有文件、HTTPS、数据库和审批边界均指向独立实例：

- 在独立 `sample` 库创建 8 条虚构客户和 10 条订单，不覆盖用户修改。
- 通过正常来源、目录和挂载接口建立来源资源；提交人与部门审核人不同，主管审核也使用另一个身份。
- 由不同审核者通过真实加工授权接口明确来源、字段、用途、期限、派生与共享权限，再以普通 provider 身份实际执行来源读取、文字去空格、质量检查和专属 Doris 写入，确认 6 条合格客户、2 条问题客户。
- 发布成果和目录；发布者也必须真实申请并取得独立批准后才能读取受控交付。消费者另行申请，核验自批被拒绝，再由不同审核者批准，验证字段投影交付与审计。

2026-11-11 已在本机实际完成上述轻量基线：8 条虚构客户来源，6 条合格成果、2 条质量问题，发布者及消费者分别取得独立批准，真实交付和审计通过。主服务未重启，现有数据未覆盖。

这些路径不要求 Kafka、Connect 或 Flink。`verification.json` 仅在实际业务检查通过后生成，分别标注普通角色业务、代码沙箱、双 cookie/主实例边界的已有证据，以及部署实例上的完整安全策略闭环、外部 API、未覆盖载体和可选流式/Oracle 的未验证范围；不会把后端回归测试混同为全部部署链路已验收。单纯生成配置或启动服务不能被报告为全部体验通过。

`bootstrap` 可续执行初始化，`verify` 可再次核验闭环，`provision` 补充缺失身份；`stop` 只停止体验项目，保留独立卷。启动中发生异常会停止本项目服务，不更改其他项目。不要直接使用绑定主实例路径和端口的旧 smoke/初始化脚本。

## 可选功能

```sh
python3 scripts/experience_env.py enable --feature code
python3 scripts/experience_env.py enable --feature streaming
python3 scripts/experience_env.py enable --feature mysql
python3 scripts/experience_env.py enable --feature oracle
```

2026-11-11 已实测代码功能：普通 provider 与 governor 可见两个真实运行时；Python 3.14.9、Node.js 24.22.1 的 Unicode、大整数和精确小数转换通过。实际容器核验及两个语言的无网络、只读根文件系统、受限临时目录测试通过；单任务容量、错误脱敏、无限循环超时、运行中取消及容器清理通过，未遗留任务容器。证据见私有 `code-verification.json`。随后以普通 provider 实际执行 Python → JavaScript 双代码节点治理流程，确认 6 条合格成果、2 条质量问题及两个节点的实际转换值；运行期间物理沙箱容器峰值为 1，未提高单任务容量。证据见 `two-script-verification.json`。

每次启用都会重新检查所选服务的完整预算。流式服务和 MySQL/Oracle 样例仅在显式启用后启动，使用独立卷和内部网络；容量不足则拒绝新增服务。启动流式组件不等于已经创建可核实的流作业，仍须通过真实业务接口建立任务。

`prepare --mode full` 保留全量克隆作为可选高级模式：独立 Doris、Kafka、Connect、Flink 和全部样例源，其约 15GiB 的额外预留不是轻量体验的最低要求。全量模式使用独立 `/24` 网络（默认 `172.30.239.0/24`），检查与现有网络重叠，Doris FE 的变量为 `DORIS_PRIORITY_NETWORKS`。两种模式间切换前必须停止体验项目，不会停止主实例。

## 账号边界

`accounts.json` 为私有本地文件，包含 provider、directory_reviewer、operator、governor、consumer、auditor、security_author、security_reviewer 八个独立身份。最后两个是**该隔离实例的完整管理员**，分别创建和独立复核安全控制。账号虽然选择安全平台，但现有后端 `admin` 角色跨平台生效，不能称为仅限安全平台管理员。其余身份使用标准业务角色；auditor 不能创建或批准安全控制。

私有 `bootstrap-admin.json` 仅用于初始岗位授权、主管审核和自定义字典，不属于共享账号。`account-guide.json` 记录实际权限边界，不包含密码。`PUBLIC_TEST_ACCOUNTS_FILE` 保持空值，登录页凭据接口禁用；由操作者有意选择要分享的独立身份，不公开账号文件。

再次准备保留所有原密码；再次创建身份时核对角色、平台、部门及启用状态，差异会导致拒绝，不会静默提权。独立环境初始化不改变主实例身份及数据。主实例现已另按用户授权，为专用公开岗位显式启用full真实业务模式，访问既有数据并保留正常授权与独立复核；未配置full的public_test默认只读边界继续生效，见[部署说明](operations/deployment-readiness.md)。

## 轻量 ETL 交付目标与接口体验

完成轻量基线后可执行：

```sh
python3 scripts/experience_env.py fixtures
```

该命令只在体验 PostgreSQL 的 `sample` 数据库创建 `experience_delivery.customers` 空目标表，不增加进程；字段为 `id/name/age/city`，主键为 `id`。目标连接使用独立 `experience_etl` 账号，仅取得目标 schema 使用权及该表的查询、插入、更新、删除权限，不授予超级用户、建库、建角色、复制或来源表读取权限。它通过普通 provider 的来源登记接口建立“独立体验ETL交付目标”，不会直接写业务元数据。

私有 `.local/experience/delivery-fixture.json` 保存连接信息和登记后的 `source_id`。再次执行复用已登记连接，不清空交付数据；如果首次发现同名角色或 schema、但没有本项目归属记录，则拒绝接管。中途中断且归属记录仍为 `provisioning` 时须先核对事务结果再恢复，不能删除归属记录后盲目重试。

在交换平台以 provider 登录，使用已发布客户成果创建 ETL 任务，目标选择“独立体验ETL交付目标” → `experience_delivery.customers`，四字段同名映射，主键 `id`。来源加工及交付仍须通过独立岗位审批。目标就绪或连接已登记不表示 ETL 任务已经成功运行；应检查实际运行、六条目标记录和对账结果。

已有成果也可用于原生数据接口，无需新增外部网络出口：在“数据服务”中选择已发布客户成果及其已审核目录，以 `SELECT id,name,age,city FROM result ORDER BY id`、快照模式和最大页大小 20 验证查询，然后正常发布、挂载、独立审批并创建消费者密钥。`/api/v1/data-services/validate` 是实际结果验证接口；发布接口为 `/api/v1/data-services`。接口自身调用权限仍受已有授权与安全策略检查。外部 HTTP API 适配器保持 `API_ALLOWED_TARGETS=[]`；原生接口可用不代表任意外部地址已开放或已验证。

## CDC 与流式计算的区别

`--feature cdc` 是仅包含 Kafka（640MiB）及 Connect（768MiB）的可选捕获组合，不包含 Flink 和额外来源数据库。结合代码功能时全部体验服务预留为 **3.6875GiB**，再加 **1GiB** 主机余量；在其他容器约占 4GiB 的 8GiB Docker VM 中容量检查会拒绝启用。此组合仅提供 CDC 捕获组件，不自动创建复制用户、publication、捕获任务或验收证据。

已有持续治理工作器使用 Flink；因此仅启用 `cdc` 不能宣称持续治理已经可用。原有 `streaming` 组合仍同时包含 Kafka、Connect 及完整 Flink 服务，只能在完整容量预检通过后启用。普通批量治理、双语言代码、目录审批、受控交付及原生数据服务不依赖这些组件。AI 对话仍需真实模型服务配置，不能通过虚构凭据完成体验。

2026-11-11 已在独立 `19443` 实例完成上述新增路径：普通 provider 的“独立体验：六条客户成果受控ETL交付”任务成功，目标物理六行与有权读取的成果逐字段一致，对账状态 `passed`；目标数据库账号实际读取来源客户表和元数据库 `users` 均被拒绝。证据为私有 `etl-verification.json`，目标数据未清空。

“独立体验客户原生接口”和“独立体验客户接口目录”已保留供体验：真实查询验证、独立目录/挂载复核、消费者自批拒绝及另一身份批准均通过，消费者密钥实际调用返回六条预期记录。证据为私有 `native-api-verification.json`；密钥仅存私有初始化进度，不在本文或登录页公开。外部 HTTP 上游仍未验收。PostgreSQL CDC 与 Python→JavaScript 持续处理的真实全链路、保存点及普通检查点恢复、应用重启、撤权、超时和取消已完成验收，详见[持续脚本验收报告](validation/governance/2026-11-11-continuous-sandbox-scripts.md)。MySQL/Oracle真实来源、高可用集群冷恢复及生产吞吐不在本次已通过范围。


## 异常行为的临时阻断

独立应用设置 `ISOLATED_EXPERIENCE_GUARD=true`。启动时要求独立会话名称且未配置公开测试账号列表，避免误用于主实例。正常功能按既有岗位、授权和独立复核开放，不增加业务动作白名单。

同一身份一分钟最多600次认证请求、240次写操作、8个并行写请求；一分钟20次被明确禁止的写操作也会触发30秒写操作冷却。冷却不延长已有权限，也不取消权限；正常读取仍可用，只有总请求洪泛会暂时限制读取。请求返回429及 `Retry-After`，到期自动恢复。普通校验错误、登录失效、只读页面的权限差异不会计入越权写入触发条件。异步作业继续受既有工作组、沙箱和目标锁限制。

阻断证据使用有容量轮转的结构化服务日志，不把每次恶意请求写入元数据库；记录身份、路由、原因和追踪号，避免正文及凭据。它覆盖已实现的频率/并发/反复越权信号，不能代替完整鉴权或声称识别全部恶意行为。
