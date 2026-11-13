# 本地运行和恢复

## 配置与版本

使用 Compose 项目名 datamesh。平台 PostgreSQL、原始归档、Kafka、Flink 状态、Doris、三类样例源各有独立命名卷；平台 PostgreSQL 和源 PostgreSQL 不共享实例或账号。迁移使用 golang-migrate v4.19.1，不在 Go 服务启动时 AutoMigrate。

运行配置由 `scripts/dev-env.sh init` 写入权限为 600 的 `.local/runtime.env`，目录权限为 700。不会覆盖已有凭据；证书、初始账号、SQL 联调配置、备份均留在忽略目录。Go 服务只得到应用数据库账号和会话密钥，不获取数据库迁移账号。

镜像按 digest 固定，JAR 下载校验 SHA-256。当前版本矩阵针对 Linux ARM64；其他架构需要重新验证，不能默认采用仿真。Docker 内存预检结合所选分组上限与当前容器用量，不能代替性能压测。

## 启动和健康

本机统一业务入口为 `https://localhost:18443`。2026-11-12 已将完整流式组件接入主环境，独立19443验证服务停止并保留数据卷；不同时启动两套计算链路。初始化会保留或生成 `.local/stream-script/token`（目录700、文件640），仅应用、TaskManager、SQL Gateway获得只读令牌，后两者仅挂载应用公开证书及令牌补充组。构建适配器后放置于 `.local/flink-jars/datamesh-stream-script.jar`；令牌不进入SQL、代码沙箱或前端。

SQL Gateway使用真正的JVM堆参数和1280MiB容器预算，及时关闭已确认提交的独立会话；未知提交先核对实际作业。Doris维持3GiB容器/2560MiB内部上限，通过限制线程分配缓存降低空闲内存。当前验证结果与持续运行边界见[主环境核验记录](../validation/experience/2026-11-12-main-18443.md)。

先启动 base，独立执行 migrate，再构建和启动 app。数据验证依次启动对应源、Kafka/Connect、Flink/Doris；`scripts/check.sh data` 负责样例的资源分组和状态核对。

- PostgreSQL：连接测试及独立版本化迁移。
- RustFS：实际 S3 写、读、列举、数据与清单校验。
- Kafka：创建指定原始主题、生产消费；Connect 的配置与状态存储在独立内部主题。
- Connect：分别检查插件、连接器及 task 状态；注册后 404 可能是状态传播窗口，不能因此重复创建。
- Flink：检查 TaskManager、可用 slots、实际 job 和检查点；仅 HTTP 可达不表示作业成功。
- Doris：分别核对 FE/BE Alive、实际写入和查询，以及错误密码被拒绝。
- Go：`/health/live` 和 `/health/ready`；依赖失败返回 503，未实现业务 API 返回 404。

默认配置只适用于本地开发联调，未建立生产高可用、外部接入网络隔离及性能承诺。本机 Docker 内存已按用户授权调整为 12 GiB；沙箱复用现有服务，不再增加虚拟机内存。

## CSV 数据沙箱

迁移 `000048` 后，独立入口为 `/platform/governance/sandbox`，可从轻量治理导入文件并将 SQL 结果送回轻量治理。首次配置执行 `scripts/dev-env.sh init` 补齐私密凭据，再运行 `python3 scripts/sandbox_env.py prepare` 和 `python3 scripts/sandbox_env.py provision`；已部署实例不需重复初始化。脚本仅创建专用 `datamesh_sandbox` 命名空间及受限读写身份，保留原有配置，不向应用提供 Doris 管理员凭据。

应用沿用 512 MiB 容器预算，Go 内存目标为 448 MiB；沙箱任务按全局租约串行执行。备份与恢复需同时保留 PostgreSQL 元数据、Doris 沙箱表、RustFS 对象及现有运行配置中的加密密钥，不能只恢复元数据或重新生成密钥。普通账号沙箱保留 7 天，公开完整体验按登录隔离并保留 24 小时；已送入轻量治理的独立文件不随原沙箱关闭而删除。使用限制、取消与到期验证边界见[沙箱验收记录](../validation/experience/2026-11-12-csv-data-sandbox.md)。

## 账号和权限

一套账号在四个平台共用，Cookie 为 Secure、HttpOnly、SameSite=Strict。登录与所有受保护写操作校验来源，写操作还需 X-CSRF-Token。密码散列、会话散列和完整凭据不返回接口；前端不得持久化密码或会话原文。

创建账号和禁用操作通过受控管理命令。禁用删除所有会话，角色/平台权限每请求读取。普通应用账号可追加审计，数据库拒绝其修改或删除审计记录。

身份测试由 `scripts/test_identity.py` 为每次检查创建随机命名的 `datamesh_test_` 数据库，使用同一成熟迁移工具建表。测试代码拒绝使用运行库，结束后删除测试库。早期开发测试遗留账号已停用并失效会话，审计保留。

## 保存点与分组恢复

当前验证入口只控制本项目指定的样例作业和样例连接器。

1. 正在运行的 Flink 样例通过 `scripts/smoke/savepoint.py` 创建保存点并受控停止。
2. 保存点路径、来源 job_id 和 sink label 前缀记录在 `.local/smoke/flink-savepoint.json`。状态目录在独立命名卷中。
3. `scripts/smoke/processing.py` 查询现有 job；运行或恢复中的作业继续复用。终态或 JobManager 重建后才按已保存元数据提交恢复。
4. 提交超时或无法确认 job_id 时记录 unknown，先核对实际 `datamesh-wp00-postgres` 作业及脱敏日志，再决定处理，不能直接再建任务。
5. MySQL/Oracle 源停止前，先暂停连接器并核对状态。保留 Kafka 和源数据卷，不通过清卷“修复”位点。

原始接收、治理处理、Doris 和归档是不同进度。本批的保存点恢复和样例对账不证明完整平台控制器已经实现，也不证明任意旧事件重放安全或跨两路 exactly-once。

## 备份与停止

`scripts/smoke/restore.py` 使用 pg_dump 创建 `.local/backups/platform.dump`，在独立、无网络、无宿主端口的临时 PostgreSQL 容器中还原并核对身份、会话和审计表。原运行库不被覆盖；临时实例在检查结束后清理。备份包含受保护数据，不应进入版本库或公开工件。

停止使用 `scripts/dev-env.sh stop <分组>`，默认保留命名卷。普通停止与清除数据是两件事，默认脚本不提供清卷操作。若恢复配置未知、源日志缺失、检查点不可用或清单不完整，应保留证据并阻断，不能跳到最新进度假装完成。

## 本批仍未覆盖的验收

三库快照期间并发变更、源日志过期、任意 DDL、主键变更、严格源序列及旧版本重放、持续归档与失败补偿、质量门禁、跨部门数据授权、审批与撤权业务闭环、六组前端页面和全部 AT-01—AT-24，由后续工作包实现并验收。本批真实烟测不会替代这些验收。
