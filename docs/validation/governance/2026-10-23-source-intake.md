# 来源入口与空间元数据前置关系

用户要求：新增来源统一从共享交换资源进入，元数据管理先完成当前空间来源关联，不混入治理成果。

## 实现

- 新增只读 source-candidates 接口，复用已有治理职责授权与分页搜索，仅返回 table_resource、file_resource、api_resource；停用资源不供新增选择。
- 来源关联后端拒绝 governed_dataset 和 raw_object 直接关联。治理成果及直接连接采集对象不再作为新增候选；已有引用不删除、不改写。
- 带 workspace_id 的元数据目录强制按有效来源关联限制，不可通过 workspace_sources_only=false 绕过。详情、结构读取与维护也校验来源已关联。
- 结构版本不同仍可查看元数据，便于发现变化；关联的固定契约版本不会自动修改。
- 来源选择、元数据空状态、使用说明同步调整。数据流来源选项跟随同一空间目录范围。
- 未带空间的旧接口保留原有权限行为，以兼容既有调用；后台任务与历史数据流执行机制不变。
- 无数据库迁移，无主环境业务数据改写。

## 验证

- TypeScript、生产构建通过。
- 空间相关前端8项通过；元数据及数据流相关18项通过。
- Go vet及workflow单元检查通过。
- 18项接口契约检查及OpenAPI校验通过。
- 隔离PostgreSQL的DefinitionScope、ExecutionScope通过：未关联元数据为空/详情拒绝，候选只含交换资源，版本变化仍可见，发布成果仍不能直接新增关联，历史成果引用执行与撤回校验继续生效。

部署结果见 `.local/governance-source-intake-deployment-path` 对应manifest。

发布 `governance-source-intake-20261023-102139` 已 verified。7个空间的来源候选和元数据均只返回共享交换资源类型；44项HTTPS检查、3个静态文件校验通过，schema保持28，保护业务数据未变化。首次备份期间记录更新导致发布中止；重新取得一致备份并恢复验证51张表后才完成发布。
