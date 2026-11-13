import {usePagination} from "../hooks/usePagination";
import {GovernanceDateTimePicker,GovernanceTimePicker} from "./GovernanceDateInputs";
import {usePath} from "../app/navigation";
import {GovernanceGuide} from "./GovernanceGuide";
import {GovernanceRunParameters,governanceParameterDefaults,type GovernanceParameterValues} from "./GovernanceRunParameters";
import {GovernanceDialog} from "./GovernanceDialog";
import {Alert,App,Button,Descriptions,Form,Input,InputNumber,Select,Space,Table} from "antd";
import {useState} from "react";
import type {Page} from "../api/types";
import type {GovernanceRecord,GovernanceRef} from "../api/governance";
import {governanceRunCodes} from "../api/governance";
import type {GovernanceSchedule} from "../api/governanceOperations";
import {errorMessage} from "../api/client";
import {useAction} from "../hooks/useAction";
import {useQuery} from "../hooks/useQuery";
import {useSession} from "../stores/session";
import {Failure,Stamp,Status,Title} from "./common";
import {GovernanceReferenceSelect} from "./GovernanceReferenceSelect";
import {scheduleLocalInput,scheduleWallTime,scheduleRule,type ScheduleTiming} from "./governanceScheduleTime";
type ScheduleForm={target_kind:"workflow"|"dataflow";workflow:GovernanceRef;timing:ScheduleTiming;clock:string;weekday:number;cron:string;timezone:string;interval_count:number;interval_unit:number;missed_policy:"skip"|"latest";next_run_at:string;parameters:GovernanceParameterValues};
const defaultZone=Intl.DateTimeFormat().resolvedOptions().timeZone||"Asia/Shanghai";
const zones=[...new Set([defaultZone,"Asia/Shanghai","UTC",...Intl.supportedValuesOf("timeZone")])];
function displayTime(at:string,zone:string){try{return new Intl.DateTimeFormat("zh-CN",{timeZone:zone,dateStyle:"short",timeStyle:"medium"}).format(new Date(at))}catch{return "时间或时区无效"}}
export function GovernanceSchedules({target:fixedTarget}:{target?:GovernanceRecord}={}){
 const path=usePath(),query=new URLSearchParams(path.split("?")[1]),queryKind=query.get("target_kind"),queryID=query.get("target_id"),queryVersion=query.get("target_version");
 const linkedTarget=useQuery<GovernanceRecord>(!fixedTarget&&queryID&&queryVersion&&(queryKind==="workflow"||queryKind==="dataflow")?`/api/v1/governance/${queryKind==="workflow"?"workflows":"dataflows"}/${encodeURIComponent(queryID)}/versions/${encodeURIComponent(queryVersion)}`:null);
 const target=fixedTarget??linkedTarget.data;
 const [form]=Form.useForm<ScheduleForm>(),values=Form.useWatch([],{form,preserve:true}) as Partial<ScheduleForm>|undefined;
 const [selectedSchedule,setSelectedSchedule]=useState<GovernanceSchedule|null>(null),[editingSchedule,setEditingSchedule]=useState<GovernanceSchedule|null>(null),[editing,setEditing]=useState(false),[triggerPage,setTriggerPage,triggerPagePagination]=usePagination(),[page,setPage,pagePagination]=usePagination();
 const {message}=App.useApp(),action=useAction(),user=useSession(s=>s.user)!;
 const targetKind=values?.target_kind??(target?.kind==="dataflow"?"dataflow":"workflow");
 const ref=values?.workflow??(!editingSchedule&&target?{id:target.id,version:target.version}:undefined);
 const scheduleDefaults=useQuery<{defaults:GovernanceParameterValues}>(editing&&targetKind==="workflow"&&ref?.id?`/api/v1/governance/workflows/${ref.id}/versions/${ref.version}/parameters`:null);
 const flowRecord=useQuery<GovernanceRecord>(editing&&targetKind==="dataflow"&&ref?.id?`/api/v1/governance/dataflows/${ref.id}/versions/${ref.version}`:null);
 const defaults=targetKind==="dataflow"?governanceParameterDefaults(flowRecord.data?.definition.dataflow??target?.definition.dataflow??{nodes:[]}):scheduleDefaults.data?.defaults??{};
 const parameters={...defaults,...Object.fromEntries(Object.entries(values?.parameters??{}).filter(([key])=>key in defaults))};
 const timing=values?.timing??"hourly",timezone=values?.timezone??defaultZone;
 const rule=scheduleRule({...values,timing});
 let firstRun="",timeError="";
 if(editing&&values?.next_run_at){try{firstRun=scheduleWallTime(values.next_run_at,timezone);if(Date.parse(firstRun)<=Date.now())timeError="请选择未来的首次触发时间"}catch(e){timeError=errorMessage(e)}}
 const previewPath=editing&&firstRun&&Number.isFinite(rule.interval_seconds)?`/api/v1/governance/schedules/preview?timezone=${encodeURIComponent(timezone)}&${rule.cron?`cron=${encodeURIComponent(rule.cron)}`:`interval_seconds=${rule.interval_seconds}`}&after=${encodeURIComponent(new Date(Date.parse(firstRun)-(rule.cron?1:rule.interval_seconds*1000)).toISOString())}`:null;
 const preview=useQuery<{items:string[];timezone:string;dst_policy:string}>(previewPath);
 const triggers=useQuery<Page<{scheduled_at:string;run_id:string|null;code:string}>>(selectedSchedule?`/api/v1/governance/schedules/${selectedSchedule.id}/triggers?page=${triggerPage}&page_size=${triggerPagePagination.pageSize}`:null);
 const list=useQuery<Page<GovernanceSchedule>>(`/api/v1/governance/schedules?page=${page}&page_size=${pagePagination.pageSize}${target?`&target_kind=${target.kind}&target_id=${encodeURIComponent(target.id)}`:""}`);
 function openEditor(row?:GovernanceSchedule){
  setSelectedSchedule(null);setEditingSchedule(row??null);form.resetFields();
  if(row){const unit=row.interval_seconds%86400===0?86400:row.interval_seconds%3600===0?3600:row.interval_seconds%60===0?60:1;form.setFieldsValue({target_kind:row.dataflow?"dataflow":"workflow",workflow:row.dataflow??row.workflow,timing:row.cron?"calendar":"interval",cron:row.cron??"",timezone:row.timezone,interval_count:row.interval_seconds/unit,interval_unit:unit,missed_policy:row.missed_policy,next_run_at:scheduleLocalInput(row.next_run_at,row.timezone),parameters:{...row.parameters}})}
  else form.setFieldsValue({target_kind:target?.kind==="dataflow"?"dataflow":"workflow",workflow:target?{id:target.id,version:target.version}:undefined});
  setEditing(true);
 }
 async function save(v:ScheduleForm){try{
  if(!preview.data?.items||preview.data.items.length!==5||preview.loading||scheduleDefaults.loading||flowRecord.loading||scheduleDefaults.error||flowRecord.error||timeError)throw new Error("请先核对有效的未来五次触发时间");
  await action.run(editingSchedule?`/api/v1/governance/schedules/${editingSchedule.id}/update`:"/api/v1/governance/schedules",{...(editingSchedule?{base_version:editingSchedule.version}:{}),[v.target_kind]:v.workflow,parameters,timezone:v.timezone,...scheduleRule(v),missed_policy:v.missed_policy,next_run_at:scheduleWallTime(v.next_run_at,v.timezone),concurrency_policy:"forbid"});
  setEditing(false);setEditingSchedule(null);list.reload();
 }catch(e){message.error(errorMessage(e))}}
 async function toggle(row:GovernanceSchedule){try{setSelectedSchedule(await action.run<GovernanceSchedule>(`/api/v1/governance/schedules/${row.id}/${row.state==="active"?"pause":"resume"}`,{base_version:row.version}));list.reload()}catch(e){message.error(errorMessage(e))}}
 return <>{Boolean(linkedTarget.error)&&<Failure error={linkedTarget.error}/>}<Title title={target?"定时运行":"调度计划"} description="按时间自动执行批量数据流或工作流；流式处理在持续运行中管理。" actions={<Space size={16}><Button onClick={list.reload}>刷新</Button><Button disabled={action.readOnly||Boolean(target&&target.state!=="published")} type="primary" onClick={()=>openEditor()}>新建调度</Button></Space>}/><GovernanceGuide topic="schedule"/>
 {editing&&<GovernanceDialog guide="schedule" header={<Title title={editingSchedule?"调整计划":"新建调度"} description="核对版本、参数与未来五次触发时间后保存。"/>} actions={<Button disabled={action.readOnly||preview.loading||scheduleDefaults.loading||flowRecord.loading||Boolean(scheduleDefaults.error||flowRecord.error)||preview.data?.items?.length!==5||Boolean(timeError)} type="primary" loading={action.busy} onClick={()=>form.submit()}>{editingSchedule?"保存计划调整":"保存并启用调度"}</Button>} onClose={()=>{setEditing(false);setEditingSchedule(null)}} busy={action.busy} width={800}>
 <div className="governance-editor-panel"><Form form={form} layout="vertical" initialValues={{target_kind:"workflow",timing:"hourly",clock:"09:00",weekday:1,cron:"0 9 * * 1-5",timezone:defaultZone,interval_count:1,interval_unit:3600,missed_policy:"skip",parameters:{}}} onFinish={()=>save(form.getFieldsValue(true))}>
 {editingSchedule?<><p>执行对象：{(editingSchedule.dataflow??editingSchedule.workflow).id}（保留原计划与触发历史）</p><Form.Item label="目标已发布版本" htmlFor="schedule-target-version" required><InputNumber id="schedule-target-version" min={1} precision={0} value={ref?.version} placeholder="请输入已发布版本号" onChange={version=>form.setFieldValue("workflow",{id:ref?.id,version})}/></Form.Item></>:<><Form.Item name="target_kind" label="执行对象" hidden={Boolean(target)}><Select placeholder="请选择执行对象" options={[{value:"workflow",label:"工作流"},{value:"dataflow",label:"批量数据流"}]} onChange={()=>{form.setFieldValue("workflow",undefined);form.setFieldValue("parameters",{})}}/></Form.Item>{target?<p>{target.definition.name} · v{target.version}</p>:<Form.Item name="workflow" label={targetKind==="dataflow"?"数据流版本":"工作流版本"} rules={[{required:true}]}><GovernanceReferenceSelect kind={targetKind} executionMode={targetKind==="dataflow"?"batch":undefined} departmentId={user.department_id} onChange={()=>form.setFieldValue("parameters",{})}/></Form.Item>}</>}
 <Form.Item name="parameters" noStyle><GovernanceRunParameters defaults={defaults} value={values?.parameters??{}} onChange={value=>form.setFieldValue("parameters",value)}/></Form.Item>
 {Boolean(scheduleDefaults.error||flowRecord.error)&&<Failure error={scheduleDefaults.error||flowRecord.error}/>}
 <Form.Item name="timezone" label="执行时区" rules={[{required:true}]}><Select showSearch placeholder="请选择执行时区，可搜索 IANA 名称" options={zones.map(value=>({value,label:value}))} onChange={zone=>{if(firstRun)form.setFieldValue("next_run_at",scheduleLocalInput(firstRun,zone))}}/></Form.Item>
 <Form.Item name="next_run_at" label="首次触发（所选时区）" rules={[{required:true}]}><GovernanceDateTimePicker placeholder="请选择首次触发时间"/></Form.Item>{timeError&&<Alert type="error" title={timeError}/>}
 <Form.Item name="timing" label="执行周期"><Select placeholder="请选择执行周期" options={[{value:"hourly",label:"每小时（整点）"},{value:"daily",label:"每天"},{value:"workday",label:"工作日（周一至周五）"},{value:"weekly",label:"每周"},{value:"interval",label:"自定义间隔"},{value:"calendar",label:"高级日历规则（cron）"}]}/></Form.Item>
 {["daily","workday","weekly"].includes(timing)&&<Form.Item name="clock" label="执行时间" rules={[{required:true}]}><GovernanceTimePicker placeholder="请选择执行时间"/></Form.Item>}
 {timing==="weekly"&&<Form.Item name="weekday" label="星期"><Select placeholder="请选择星期" options={["周日","周一","周二","周三","周四","周五","周六"].map((label,value)=>({value,label}))}/></Form.Item>}
 {timing==="interval"&&<Space size={16} wrap><Form.Item name="interval_count" label="间隔数值" rules={[{required:true,type:"number",min:1}]}><InputNumber min={1} max={31536000} precision={0} placeholder="请输入间隔数值"/></Form.Item><Form.Item name="interval_unit" label="时间单位"><Select placeholder="请选择时间单位" options={[{value:60,label:"分钟"},{value:3600,label:"小时"},{value:86400,label:"天"},{value:1,label:"秒（至少60）"}]}/></Form.Item></Space>}
 {timing==="calendar"&&<><Form.Item name="cron" label="高级日历规则" rules={[{required:true,max:100}]}><Input placeholder="分 时 日 月 星期，例如 0 9 * * 1-5"/></Form.Item><Alert type="info" title="夏令时规则" description="后续日历触发跳过不存在时刻，重复时刻仅触发第一次。首次触发不接受不存在或重复的时刻。"/></>}
 <Form.Item name="missed_policy" label="错过触发策略" rules={[{required:true}]}><Select placeholder="请选择错过触发时的处理方式" options={[{value:"skip",label:"跳过错过的触发"},{value:"latest",label:"补触发最近一次"}]}/></Form.Item>
 {editingSchedule&&<Descriptions title="当前 → 调整后" column={1} items={[{key:"version",label:"版本",children:`v${(editingSchedule.dataflow??editingSchedule.workflow).version} → v${ref?.version??"—"}`},{key:"rule",label:"规则",children:`${editingSchedule.cron||`${editingSchedule.interval_seconds} 秒`} → ${rule.cron||`${rule.interval_seconds} 秒`}`},{key:"zone",label:"时区",children:`${editingSchedule.timezone} → ${timezone}`},{key:"first",label:"下次触发起点",children:`${displayTime(editingSchedule.next_run_at,editingSchedule.timezone)} → ${firstRun?displayTime(firstRun,timezone):"待填写"}`},{key:"policy",label:"错过策略",children:`${editingSchedule.missed_policy==="skip"?"跳过":"补最近一次"} → ${values?.missed_policy==="skip"?"跳过":"补最近一次"}`},{key:"params",label:"参数",children:`${JSON.stringify(editingSchedule.parameters??{})} → ${JSON.stringify(parameters)}`},{key:"state",label:"保存后状态",children:<Status value={editingSchedule.state}/>} ]}/>}
 <p>未来五次触发 · {timezone}</p>{preview.error?<Failure error={preview.error}/>:preview.data?.items?.map(at=><div key={at} className="helper-text">{displayTime(at,timezone)}</div>)??<p className="helper-text">填写有效时间后自动预览，核对后方可保存。</p>}
 </Form></div></GovernanceDialog>}
 {list.error?<Failure error={list.error}/>:<Table rowKey="id" dataSource={list.data?.items} scroll={{x:900}} pagination={{...pagePagination,total:list.data?.total}} columns={[{title:"执行对象",render:(_,r)=><Button type="link" onClick={()=>{setEditing(false);setSelectedSchedule(r);setTriggerPage(1)}}>{r.dataflow?"批量数据流":"工作流"} · {(r.dataflow??r.workflow).id} · v{(r.dataflow??r.workflow).version}</Button>},{title:"状态",render:(_,r)=><Status value={r.state}/>},{title:"调度规则",render:(_,r)=>r.cron||`${r.interval_seconds} 秒`},{title:"下一次触发",render:(_,r)=><>{displayTime(r.next_run_at,r.timezone)}<div className="helper-text">{r.timezone}</div></>},{title:"最近诊断",render:(_,r)=>governanceRunCodes[r.last_error_code]??r.last_error_code??"—"},{title:"操作",render:(_,r)=><Button disabled={action.busy} onClick={()=>{setEditing(false);setSelectedSchedule(r);setTriggerPage(1)}}>管理调度</Button>}]}/>}
 {selectedSchedule&&<GovernanceDialog guide="schedule" header={<Title title="调度管理与触发记录" description="核对规则、参数和触发历史，可调整原计划。"/>} actions={<Space size={16} wrap><Button disabled={action.readOnly||action.busy} onClick={()=>openEditor(selectedSchedule)}>调整计划</Button><Button disabled={action.readOnly} loading={action.busy} onClick={()=>toggle(selectedSchedule)}>{selectedSchedule.state==="active"?"暂停调度":"启用调度"}</Button></Space>} busy={action.busy} onClose={()=>setSelectedSchedule(null)}><Descriptions items={[{key:"state",label:"状态",children:<Status value={selectedSchedule.state}/>},{key:"rule",label:"调度规则",children:selectedSchedule.cron||`${selectedSchedule.interval_seconds} 秒`},{key:"zone",label:"时区",children:selectedSchedule.timezone},{key:"parameters",label:"固定运行参数",children:Object.entries(selectedSchedule.parameters??{}).map(([key,value])=>`${key} = ${String(value)}`).join("；")||"使用版本默认值"}]}/>{triggers.error?<Failure error={triggers.error}/>:<Table rowKey="scheduled_at" dataSource={triggers.data?.items} pagination={{...triggerPagePagination,total:triggers.data?.total}} columns={[{title:"计划时刻",render:(_,r)=><Stamp value={r.scheduled_at}/>},{title:"运行",dataIndex:"run_id"},{title:"结果",render:(_,r)=>governanceRunCodes[r.code]??r.code}]}/>}</GovernanceDialog>}</>
}
