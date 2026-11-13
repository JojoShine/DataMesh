# CDC 条件诊断与处理指引验证

日期：2026-10-21。本次仅增加条件诊断和操作指引，未实现通用远程 CDC 作业创建。

## 行为

- 数据源管理的「CDC 预检」、建立接入任务的「检查 CDC 条件」使用同一报告，显示当前值、要求、负责人、处理步骤、可复制只读验证 SQL，支持重新检查。
- 检查结果区分已通过、未满足、无法检测、平台暂不支持和等待检查。连接失败时其余数据库检查等待执行；无法读取系统视图不等同于参数错误。
- PostgreSQL 检查日志、采集账号复制权限、容量及表访问/复制标识。MySQL 检查 binlog、账号授权、主键及所选表真实 SELECT 权限。Oracle 检查归档/补充日志，完整 LogMiner 和逐表日志要求明确交给 DBA 核对。
- 支持传入 schema/table 缩小范围，兼容原有空对象请求；保留权限和可信白名单校验。检查内置作业以外的表不会覆盖其原有 CDC 状态。
- 预检不修改外部数据库参数、发布、复制槽或业务记录，不会为远程源创建虚假采集对象。MySQL 读取权限通过零行查询验证。

## 已执行验证

- PostgreSQL/MySQL 真实数据库回归（race）：普通账号缺少复制权限、无主键表、引号表名、错误凭据、仅写账号可见表却不可读、特殊输入、本地作业范围状态保护。临时库和账号在结束时清理。
- 后端 go vet、内部单元测试、服务及管理程序构建通过。
- 前端类型检查、42 项测试及生产构建通过。初次并行运行出现 4 项超时，单工作进程完整重跑 42 项通过。
- 11 项契约测试、OpenAPI 校验、3 项工具测试通过。
- 浏览器检查数据源与任务两个入口、1920×1080 桌面和 390×844 窄屏。窄屏报告使用单列，桌面使用表格；底部操作保留可见。
- 更新本地服务前确认无活动 ETL/业务操作；部署后核验就绪、原有数据源/白名单/ETL 查询和生产资源与构建一致。

## 实际登记源检查

「数可信息」所选 public.parking_application：连接、复制权限、复制容量和表复制标识通过；wal_level 为 replica，尚未满足 logical 要求。发布/复制槽配置等待平台接入；通用远程 CDC 作业明确为平台暂不支持。

内置 PostgreSQL 的连接、数据库条件及发布通过；采集服务当前无法核实，报告为无法检测，不将数据库条件通过等同于作业已可运行。Oracle 外部实例配置分支未在真实 Oracle 上重新验证。

详细诊断及部署核验保存在本地 `.local/smoke/cdc-diagnostics.json`、`cdc-guidance-deployment.json`，不包含数据库凭据。

## 条件依据

- [PostgreSQL 逻辑复制配置](https://www.postgresql.org/docs/17/logical-replication-config.html)
- [Debezium MySQL 配置说明](https://debezium.io/documentation/reference/stable/connectors/mysql.html)
- [Debezium Oracle 配置说明](https://debezium.io/documentation/reference/stable/connectors/oracle.html)
