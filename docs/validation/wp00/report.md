# WP-00 首批验证记录

日期：2026-10-15。环境：Apple Silicon，Docker Linux ARM64，8 CPU、约 7.75 GiB 内存。项目目录为 `/Users/tbtparent/Projects/DataMesh`，Compose 项目名固定为 `datamesh`。

## 交付结论与范围

本批交付四平台共享契约、本地 Docker 环境、轻量共享账号后台和真实数据链路联调入口。共享交换覆盖前道原始接入与后道治理成果交换；治理、资产、安全职责及交接见架构文档。四平台同源共享账号与 Cookie，不引入独立身份服务。

这不是一期全部功能的验收。业务 OpenAPI 中除身份与健康之外的路由仅定义契约，运行后台返回 404。未以模拟响应代替业务实现。

## 固定版本

| 组件 | 实测版本 |
| --- | --- |
| Go / Gin / GORM | 1.25.5 / 1.11.0 / 1.31.1 |
| 平台及样例 PostgreSQL | 17.11 |
| MySQL / Oracle Free 镜像 | 8.4.7 / 23.26.3-slim |
| Kafka / Debezium Connect | 3.9.1 / 3.6.3.Final |
| Flink / Kafka SQL connector | 1.20.3 / 3.4.0-1.20 |
| Doris / Flink Doris connector | 4.0.3 / 1.20-25.1.0 |
| RustFS / golang-migrate | 1.0.0 / 4.19.1 |

精确镜像摘要、架构、JAR 地址和 SHA-256 以 `deploy/versions.lock.json` 为准。未使用跨架构仿真，没有修改 Docker Desktop 全局设置，也没有接管原有其他项目的 PostgreSQL、Redis、MinIO。

## 实际检查

| 检查 | 结果及证明范围 |
| --- | --- |
| `scripts/check.sh contracts` | 9 项正反例通过，OpenAPI 3.1 校验通过；证明契约一致性 |
| `scripts/check.sh tooling` | 3 项连接器控制边界通过，Python 脚本语法通过 |
| `scripts/check.sh environment` | 原生镜像、端口、内存与 Compose 配置检查通过 |
| `python3 scripts/smoke/preflight_negative.py` | 占用端口、缺失锁定镜像、资源超额、Docker 不可达均被拒绝；无服务写入 |
| `scripts/check.sh go` | Go 格式、vet、构建；身份回归另使用真实 PostgreSQL |
| `scripts/check.sh integration` | 创建随机隔离测试库、执行迁移、Go race 身份测试，结束后删除测试库 |
| `python3 scripts/smoke/identity.py` | 实际 HTTPS 登录、同一会话访问四平台能力接口、退出后失效；显式信任本地证书 |
| 独立迁移 | 空库建表及已迁移库重复执行通过；应用不自动改表 |
| 三库采集 | 真正 Debezium 快照及后续 insert/update/delete，与源主键、姓名、小数核对；未采用定时 SQL 替代 CDC |
| Flink → Doris | PostgreSQL 样例 1000 行核对、TRIM 转换、精确小数、更新与删除；键含来源和对象标识 |
| 原始归档 | 三库有界 Kafka 导出，topic/partition/offset 和原生源位点保留；gzip/SHA-256、连续范围、S3 写读列举、清单写读通过 |
| Flink 保存点 | 实际保存点停止计算，源与 Kafka 保留，重新启动并恢复作业后对账通过 |
| 平台备份恢复 | pg_dump 恢复至无网络、无宿主端口的临时实例，核对身份/会话/审计表，原运行库不覆盖 |

PostgreSQL 和 MySQL 最初的基线快照在早期候选连接器版本运行过；最终版本另用独立连接器、主题、位点执行全新快照及增删改，并核实事件 `source.version=3.6.3.Final`。临时连接器和专用探针数据清理后才记录通过，不重置基线连接器的保存进度。Oracle 在最终版本上已完成真实 LogMiner 快照与增量验证。

身份覆盖共享会话、会话散列、权限回收、停用、重新启用不恢复旧会话、空闲/绝对过期、轮换、退出、CSRF/来源、输入/限流、重复账号竞争和应用数据库角色不可修改审计。登录与禁用通过账号行锁串行化，另有受控并发回归，防止禁用清理后又出现新会话。平台能力不等于数据资源授权。

## 证据位置与运行状态

`scripts/check.sh all` 最终集中检查退出码为 0。随后针对登录/禁用并发修复再次执行 Go 格式、vet、构建和隔离身份 race 回归（61.269 秒），退出码为 0；后台镜像已重建并重新通过实际 HTTPS 登录检查，日志为 `.local/final-identity-fix-check.log`。详细运行日志为私有 `.local/final-all-check.log`；单项结构化结果在 `.local/smoke/*-result.json`，归档清单同时写入对象存储；保存点元数据在 `.local/smoke/flink-savepoint.json`。这些联调数据、备份、SQL 和凭据留在忽略目录，不作为公开工件。

验证分组运行以适应本机资源。结束时保留 PostgreSQL → Kafka → Flink → Doris 样例和基础服务运行；MySQL/Oracle 连接器暂停，源容器停止。没有声称三库和全部计算组件可同时满足本机资源预算。

本轮基线有界导出为 PostgreSQL 1019 条、MySQL 16 条、Oracle 10 条事件，最终源状态分别核对 1000、3、3 行。全新最终版本快照的临时探针另计，不与基线范围混算。恢复后观测到 1 次 restored、10 次 completed、0 次 failed 检查点；该计数只描述此恢复作业的观测窗口。

早期开发测试遗留的 7 个账号已停用并失效会话，保留审计。后续身份测试使用临时数据库，不再污染运行库。数据库级恢复仅验证当前身份基线，不代表未来业务表恢复已经验收。

## 保留的验收缺口

- 三库快照期间并发增删改、主键变化、源日志过期与全部 DDL 类型。
- 严格源版本比较、旧事件重放保护、完整控制器幂等与 unknown 恢复、长期积压保护和故障矩阵。
- 生产事件规范化处理、质量规则与门禁、血缘和模式演进、持续原始归档与分路补偿。
- 目录发布、申请审批、跨部门和字段授权、原始/治理成果交付与撤权的业务执行。
- 六组业务页面、API 数据源两种模式、完整 FLOW/SSO 及 AT-01—AT-24。
- 全体非源组件统一删除重建矩阵、生产高可用、性能与压力验收。

已有停止重启、部分组件重建、保存点恢复及有界对账证据，不将其扩大为全部故障恢复或跨两路 exactly-once。检查点曾出现重启窗口失败；恢复后正常完成检查点，不宣称全过程零失败。以上缺口保持在后续工作包，原 P0 范围不削减。
