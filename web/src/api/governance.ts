import type {SourceObject,Resource,Field} from "./types";
export type GovernanceKind = "domain" | "term" | "standard" | "model" | "metadata" | "dataflow" | "workflow" | "quality_template" | "master" | "scenario";
export interface GovernanceRef { id: string; version: number }
export interface GovernanceStandard {
 code: string; logical_type: string; length: number; precision: number; scale: number;
 nullable: boolean; sensitive: boolean; pattern: string; values: string[]; value_meanings?: Record<string,string>;
}
export interface GovernanceField {
 business_name?:string;
 name: string; description: string; logical_type: string; length: number; precision: number; scale: number;
 nullable: boolean; primary_key: boolean; sensitive: boolean; standard?: GovernanceRef;
}
export interface GovernanceRelation {
 name: string; target: GovernanceRef; fields: string[]; target_fields: string[];
 cardinality: "one_to_one" | "many_to_one" | "one_to_many";
}
export interface GovernanceModel {
 data_layer?:GovernanceDataLayer;
 layer: "conceptual" | "logical" | "physical"; schema: string; table: string;
 fields: GovernanceField[]; relations: GovernanceRelation[];
 contract: { freshness_seconds: number; max_failure_rate: number; usage_restrictions: string };
}
export interface GovernanceFieldAnnotation { name: string; description: string; standard?: GovernanceRef; term?: GovernanceRef }
export interface GovernanceMetadata { object_id: string; observation_id: string; fields: GovernanceFieldAnnotation[] }
export interface MetadataColumn { name:string; native_type:string; length:number|null; precision:number|null; scale:number|null; collation:string;comment?:string;default?:string|null }
export interface MetadataResource extends SourceObject { data_layer?:GovernanceDataLayer; field_labels?:Record<string,string>;display_name?:string;description?:string;provider_name?:string; collection?:MetadataCollectionPolicy;pending_changes?:number; input_parameters?:{name:string;type:string;required:boolean}[];structured_input?:{model:GovernanceRef;sheet:string;header_row:number}; resource_type?: "raw_object"|"governed_dataset"|"table_resource"|"file_resource"|"api_resource"|"master_snapshot"; observation_kind?: "live_schema"|"registered_contract"; adapter?:string }
export function governanceResourceType(resource:Pick<MetadataResource,"resource_type"|"adapter">):string {
 return {master_snapshot:"主数据确认快照（私有）",raw_object:"采集对象",governed_dataset:"治理成果",table_resource:resource.adapter==="excel"?"Excel导入表":"数据库表",file_resource:"文件",api_resource:"API"}[resource.resource_type??"raw_object"];
}
export interface MetadataCollectionPolicy {object_id:string;department_id:string;actor_id:string;enabled:boolean;requested:boolean;interval_minutes:number;version:number;next_at:string;last_attempt_at:string|null;last_success_at:string|null;failures:number;error_code:string;source_version:number;object_version:number;observation_id:string}
export interface MetadataCollectionRun {id:string;object_id:string;actor_id:string;trigger:"initial"|"scheduled"|"resource_changed"|"manual";state:"succeeded"|"failed"|"blocked";observation_id:string;changed:boolean;error_code:string;created_at:string}
export interface MetadataChangeReview {observation_id:string;object_id:string;state:"pending"|"acknowledged"|"deferred";note:string;reviewed_by:string|null;reviewed_at:string|null;created_at:string}
export interface MetadataCollectionPage {items:MetadataCollectionRun[];total:number;page:number;page_size:number;policy:MetadataCollectionPolicy|null;pending_reviews:MetadataChangeReview[];pending_total?:number;observed_at:string}
export function metadataCollectionState(policy:MetadataCollectionPolicy|undefined|null):{label:string;color?:string}{
 if(!policy)return {label:"等待安排自动检查"};if(policy.requested)return {label:"等待检查",color:"processing"};if(policy.failures)return {label:policy.error_code==="METADATA_AUTHORIZATION_LOST"?"检查权限待确认":"检查未成功",color:"error"};if(!policy.enabled)return {label:"已暂停"};if(!policy.last_success_at)return {label:"等待首次检查",color:"processing"};if(Date.now()-Date.parse(policy.last_success_at)>policy.interval_minutes*60000*1.5)return {label:"结构记录需要更新",color:"warning"};return {label:"结构更新正常",color:"success"};
}
export interface MetadataObservation {
 resource_type?: MetadataResource["resource_type"]; observation_kind?: "live_schema"|"registered_contract";
 id: string; object_id: string; version: number; source_id: string; source_version: number; object_version: number;
 schema: string; table: string; fields: import("./types").Field[]; columns?: MetadataColumn[]; changes: {code:string;field?:string;breaking:boolean}[];
 matches_active: boolean; observed_by: string; created_at: string;
}
export interface GovernanceQualityTemplate {fields:import("./types").Field[];reference_fields:import("./types").Field[];checks:GovernanceFlowCheck[]}
export interface GovernanceMetric {source:GovernanceFlowSource;dimensions:string[];filters:GovernanceFlowCondition[];aggregates:GovernanceFlowAggregate[];period:"all_time"|"range";time_type?:"date"|"datetime"|"";time_field:string;from:string;until:string;model?:GovernanceRef}
export interface GovernanceMasterSource {id:string;resource:Resource;record_id:string;mapping:Record<string,string>}
export interface GovernanceMaster {model_ref?:GovernanceRef;keys:string[];model:GovernanceModel;sources:GovernanceMasterSource[]}
export interface GovernanceScenario {kind:"dataflow"|"workflow";reference:GovernanceRef;acceptance:string;inputs:{task_id:string;node_id:string;fields:Field[]}[]}
export interface GovernanceDefinition {
 metric?:GovernanceMetric;master?:GovernanceMaster;scenario?:GovernanceScenario;
 domain_scope?: "department"|"enterprise"; enterprise_domain?: GovernanceRef;
 quality_template?:GovernanceQualityTemplate;
 name: string; description: string; owner_id: string; steward_id: string;
 domain?: GovernanceRef; standard?: GovernanceStandard; model?: GovernanceModel; metadata?: GovernanceMetadata; dataflow?: GovernanceDataflow; workflow?: GovernanceWorkflow;
}
export interface GovernanceRecord {
 execution_mode?:"batch"|"continuous";
 workspace_id?:string|null;
 id: string; kind: GovernanceKind; department_id: string; latest_version: number; version: number;
 state: "draft" | "published" | "retired"; definition: GovernanceDefinition;
 change_note: string; migration_note: string; created_at: string; published_at: string | null;
}
export interface GovernanceMember { user_id: string; username: string; department_id: string }
export const governancePaths: Record<GovernanceKind, string> = {master:"masters",scenario:"scenarios",domain: "domains", term: "terms", standard: "standards", model: "models", metadata:"metadata-definitions",dataflow:"dataflows",workflow:"workflows",quality_template:"quality-templates"};
export const governanceNames: Record<GovernanceKind, string> = {master:"主数据",scenario:"场景方案",domain: "主题域", term: "业务术语", standard: "数据标准", model: "数据模型",metadata:"业务说明",dataflow:"数据流",workflow:"工作流",quality_template:"检查方案"};
export const governanceTypes = [
 {value:"string", label:"文本"}, {value:"integer", label:"整数"}, {value:"decimal", label:"精确小数"},
 {value:"boolean", label:"布尔"}, {value:"date", label:"日期"}, {value:"datetime", label:"日期时间"},
];
export const governanceLayers = [{value:"conceptual",label:"概念模型"},{value:"logical",label:"逻辑模型"},{value:"physical",label:"物理模型"}];
export const governanceChangeNames: Record<string,string> = {
 master_changed:"主数据实体或来源映射变化",metric_changed:"指标统计口径变化",scenario_changed:"场景方案或输入契约变化",
 field_source_comment_changed:"来源字段注释变化",field_default_changed:"默认值变化",quality_template_changed:"质量模板字段或规则变化",workflow_changed:"工作流任务或依赖变化",dataflow_changed:"数据流节点或连线变化",field_database_type_changed:"数据库类型、长度、精度或排序规则变化",metadata_changed:"业务注释变化",field_removed:"删除字段",field_added:"新增字段",field_type_changed:"字段类型或精度变化",field_length_changed:"字段长度变化",
 field_nullability_changed:"可空约束变化",primary_key_changed:"主键变化",field_sensitivity_changed:"敏感标记变化",
 field_standard_changed:"字段标准变化",model_target_changed:"模型层级或物理目标变化",relations_changed:"实体关系变化",contract_changed:"输出契约变化",standard_changed:"数据标准变化",code_meanings_changed:"码值业务含义变化",domain_changed:"主题域变化",enterprise_domain_changed:"企业主题域依据变化",
};
export interface ProfileFrequency {value:string;count:number;truncated:boolean}
export interface ProfileField {name:string;logical_type:string;null_count:number;distinct_count:number|null;duplicate_count:number|null;min:string|null;max:string|null;redacted:boolean;values_truncated:boolean;frequencies:ProfileFrequency[]}
export interface GovernanceStandardAudit {rows:number;rejected_rows:number;fields:{status:string;field:string;name:string;standard:GovernanceRef;violations:number;ordinals:number[]}[]}
export interface ProfileReport {standard_audit?:GovernanceStandardAudit;business_time?:string;business_time_status?:string;mode:"sample"|"full";coverage:"sample"|"full";sampling:string;rows:number;total_rows:number;fields:ProfileField[];anomalies:{ordinal:number;field:string;code:string}[];snapshot_kind:string;observed_at:string}
export interface GovernanceRunBase {department_id?:string;workspace_id:string;id:string;state:"queued"|"running"|"succeeded"|"failed"|"cancelled"|"interrupted";processed:number;error_code:string;created_at:string;started_at:string|null;finished_at:string|null;retry_of:string|null;cancel_requested:boolean}
export interface GovernanceProfileRun extends GovernanceRunBase {kind:"profile";definition:{mode:"sample"|"full";input?:{resource:import("./types").Resource}};result:ProfileReport|null}
export interface GovernanceFlowRun extends GovernanceRunBase {kind:"dataflow";definition:GovernanceFlowExecution;result:GovernanceFlowReport|null}
export interface GovernanceWorkflowTask {kind?:"dataflow"|"quality_check"|"publish_approval";input_task?:string;inputs?:Record<string,string>;max_failure_rate?:number;id:string;dataflow:GovernanceRef;depends_on:string[];condition:"all_success"|"all_failure"|"all_done"}
export interface GovernanceWorkflow {parameters?:Record<string,string|number|boolean>;tasks:GovernanceWorkflowTask[]}
export interface GovernanceWorkflowRun extends GovernanceRunBase {kind:"workflow";definition:{workflow:GovernanceRef;graph:GovernanceWorkflow;parameters?:Record<string,string|number|boolean>};result:Record<string,string>|null}
export interface GovernanceCodePreviewRun extends GovernanceRunBase {kind:"dataflow_preview";definition:GovernanceCodePreviewExecution;result:GovernanceCodePreviewReport|null}
export type GovernanceCodePreviewExecution=Omit<GovernanceFlowExecution,"flow"|"pinned">&{flow:{id:"";version:0};preview:{node_id:string;sample_limit:20};pinned:{kind:GovernanceKind;ref:GovernanceRef|{id:"";version:0};definition:GovernanceDefinition}[]};
export type GovernanceCodePreviewReport=Omit<GovernanceFlowReport,"flow"|"model"|"dataset_id">&{flow:{id:"";version:0};model:{id:"";version:0};dataset_id?:never};
export interface GovernanceCodeRuntime {display_name?:string;language:"python"|"javascript";runtime_version:string;libraries:string[];available:boolean}
export interface GovernanceCodeSamples {input:Record<string,unknown>[];output:Record<string,unknown>[];scope:string;sample_limit:number}
export interface GovernanceMasterRun extends GovernanceRunBase{kind:"master";definition:{master:GovernanceRef};result:{input_rows:number;entities:number;conflicting_entities:number;missing_key_rows:number;coverage:string;snapshot_kind:string}|null}
export type GovernanceRun = GovernanceMasterRun | GovernanceProfileRun | GovernanceFlowRun | GovernanceWorkflowRun | GovernanceCodePreviewRun;

export interface GovernanceRunEvent {error_line?:number;error_column?:number;error_field?:string;id:number;run_id:string;node_id:string;code:string;processed:number;created_at:string}
export const governanceRunCodes:Record<string,string>={MASTER_QUEUED:"主数据匹配已排队",MASTER_STARTED:"开始匹配实体",MASTER_COMPLETED:"实体匹配完成",PROFILE_QUEUED:"剖析已排队",PROFILE_STARTED:"开始剖析",PROFILE_COMPLETED:"剖析完成",CANCEL_REQUESTED:"已请求取消",GOVERNANCE_CANCELLED:"运行已取消",GOVERNANCE_SNAPSHOT_LOST:"读取快照已丢失，需重新观察",GOVERNANCE_TIMEOUT:"运行超时",GOVERNANCE_AUTHORIZATION_CHANGED:"来源、账号或授权已变化",PROCESSING_AUTHORIZATION_REQUIRED:"加工授权已失效",GOVERNANCE_LIMIT:"数据量超过治理上限",SOURCE_SCHEMA_CHANGED:"来源结构与契约不同",GOVERNANCE_EXECUTION_FAILED:"读取或统计失败，请核对来源状态后重新执行"};

export interface GovernanceFlowEdge {node:string;port?:""|"good"|"bad"}
export interface GovernanceFlowSource {parameter_bindings?:Record<string,string>;parameters?:Record<string,string>;resource:import("./types").Resource;fields:string[]}
export interface GovernanceFlowColumn {source:string;target:string;rule?:""|"copy"|"trim"|"upper"|"lower"}
export interface GovernanceFlowCondition {parameter?:string;field:string;operator:"eq"|"ne"|"gt"|"gte"|"lt"|"lte"|"is_null"|"not_null";value?:string|number|boolean|null}
export interface GovernanceFlowJoinColumn {side:"left"|"right";source:string;target:string}
export interface GovernanceFlowJoin {kind:"inner"|"left";left_keys:string[];right_keys:string[];columns:GovernanceFlowJoinColumn[]}
export interface GovernanceFlowAggregate {operation:"count"|"sum"|"min"|"max";source:string;target:string}
export interface GovernanceFlowCheck {field:string;kind:"format"|"required"|"type"|"length"|"range"|"enum"|"business_key"|"compare_fields"|"reference_exists";blocking:boolean;parameters?:Record<string,unknown>|null}
export interface GovernanceFlowCode {field:string;standard:GovernanceRef;values:{from:string;to:string}[];unmatched:"keep"|"null"|"reject"}
export type GovernanceFlowKind = "script"|"source"|"project"|"transform"|"filter"|"deduplicate"|"join"|"aggregate"|"quality"|"code_map"|"output";
export interface GovernanceFlowScript {language:"python"|"javascript";runtime_version:string;source:string;fields:GovernanceField[];parameters?:Record<string,string|number|boolean>}
export interface GovernanceFlowNode {data_layer?:GovernanceDataLayer;infer_model?:boolean;script?:GovernanceFlowScript;template?:GovernanceRef;id:string;kind:GovernanceFlowKind;inputs:GovernanceFlowEdge[]|null;source?:GovernanceFlowSource;columns?:GovernanceFlowColumn[];condition?:GovernanceFlowCondition;keys?:string[];join?:GovernanceFlowJoin;group_by?:string[];aggregates?:GovernanceFlowAggregate[];checks?:GovernanceFlowCheck[];model?:GovernanceRef;code?:GovernanceFlowCode}
export interface GovernanceDataflow {nodes:GovernanceFlowNode[]}
export interface GovernanceFlowExecution {lightweight?:{unit_id:string;version:number};execution_plan?:GovernanceExecutionPlan;parameters?:Record<string,string|number|boolean>;flow:GovernanceRef;pinned:{kind:GovernanceKind;ref:GovernanceRef;definition:GovernanceDefinition}[];inputs:Record<string,{resource:import("./types").Resource;fields:import("./types").Field[];upload_id?:string;source_name?:string;field_labels?:Record<string,string>}>}
export interface GovernanceFieldOrigin {resource:import("./types").Resource;field:string;node:string}
export interface GovernanceFlowNodeResult {node_id:string;input_count:number;output_count:number;rejected_count:number;duration_ms:number}
export interface GovernanceFlowReport {storage?:GovernanceStorage;data_layer?:GovernanceDataLayer;engine?:"doris_sql"|"flink";generated_model?:GovernanceRef;field_labels?:Record<string,string>;unmodeled?:boolean;conservative_lineage?:boolean;dataset_id?:string;flow:GovernanceRef;model:GovernanceRef;output_node:string;output_rows:number;rejected_rows:number;snapshot_scope:"per_source_statement"|"per_source_snapshot"|"committed_savepoint";nodes:GovernanceFlowNodeResult[];lineage:Record<string,GovernanceFieldOrigin[]>}
export const governanceFlowNames:Record<GovernanceFlowKind,string>={script:"自定义代码",source:"数据来源",project:"字段投影",transform:"文本转换",filter:"数据筛选",deduplicate:"记录去重",join:"多表关联",aggregate:"分组汇总",quality:"质量分流",code_map:"码表映射",output:"结果输出"};
Object.assign(governanceRunCodes,{FLOW_QUEUED:"数据流已排队",FLOW_STARTED:"开始数据流执行",FLOW_NODE_COMPLETED:"节点处理完成",FLOW_COMPLETED:"数据流执行完成",GOVERNANCE_JOIN_LIMIT:"关联扩张超出治理预算，请先筛选、投影或预聚合",GOVERNANCE_MASKED_KEY:"脱敏字段不能作为关联、分组或去重键",GOVERNANCE_MASKED_TYPE:"敏感数值或日期字段无法在当前脱敏范围内参与计算",GOVERNANCE_QUALITY_BLOCKED:"质量不合格比例超过模型门槛",GOVERNANCE_CODE_UNMATCHED:"存在未匹配码值，已阻止产出",GOVERNANCE_OUTPUT_DUPLICATE_KEY:"模型输出存在重复主键",GOVERNANCE_OUTPUT_NULL:"输出不符合模型非空约束",GOVERNANCE_OUTPUT_TYPE:"输出不符合模型类型、范围或精度",GOVERNANCE_STANDARD_FAILED:"输出未通过关联标准检查",GOVERNANCE_REFERENCE_UNAVAILABLE:"模型、标准或数据流引用版本已停用"});

Object.assign(governanceRunCodes,{WORKFLOW_QUEUED:"工作流已排队",WORKFLOW_STARTED:"工作流开始",WORKFLOW_TASK_FAILED:"工作流存在失败任务",WORKFLOW_succeeded:"工作流完成",WORKFLOW_failed:"工作流失败",WORKFLOW_cancelled:"工作流取消",WORKFLOW_DISPATCH_DENIED:"子任务来源、授权或引用不可用",GOVERNANCE_OUTPUT_STALE:"结果已超过约定的使用时间，需要重新生成",GOVERNANCE_OUTPUT_INVALID:"治理成果证据不一致",SCHEDULE_CONCURRENCY_SKIPPED:"上次工作流仍在运行，已跳过本次触发",SCHEDULE_MISSED_SKIPPED:"错过的触发已按策略跳过",SCHEDULE_AUTHORIZATION_DENIED:"调度执行人的权限已失效"});

Object.assign(governanceRunCodes,{QUALITY_CHECKED:"质量节点已核验",GOVERNANCE_PARAMETER_INVALID:"运行参数未声明或类型不符",WORKFLOW_ARTIFACT_SCHEMA:"前置成果与输入字段类型不兼容",WORKFLOW_EVIDENCE_UNAVAILABLE:"前置任务没有可用的成功成果",WORKFLOW_QUALITY_BLOCKED:"独立质量门禁未通过",PUBLICATION_APPROVAL_REQUIRED:"等待有发布职责的人员核验",PUBLICATION_REJECTED:"发布审批已驳回",WORKFLOW_RESUMED:"已复核并保留成功任务",GOVERNANCE_LINEAGE_DEPTH:"连续成果来源超过32层",GOVERNANCE_FILE_CONTRACT_REQUIRED:"请先登记结构化文件输入契约"});

Object.assign(governanceRunCodes,{CODE_PREVIEW_QUEUED:"代码试运行已排队",CODE_PREVIEW_COMPLETED:"代码试运行完成，未登记成果",SCRIPT_RUNTIME_UNAVAILABLE:"指定的代码运行环境不可用，请联系管理员检查执行服务",SCRIPT_OUTPUT_INVALID:"代码返回的字段、类型或精度与声明不一致",SCRIPT_CONTRACT_INVALID:"请补全代码及输出字段声明",SCRIPT_TERMINATED:"代码任务已终止，可能超时或超过内存限制",SCRIPT_OUTPUT_LIMIT:"代码输出超过运行上限",SCRIPT_CAPACITY:"代码执行任务已满，请稍后重试",SCRIPT_ERROR:"代码处理失败，请查看节点及行号",SCRIPT_VALUEERROR:"代码值处理失败，请查看行号",SCRIPT_TYPEERROR:"代码类型处理失败，请查看行号",SCRIPT_SYNTAXERROR:"代码语法有误，请查看行号",SCRIPT_NAMEERROR:"代码引用了未定义的名称",SCRIPT_KEYERROR:"代码访问的字段不存在",SCRIPT_INVALID_RETURN:"代码必须为每条输入返回一个对象",SCRIPT_PROTOCOL_INVALID:"代码返回的数据格式不符合约定"});

Object.assign(governanceRunCodes,{SCRIPT_ZERODIVISIONERROR:"代码计算出现除以零，请查看对应行",SCRIPT_OVERFLOWERROR:"代码计算结果超出范围",SCRIPT_INDEXERROR:"代码访问的位置超出列表范围",SCRIPT_ATTRIBUTEERROR:"代码使用了不适用的属性或方法",SCRIPT_MEMORYERROR:"代码使用的内存超过限制",SCRIPT_RECURSIONERROR:"代码调用层级过深，请检查重复调用",SCRIPT_IMPORTERROR:"代码引用的库不在固定环境中",SCRIPT_RANGEERROR:"代码处理的数值或长度超出范围",SCRIPT_INPUT_INVALID:"代码或业务参数格式不正确",SCRIPT_BATCH_INVALID:"本批数据超出代码处理限制",SCRIPT_ROW_INVALID:"代码返回记录的字段或顺序不符合约定",SCRIPT_SEQUENCE_INVALID:"代码任务的数据批次顺序异常，请重新运行",SCRIPT_TIMEOUT:"代码运行超过允许时间，请简化处理或拆分任务"});

export type GovernanceDataLayer="ods"|"dwd"|"dim"|"dws"|"ads";
export const governanceDataLayers:{value:GovernanceDataLayer;label:string}[]=[{value:"ods",label:"原始接入 · ODS"},{value:"dwd",label:"标准明细 · DWD"},{value:"dim",label:"公共维度 · DIM"},{value:"dws",label:"主题汇总 · DWS"},{value:"ads",label:"应用服务 · ADS"}];
export const governanceDataLayerLabel=(layer?:string)=>governanceDataLayers.find(item=>item.value===layer)?.label??"尚未标注";
export interface GovernanceStorage{engine:"doris";database:string;table:string;data_layer:GovernanceDataLayer}
export interface GovernanceExecutionPlan{mode:"batch"|"continuous";engine:"doris_sql"|"flink";input_channel:"source_snapshot"|"kafka_cdc";result_store:"doris";data_layer:GovernanceDataLayer;reason:string;archive:"rustfs_on_demand"}
export interface GovernanceStream{freshness?:GovernanceStreamFreshness;execution_plan?:GovernanceExecutionPlan;id:string;workspace_id:string;flow_id:string;flow_version:number;state:"starting"|"running"|"pausing"|"paused"|"failed"|"unknown";desired_state:string;job_id?:string;generation:number;error_code?:string;input_events:number;output_rows:number;rejected_rows:number;output_offset:number;caught_up:boolean;created_at:string;updated_at:string}

// Keep one persisted classification; business wording maps to the existing layer contract.
export const governanceBusinessPurposes:{value:GovernanceDataLayer;label:string}[]=[
 {value:"dwd",label:"整理统一的业务明细"},
 {value:"dim",label:"维护可复用的基础资料"},
 {value:"dws",label:"汇总可复用的统计指标"},
 {value:"ads",label:"提供特定报表或应用所需数据"},
];
export const governanceBusinessPurposeLabel=(layer?:string)=>layer==="ods"?"保留来源原始数据":governanceBusinessPurposes.find(item=>item.value===layer)?.label??"尚未确定";

export interface GovernanceStreamHealth {state:"running"|"paused"|"stopped"|"unhealthy"|"unknown"|"readable";observed_at:string|null}
export interface GovernanceStreamSourceFreshness extends GovernanceStreamHealth {node_id:string;latest_event_at:string|null;forwarded_to_at:string|null;backlog_offsets:number|null}
export interface GovernanceStreamFreshness {source_capture:GovernanceStreamHealth;flink_compute:GovernanceStreamHealth;doris_write:GovernanceStreamHealth;sources:GovernanceStreamSourceFreshness[];processed_to_at:string|null;end_to_end_latency_ms:number|null;doris_output_offset:number|null}
