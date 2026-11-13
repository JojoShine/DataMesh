import {sourceDefinitions} from "./src/api/sourceTypes";
// Development-only visual fixture. This entry is not part of the production build.
import { createRoot } from "react-dom/client";
import { App } from "./src/app/App";
import type { Asset, Audit, Dataset, Source, SourceObject, Task } from "./src/api/types";
import "./src/style.css";
import type {GovernanceRecord,MetadataObservation,GovernanceProfileRun as GovernanceRun} from "./src/api/governance";
const previewView = new URLSearchParams(location.search).get("view") ?? "sources";
const governanceModel: GovernanceRecord = {
 id: "preview-model", kind: "model", department_id: "平台管理部", latest_version: 1, version: 1, state: "published",
 definition: {name: "客户主题模型", description: "统一客户标识、姓名和金额口径，作为治理输出模型。", owner_id: "sample", steward_id: "sample",
 model: {layer: "physical", schema: "governance", table: "dim_customer", fields: [
 {name:"customer_id", description:"客户唯一标识",logical_type:"integer",length:0,precision:0,scale:0,primary_key:true,nullable:false,sensitive:false},
 {name:"customer_name",description:"清洗后的客户姓名",logical_type:"string",length:100,precision:0,scale:0,primary_key:false,nullable:true,sensitive:true},
 {name:"total_amount",description:"客户累计金额",logical_type:"decimal",length:0,precision:18,scale:2,primary_key:false,nullable:false,sensitive:false}
 ], relations: [], contract: {freshness_seconds:3600,max_failure_rate:0,usage_restrictions:"按业务授权使用"}}},
 change_note:"建立客户统一模型",migration_note:"",created_at:"2026-10-21T01:00:00Z",published_at:"2026-10-21T01:30:00Z"
};
const governanceFlow:GovernanceRecord={...governanceModel,id:"preview-flow",kind:"dataflow",definition:{name:"客户汇总治理（界面示例）",description:"清洗、汇总与质量分流的界面预览；不执行示例写入。",owner_id:"sample",steward_id:"sample",dataflow:{nodes:[{id:"source_1",kind:"source",inputs:[],source:{resource:{resource_type:"raw_object",resource_id:"sample-object",contract_version:1},fields:["id","name","amount"]}},{id:"clean_1",kind:"transform",inputs:[{node:"source_1"}],columns:[{source:"id",target:"id",rule:"copy"},{source:"name",target:"name",rule:"trim"},{source:"amount",target:"amount",rule:"copy"}]},{id:"output_1",kind:"output",inputs:[{node:"clean_1"}],model:{id:"preview-model",version:1}}]}}};
const governanceWorkflow:GovernanceRecord={...governanceModel,id:"preview-workflow",kind:"workflow",definition:{name:"日常治理编排（界面示例）",description:"固定版本任务依赖预览",owner_id:"sample",steward_id:"sample",workflow:{tasks:[{id:"customers",dataflow:{id:"preview-flow",version:1},depends_on:[],condition:"all_success"}]}}};
const governanceEnterpriseDomain:GovernanceRecord={...governanceModel,id:"preview-enterprise-domain",kind:"domain",definition:{name:"客户（企业统一示例）",description:"企业客户业务范围由统一定义确认；系统来源与部门职责分别保留，不自动合并客户或开放数据权限。",domain_scope:"enterprise",owner_id:"sample",steward_id:"sample"}};
const governanceResponsibility:GovernanceRecord={...governanceModel,id:"preview-domain-responsibility",kind:"domain",definition:{name:"客户 · 本部门责任（示例）",description:"本部门负责客户基础资料及口径维护，不负责交易和售后记录。",domain_scope:"department",enterprise_domain:{id:"preview-enterprise-domain",version:1},owner_id:"sample",steward_id:"sample"}};
const governanceStandard:GovernanceRecord={...governanceModel,id:"preview-enable-standard",kind:"standard",definition:{name:"启停标志（跨表复用示例）",description:"客户与商品的启停标志含义和约束一致时共用；订单付款状态另建标准。",owner_id:"sample",steward_id:"sample",standard:{code:"common.enable_flag",logical_type:"integer",length:0,precision:0,scale:0,nullable:false,sensitive:false,pattern:"",values:["0","1"],value_meanings:{"0":"停用","1":"启用"}}}};

const dataset: Dataset = {
  dataset_id: "sample-customer", contract_version: 1, provider_department_id: "数据提供部门",
  definition: {
    name: "客户基础数据（本地验证）", description: "由本地 PostgreSQL 客户表持续采集，经姓名空白清理与精确小数处理后形成的真实治理成果。",
    source_object_id: "sample-object", owner_id: "sample",
    usage_restrictions: "仅供本地开发验证，敏感姓名以脱敏形式交付。",
    fields: [
      { name: "id", logical_type: "integer", primary_key: true, sensitive: false },
      { name: "name", logical_type: "string", primary_key: false, sensitive: true },
      { name: "amount", logical_type: "decimal", primary_key: false, sensitive: false },
    ],
  },
  publication_status: "published", quality_status: "passed", completeness_status: "passed",
  latest_watermark: { topic: "datamesh_postgres.public.customers", partition: 0, row_count: 1000 },
};
const sourceObject: SourceObject = {
  object_id: "sample-object", source_id: "sample-customer", qualified_name: "public.customers",
  schema_version: 1, fields: dataset.definition.fields,
};
const progress = { status: "streaming", last_success_at: null, position: null, error_code: null };
const metadataObservation:MetadataObservation={id:"preview-observation",object_id:sourceObject.object_id,version:2,source_id:sourceObject.source_id,source_version:1,object_version:1,schema:"public",table:"customers",fields:sourceObject.fields,columns:sourceObject.fields.map(f=>({name:f.name,native_type:f.logical_type==="integer"?"bigint":f.logical_type==="decimal"?"numeric(20,6)":"text",length:null,precision:null,scale:null,collation:""})),changes:[],matches_active:true,observed_by:"sample",created_at:"2026-10-21T01:00:00Z"};
const task: Task = {
  task_id: "sample-task", run_id: null, source_id: "sample-customer", object_id: "sample-object",
  config_version: 1, state: "streaming", ingestion: progress, processing: progress, result: progress, archive: progress,
};
const audit: Audit = {
  audit_id: "sample-audit", actor_id: "管理员", action: "dataset.read", object_id: "sample-customer",
  result: "success", trace_id: "sample-trace", created_at: "2026-10-16T02:00:00Z",
};
const assets: Asset[] = [
  { dataset_id: "sample-customer", name: "客户基础信息", description: "汇聚客户基础资料，为客户服务与业务协同提供数据支持。", provider_department_id: "客户服务部", field_count: 12, updated_at: "2026-10-14T00:00:00+08:00", usage_restrictions: "仅用于授权业务", contract_version: 1, resource_type: "governed" },
  { dataset_id: "sample-orders", name: "订单主题数据", description: "整理订单、支付与交付信息，支持跨部门业务协同。", provider_department_id: "交易运营部", field_count: 24, updated_at: "2026-10-13T00:00:00+08:00", usage_restrictions: "仅用于授权业务", contract_version: 1, resource_type: "governed" },
  { dataset_id: "sample-finance", name: "财务核算数据", description: "统一账目与核算口径，为经营分析提供数据依据。", provider_department_id: "财务管理部", field_count: 18, updated_at: "2026-10-12T00:00:00+08:00", usage_restrictions: "仅用于授权业务", contract_version: 1, resource_type: "governed" },
];
const sources: Source[] = [
  { source_id: "sample-customer", name: "客户服务业务库", kind: "postgres", owner_id: "sample", department_id: "客户服务部", source_version:1,capture_template:false,configured_version: "17.11", credential_configured: true, connection_status: "passed", cdc_status: "passed" },
  { source_id: "sample-orders", name: "订单中心业务库", kind: "mysql", owner_id: "sample", department_id: "交易运营部", source_version:1,capture_template:false,configured_version: "8.4.7", credential_configured: true, connection_status: "passed", cdc_status: "untested" },
  { source_id: "sample-finance", name: "财务核算业务库", kind: "oracle", owner_id: "sample", department_id: "财务管理部", source_version:1,capture_template:false,configured_version: "23.26.3", credential_configured: true, connection_status: "failed", cdc_status: "untested" },
];
const directoryFixture = {resources:new URLSearchParams(location.search).get("forms")==="multiple"?(["governed_dataset","file_resource","api_resource"] as const).map((type,index)=>({resource:{resource_type:type,resource_id:`preview-resource-${index}`,contract_version:1},name:["客户基础数据治理成果","客户信息文件","客户信息查询接口"][index],fields:dataset.definition.fields,available:true,sharing_snapshot:null})):undefined,directory_id:"preview-directory",version:1,state:"published",department_id:"数据提供部门",revision_id:"preview-revision",revision_state:"approved",published_revision_id:"preview-revision",reason:"",available:false,sharing_snapshot:null,resource:null,fields:[],permissions:{manage_resource:true,operate_resource:true},definition:{name:"客户基础数据（样式预览）",description:"治理成果目录样式样例，仅用于检查页面布局。",classification:"department",classification_path:"department/local-test",resource_code:"TEST-PREVIEW-001",dictionary_id:"preview-dictionary",provider_name:"数据提供部门",provider_code:"LOCAL-TEST",responsible_user_id:"sample",update_frequency:"每日",format:"database",data_layer:new URLSearchParams(location.search).get("layer")==="raw"?"raw_object":"governed_dataset",items:dataset.definition.fields.map(f=>({...f,business_name:{id:"客户编号",name:"客户姓名",amount:"金额"}[f.name],length:null,description:""})),sharing_type:"conditional",sharing_conditions:"按批准用途和字段使用",non_sharing_reason:"",legal_basis:"",sharing_method:"query",usage_restrictions:"仅供本地样式检查",security_classification:"一般数据（样例）",open_to_public:false,open_conditions:"",department_approval_ref:"PREVIEW-ONLY",assessment_ref:"PREVIEW-ONLY",related_resource_codes:[]}};
const etlDefinition={name:"客户交换（样式预览）",source:{source_id:"sample-postgres",schema:"public",table:"customers"},target:{source_id:"sample-postgres",schema:"public",table:"customers_copy"},mapping:[{source:"id",target:"id",rule:"copy"},{source:"name",target:"name",rule:"trim"}]};
const etlTime="2026-10-20T08:00:00Z";
const completedPreview=new URLSearchParams(location.search).get("status")==="succeeded";
const etlPreviewRun={id:"preview-etl-run",state:completedPreview?"succeeded":"unknown",created_at:etlTime,updated_at:etlTime,data:{definition:etlDefinition,read:200,written:completedPreview?200:0,planned:200,timings:{prepare_ms:120,read_transform_ms:220,write_ms:450,index_ms:80,before_check_ms:300,commit_ms:20,total_ms:1190},task_version:2,attempt:1,message:completedPreview?"目标事务已提交":"提交结果待核实，请先核对目标事务",error_code:completedPreview?undefined:"RUN_UNKNOWN",trace_id:"preview-trace",batches:[{first:1,last:100,state:"pending_commit"},{first:101,last:200,state:"pending_commit"}]}};
const profilePreviewRun:GovernanceRun={id:"preview-profile",kind:"profile",state:"succeeded",definition:{mode:"sample",input:{resource:{resource_type:"raw_object",resource_id:"sample-object",contract_version:1}}},processed:1000,error_code:"",cancel_requested:false,created_at:"2026-10-21T01:00:00Z",started_at:"2026-10-21T01:01:00Z",finished_at:"2026-10-21T01:01:04Z",retry_of:null,result:{mode:"sample",coverage:"sample",sampling:"first_rows_not_random",rows:1000,total_rows:50000,snapshot_kind:"database_statement",observed_at:"2026-10-21T01:01:04Z",fields:[
{name:"id",logical_type:"integer",null_count:0,distinct_count:1000,duplicate_count:0,min:"1",max:"1000",redacted:false,values_truncated:false,frequencies:[]},
{name:"name",logical_type:"string",null_count:3,distinct_count:null,duplicate_count:null,min:null,max:null,redacted:true,values_truncated:false,frequencies:[]},
{name:"amount",logical_type:"decimal",null_count:12,distinct_count:700,duplicate_count:288,min:"0.00",max:"12500.50",redacted:false,values_truncated:false,frequencies:[{value:"100.00",count:21,truncated:false},{value:"250.00",count:15,truncated:false}]}
],anomalies:[{ordinal:27,field:"amount",code:"duplicate_value"}]}};
const metadataPolicyPreview={object_id:"sample-object",department_id:"平台管理部",actor_id:"sample",enabled:true,requested:false,interval_minutes:1440,version:1,next_at:"2026-10-23T08:00:00Z",last_attempt_at:"2026-10-22T08:00:00Z",last_success_at:"2026-10-22T08:00:00Z",failures:0,error_code:"",source_version:1,object_version:1,observation_id:"preview-observation"};
window.fetch = async (input, init) => {
  const url = new URL(String(input), location.origin);
  // Diagnostic preview returns a fixed report; it does not write data or contact a source.
  if (init?.method === "POST" && /^\/api\/v1\/sources\/sample-[a-z]+\/preflight-cdc$/.test(url.pathname)) return Response.json({source_id:url.pathname.split("/")[4],kind:"cdc",passed:false,checked_at:"2026-10-21T02:00:00Z",checks:[
    {check:"authentication",label:"数据库连接",status:"passed",passed:true,actual:"连接成功",expected:"连接成功",owner:"数据源维护人员 / 数据库管理员",reason:"使用已登记账号完成数据库连接",suggestion:null},
    {check:"wal_level",label:"逻辑复制日志",status:"failed",passed:false,actual:"replica",expected:"logical",owner:"数据库管理员",reason:"当前值未满足预期，请按处理步骤完成后重新检查",suggestion:"在实例参数中开启逻辑复制后重新检查",steps:["在数据库配置文件或云数据库参数组中将 wal_level 设置为 logical","确认是否需要重启；需要时由 DBA 安排维护窗口并重启实例","重新执行 SHOW wal_level，确认返回 logical"],verify_sql:"SHOW wal_level;"},
    {check:"platform_cdc",label:"平台采集作业",status:"unsupported",passed:false,actual:"通用远程 CDC 作业尚未接入",expected:"已绑定可用采集作业",owner:"平台管理员",reason:"此远程源暂不能创建 CDC 作业；该限制不是数据库配置错误",suggestion:"联系平台管理员处理采集作业接入",steps:["确认平台已实现此数据源的 CDC 作业创建及生命周期管理","平台条件满足后重新检查；仅调整数据库参数不能解除平台能力限制"]},
  ]});
  if(init?.method==="POST"&&url.pathname==="/api/v1/data-services/validate")return Response.json({records:[{id:1,name:"张清越",amount:"120.00"}],fields:dataset.definition.fields,parameters:[],dataset_id:dataset.dataset_id});
  if(url.pathname==="/api/v1/data-services/sources")return Response.json({items:[{id:dataset.dataset_id,name:"业务明细成果",fields:dataset.definition.fields,created_at:"2026-11-04T00:00:00Z"}],has_more:false});
  if(url.pathname==="/api/v1/data-services")return Response.json({items:[],total:0});
  if (init?.method && init.method !== "GET") return Response.json({ code: "PREVIEW_READ_ONLY", message: "设计样板仅供查看，不执行业务操作。" }, { status: 403 });
  if (url.pathname === "/api/public/assets") {
    const items = assets.filter(a => a.name.includes(url.searchParams.get("search") ?? "") && (!url.searchParams.get("department_id") || a.provider_department_id === url.searchParams.get("department_id")));
    return Response.json({ items, total: items.length, page: 1, page_size: 12, departments: assets.map(a => a.provider_department_id) });
  }
  if(url.pathname==="/api/v1/governance/dataflows")return Response.json({items:[governanceFlow],total:1,page:1,page_size:20});
  if(url.pathname==="/api/v1/governance/workflows")return Response.json({items:[governanceWorkflow],total:1,page:1,page_size:20});
  if(url.pathname==="/api/v1/governance/dataflows/preview-flow/versions")return Response.json({items:[governanceFlow],total:1,page:1,page_size:20});
  if(url.pathname==="/api/v1/governance/workflows/preview-workflow/versions")return Response.json({items:[governanceWorkflow],total:1,page:1,page_size:20});
  if(url.pathname==="/api/v1/governance/schedules"||url.pathname==="/api/v1/governance/quality-issues"||url.pathname==="/api/v1/governance/lineage")return Response.json({items:[],total:0,page:1,page_size:20});
  if(["/api/v1/governance/alerts","/api/v1/governance/alert-rules","/api/v1/governance/lifecycle","/api/v1/governance/retention-policies","/api/v1/governance/migrations","/api/v1/governance/model-deployments","/api/v1/governance/quality-templates","/api/v1/governance/publication-approvals"].includes(url.pathname))return Response.json({items:[],total:0,page:1,page_size:20,limit:100});
  if(url.pathname==="/api/v1/governance/quality-trends")return Response.json({items:[],timezone:"Asia/Shanghai",unit:"quality_node_row_checks"});
  if(url.pathname.endsWith("/parameters"))return Response.json({defaults:{}});
  if(url.pathname==="/api/v1/governance/dashboard")return Response.json({days:Number(url.searchParams.get("days")??30),timezone:url.searchParams.get("timezone")??"Asia/Shanghai",scope:"all",started_at:new Date(Date.UTC(2026,9,23-Number(url.searchParams.get("days")??30))-8*3600000).toISOString(),observed_at:"2026-10-22T03:00:00Z",definitions:[{kind:"domain",state:"published",count:2},{kind:"standard",state:"published",count:8},{kind:"term",state:"published",count:6},{kind:"model",state:"published",count:4},{kind:"dataflow",state:"published",count:3},{kind:"workflow",state:"published",count:2},{kind:"quality_template",state:"published",count:2}],resources:[{kind:"table_resource",count:8},{kind:"file_resource",count:2},{kind:"api_resource",count:1}],observations:12,rules:[{state:"published",count:4}],outputs:[{state:"published",quality:"passed",completeness:"complete",count:6},{state:"draft",quality:"not_checked",completeness:"pending",count:2}],runs:[{kind:"dataflow",state:"succeeded",count:18},{kind:"workflow",state:"succeeded",count:5},{kind:"dataflow",state:"failed",count:2},{kind:"profile",state:"succeeded",count:3}],run_trend:[{day:"2026-10-18",succeeded:6,failed:1,other:0},{day:"2026-10-19",succeeded:4,failed:0,other:0},{day:"2026-10-20",succeeded:7,failed:0,other:0},{day:"2026-10-21",succeeded:5,failed:1,other:0},{day:"2026-10-22",succeeded:4,failed:0,other:0}],quality_trend:[{day:"2026-10-18",checked:1000,passed:980,rejected:20,runs:6},{day:"2026-10-19",checked:1500,passed:1500,rejected:0,runs:4},{day:"2026-10-20",checked:1200,passed:1200,rejected:0,runs:7},{day:"2026-10-21",checked:2000,passed:1990,rejected:10,runs:6},{day:"2026-10-22",checked:1000,passed:1000,rejected:0,runs:4}],schedules:[{state:"active",count:2},{state:"paused",count:1}],issues:[{state:"open",count:2},{state:"closed",count:3}],overdue_issues:1,alerts:[{state:"in_app",count:2},{state:"failed",count:1}],alert_rules:2,retention_policies:1,purged_snapshots:2,lifecycle_outputs:8,migrations:[{state:"open",count:1}],model_deployments:[{state:"succeeded",count:2}],lineage_runs:18});
  if(url.pathname==="/api/v1/governance/operations")return Response.json({run_states:[{state:"succeeded",count:1}],definitions:[{kind:"model",count:1},{kind:"dataflow",count:1}],open_issues:0,overdue_issues:0,recent_runs:[],observed_at:"2026-10-21T11:00:00Z"});
  if(url.pathname==="/api/v1/governance/runs"&&url.searchParams.get("kind")&&url.searchParams.get("kind")!=="profile")return Response.json({items:[],total:0,page:1,page_size:20});
  if(url.pathname==="/api/v1/governance/metadata")return Response.json({items:[{...sourceObject,resource_type:"raw_object",observation_kind:"live_schema",adapter:"database",collection:metadataPolicyPreview,pending_changes:1}],total:1,page:1,page_size:20});
  if(url.pathname==="/api/v1/governance/runs")return Response.json({items:[{...profilePreviewRun,result:null}],total:1,page:1,page_size:20});
  if(url.pathname==="/api/v1/governance/runs/preview-profile")return Response.json(profilePreviewRun);
  if(url.pathname==="/api/v1/governance/runs/preview-profile/events")return Response.json({items:[{id:1,run_id:"preview-profile",node_id:"",code:"PROFILE_COMPLETED",processed:1000,created_at:profilePreviewRun.finished_at}],total:1,page:1,page_size:50});
  if(url.pathname==="/api/v1/governance/metadata/sample-object/collection")return Response.json({policy:metadataPolicyPreview,items:[{id:"preview-metadata-run",object_id:"sample-object",actor_id:"sample",trigger:"scheduled",state:"succeeded",observation_id:"preview-observation",changed:true,error_code:"",created_at:"2026-10-22T08:00:00Z"}],total:1,page:1,page_size:20,pending_total:1,pending_reviews:[{observation_id:"preview-observation",object_id:"sample-object",state:"pending",note:"",reviewed_by:null,reviewed_at:null,created_at:"2026-10-22T08:00:00Z"}],observed_at:"2026-10-22T08:00:00Z"});
  if(url.pathname==="/api/v1/governance/metadata/sample-object/observations/preview-observation")return Response.json({...metadataObservation,changes:[{code:"field_added",field:"customer_level",breaking:false}]});
  if(url.pathname==="/api/v1/governance/impact")return Response.json({root:"resource:sample-object:1",nodes:[{kind:"resource",id:"sample-object",version:1,state:"available"}],edges:[],fields:null,transformations:null,truncated:false,restricted_dependencies:0});
  if (url.pathname === "/api/v1/governance/metadata/sample-object/observations") return Response.json({items:[metadataObservation],total:1,page:1,page_size:20,object:sourceObject,department_id:"平台管理部",source_version:1,object_version:1});
  if (url.pathname === "/api/v1/governance/metadata-definitions") return Response.json({items:[],total:0,page:1,page_size:20});
  if (url.pathname === "/api/v1/governance/members") return Response.json({items:[{user_id:"sample",username:"管理员",department_id:"平台管理部"}]});
  if (url.pathname === "/api/v1/governance/models") return Response.json({items:[governanceModel],total:1,page:1,page_size:20});
  if (url.pathname === "/api/v1/governance/models/preview-model/versions/1") return Response.json(governanceModel);
  if (url.pathname === "/api/v1/governance/models/preview-model/versions") return Response.json({items:[governanceModel],total:1,page:1,page_size:20});
  if (url.pathname === "/api/v1/governance/domains") {const rows=url.searchParams.get("enterprise_domain_id")?[governanceResponsibility]:url.searchParams.get("domain_scope")==="enterprise"?[governanceEnterpriseDomain]:[governanceEnterpriseDomain,governanceResponsibility];return Response.json({items:rows,total:rows.length,page:1,page_size:20});}
  if (url.pathname === "/api/v1/governance/standards")return Response.json({items:[governanceStandard],total:1,page:1,page_size:20});
  for(const record of [governanceEnterpriseDomain,governanceResponsibility,governanceStandard]){const path=`/api/v1/governance/${record.kind==="domain"?"domains":"standards"}/${record.id}/versions`;if(url.pathname===path+"/1")return Response.json(record);if(url.pathname===path)return Response.json({items:[record],total:1,page:1,page_size:20});}
  if (url.pathname === "/api/v1/governance/terms") return Response.json({items:[],total:0,page:1,page_size:20});
  if (url.pathname === "/api/v1/me" && ["home", "login"].includes(previewView)) return Response.json({ code: "UNAUTHENTICATED" }, { status: 401 });
  if (url.pathname === "/api/v1/me") return Response.json({ user_id: "sample", username: "管理员", department_id: "平台管理部", platforms: ["exchange", "governance", "assets", "security"], roles: ["admin"], csrf_token: "fixture-only" });
  if(url.pathname==="/api/v1/resources/raw")return Response.json({items:[sourceObject],total:1,page:1,page_size:20});
  if(url.pathname==="/api/v1/managed-resources")return Response.json({items:[]});
  if(url.pathname==="/api/v1/resource-access")return Response.json({manage_resource:true,operate_resource:true,authority_review:true});
  if(url.pathname==="/api/v1/resource-directories")return Response.json({items:[directoryFixture],total:1,page:1,page_size:20});
  if(url.pathname==="/api/v1/resource-directories/preview-directory")return Response.json(directoryFixture);
  if(url.pathname==="/api/v1/directory-reviews")return Response.json({items:[{review_id:"preview-review",version:1,state:"pending",department_id:"数据提供部门",definition:{target_kind:"directory",target_id:"preview-directory",revision_id:"preview-revision",stage:url.searchParams.get("stage"),submitted_by:"编目人员",reviewer_id:"",reason:""},snapshot:{definition:directoryFixture.definition},previous:null,created_at:"2026-10-16T05:00:00Z",can_review:true}],total:1,page:1,page_size:20});
  if(url.pathname==="/api/v1/organization-units")return Response.json({items:[{id:"preview-org",parent_id:null,kind:"organization",name:"平台管理组织（示例）",code:"PREVIEW-ORG",active:true,version:1},{id:"平台管理部",parent_id:"preview-org",kind:"department",name:"平台管理部（示例）",code:"PREVIEW-DEPT",active:true,version:1}]});
  if(url.pathname==="/api/v1/directory-provider")return Response.json({department_id:"平台管理部",department_name:"平台管理部（示例）",organization_id:"preview-org",provider_name:"平台管理组织（示例）",provider_code:"PREVIEW-ORG",active:true});
  if(url.pathname==="/api/v1/directory-dictionaries")return Response.json({items:[{id:"preview-dict",version:1,definition:{name:"内部演示字典",version:"1",source:"界面示例，不代表正式国家编码规范",verified:true,test_only:true,code_pattern:"^PREVIEW-[0-9]{6}$",code_template:"PREVIEW-{sequence}",sequence_width:6,classifications:["业务/客户","业务/订单"],verification_ref:"preview-only"}}]});
  if(url.pathname==="/api/v1/accounts")return Response.json({items:[{user_id:"preview-person",username:"preview.reader",display_name:"演示人员",department_id:"平台管理部",roles:["consumer"],platforms:["exchange","assets"],active:true,version:1}],total:1,page:1,page_size:20});
  if(["/api/v1/resource-mounts","/api/v1/processing-authorizations","/api/v1/resource-assignments","/api/v1/directory-dictionaries"].includes(url.pathname))return Response.json({items:[],total:0,page:1,page_size:20});
  if(url.pathname==="/api/v1/directory-responsibles")return Response.json({items:[{user_id:"sample",username:"样例责任人"}]});
  if (url.pathname === "/api/v1/datasets") return Response.json({ items: [dataset], total: 1, page: 1, page_size: 20 });
  if(url.pathname==="/api/v1/source-types")return Response.json({items:sourceDefinitions.map(t=>({...t,reader:!["postgres","mysql","oracle"].includes(t.kind),cdc:["postgres","mysql","oracle"].includes(t.kind),write:["postgres","mysql","oracle"].includes(t.kind),status:t.kind==="kingbase"?"driver_missing":"ready",max_rows:10000,http:["clickhouse","elasticsearch","hbase"].includes(t.kind)}))});
  if(url.pathname==="/api/v1/source-allowed-targets")return Response.json({items:[{target_id:"preview-platform",kind:"postgres",host:"db.example.internal",port:5432,database:"business",origin:"platform",created_at:"2026-10-21T00:00:00Z"},{target_id:"preview-environment",kind:"mysql",host:"mysql.example.internal",port:3306,database:"business",origin:"environment",created_at:null},{target_id:"preview-builtin",kind:"oracle",host:"oracle-source",port:1521,database:"FREE",origin:"builtin",created_at:null}],total:3});
  if(url.pathname==="/api/v1/etl-capabilities")return Response.json({direct:{max_rows:2000000,max_bytes:1073741824,timeout_seconds:1800,batch_rows:500},platform_resource:{max_rows:10000,max_bytes:20971520},concurrency:2});
  if (url.pathname === "/api/v1/etl-tasks") return Response.json({items:[{id:"preview-etl-task",state:"ready",version:2,created_at:etlTime,updated_at:etlTime,data:etlDefinition}],total:1,page:1,page_size:20});
  if(url.pathname==="/api/v1/etl-tasks/preview-etl-task/runs")return Response.json({items:[etlPreviewRun],total:1,page:1,page_size:20});
  if(url.pathname==="/api/v1/etl-runs/preview-etl-run/reconciliation"){const latest=completedPreview?{id:"preview-report",run_id:etlPreviewRun.id,version:1,state:"passed",input_snapshot_id:"preview-fixed-snapshot",input_scope:null,task_version:2,read_rows:200,processed_rows:200,key_count:200,checked_rows:200,mismatch_count:0,duration_ms:350,checked_at:etlTime,created_at:etlTime,updated_at:etlTime}:null;return Response.json({run_id:etlPreviewRun.id,latest,history:latest?[latest]:[],total:latest?1:0,page:1,page_size:20,can_retry:completedPreview,recipient_id:"sample",execution_state:etlPreviewRun.state})}
  if(url.pathname==="/api/v1/etl-runs/preview-etl-run/receipts")return Response.json({run_id:etlPreviewRun.id,version:1,state:completedPreview?"pending":"not_applicable",items:[],total:0,page:1,page_size:20,recipient_id:"sample",can_accept:completedPreview,can_reject:completedPreview,delegate:false});
  if(url.pathname==="/api/v1/etl-runs/preview-etl-run/events")return Response.json({items:[{id:3,run_id:etlPreviewRun.id,task_id:"preview-etl-task",level:"error",stage:"unknown",code:"RUN_UNKNOWN",message:"提交结果待核实，请核对目标事务",actor_id:"sample",trace_id:"preview-trace",created_at:etlTime},{id:2,run_id:etlPreviewRun.id,task_id:"preview-etl-task",level:"info",stage:"loading",code:"RUN_LOADING",message:"已写入200行，事务尚未提交",actor_id:"sample",trace_id:"preview-trace",created_at:etlTime}],total:2,page:1,page_size:50});
  if(url.pathname==="/api/v1/etl-alerts")return Response.json({items:[{id:"preview-alert",run_id:etlPreviewRun.id,severity:"error",state:"open",code:"RUN_UNKNOWN",message:"目标事务结果待核实",created_at:etlTime,reason:""}],total:1,page:1,page_size:20});
  if (url.pathname === "/api/v1/tasks") return Response.json({ items: [task], total: 1, page: 1, page_size: 20 });
  if (url.pathname === "/api/v1/metadata") return Response.json({ items: [sourceObject], total: 1, page: 1, page_size: 100 });
  if (url.pathname === "/api/v1/audit") return Response.json({ items: [audit], total: 1, page: 1, page_size: 20 });
  if (["/api/v1/rules", "/api/v1/accounts"].includes(url.pathname)) return Response.json({ items: [], total: 0, page: 1, page_size: 20 });
  if (url.pathname === "/api/v1/catalog/objects") return Response.json({ items: [], total: 0, page: 1, page_size: 100 });
  if (url.pathname === "/api/v1/sources") {
    const items = sources.filter(s => s.name.includes(url.searchParams.get("search") ?? ""));
    return Response.json({ items, total: items.length, page: 1, page_size: 20 });
  }
  if (/^\/api\/v1\/sources\/sample-[a-z]+\/tables$/.test(url.pathname)) return Response.json({items:[{schema:"public",name:"customers",kind:"BASE TABLE"},{schema:"public",name:"orders",kind:"BASE TABLE"}]});
  if (/^\/api\/v1\/sources\/sample-[a-z]+\/objects$/.test(url.pathname)) return Response.json({ items: [{ object_id: "sample-object", source_id:url.pathname.split("/")[4], qualified_name: "public.customers", schema_version: 1, fields: [ { name: "id", logical_type: "int64", primary_key: true, sensitive: false }, { name: "name", logical_type: "string", primary_key: false, sensitive: true }, { name: "amount", logical_type: "decimal(20,6)", primary_key: false, sensitive: false } ] }], total: 1 });
  return Response.json({ code: "PREVIEW_READ_ONLY", message: "此入口仅展示数据源管理样板。" }, { status: 404 });
};
const previewRoutes: Record<string, string> = {
  home: "/", login: "/login", catalog: "/platform/assets/catalog", sources: "/platform/exchange/sources",
  etl: "/platform/exchange/etl", tasks: "/platform/exchange/tasks", metadata: "/platform/governance/metadata", audit: "/platform/security/audit",
  directory:"/platform/exchange/directory",workspace:"/platform/exchange/workspace",reviews:"/platform/exchange/reviews",
  models:"/platform/governance/models", standards:"/platform/governance/standards", domains:"/platform/governance/domains",
  dataflows:"/platform/governance/dataflows",workflows:"/platform/governance/workflows",overview:"/platform/governance/overview",issues:"/platform/governance/issues",lineage:"/platform/governance/lineage",operations:"/platform/governance/operations",alerts:"/platform/governance/alerts",lifecycle:"/platform/governance/lifecycle",
  services:"/platform/exchange/data-services",organizations:"/platform/security/organizations",coding:"/platform/exchange/directory-coding",rules: "/platform/governance/rules", accounts: "/platform/security/accounts",
};
history.replaceState(null, "", previewRoutes[previewView] ?? previewRoutes.sources);
const root = document.getElementById("root");
if (!root) throw new Error("预览根节点缺失");
createRoot(root).render(<App />);

const notice = document.createElement("div");
notice.textContent = "设计预览 · 示例数据 · 仅供查看";
notice.style.cssText = "position:fixed;bottom:4px;left:50%;transform:translateX(-50%);font-size:11px;color:#60749a;z-index:2000;pointer-events:none";
document.body.append(notice);
