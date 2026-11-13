# 数据资产后台能力实施计划

目标：在既有资产消费边界内补齐数据产品、契约核验、认证生命周期、业务语义和本人运营统计。使用 executing-plans 连续执行；前后台按以下固定接口并行实施，最终独立审查。

约束：保留目录与交付授权；产品不授予访问权限；不新增依赖；所有变更使用现有 CSRF、幂等、审计和乐观版本。仓库无 HEAD，原地增量编辑、不提交。

## 后端接口契约

资源 Ref = {resource_type,resource_id,contract_version}（沿用 Resource）。
定义 ProductDefinition = {name:string,description:string,domain:GovernanceRef|null,terms:GovernanceRef[],resources:Resource[],contract:{freshness_seconds:number,max_failure_rate:number|null},usage_restrictions:string}。
资源最多10项，术语最多20项；freshness_seconds 0表示未约定，最大31536000；max_failure_rate 0..1，null表示未约定。

Product = {id,department_id,owner_id,state,version,draft_id:string,published_id:string,revision_id:string,revision_state:string,definition:ProductDefinition,members:{resource:Resource,name:string,fields:{name,logical_type,sensitive}[]}[],semantics:{domain:{id,version,name,description}|null,terms:{id,version,name,description}[]},reviewer_id:string,review_note:string,valid_until:string|null,certification_status:"valid"|"expired"|"unreviewed",replacement_id:string,reason:string,can_manage:boolean,can_review:boolean}。

- GET /api/v1/assets/products?page&search&manage=true|false：Page<Product>。默认仅发布/弃用产品，manage=true仅有权限的本部门管理者可见草稿。普通使用者与维护者默认均看发布版；管理模式选择草稿。
- GET /api/v1/assets/products/:id?manage=true|false：Product；非公开草稿不可被普通使用者查看。
- POST /api/v1/assets/products：ProductDefinition -> Product，初始草稿。资产平台 provider/governor/admin 可创建；普通用户不可创建。
- POST /api/v1/assets/products/:id/revisions：{expected_version,definition:ProductDefinition} -> Product。仅本人或管理员可维护，已提交草稿不能修改；旧发布版继续可见。
- POST /api/v1/assets/products/:id/submit：{expected_version,reason:string} -> Product。
- POST /api/v1/assets/products/:id/approve：{expected_version,reason:string,valid_until:ISO string} -> Product。仅本部门 governor/admin，必须与产品负责人分离，最长366天；发布时重新核对全部资源及业务语义。
- POST /api/v1/assets/products/:id/reject：{expected_version,reason:string} -> Product。相同审核者规则。
- POST /api/v1/assets/products/:id/deprecate：{expected_version,reason:string,replacement_id:string} -> Product。已发布产品可弃用，替代项可为空，非空须为另一已发布产品；不撤销原授权。
- POST /api/v1/assets/products/:id/retire：{expected_version,reason:string} -> Product。仅可停用已弃用产品，消费侧不可再查找。
- GET /api/v1/assets/product-semantics?kind=domain|term&page&search：Page<{id,version,name,description}>。仅管理角色，复用有权限读取的已发布治理定义；输入框远程检索，不粘贴内部完整定义。
- POST /api/v1/assets/products/:id/check：{expected_version} -> ProductCheck。任何资产用户可核对已发布产品，以已发布版本执行，检查结果只归本人。
- GET /api/v1/assets/products/:id/checks?page：Page<ProductCheck>，仅本人历史。
ProductCheck = {id,product_id,revision_id,state:"passed"|"violated"|"unknown",checked_at,items:{resource:Resource,name:string,state,checks:{code,status:"passed"|"violated"|"unknown",message:string}[]}[]}。
检查项：共享及固定版本可用性、已发布字段兼容性、生成时间对应新鲜度、实际隔离记录比例。未知不视为通过；有违约优先返回violated。结果只在主动核验时生成，不声称后台推送。核验列表就是可追溯违约记录。违约同时写入个人站内提醒，相同发布版与相同证据去重。
- GET /api/v1/assets/usage?days=30（1..90）：{days,from,to,total:number,items:{action:string,count:number}[],daily:{date:string,count:number}[],cost:null,value:null,scope:string}。仅本人成功交付审计，次数不代表数据行数/字节数/人数；不记录敏感参数。时间以UTC明确展示。

## 任务

- [x] 后端：先写验证定义、状态/审批、发布隔离、版本冲突、资源暂停、契约未知/违约、本人使用统计的失败测试；实现三个独立表及迁移、产品与契约接口、语义安全映射、审计聚合。生产数据库不迁移，仅隔离测试库。
- [x] 前端：数据产品列表/详情、单层表单与审批流程；契约与认证页面（同一产品组件模式）；本人使用统计；菜单/指南及行为测试。拥有 web 下文件，后端不编辑。
- [x] 集成验证：真实PostgreSQL隔离测试、Go静态检查、前端相关测试与构建、桌面窄屏检查；最终独立审查。

## 裁定与边界

产品明确引用既有已共享版本并存储审核时的字段快照；授权仍逐个资源申请，产品审批不等于数据使用审批。产品变更不自动改旧产品发布版。认证为人工限期审查，不宣称客观质量全部通过。无数据证据的成本与价值返回null。已发布业务语义仅映射名称与说明；血缘以产品到共享资源的安全关联及原有来源处理说明为边界，绝不暴露内部节点SQL、凭据或未共享来源。


验证收尾：真实PostgreSQL 5项顶层业务场景及竞态检测通过；Go静态检查、全部内部测试和服务构建通过；前端全量255项通过、1项按原配置跳过；前端生产构建和19项契约测试通过；27个布局检查通过。独立审查1项重要发现已按失败测试→修复→数据库回归完成。详见 `docs/validation/asset-products/validation.md`。
