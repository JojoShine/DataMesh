# 平台可信数据源白名单验收（2026-10-21）

入口：共享交换 → 数据源管理 → 可信数据源。仅admin可查看和增删，支持PostgreSQL、MySQL、Oracle。精确匹配类型/地址/端口/数据库或Service Name，不接受通配符或凭据。平台配置保存于数据库；环境变量与内置样例保持只读兼容。

平台条目删除为软撤销，保留目标和审计历史。所有新连接实时检查，无进程缓存，无需重启；不会删除数据源或强制终止已建立的外部连接。同一目标若仍被部署配置放行，则仍允许连接。

## 验证结果

- 真实PostgreSQL隔离集成：新增前拒绝、新增后真实连接成功、新服务实例可读取、保存源测试与更新、撤销后连接及表发现拒绝、重新添加、三种数据库类型、静态只读、输入边界、非admin/CSRF拒绝、幂等和操作审计，全部通过。
- 独立代码复核发现事务内另取元数据连接的等待风险。已改为所有策略查询显式复用现有事务；最大连接数1时创建、保存源测试、更新、撤销和重新添加回归通过。修改前该回归出现503，修改后通过。
- 现有TestSourceDraftConnection与TestETLRealExchange回归通过；测试均使用临时数据库并在完成后清理。
- 前端36项测试、TypeScript检查/生产构建、Go vet/单元测试/服务构建、11项契约测试和OpenAPI校验通过。构建仍有既有主包体积提示。
- 1920×1080、1440×900、800×900只读示例页面检查通过；生产页面使用真实接口，不包含示例白名单。
- 本机迁移000008和服务已更新。实际HTTPS就绪、admin白名单查询、现有数据源/ETL查询、无凭据字段、最新静态资源与构建完全一致、登录/退出均验证通过。本机业务白名单没有添加测试目标。

复现：`python3 scripts/test_identity.py --run 'TestSourceAllowlistAdministration|TestSourceDraftConnection|TestETLRealExchange'`，以及`npm --prefix web test`、`npm --prefix web run build`、`scripts/check.sh contracts`。

新增、删除分别沿用业务操作审计`add_source_allowed_target`和`remove_source_allowed_target`，记录操作者、目标编号、结果及追踪号；审计失败会回滚白名单写入。目标明细保留在`source_allowed_targets`历史行。
