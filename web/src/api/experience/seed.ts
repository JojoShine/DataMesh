import type {Field,Principal} from "../types";
import type {GovernanceDefinition,GovernanceRecord,GovernanceDataflow} from "../governance";
import type {GovernanceWorkspace} from "../governanceWorkspace";
import {now,record,type ExperienceState} from "./store";
export const SAMPLE_SPACE="exp-workspace";
const field=(name:string,business_name:string,type="string",primary_key=false):Field=>({name,business_name,logical_type:type,nullable:!primary_key,primary_key,sensitive:false,precision:null,scale:null});
export function sampleGraph(resource="exp-residents",file=false):GovernanceDataflow{return {nodes:[
 {id:"source_1",kind:"source",inputs:[],source:{resource:{resource_type:file?"file_resource":"table_resource",resource_id:resource,contract_version:1},fields:["person_id","person_name","district","age"]}},
 {id:"clean_1",kind:"transform",inputs:[{node:"source_1"}],columns:[{source:"person_id",target:"person_id",rule:"copy"},{source:"person_name",target:"person_name",rule:"trim"},{source:"district",target:"district",rule:"trim"},{source:"age",target:"age",rule:"copy"}]},
 {id:"quality_1",kind:"quality",inputs:[{node:"clean_1"}],checks:[{field:"person_name",kind:"required",blocking:true},{field:"age",kind:"range",blocking:true,parameters:{min:0,max:120}}]},
 {id:"output_1",kind:"output",inputs:[{node:"quality_1",port:"good"}],infer_model:true,data_layer:"dwd"},
]}}
export function seed(user:Principal):ExperienceState {
 const state:ExperienceState={schema:1,owner:user.user_id,collections:{}};const created=now();
 const workspace:GovernanceWorkspace={id:SAMPLE_SPACE,department_id:user.department_id,name:"治理体验空间",description:"本浏览器独立保存的示例治理数据与运行结果",owner_id:user.user_id,state:"active",is_legacy:false,max_concurrent_runs:1,version:1,created_at:created,updated_at:created,required_release_kinds:[]};
 state.collections.workspaces=[record(workspace)];
 const residents=Array.from({length:80},(_,index)=>({person_id:`P${String(index+1).padStart(5,"0")}`,person_name:index===6?"":` ${["王明","李华","张琳","赵敏","陈晨"][index%5]} `,district:["城东街道","城西街道","城南街道","城北街道"][index%4],age:index===12?145:18+index%60}));
 const enrollment=Array.from({length:100},(_,index)=>({registration_id:`R2026${String(index+1).padStart(5,"0")}`,person_id:`P${String(index%80+1).padStart(5,"0")}`,enrollment_year:2026,application_status:["待审核","审核通过","待补正"][index%3]}));
 const samples=[{id:"exp-residents",name:"居民基础信息（体验样本）",fields:[field("person_id","人员编号","string",true),field("person_name","姓名"),field("district","所属街道"),field("age","年龄","integer")],rows:residents},
 {id:"exp-enrollment",name:"招生报名登记（体验样本）",fields:[field("registration_id","报名编号","string",true),field("person_id","人员编号"),field("enrollment_year","报名年度","integer"),field("application_status","办理状态")],rows:enrollment}];
 state.collections.sources=samples.map(sample=>record({id:sample.id,object_id:sample.id,source_id:"",qualified_name:sample.name,display_name:sample.name,description:"浏览器生成的虚构体验记录，与正式来源数据库无关",schema_version:1,resource_type:"table_resource",adapter:"browser_example",data_layer:"dwd",fields:sample.fields,field_labels:Object.fromEntries(sample.fields.map(f=>[f.name,f.business_name])),rows:sample.rows,workspaces:[SAMPLE_SPACE]}));
 state.collections.files=samples.map(sample=>record({file_id:sample.id,name:sample.name+".csv",size:JSON.stringify(sample.rows).length,rows:sample.rows.length,fields:sample.fields,field_labels:Object.fromEntries(sample.fields.map(f=>[f.name,f.business_name])),version:1}));
 const owner={owner_id:user.user_id,steward_id:user.user_id};
 const definitions:{id:string;kind:GovernanceRecord["kind"];definition:GovernanceDefinition}[]=[
 {id:"exp-domain",kind:"domain",definition:{name:"公共服务",description:"人口信息与招生报名治理体验",domain_scope:"department",...owner}},
 {id:"exp-term",kind:"term",definition:{name:"人员编号",description:"体验样本中连接居民与报名登记的编号",...owner}},
 {id:"exp-standard",kind:"standard",definition:{name:"年龄取值规范",description:"年龄应在0至120之间",...owner,standard:{code:"AGE",logical_type:"integer",length:0,precision:0,scale:0,nullable:false,sensitive:false,pattern:"",values:[]}}},
 {id:"exp-template",kind:"quality_template",definition:{name:"居民信息基础检查",description:"姓名必填、年龄范围检查",...owner,quality_template:{fields:samples[0].fields,reference_fields:[],checks:sampleGraph().nodes[2].checks??[]}}},
 ...samples.map(sample=>({id:sample.id+"-model",kind:"model" as const,definition:{name:sample.name.replace("（体验样本）","模型"),description:"体验样本的字段约定",...owner,model:{layer:"logical" as const,schema:"",table:"",data_layer:"dwd" as const,fields:sample.fields.map(f=>({...f,description:f.business_name??"",length:0,precision:0,scale:0})),relations:[],contract:{freshness_seconds:0,max_failure_rate:1,usage_restrictions:"仅用于本浏览器体验"}}}})),
 {id:"exp-flow",kind:"dataflow",definition:{name:"居民信息整理与质量检查",description:"清理姓名空白，隔离姓名缺失及年龄异常的样本",...owner,dataflow:sampleGraph()}},
 ];
 state.collections.definitions=definitions.map(def=>record({...def,...{workspace_id:SAMPLE_SPACE,department_id:user.department_id,latest_version:1,version:1,state:"published",change_note:"内置体验样本",migration_note:"",created_at:created,published_at:created,execution_mode:"batch"}} satisfies GovernanceRecord));
 state.collections.units=[record({id:"exp-unit",name:"居民信息轻量处理",version:1,graph:sampleGraph("exp-residents",true),files:state.collections.files,created_at:created,updated_at:created})];
 return state;
}
