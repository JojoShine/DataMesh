import type {Principal} from "../types";
import {governancePaths} from "../governance";
import {execute,graph,rows} from "./engine";
import {seed,SAMPLE_SPACE} from "./seed";
import {ExperienceError,find,invalid,key,list,now,numeric,object,page,persist,put,read,record,text,storageKey,type ExperienceState,type ObjectValue,type Value} from "./store";
export {ExperienceError} from "./store";
const root="/api/v1/governance/",light="/api/v1/lightweight-governance/";
export function isGovernanceExperiencePath(path:string){const pathname=path.split("?")[0];return pathname.startsWith(root)||pathname.startsWith(light)||/^\/api\/v1\/datasets\/exp-[^/]+\/(quality|lineage|check)$/.test(pathname)}
function contents(row:ObjectValue){return object(row.definition)?record(row.definition):{}}
function latest(state:ExperienceState,kind?:string,published=false){const definitions=list(state,"definitions").filter(row=>(!kind||row.kind===kind)&&(!published||row.state==="published"));return definitions.filter(row=>!definitions.some(other=>other.id===row.id&&numeric(other.version)>numeric(row.version)))}
function definition(state:ExperienceState,id:string,version?:number){const row=(version?list(state,"definitions").find(row=>row.id===id&&row.version===version):latest(state).find(row=>row.id===id));if(!row)throw new ExperienceError(404,"EXPERIENCE_NOT_FOUND","体验定义或固定版本不存在");return row}
function saveDefinition(state:ExperienceState,row:ObjectValue){state.collections.definitions=[row,...list(state,"definitions").filter(old=>old.id!==row.id||old.version!==row.version)]}
function sourceView(row:ObjectValue){const {rows:sampleRows,workspaces,...metadata}=row;return metadata}
function releaseKinds(value:unknown){if(value===undefined)return [];if(!Array.isArray(value)||value.some(kind=>!Object.keys(governancePaths).includes(text(kind))))return invalid("发布要求包含未知定义类型");return value.map(item=>text(item))}
function modelLayer(row:ObjectValue){const model=contents(row).model;return object(model)?text(model.data_layer):""}
function submitting(method:string){if(method!=="POST")throw new ExperienceError(405,"EXPERIENCE_METHOD_NOT_ALLOWED","此操作需要明确提交，不能通过查看链接执行")}
function stateError(message:string):never{throw new ExperienceError(409,"EXPERIENCE_STATE_CONFLICT",message)}
function editable(state:ExperienceState,workspace:string){if(find(state,"workspaces",workspace).state!=="active")stateError("此体验空间已经归档，不能修改或运行")}
function workspaceRows(state:ExperienceState,collection:string,workspace:string){return list(state,collection).filter(row=>!row.workspace_id||row.workspace_id===workspace)}
function filtered(rows:ObjectValue[],query:URLSearchParams){const search=(query.get("search")??"").toLocaleLowerCase();return rows.filter(row=>!search||`${text(row.name)} ${text(contents(row).name)} ${text(row.qualified_name)}`.toLocaleLowerCase().includes(search))}
function rememberRun(state:ExperienceState,result:ReturnType<typeof execute>){if(list(state,"runs").length>=30)invalid("体验空间最多保留30次运行，请先重置体验空间");const run=result.run;if(result.model)saveDefinition(state,result.model);put(state,"runs",run);put(state,"outputs",result.output);put(state,"rejections",record({id:run.id,items:result.rejections}));const events=object(run.result)&&Array.isArray(run.result.nodes)?run.result.nodes.map((node,index)=>record({id:index+1,run_id:run.id,node_id:object(node)?node.node_id:"",code:"FLOW_NODE_COMPLETED",processed:object(node)?node.output_count:0,created_at:now()})):[];put(state,"events",record({id:run.id,items:events}))}
function validateDefinition(value:unknown){const data=record(value);if(!text(data.name).trim()||text(data.name).length>200)invalid("请填写200字以内的名称");if(data.dataflow)graph(data.dataflow);if(data.model){if(!object(data.model)||!Array.isArray(data.model.fields)||!data.model.fields.length)invalid("请补全模型字段");const names=data.model.fields.map(f=>object(f)?text(f.name):"");if(names.some(name=>!name||["__proto__","prototype","constructor"].includes(name))||new Set(names).size!==names.length)invalid("模型字段名不能为空或重复")};return data}
function outcome(state:ExperienceState,url:URL,method:string,input:ObjectValue,user:Principal):unknown{
 const pathname=url.pathname;const query=url.searchParams;const workspace=query.get("workspace_id")??SAMPLE_SPACE;
 const path=pathname.startsWith(root)?pathname.slice(root.length).split("/"):[];
 if(method!=="GET"&&!pathname.startsWith(light)&&path[0]!=="workspaces"&&path[0]!=="experience-reset")editable(state,workspace);
 if(path[0]==="members")return {items:[{user_id:user.user_id,username:user.username,department_id:user.department_id}]};
 if(path[0]==="code-runtimes")return {items:[]};
 if(path[0]==="experience-reset"){if(method!=="POST")return invalid("重置需要明确提交");state.collections=seed(user).collections;return {reset:true}}
 if(path[0]==="workspaces"){
  if(path.length===1){if(method==="GET")return page(filtered(list(state,"workspaces").filter(row=>!query.get("state")||row.state===query.get("state")),query),query);
   submitting(method);if(!text(input.name).trim())invalid("请输入空间名称");const id=key(),date=now();const row=record({id,name:text(input.name).trim(),description:text(input.description),department_id:user.department_id,owner_id:user.user_id,state:"active",is_legacy:false,max_concurrent_runs:Math.max(1,Math.min(10,numeric(input.max_concurrent_runs,1))),version:1,created_at:date,updated_at:date,required_release_kinds:releaseKinds(input.required_release_kinds)});put(state,"workspaces",row);return row}
  const row=find(state,"workspaces",path[1]);if(method==="GET"&&path.length===2)return row;
  if(path[2]==="overview")return {workspace:row,source_count:list(state,"sources").filter(s=>Array.isArray(s.workspaces)&&s.workspaces.includes(path[1])).length,model_count:latest(state,"model").filter(d=>d.workspace_id===path[1]).length,dataflow_count:latest(state,"dataflow").filter(d=>d.workspace_id===path[1]).length,workflow_count:latest(state,"workflow").filter(d=>d.workspace_id===path[1]).length,run_states:counts(workspaceRows(state,"runs",path[1]),"state"),recent_runs:workspaceRows(state,"runs",path[1]).slice(0,5),recent_results:workspaceRows(state,"outputs",path[1]).slice(0,5).map(output=>({dataset_id:output.dataset_id,name:contents(output).name,publication_status:output.publication_status,created_at:output.created_at??now()})),quality_summary:qualitySummary(state,path[1]),observed_at:now()};
  if(path[2]==="sources"||path[2]==="source-candidates"){
   const sources=list(state,"sources"),linked=(source:ObjectValue)=>Array.isArray(source.workspaces)&&source.workspaces.includes(path[1]);
   if(method==="GET")return page(filtered(sources.filter(source=>path[2]==="source-candidates"?!linked(source):linked(source)).map(source=>path[2]==="source-candidates"?sourceView(source):record({reference:{workspace_id:path[1],resource_type:source.resource_type,resource_id:source.object_id,contract_version:source.schema_version,active:true},resource:sourceView(source),available:true})),query),query);
   submitting(method);editable(state,path[1]);const resource=object(input.resource)?input.resource:input;const source=find(state,"sources",text(resource.resource_id));const spaces=Array.isArray(source.workspaces)?source.workspaces:[];source.workspaces=path[3]==="remove"?spaces.filter(id=>id!==path[1]):[...new Set([...spaces,path[1]])];return {updated:true};
  }
  if(method==="POST"&&(path[2]==="archive"||path[2]==="update")){editable(state,path[1]);if(input.base_version!==row.version)stateError("空间信息已更新，请刷新");if(path[2]==="archive")row.state="archived";else {if(!text(input.name).trim())invalid("请输入空间名称");row.name=text(input.name);row.description=text(input.description);row.max_concurrent_runs=Math.max(1,Math.min(10,numeric(input.max_concurrent_runs,numeric(row.max_concurrent_runs,1))));if(input.required_release_kinds!==undefined){if(!text(input.release_policy_reason).trim())invalid("请填写发布要求调整原因");row.required_release_kinds=releaseKinds(input.required_release_kinds)}}row.version=numeric(row.version)+1;row.updated_at=now();return row}
 }
 if(path[0]==="metadata"){
  const sources=list(state,"sources").filter(source=>!query.get("workspace_sources_only")||Array.isArray(source.workspaces)&&source.workspaces.includes(workspace));
  if(path.length===1)return page(filtered(sources.map(sourceView),query),query);
  const source=find(state,"sources",path[1]);if(path[2]==="contract")return {...sourceView(source),resource:{resource_type:source.resource_type,resource_id:source.object_id,contract_version:source.schema_version}};
  if(path[2]==="observations")return {...page([record({id:path[1]+"-observation",object_id:path[1],version:1,object_version:1,source_version:1,source_id:"",schema:"",table:source.qualified_name,fields:source.fields,columns:[],changes:[],matches_active:true,observed_by:user.user_id,created_at:now(),resource_type:source.resource_type,observation_kind:"registered_contract"})],query),object:sourceView(source)};
  if(path[2]==="collection")return {...page([],query),policy:null,pending_reviews:[],observed_at:now()};
 }
 if(path[0]==="dataflows"&&path[1]==="code-preview")throw new ExperienceError(403,"EXPERIENCE_CODE_BLOCKED","体验空间不执行任意Python或JavaScript；请使用内置步骤处理样本");
 if(path[0]==="profiles"&&method==="POST")return profile(state,input,user,workspace);
 const kind=Object.entries(governancePaths).find(([,value])=>value===path[0])?.[0];
 if(kind){
  if(method!=="GET"&&input.execution_mode&&input.execution_mode!=="batch")invalid("体验空间仅支持批量处理，不启动持续运行");
  if(path.length===1){if(method==="GET")return page(filtered(latest(state,kind,query.get("state")==="published"||query.get("published")==="true").filter(row=>(!row.workspace_id||row.workspace_id===workspace)&&(!query.get("published")||row.state==="published")&&(query.get("purpose")!=="metric"||object(contents(row).metric))&&(!query.get("domain_scope")||contents(row).domain_scope===query.get("domain_scope"))&&(!query.get("data_layer")||modelLayer(row)===query.get("data_layer"))),query),query);
   submitting(method);const data=validateDefinition(input.definition),id=key(),created=now();const row=record({id,kind,workspace_id:workspace,department_id:user.department_id,latest_version:1,version:1,state:"draft",definition:data,change_note:text(input.change_note),migration_note:"",created_at:created,published_at:null,execution_mode:"batch"});saveDefinition(state,row);return row}
  const id=path[1],version=path[2]==="versions"&&path[3]?Number(path[3]):undefined;const row=definition(state,id,version);if(row.kind!==kind)invalid("定义类型不匹配");if(method!=="GET")editable(state,text(row.workspace_id,workspace));
  if(path.length===2&&method==="GET")return row;
  if(path[2]==="versions"&&path.length===3){if(method==="GET")return page(list(state,"definitions").filter(d=>d.id===id),query);
   submitting(method);const current=definition(state,id);if(input.base_version!==current.version)stateError("当前定义已有更新，请重新打开后修改");const version=numeric(current.version)+1;
   const next={...current,definition:validateDefinition(input.definition),version,latest_version:version,state:"draft",change_note:text(input.change_note),published_at:null,created_at:now()};saveDefinition(state,next);for(const old of list(state,"definitions").filter(d=>d.id===id))old.latest_version=version;return next}
  if(path[2]==="diff"){const before=definition(state,id,Number(query.get("from"))),after=definition(state,id,Number(query.get("to")));return {before:before.definition,after:after.definition,changes:JSON.stringify(before.definition)===JSON.stringify(after.definition)?[]:[{code:kind+"_changed",breaking:true}]}}
  if(path[2]==="impact")return {items:[],total:0};
  if(path[2]==="versions"&&path.length===4&&method==="GET")return row;
  if(path[4]==="layout"){if(method==="GET")return list(state,"layouts").find(r=>r.id===id+":"+version)??{revision:0,positions:{},viewport:{x:0,y:0,zoom:1}};if(method!=="PUT")throw new ExperienceError(405,"EXPERIENCE_METHOD_NOT_ALLOWED","请使用画布保存操作");const previous=list(state,"layouts").find(r=>r.id===id+":"+version);if(previous&&input.expected_revision!==undefined&&input.expected_revision!==previous.revision)stateError("画布布局已更新，请刷新");const layout=record({id:id+":"+version,revision:numeric(previous?.revision)+1,positions:input.positions??{},viewport:input.viewport??{x:0,y:0,zoom:1}});put(state,"layouts",layout);return layout}
  if(path[4]==="field-labels"){const model=contents(row).model;return {field_labels:object(model)&&Array.isArray(model.fields)?Object.fromEntries(model.fields.flatMap(f=>object(f)?[[text(f.name),text(f.business_name)||text(f.description)||text(f.name)]]:[])):{}}}
  if(path[4]==="parameters")return {items:[]};
  if(path[4]==="ddl")return {ddl:"-- 体验空间仅保存模型定义，不连接数据库或执行DDL。",executed:false};
  if(path[4]==="publish"||path[4]==="retire"){submitting(method);const currentState=text(row.state);if(path[4]==="publish"&&currentState!=="draft"&&currentState!=="published")stateError("已停用的版本不能重新发布，请创建修订");const space=find(state,"workspaces",text(row.workspace_id,workspace));if(path[4]==="publish"&&Array.isArray(space.required_release_kinds)&&space.required_release_kinds.includes(kind))stateError("当前体验空间要求独立审核；真实审核需在正式环境办理。可在空间设置中调整体验发布要求");row.state=path[4]==="publish"?"published":"retired";row.published_at=path[4]==="publish"?now():row.published_at;row.migration_note=text(input.migration_note);return row}
  if(path[4]==="run"&&kind==="workflow"){submitting(method);return workflow(state,row)}
  if(path[4]==="run"&&kind==="dataflow"){submitting(method);if(contents(row).metric)throw new ExperienceError(409,"EXPERIENCE_OPERATION_UNAVAILABLE","体验空间可维护指标口径；指标执行需要正式治理环境。样本统计可在数据流中使用分组汇总步骤");if(row.state!=="published")stateError("请先在体验空间发布此定义版本");if(object(input.parameters)&&Object.keys(input.parameters).length)invalid("体验空间暂不执行参数绑定，请使用固定样本配置");if(input.execution_mode==="continuous")invalid("体验空间仅运行有限样本，不启动持续任务");const result=execute(state,contents(row),{id,version:numeric(row.version)},text(row.workspace_id,workspace));rememberRun(state,result);return result.run}
 }
 if(path[0]==="runs"){
  if(path.length===1)return page(workspaceRows(state,"runs",workspace).filter(run=>(!query.get("kind")||run.kind===query.get("kind"))&&(!query.get("state")||run.state===query.get("state"))&&(!query.get("definition_id")||object(run.definition)&&object(run.definition.flow)&&run.definition.flow.id===query.get("definition_id"))),query);
  const run=find(state,"runs",path[1]);if(path.length===2)return run;
  if(path[2]==="tasks")return {items:find(state,"workflow-tasks",path[1]).items};
  if(path[2]==="records"){const output=list(state,"outputs").find(o=>o.governance_run_id===run.id);return page(output?rows(output.rows).map(record):[],query)}
  if(path[2]==="events"){const items=find(state,"events",path[1]).items;return page(Array.isArray(items)?items.filter(object).map(record):[],query)}
  if(path[2]==="model-proposal"){const output=list(state,"outputs").find(o=>o.governance_run_id===run.id);const model=output&&object(output.model)?definition(state,text(output.model.id),numeric(output.model.version)):null;if(!model)invalid("此运行尚无模型建议");return {definition:model.definition,generated_model:output?.model,change_note:"体验结果字段推导，尚未完成正式核验"}}
  if(path[2]==="output"){const output=list(state,"outputs").find(o=>o.governance_run_id===run.id);return {dataset_id:output?.dataset_id??"",contract_version:output?.contract_version??1,publication_status:output?.publication_status??"draft",governance_run_id:run.id,downstream_mount_ids:[],created_at:run.created_at}}
 }
 if(path[0]==="datasets"){if(path.length===1)return page(filtered(workspaceRows(state,"outputs",workspace).map(publicOutput),query),query);return publicOutput(find(state,"outputs",path[1]))}
 if(path[0]==="operations")return {run_states:counts(workspaceRows(state,"runs",workspace),"state"),definitions:counts(latest(state),"kind"),open_issues:list(state,"quality-issues").filter(r=>r.state!=="closed").length,overdue_issues:0,recent_runs:workspaceRows(state,"runs",workspace).slice(0,10),observed_at:now()};
 if(path[0]==="dashboard")return dashboard(state,workspace,query);
 if(path[0]==="quality-trends")return {items:qualityDays(state,workspace,query),timezone:query.get("timezone")??"Asia/Shanghai",unit:"行次"};
 const editableCollections=["schedules","monitors","alert-rules","retention-policies","quality-issues","releases","scenarios"];
 const readCollections=["standard-coverage","alerts","issue-notices","lineage","lifecycle","migrations","model-deployments","scenario-instances"];
 if(editableCollections.includes(path[0])||readCollections.includes(path[0])){
  if(path.length===1){if(method==="GET"){const data=workspaceRows(state,path[0],workspace);return ["alert-rules","retention-policies","migrations"].includes(path[0])?{items:data,limit:20}:page(data,query)}
   if(!editableCollections.includes(path[0]))invalid("此功能需要正式治理环境办理");if(path[0]==="releases")invalid("体验定义可直接在空间内发布；真实独立审核须在正式环境办理");if(path[0]==="schedules")invalid("体验空间支持手动运行，不创建后台定时任务");if(text(input.webhook))invalid("体验空间不请求外部回调地址");const row=record({...input,id:key(),workspace_id:workspace,version:1,state:"draft",created_at:now()});put(state,path[0],row);return row}
  const row=find(state,path[0],path[1]);if(method==="GET"&&path.length===2)return row;
  if(path[2]==="run")invalid("此操作需要正式治理环境执行，体验空间可在数据流与轻量治理中运行内置步骤");
  if(path[2]==="observations")return {...page([],query),monitor:row};
  if(method!=="GET"&&path.length===2){if(input.expected_version!==undefined&&input.expected_version!==row.version)stateError("配置已有更新，请刷新");if(text(input.webhook))invalid("体验空间不请求外部回调地址");Object.assign(row,input,{version:numeric(row.version)+1});return row}
 }
 if(pathname.startsWith(light))return lightweight(state,pathname.slice(light.length).split("/"),query,method,input,user);
 if(/^\/api\/v1\/datasets\//.test(pathname)){const parts=pathname.split("/"),output=find(state,"outputs",parts[4]);if(parts[5]==="lineage")throw new ExperienceError(409,"EXPERIENCE_EVIDENCE_UNAVAILABLE","浏览器体验保留输入与处理步骤，不生成正式服务端血缘证据");return {status:"not_checked",checks:[],message:"体验运行不生成正式服务端核验证据，请在正式环境核验后办理发布"}}
 throw new ExperienceError(409,"EXPERIENCE_OPERATION_UNAVAILABLE","这项操作需要正式治理环境；体验空间可编辑定义、轻量处理，并手动运行内置清洗、关联和质量检查");
}
function publicOutput(output:ObjectValue){const {rows:sampleRows,...metadata}=output;return metadata}
function counts(items:ObjectValue[],field:string){return [...new Set(items.map(row=>text(row[field])))].map(value=>({[field]:value,count:items.filter(row=>row[field]===value).length}))}
function dashboard(state:ExperienceState,workspace:string,query:URLSearchParams){const runs=periodRuns(state,workspace,query),definitions=latest(state).filter(r=>!r.workspace_id||r.workspace_id===workspace);const days=Math.min(90,Math.max(1,Number(query.get("days"))||30));const date=now();return {days,timezone:query.get("timezone")??"Asia/Shanghai",scope:"visible",started_at:new Date(Date.now()-(days-1)*86400000).toISOString(),observed_at:date,definitions:definitions.map(r=>({kind:r.kind,state:r.state,count:1})),resources:counts(list(state,"sources").filter(r=>Array.isArray(r.workspaces)&&r.workspaces.includes(workspace)).map(r=>({...r,kind:r.resource_type})),"kind"),observations:list(state,"sources").filter(r=>Array.isArray(r.workspaces)&&r.workspaces.includes(workspace)).length,rules:[],outputs:workspaceRows(state,"outputs",workspace).map(o=>({state:o.publication_status,quality:o.quality_status,completeness:o.completeness_status,count:1})),runs:runs.map(r=>({kind:r.kind,state:r.state,count:1})),run_trend:[...new Set(runs.map(r=>dayKey(text(r.created_at),query.get("timezone")??"Asia/Shanghai")))].map(day=>({day,succeeded:runs.filter(r=>dayKey(text(r.created_at),query.get("timezone")??"Asia/Shanghai")===day&&r.state==="succeeded").length,failed:runs.filter(r=>dayKey(text(r.created_at),query.get("timezone")??"Asia/Shanghai")===day&&r.state==="failed").length,other:runs.filter(r=>dayKey(text(r.created_at),query.get("timezone")??"Asia/Shanghai")===day&&r.state!=="succeeded"&&r.state!=="failed").length})),quality_trend:qualityDays(state,workspace,query),schedules:[],issues:[],overdue_issues:0,alerts:[],alert_rules:list(state,"alert-rules").length,retention_policies:list(state,"retention-policies").length,purged_snapshots:0,lifecycle_outputs:0,migrations:[],model_deployments:[],lineage_runs:0}}
function lightweight(state:ExperienceState,path:string[],query:URLSearchParams,method:string,input:ObjectValue,user:Principal):unknown{
 if(path[0]==="files")return page(list(state,"files"),query);
 if(path[0]==="units"){
  if(path.length===1){if(method==="GET")return page(list(state,"units"),query);const design=graph(input.graph);if(!text(input.name).trim())invalid("请输入处理单元名称");submitting(method);const row=record({id:key(),name:text(input.name),graph:input.graph,version:1,files:unitFiles(state,design),created_at:now(),updated_at:now()});put(state,"units",row);return row}
  const row=find(state,"units",path[1]);if(path[2]==="runs")return page(list(state,"runs").filter(run=>object(run.definition)&&object(run.definition.lightweight)&&run.definition.lightweight.unit_id===row.id),query);
  if(path[2]==="run"){submitting(method);if(input.expected_version!==row.version)stateError("处理单元已经更新，请重新打开");const result=execute(state,record({name:row.name,description:"浏览器内体验处理",owner_id:user.user_id,steward_id:user.user_id,dataflow:row.graph}),{id:text(row.id),version:numeric(row.version)},SAMPLE_SPACE,text(row.id));rememberRun(state,result);return result.run}
  if(method==="GET")return row;if(input.expected_version!==row.version)stateError("处理单元已经更新，请重新打开");const design=graph(input.graph);if(!text(input.name).trim())invalid("请输入处理单元名称");Object.assign(row,{name:text(input.name),graph:input.graph,files:unitFiles(state,design),version:numeric(row.version)+1,updated_at:now()});return row;
 }
 if(path[0]==="runs"){const run=find(state,"runs",path[1]);if(path.length===2)return run;if(path[2]==="records"){const output=list(state,"outputs").find(row=>row.governance_run_id===run.id);return page(output?rows(output.rows).map(record):[],query)}if(path[2]==="rejections"||path[2]==="events"){const saved=find(state,path[2],text(run.id));return page(Array.isArray(saved.items)?saved.items.filter(object).map(record):[],query)}}
 throw new ExperienceError(409,"EXPERIENCE_OPERATION_UNAVAILABLE","体验空间不执行此操作");
}
function unitFiles(state:ExperienceState,design:ReturnType<typeof graph>){const ids=design.nodes.flatMap(node=>node.source?[node.source.resource.resource_id]:[]);if(new Set(ids).size>8)invalid("每个体验处理单元最多关联8份文件");return list(state,"files").filter(file=>ids.includes(text(file.file_id)))}
async function upload(state:ExperienceState,body:FormData){const value=body.get("file");if(!(value instanceof Blob)||typeof value.name!=="string"||!value.name.toLowerCase().endsWith(".csv"))invalid("请选择CSV文件");if(value.size>1024*1024)invalid("浏览器体验每份CSV最多1 MiB");const raw=await value.text(),parsed=parseCSV(raw);const names=parsed.shift()??[];if(!names.length||names.length>50||new Set(names).size!==names.length||names.some(name=>!name||["__proto__","constructor","prototype"].includes(name)))invalid("CSV需有1至50个不重复的字段名");if(parsed.length>1000||parsed.some(row=>row.length!==names.length))invalid("CSV最多1000行，且每行字段数必须与表头一致");const id=key(),fields=names.map(name=>({name,business_name:name,logical_type:"string",nullable:true,primary_key:false,sensitive:false,precision:null,scale:null})),data=parsed.map(row=>Object.fromEntries(names.map((name,index)=>[name,row[index]])));const file=record({file_id:id,name:value.name,size:value.size,rows:data.length,fields,field_labels:Object.fromEntries(names.map(name=>[name,name])),version:1});put(state,"files",file);put(state,"sources",record({id,object_id:id,source_id:"",qualified_name:value.name,display_name:value.name,fields,field_labels:file.field_labels,schema_version:1,resource_type:"file_resource",adapter:"browser_example",rows:data,workspaces:[SAMPLE_SPACE]}));return file}
export function parseCSV(raw:string){const text=raw.replace(/^\uFEFF/,""),result:string[][]=[];let row:string[]=[],cell="",quoted=false,closed=false;for(let i=0;i<text.length;i++){const c=text[i];if(quoted){if(c==='"'&&text[i+1]==='"'){cell+='"';i++}else if(c==='"'){quoted=false;closed=true}else cell+=c}else if(c==='"'){if(cell||closed)invalid("CSV引号位置不正确");quoted=true}else if(c===','){row.push(cell);cell="";closed=false}else if(c==='\n'||c==='\r'){if(c==='\r'&&text[i+1]==='\n')i++;row.push(cell);result.push(row);row=[];cell="";closed=false;if(result.length>1001)invalid("CSV最多1000行数据")}else{if(closed)invalid("CSV结束引号后应为分隔符");cell+=c}}if(quoted)invalid("CSV有未闭合的引号");if(row.length||cell||closed){row.push(cell);result.push(row)}return result}
export async function governanceExperience(path:string,options:RequestInit,user:Principal):Promise<unknown>{
 if(!user.public_test)throw new ExperienceError(403,"EXPERIENCE_ONLY","仅体验身份使用独立空间");
 const operation=async()=>{options.signal?.throwIfAborted();const state=read(user,()=>seed(user));const method=(options.method??"GET").toUpperCase();let input:ObjectValue={};
  if(options.body instanceof FormData){if(path.split("?")[0]!==light+"files"||method!=="POST")invalid("请从轻量治理的CSV入口导入文件");const result=await upload(state,options.body);options.signal?.throwIfAborted();persist(user,state);return result}
  if(options.body){if(typeof options.body!=="string")invalid("体验请求格式不正确");if(options.body.length>512*1024)invalid("体验配置过大，请精简后保存");let raw:unknown;try{raw=JSON.parse(options.body)}catch{return invalid("体验请求不是有效的JSON")};input=record(raw)}
  const result=outcome(state,new URL(path,"https://experience.invalid"),method,input,user);persist(user,state);return structuredClone(result);
 };
 return typeof navigator!=="undefined"&&navigator.locks?navigator.locks.request(storageKey(user),operation):operation();
}
function profile(state:ExperienceState,input:ObjectValue,user:Principal,workspace:string){
 if(list(state,"runs").length>=30)invalid("体验空间最多保留30次运行，请先重置体验空间");
 if(!object(input.resource))invalid("请选择体验样本");const source=find(state,"sources",text(input.resource.resource_id));
 const data=rows(source.rows),mode=input.mode==="full"?"full":"sample",selected=Array.isArray(input.fields)?input.fields.map(value=>text(value)):[];
 const sourceFields=Array.isArray(source.fields)?source.fields:[];if(!selected.length||selected.some(name=>!sourceFields.some(f=>object(f)&&f.name===name)))invalid("请选择样本中的字段");
 const observed=mode==="sample"?data.slice(0,Math.min(1000,Math.max(1,numeric(input.sample_rows,1000)))):data;
 const fields=selected.map(name=>{const values=observed.map(row=>row[name]),nonempty=values.filter(value=>value!==null&&value!==undefined&&String(value).trim()!==""),counts=new Map<string,number>();for(const value of nonempty)counts.set(String(value),(counts.get(String(value))??0)+1);const sorted=[...nonempty].sort((a,b)=>typeof a==="number"&&typeof b==="number"?a-b:String(a).localeCompare(String(b)));const spec=Array.isArray(source.fields)?source.fields.find(f=>object(f)&&f.name===name):null;return {name,logical_type:object(spec)?text(spec.logical_type):"string",null_count:values.length-nonempty.length,distinct_count:counts.size,duplicate_count:nonempty.length-counts.size,min:sorted.length?String(sorted[0]):null,max:sorted.length?String(sorted.at(-1)):null,redacted:false,values_truncated:false,frequencies:[...counts].sort((a,b)=>b[1]-a[1]).slice(0,10).map(([value,count])=>({value,count,truncated:false}))}});
 const created=now(),run=record({id:key(),workspace_id:workspace,kind:"profile",state:"succeeded",processed:observed.length,error_code:"",created_at:created,started_at:created,finished_at:now(),retry_of:null,cancel_requested:false,definition:{mode,input:{resource:input.resource}},result:{mode,coverage:mode,sampling:"当前浏览器体验样本",rows:observed.length,total_rows:data.length,fields,anomalies:[],snapshot_kind:"per_source_snapshot",observed_at:created}});put(state,"runs",run);put(state,"events",record({id:run.id,items:[{id:1,run_id:run.id,node_id:"",code:"PROFILE_COMPLETED",processed:observed.length,created_at:created}]}));return run;
}
function qualitySummary(state:ExperienceState,workspace:string){const totals=qualityDays(state,workspace).reduce((sum,day)=>({checked:sum.checked+day.checked,passed:sum.passed+day.passed}),{checked:0,passed:0});return {...totals,pass_rate:totals.checked?totals.passed/totals.checked:null,scope:"当前浏览器体验记录"}}
function dayKey(value:string,zone:string){try{return new Intl.DateTimeFormat("sv-SE",{timeZone:zone,year:"numeric",month:"2-digit",day:"2-digit"}).format(new Date(value))}catch{return invalid("统计时区或日期不正确")}}
function periodRuns(state:ExperienceState,workspace:string,params:URLSearchParams){const zone=params.get("timezone")??"Asia/Shanghai",days=Math.min(366,Math.max(1,Number(params.get("days"))||30)),today=dayKey(now(),zone),first=new Date(Date.parse(today+"T00:00:00Z")-(days-1)*86400000).toISOString().slice(0,10);return workspaceRows(state,"runs",workspace).filter(run=>{const day=dayKey(text(run.created_at),zone);return day>=first&&day<=today})}
function qualityDays(state:ExperienceState,workspace:string,params?:URLSearchParams){
 const totals=new Map<string,{day:string;checked:number;passed:number;rejected:number;runs:number}>();
 for(const run of params?periodRuns(state,workspace,params):workspaceRows(state,"runs",workspace)){
  if(params?.get("definition_id")&&(!object(run.definition)||!object(run.definition.flow)||run.definition.flow.id!==params.get("definition_id")))continue;
  if(run.kind!=="dataflow"||!object(run.result)||!Array.isArray(run.result.nodes)||!object(run.definition)||!Array.isArray(run.definition.pinned))continue;
  const pinned=run.definition.pinned.find(value=>object(value)&&value.kind==="dataflow"),definition=object(pinned)&&object(pinned.definition)?pinned.definition:null;
  const design=definition&&object(definition.dataflow)&&Array.isArray(definition.dataflow.nodes)?definition.dataflow.nodes:[];
  const qualityIDs=design.flatMap(node=>object(node)&&node.kind==="quality"?[text(node.id)]:[]);
  const nodes=run.result.nodes.filter(node=>object(node)&&qualityIDs.includes(text(node.node_id)));
  if(!nodes.length)continue;const day=dayKey(text(run.created_at),params?.get("timezone")??"Asia/Shanghai"),entry=totals.get(day)??{day,checked:0,passed:0,rejected:0,runs:0};entry.runs++;
  for(const node of nodes)if(object(node)){entry.checked+=numeric(node.input_count);entry.passed+=numeric(node.output_count);entry.rejected+=numeric(node.rejected_count)}totals.set(day,entry);
 }
 return [...totals.values()].sort((a,b)=>a.day.localeCompare(b.day));
}
export function experienceCSV(user:Principal,runID:string){
 if(!user.public_test)throw new ExperienceError(403,"EXPERIENCE_ONLY","体验输出仅在当前浏览器提供");
 const state=read(user,()=>seed(user)),output=list(state,"outputs").find(output=>output.governance_run_id===runID);if(!output)throw new ExperienceError(404,"EXPERIENCE_NOT_FOUND","本浏览器没有此运行结果");
 const data=rows(output.rows),definition=contents(output),fields=Array.isArray(definition.fields)?definition.fields.flatMap(field=>object(field)?[text(field.name)]:[]):Object.keys(data[0]??{});
 const escape=(value:unknown)=>{let cell=value===null||value===undefined?"":String(value);if(/^[=+@\t\r]/.test(cell)||/^-/.test(cell)&&!/^-[0-9]+(\.[0-9]+)?$/.test(cell))cell="'"+cell;return '"'+cell.replaceAll('"','""')+'"'};
 return new Blob(["\uFEFF"+[fields,...data.map(row=>fields.map(name=>row[name]))].map(row=>row.map(escape).join(",")).join("\r\n")],{type:"text/csv;charset=utf-8"});
}
function workflow(state:ExperienceState,definitionRow:ObjectValue){
 if(definitionRow.state!=="published")stateError("请先在体验空间发布工作流版本");const config=contents(definitionRow).workflow;
 if(!object(config)||!Array.isArray(config.tasks)||!config.tasks.length||config.tasks.length>10)invalid("体验工作流需要1至10个任务");
 if(list(state,"runs").length+config.tasks.length+1>30)invalid("此工作流将超过30次体验运行的保留上限，请先重置空间");
 const pending=config.tasks.map(task=>record(task)),ids=pending.map(task=>text(task.id));if(new Set(ids).size!==ids.length||ids.some(id=>!id))invalid("工作流任务编号不能为空或重复");
 const tasks:ObjectValue[]=[],results:ObjectValue={},id=key(),created=now();
 while(pending.length){let progress=false;for(let index=pending.length-1;index>=0;index--){const task=pending[index],dependencies=Array.isArray(task.depends_on)?task.depends_on.map(value=>text(value)):[];if(dependencies.some(dep=>!ids.includes(dep)))invalid("工作流引用了不存在的前置任务");if(dependencies.some(dep=>!tasks.some(done=>done.task_id===dep)))continue;
   if(task.kind&&task.kind!=="dataflow")invalid("体验工作流仅运行数据流任务，不执行发布或外部审批任务");if(object(task.inputs)&&Object.keys(task.inputs).length)invalid("体验工作流暂不接续运行产物，请在单个数据流中完成关联");
   const previous=tasks.filter(done=>dependencies.includes(text(done.task_id))),condition=text(task.condition,"all_success"),allowed=condition==="all_done"||condition==="all_failure"&&previous.length>0&&previous.every(done=>done.state==="failed")||condition==="all_success"&&previous.every(done=>done.state==="succeeded");
   if(!["all_success","all_failure","all_done"].includes(condition))invalid("工作流触发条件不正确");
   let done:ObjectValue=record({task_id:task.id,child_run_id:null,state:allowed?"succeeded":"skipped",error_code:""});
   if(allowed){if(!object(task.dataflow))invalid("请选择固定数据流版本");try{const flow=definition(state,text(task.dataflow.id),numeric(task.dataflow.version));if(flow.state!=="published"||flow.kind!=="dataflow")stateError("子数据流版本尚未发布或不可执行");const result=execute(state,contents(flow),{id:text(flow.id),version:numeric(flow.version)},text(definitionRow.workspace_id));rememberRun(state,result);done.child_run_id=result.run.id;done.result=record({dataset_id:result.output.dataset_id});results[text(task.id)]=result.run.id}catch(error){if(!(error instanceof ExperienceError))throw error;done.state="failed";done.error_code=error.code;done.message=error.message}}
   tasks.push(done);pending.splice(index,1);progress=true;
  }if(!progress)invalid("工作流存在循环依赖");
 }
 const failed=tasks.some(task=>task.state==="failed"),run=record({id,workspace_id:definitionRow.workspace_id,kind:"workflow",state:failed?"failed":"succeeded",processed:tasks.filter(task=>task.state==="succeeded").length,error_code:failed?"WORKFLOW_TASK_FAILED":"",created_at:created,started_at:created,finished_at:now(),retry_of:null,cancel_requested:false,definition:{workflow:{id:definitionRow.id,version:definitionRow.version},graph:config},result:results});put(state,"runs",run);put(state,"workflow-tasks",record({id,items:tasks}));put(state,"events",record({id,items:[]}));return run;
}
