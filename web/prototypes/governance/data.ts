import type {GovernanceDataflow,GovernanceFlowKind,MetadataResource} from "../../src/api/governance";
import type {Field} from "../../src/api/types";
export type View="home"|"flow"|"result";
export type Scenario="ready"|"quality"|"source";
export type Topic="domain"|"standard"|"term"|"source"|"model"|"authorization"|"workflow"|"schedule"|"quality"|"issues"|"lineage"|"lifecycle"|"sharing";
export interface RunEvidence {version:number;sourceVersion:number;state:"succeeded"|"failed";processed:number;rejected:number;output:number;id:string}
export const fields:Field[]=[{name:"id",logical_type:"integer",primary_key:true,nullable:false,sensitive:false,precision:null,scale:null},{name:"name",logical_type:"string",primary_key:false,nullable:true,sensitive:true,precision:null,scale:null},{name:"amount",logical_type:"decimal",primary_key:false,nullable:true,sensitive:false,precision:20,scale:6}];
export const fieldNames:Record<string,string>={id:"客户编号",name:"客户姓名",amount:"客户金额"};
export const resource:MetadataResource={object_id:"demo-customers",source_id:"demo-source",qualified_name:"public.customers",schema_version:1,fields,resource_type:"table_resource",adapter:"database",observation_kind:"live_schema"};
export const initialGraph:GovernanceDataflow={nodes:[
 {id:"source_customer",kind:"source",inputs:[],source:{resource:{resource_type:"table_resource",resource_id:resource.object_id,contract_version:1},fields:fields.map(f=>f.name)}},
 {id:"clean_name",kind:"transform",inputs:[{node:"source_customer"}],columns:fields.map(f=>({source:f.name,target:f.name,rule:f.name==="name"?"trim":"copy"}))},
 {id:"check_customer",kind:"quality",inputs:[{node:"clean_name"}],checks:[{field:"id",kind:"required",blocking:true,parameters:{}},{field:"id",kind:"business_key",blocking:true,parameters:{}},{field:"name",kind:"required",blocking:true,parameters:{}},{field:"amount",kind:"range",blocking:true,parameters:{min:0}}]},
 {id:"output_customer",kind:"output",inputs:[{node:"check_customer",port:"good"}],model:{id:"demo-model",version:2}}
]};
export const palette:GovernanceFlowKind[]=["source","project","transform","filter","deduplicate","join","aggregate","quality","code_map","output"];
export const topics:Record<Topic,{title:string;purpose:string;when:string;before:string;after:string;required:string;relation:string}>={
 domain:{title:"主题域与责任",purpose:"用业务范围组织数据，并明确谁确认含义、谁维护定义。",when:"需要统一客户、订单、财务等业务分类或跨团队协作时使用。",before:"先明确业务边界、部门及有效负责人员。",after:"发布后，标准、模型和业务注释等可引用该主题域版本。",required:"主题域关联按需；各治理定义的责任人、维护人必填。",relation:"客户主题域 → 客户模型 / 数据标准 / 业务注释；责任分工不自动授予审批或访问权限。"},
 standard:{title:"数据标准",purpose:"统一字段应遵循的类型、精度、格式和允许值。",when:"多个来源采用不同编码、格式或口径，需要共同规则时使用。",before:"业务已确认字段规则；需关联主题域时选择已发布版本。",after:"在模型、业务注释或码表转换中引用。单独发布标准不会执行数据清洗。",required:"按需建立；一旦引用，适用约束和版本必须有效。",relation:"客户状态标准 → 模型字段要求 / 码表转换 → 运行时校验。"},
 term:{title:"业务术语",purpose:"统一“有效客户”等业务概念的含义，避免同名不同义。",when:"业务人员需要解释字段口径、判定条件和适用范围时使用。",before:"明确业务定义、时间范围及负责人员。",after:"已发布术语关联到字段业务注释，帮助理解；筛选规则另在数据流配置。",required:"按需建立；选择关联时须引用已发布版本。",relation:"有效客户（业务定义）→ 字段注释；数据流筛选（实际执行）使用另行确认的条件。"},
 source:{title:"来源结构核对",purpose:"确认读取的是哪份数据，以及真实字段是否与登记结构一致。",when:"首次配置来源、运行失败或来源变更时。",before:"来源已接入、可见；实际读取还需字段加工权限。",after:"结构核对后选定字段和契约版本，供模型及数据流引用。",required:"结构依据正确是必选；不要求每次都重建来源或目录。",relation:"共享交换接入 → 当前来源及结构 → 模型 / 数据流 → 实际运行。"},
 model:{title:"结果字段与要求（模型）",purpose:"约定结果的字段、类型、主键、敏感范围和质量要求。",when:"准备产出一份可复用、可核验的治理成果时使用。",before:"明确结果用途与字段；引用的标准或关联模型须已发布。",after:"数据流输出节点固定引用模型版本，完整运行时核对实际结果。",required:"生成数据流成果必须有可执行的已发布输出模型，可复用。",relation:"字段要求 → 已发布模型 v2 → 数据流输出 → 实际成果。发布模型不代表数据已生成。"},
 authorization:{title:"读取与派生权限",purpose:"明确谁可以读取哪些字段用于治理，以及是否允许派生成果。",when:"剖析来源、运行数据流以及授权发生变化时。",before:"有效资源、字段范围、用途、期限及相应授权依据。",after:"运行时仍重新核对；权限失效会阻断读取或派生成果。",required:"满足现有有效权限依据是必选；看到来源不等于能读取。",relation:"资源范围 → 加工权限 → 实际读取 / 派生；成果消费使用另一套申请与授权。"},
 workflow:{title:"工作流编排",purpose:"组织多个任务的依赖、执行条件、核验和审批顺序。",when:"任务需要前后依赖、条件分支或等待发布审批时使用。",before:"引用的处理定义已发布，任务输入和依赖合法。",after:"发布后可手动执行，或交给独立调度计划自动触发。",required:"有编排需求时条件必选；单条数据流可以直接运行。",relation:"数据流任务 → 独立质量核验 → 发布审批；每个环节保留实际证据。"},
 schedule:{title:"调度计划",purpose:"规定已发布工作流在什么时候自动运行。",when:"需要每日、每周或固定间隔自动执行时使用。",before:"已发布且可运行的工作流、执行权限、时区和错过触发策略。",after:"保存调度后查看触发记录，再进入对应真实运行。",required:"定时运行时条件必选；与工作流编排分菜单办理。",relation:"工作流版本 → 时间及固定参数 → 触发记录 → 实际运行 → 告警。"},
 quality:{title:"成果质量与完整性",purpose:"判断这次结果是否满足要求，是否有完整、有效的执行证据。",when:"查看运行结果、准备发布或排查阻断时。",before:"实际运行或历史结果绑定核验已形成相应证据。",after:"通过门禁后才能核验发布；异常回到来源或处理流程整改。",required:"发布门禁必过；采样通过不能代替完整运行。",relation:"实际运行 → 质量 / 完整性 / 鲜度 / 权限核对 → 发布。"},
 issues:{title:"质量整改",purpose:"把真实质量异常分派给具体人员，以复测完成闭环。",when:"出现检查失败或隔离记录，需要跟踪处理时。",before:"真实异常运行、责任人、处理期限及原因记录。",after:"修复后按原数据流版本复测，核验成功且无隔离后关闭。",required:"登记问题后须按规则闭环；没有异常不需新建问题。",relation:"异常运行 → 分派 / 修复 → 原版本复测 → 核验关闭。"},
 lineage:{title:"血缘与变更影响",purpose:"说明结果从哪里来、经过什么处理，以及修改会影响谁。",when:"复核成果、准备修订或停用在用对象时。",before:"真实登记或运行关系及相应可见权限。",after:"据实际依赖安排修订、迁移或停用；受限范围明确提示。",required:"证据自动留痕；有阻断依赖时须处理，不以看过图代替校验。",relation:"来源 → 固定模型与处理版本 → 实际运行 → 成果 → 下游引用。"},
 lifecycle:{title:"成果保留、清理与迁移",purpose:"管理过期快照和新旧成果替换，同时保护在用数据。",when:"成果过期、需要控制快照占用或替换在用成果时。",before:"保留期限、实际依赖、发布 / 授权 / 整改保护和相应权限。",after:"清理保留回执和历史证据；迁移须核验旧依赖处理完成。",required:"按需办理；选择清理或替换后，全部预检与依赖处理必选。",relation:"影响核对 → 新成果 / 迁移 → 切换依赖和授权 → 核验 → 按规则清理。"},
 sharing:{title:"成果共享与使用授权",purpose:"把已发布成果按明确目录、范围和条件提供给获准人员使用。",when:"需要跨部门或向其他使用者提供数据时。",before:"已发布成果、目录、挂接、审核及对应职责和材料。",after:"使用方申请并获准后才能查询、下载或调用。",required:"需要共享时条件必选；成果已发布不等于已共享或已授权。",relation:"成果发布 → 目录维护 / 挂接 → 审核 → 使用申请 / 审批 → 授权交付。"}
};
export const lessons:Record<View,{title:string;target:string;why:string;before:string;after:string}[]>={
 home:[
 {title:"先明确这项工作的目标",target:"goal",why:"把来源整理成满足客户模型要求、可核验发布的成果。",before:"明确用途和负责人员；已有来源及模型可以复用。",after:"沿关系图选择当前环节继续，所有业务校验保留。"},
 {title:"理解业务规范为什么存在",target:"foundation",why:"主题域划分类别，责任明确承接人，术语解释含义，标准约定规则。",before:"业务已明确范围、口径和分工。",after:"按需关联到模型或注释；发布定义本身不清洗数据。"},
 {title:"看清前后关系和当前状态",target:"relation",why:"这条线帮助定位工作，不强迫把所有功能塞进一张长表单。",before:"前置状态以实际核验为准；“待核验”不等于已满足。",after:"选择当前环节会带入对应对象，后续仍逐项办理。"},
 {title:"从待办继续，不重新寻找对象",target:"tasks",why:"每个事项说明当前缺什么、由谁办理、接下来去哪里。",before:"只展示当前职责可见的事项。",after:"进入对应单层Dialog，返回后保留原工作上下文。"}],
 flow:[
 {title:"先理解这条数据流要解决什么",target:"flow-purpose",why:"数据流说明如何读取、清洗、检查并产出符合模型的结果。",before:"来源与加工权限有效，输出模型已发布。",after:"先配置并发布定义，再发起完整运行；两种完成状态分开。"},
 {title:"节点连线表示实际处理顺序",target:"canvas",why:"选中节点了解输入和输出。算子按业务选用，来源和模型输出不可省略。",before:"前一节点的字段必须满足后一节点的配置要求。",after:"合格数据进入输出，不合格分支保留检查证据。"},
 {title:"只配置当前选中的节点",target:"properties",why:"字段、转换和检查在右侧就地解释，不把所有节点表单纵向展开。",before:"先核对输入字段、类型及适用要求。",after:"配置完成后检查前置和结构，再保存、核对并发布。"},
 {title:"知道哪些条件会阻止下一步",target:"flow-conditions",why:"未发布模型、缺少权限、字段不相容都需先处理。",before:"每项条件给出状态、原因和办理入口。",after:"保存草稿、发布定义、完整运行分别执行，历史版本保留。"},
 {title:"实际结果属于某次运行",target:"flow-evidence",why:"结果计数、质量和日志指向明确的运行和定义版本。",before:"定义已发布，运行真实完成；本交互稿只模拟执行。",after:"从结果进入成果核验，质量和发布门禁仍须逐项通过。"}],
 result:[
 {title:"先确认核验的是哪份成果",target:"result-object",why:"来源、模型、数据流和生成时间帮助避免核错对象。",before:"应有实际成功的产出证据；失败运行不能冒充已生成成果。",after:"核对本次成果及其固定版本，再办理质量和发布。"},
 {title:"发布前逐项检查必要条件",target:"gates",why:"运行、质量、完整性、权限、鲜度和发布职责共同决定能否发布。",before:"未核验或未通过的条件都需有具体说明。",after:"全部满足后点击核验并发布；不能由教学进度替代。"},
 {title:"异常要能解释，也要能继续处理",target:"diagnostics",why:"先说明业务原因，再提供字段、规则、运行和处理入口。",before:"只展示当前证据和权限允许的信息；示例异常不是主环境诊断。",after:"回到来源或数据流处理，重新运行产出新成果后再核验。"},
 {title:"发布完成后，共享仍有后续流程",target:"sharing",why:"发布表示成果可进入共享办理，不自动公开或授予数据使用权限。",before:"目录、挂接、审核及使用授权仍按各自规则办理。",after:"按业务需要前往共享交换，或继续查看血缘及生命周期。"}]
};
export function targetClass(active:string|undefined,id:string){return active===id?" proto-highlight":""}
