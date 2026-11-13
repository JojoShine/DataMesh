# CSV 数据沙箱与轻量治理

## 目标与约定

用户要求CSV轻量治理得到可单独使用的沙箱支撑。沿用此前CSV导入后用SQL库化处理的方向，实现18443上的独立“数据沙箱”页；轻量治理能进入沙箱，也能接收沙箱保存的结果文件。无需创建正式治理空间、目录、模型或发布。不新建数据库进程或增加Docker内存，复用现有Doris和RustFS。

实现选择由代理根据既有架构决定，用户已要求合理假设下直接实施：相比每访客一套数据库，复用Doris隔离命名空间更适合12GiB主机；相比只在浏览器SQL演示，后端真实库表才能保存结果并接入现有治理。SQL使用成熟MySQL AST解析器，新增一个固定版本解析依赖以避免自行用正则构造安全边界。

## 使用闭环

创建/选择私人沙箱 → 上传CSV并命名逻辑表 → 查看字段及预览 → SQL筛选/清洗/转换/关联/CTE/聚合 → 查询结果和历史 → 保存结果为新逻辑表 → 下载CSV或送入轻量治理。轻量治理复用已有文件/运行能力，不依赖沙箱继续存活。

入口 `/platform/governance/sandbox`，置于“处理数据”与轻量治理相邻；不受正式治理空间参数影响。轻量治理提供去沙箱入口，接收 `file_id` 自动选择已移交CSV，用户仍自行命名并保存处理单元。所有显示遵循web/AGENTS.md和既有字号、Dialog及窄屏表格规范。

## 范围与边界

- CSV复用现有UTF8/BOM解析，保持前导零、空字符串、Unicode、原始字段标签；所有导入字段默认为文本，SQL中显式CAST转换。
- 账号和部门私有；公开FullExperience账号额外按当前登录会话隔离私人沙箱及移交的轻量文件/单元/运行。普通账号可跨登录复用自己的沙箱。会话边界只使用服务端认证上下文派生的不可逆范围，不接受调用方指定归属。
- 最多4个活动沙箱/账号，8张有效表/沙箱，总80MiB原始/结果内容。每文件20MiB、100列、100000行；预览最多1000行并明确是否截断，物化最多100000行/20MiB，超过则失败不静默截断。查询每次20秒引擎预算/128MiB，单沙箱并发1，全局沙箱执行并发1；其他现有治理并发不改变。
- 沙箱有效期7天，公开体验为24小时；显示到期时间。显式关闭和到期回收只处理可证明归属的沙箱表/对象。移交轻量治理的独立CSV不随沙箱清理删除。
- 允许单条SELECT（包括非递归WITH、子查询、UNION、JOIN、GROUP BY、HAVING、ORDER BY、LIMIT、CASE、CAST及明确允许的常见纯函数/聚合）。不允许用户直接执行DDL/DML、系统/跨库表、外部表函数、未知函数、变量、文件/网络读写、OUTFILE、INTO、锁、优化器提示或多语句。用户保存结果由后端生成受控DDL/写入。
- AST所有真实表必须属于当前沙箱并按服务器映射替换为已批准的物理查询，不透传原始SQL；列名/CTE作用域正确处理，语法不支持时明确提示。Doris使用新增专用数据库及受限读写身份，运行服务不得使用Doris管理员身份；用户SQL只走只读身份。SQL原文、数据正文、密码不写通用日志或审计正文。
- RustFS保存原CSV及结果对象；平台PostgreSQL只保存沙箱、表结构、作业/查询历史与运行证据，不保存查询业务行。任务排队/执行/成功/失败/取消明确可见；未知提交和进程重启保留可核对状态，不伪造成功或自动重放未知写入。导入/物化唯一物理身份，提交与发布行数/大小验证；失败有归属的物理暂存清理/可重试回收。
- 后端后台任务复用现有服务进程/lifecycle模式，不在HTTP10秒期限内做大文件库化。权限变化、关闭、取消与超时都阻止发布结果；worker使用已冻结私人范围并核对当前身份，禁止普通HTTP伪造内部范围。

## 后端契约（新增，不改变旧API）

Base `/api/v1/data-sandboxes`，要求governance/governor以及正常CSRF/审计/异常防护。

- GET base → Page<Sandbox>; POST base `{name}` → Sandbox; GET `/:id` → Sandbox; POST `/:id/close` `{expected_version}` → Sandbox。
- GET `/:id/tables` → Page<SandboxTable>; POST `/:id/tables` multipart `file`,`name` → SandboxTable (queued); GET `/:id/tables/:table_id` → SandboxTable; GET `/:id/tables/:table_id/records?page=&page_size=` → `{items,total,fields}`; POST `/:id/tables/:table_id/remove` `{}` → SandboxTable。
- POST `/:id/queries` `{sql, mode:'preview'|'materialize', table_name?:string}` → SandboxQuery (queued); GET `/:id/queries` → Page<SandboxQuery>; GET `/:id/queries/:query_id` → SandboxQuery; POST `/:id/queries/:query_id/cancel` `{}` → SandboxQuery; GET `/:id/queries/:query_id/records?page=&page_size=` → `{items,total,fields,truncated}`。
- GET `/:id/tables/:table_id/download` → CSV; POST `/:id/tables/:table_id/lightweight` `{}` → existing LightweightFile with file_id; POST `/:id/files` `{file_id,name}` → SandboxTable queued imports an already-owned lightweight upload without uploading again.

Sandbox `{id,name,state,version,expires_at,created_at,updated_at}`。
SandboxTable `{id,sandbox_id,name,state,fields:Field[],field_labels:Record<string,string>,rows,bytes,query_id?,error_code?,created_at,updated_at}`。
SandboxQuery `{id,sandbox_id,sql,mode,table_name?,state,fields:Field[],row_count,truncated,result_table_id?,error_code?,created_at,updated_at}`。
所有整数结果以字符串保留精度，小数同样为字符串；空值为null，不把空串变null。字段SQL名使用file.fields.name，中文原名显示在标签和字段表中。

PostgreSQL迁移000048新增data_sandboxes、data_sandbox_tables、data_sandbox_queries（含作业租约/取消/来源对象），只追加schema，不迁移/改写既有业务行。迁移降级有数据时拒绝。worker、HTTP和SQL编译器拆分专用文件但留在现有workflow模块；复用现有storeFile/readStoredFile及CSV解析。

SQL编译器接口：`compileSandboxSQL(source string, tables map[string]sandboxSQLTable) (string,error)`；`sandboxSQLTable{Database string; Table string; Fields []Field}`。只读编译器映射采用受控派生SELECT，避免内部ordinal出现在SELECT*结果；支持逻辑名作为列限定名，不允许用户物理schema限定。

## 验收

单项回归先失败后修复：SQL绕过/跨表/函数/CTE边界，CSV边界，私人范围含同一共享账号两个登录，会话迁移轻量后的读取/运行隔离，超限/取消/权限撤销/到期回收及进程恢复。真实18443：两CSV→SQL JOIN+CAST+CTE聚合→物化→下载精确值→转轻量治理→实际运行；重新查询、历史、跨访客拒绝、恶意SQL拒绝、宽屏1920和窄屏390无溢出。旧CSV轻量治理及既有主环境数据保持可用。发布前备份并实际恢复验备，主数据逐行摘要不变，单一主计算链路与12GiB资源保持。

## 构建复核后的实现裁定

- 表就绪状态统一为 `ready`；作业状态使用 `queued/running/succeeded/failed/canceled/unknown`。关闭先 `closing` 拒绝所有读写，再做有归属清理；清理未知时保留状态和配额，不伪报已删除。
- 80MiB按本沙箱所有未清理表的源CSV/保存结果字节与运行预留计算，排队/未知任务同样占槽及配额；每个物化任务先预留20MiB。预览对象最多4MiB/1000行，单格最多1MiB；历史对象回收不删除历史元数据。所有共享账号私人旧记录无scope时拒绝访问，普通账号兼容。
- CSV下载将SQL NULL编码为空字段并明确提示CSV表达力；沙箱内部及“送入轻量治理”采用另存的受保护JSONL数组边车，保留NULL与空串差异。轻量输入字段仍可作为文本使用，大整数和小数原文保留。后台冻结scope与不可变输入指纹，不允许HTTP伪造worker范围。
- 全局并发和空间配额必须数据库协调；外部写入前固定attempt、目标物理身份、load label及输入对象摘要。失联写入保留unknown和预留，依据引擎结果/目标内容核验后再完成或清理，禁止仅因租约过期重放。发布CAS核对token、期限、取消、空间状态/版本、当前身份权限。

## 序列化容量口径

用户上传CSV及物化结果导出CSV保持20MiB上限，物化按实际CSV编码（含标题及转义）计量。内部用于保留NULL、字段及精确文本的规范JSON对象独立上限64MiB，避免合法CSV因数组封装扩展而受到隐含20MiB限制。沙箱80MiB按实际源CSV与内部规范对象字节累计；物化先预留20MiB，获取结果后在上传规范对象前原子调整到实际字节。预览仍限1000行/4MiB，单格1MiB。

- SQL方言明确为MySQL兼容SELECT子集，整数CAST使用SIGNED（而非DDL类型BIGINT）；真实Doris精确整数/小数/CASE空值测试已证明该语法，前端及指南同步提示。
