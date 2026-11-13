import {applicationURL} from "../app/basePath";
import {usePagination} from "../hooks/usePagination";
import {DialogActions} from "../components/PlatformDialog";
import { Alert, App, Button, Form, Input, Select, Space, Table, Tag, Typography } from "antd";
import { useEffect, useState } from "react";
import { errorMessage, query } from "../api/client";
import { useAction } from "../hooks/useAction";
import { useQuery } from "../hooks/useQuery";
import { Failure, Stamp } from "../components/common";
import { useSession } from "../stores/session";
import type { Page } from "../api/types";
import type { Run } from "./ETL";

type Event = {id:number;run_id:string;level:string;stage:string;code:string;message:string;actor_id:string;trace_id:string;created_at:string};
type OperationAlert = {id:string;run_id:string;code:string;severity:string;message:string;state:string;handled_by:string;reason:string;created_at:string};
const alertStates:Record<string,string>={open:"待处理",acknowledged:"处理中",resolved:"已关闭"};
const stages=[{value:"",label:"全部阶段"},{value:"queued",label:"等待执行"},{value:"extracting",label:"提取与转换"},{value:"loading",label:"目标写入"},{value:"committing",label:"事务提交"},{value:"succeeded",label:"已完成"},{value:"failed",label:"失败"},{value:"cancelled",label:"已取消"},{value:"unknown",label:"待核实"}];

export function RunOperations({run,onChange,onCreated}:{run:Run;onChange:()=>void;onCreated:(id:string)=>void}) {
 const {message}=App.useApp();const action=useAction();const user=useSession(s=>s.user)!;
 const [operation,setOperation]=useState<string|null>(null),[reason,setReason]=useState(""),[evidence,setEvidence]=useState(""),[outcome,setOutcome]=useState("failed");
 const [page,setPage,pagePagination]=usePagination(),[level,setLevel]=useState(""),[stage,setStage]=useState("");
 const path=`/api/v1/etl-runs/${run.id}/events?`+query({page,level,stage,page_size:pagePagination.pageSize});
 const events=useQuery<Page<Event>>(path,true);
 useEffect(()=>{if(["succeeded","failed","cancelled","unknown"].includes(run.state))return;const timer=window.setInterval(events.reload,3000);return()=>window.clearInterval(timer)},[run.state,events.reload]);
 function open(kind:string){setOperation(kind);setReason("");setEvidence("");setOutcome("failed")}
 async function submit(){if(!reason.trim()||(operation==="resolve"&&!evidence.trim())){message.warning("请填写处理原因及核实依据");return}try{const next=await action.run<Run>(`/api/v1/etl-runs/${run.id}/${operation}`,{reason:reason.trim(),...(operation==="resolve"?{outcome,evidence:evidence.trim()}:{})});setOperation(null);events.reload();onChange();if(operation==="retry")onCreated(next.id);message.success(operation==="cancel"?"已请求取消，请以最终回滚状态为准":operation==="retry"?"已创建新的重试运行":operation==="reconcile"?"已转入待核实，请核对目标事务":"核实结果已保存")}catch(e){message.error(errorMessage(e))}}
 return <>
  <Space size={16} wrap>
   {["queued","extracting","loading"].includes(run.state)&&<Button danger onClick={()=>open("cancel")}>取消运行</Button>}
   {["failed","cancelled"].includes(run.state)&&<Button onClick={()=>open("retry")}>重试运行</Button>}
   {["extracting","loading","committing"].includes(run.state)&&<Button onClick={()=>open("reconcile")}>核查执行状态</Button>}
   {run.state==="unknown"&&user.roles.includes("admin")&&<Button onClick={()=>open("resolve")}>核实结果</Button>}
  </Space>
  {operation&&<section aria-label="运行处理">
   <Typography.Title level={5}>{operation==="cancel"?"取消运行":operation==="retry"?"重试运行":operation==="reconcile"?"核查执行状态":"核实运行结果"}</Typography.Title>
   <Alert type="warning" showIcon title={operation==="retry"?"重试将创建新运行并重新读取当前来源，旧运行记录保留。":operation==="resolve"?"请先核对目标数据库事务。确认已提交后不可再重试；确认未提交或已回滚后才允许重试。":operation==="reconcile"?"仅在执行器已退出时将运行转为待核实；仍在执行时会拒绝修改。转入待核实后，由管理员核对目标事务结果。":"仅提交前允许取消，完成回滚后才会显示已取消。"}/>
   <Form layout="vertical" style={{marginTop:24}}>
    {operation==="resolve"&&<><Form.Item label="目标事务结果" required><Select value={outcome} options={[{value:"failed",label:"确认未提交或已回滚"},{value:"succeeded",label:"确认已提交"}]} onChange={setOutcome}/></Form.Item><Form.Item label="核实依据" required><Input.TextArea value={evidence} onChange={e=>setEvidence(e.target.value)} rows={3} maxLength={2000} placeholder="请输入事务核对记录、工单或其他可追溯依据"/></Form.Item></>}
    <Form.Item label="处理原因" required><Input.TextArea value={reason} onChange={e=>setReason(e.target.value)} rows={3} maxLength={1000} placeholder="请说明本次处理原因，不填写密码或原始数据"/></Form.Item>
   </Form>
  <DialogActions><Button disabled={action.busy} onClick={()=>setOperation(null)}>返回</Button><Button type="primary" loading={action.busy} onClick={()=>void submit()}>确认处理</Button></DialogActions>
  </section>}
  {run.data.resolution&&<Alert type="info" title={`核实结论：${run.data.resolution.outcome==="succeeded"?"已提交":"未提交 / 已回滚"}`} description={<>{run.data.resolution.reason}<br/>核实依据：{run.data.resolution.evidence}</>}/>}
  <section aria-label="运行日志" hidden={Boolean(operation)}>
   <Typography.Title level={5}>运行日志</Typography.Title>
   <Space wrap size={16} style={{marginBottom:16}}>
    <Select aria-label="日志级别" placeholder="请选择日志级别" value={level} style={{minWidth:140}} options={[{value:"",label:"全部级别"},{value:"info",label:"信息"},{value:"warning",label:"警告"},{value:"error",label:"错误"}]} onChange={v=>{setLevel(v);setPage(1)}}/>
    <Select aria-label="日志阶段" placeholder="请选择日志阶段" value={stage} style={{minWidth:140}} options={stages} onChange={v=>{setStage(v);setPage(1)}}/>
    <Button onClick={events.reload}>刷新日志</Button><Button href={applicationURL(`/api/v1/etl-runs/${run.id}/events?`+query({level,stage,download:1}))}>下载日志</Button>
   </Space>
   {events.error?<Failure error={events.error} retry={events.reload}/>:<Table<Event> rowKey="id" loading={events.loading} dataSource={events.data?.items} scroll={{x:900}} pagination={{...pagePagination,total:events.data?.total}} columns={[
    {title:"时间",render:(_,e)=><Stamp value={e.created_at}/>},{title:"级别",render:(_,e)=><Tag color={e.level==="error"?"error":e.level==="warning"?"warning":"default"}>{{info:"信息",warning:"警告",error:"错误"}[e.level]??e.level}</Tag>},{title:"阶段",render:(_,e)=>stages.find(s=>s.value===e.stage)?.label??e.stage},{title:"事件代码",dataIndex:"code"},{title:"日志内容",dataIndex:"message"},{title:"操作人",dataIndex:"actor_id",ellipsis:true},{title:"追踪号",dataIndex:"trace_id",ellipsis:true}
   ]}/>}
   <p className="helper-text">日志按运行追加保存，保留策略为90天，服务启动时清理过期日志；自动日志不含数据内容或密码，处理说明请勿填写敏感内容。</p>
  </section>

 </>;
}

export function ETLAlerts({runID}:{runID?:string}){
 const [page,setPage,pagePagination]=usePagination(),[state,setState]=useState("open"),[selected,setSelected]=useState<{alert:OperationAlert;action:string}|null>(null),[reason,setReason]=useState("");
 const alerts=useQuery<Page<OperationAlert>>("/api/v1/etl-alerts?"+query({page,state,run_id:runID,page_size:pagePagination.pageSize}));const action=useAction();const {message}=App.useApp();
 async function submit(){if(!selected||!reason.trim()){message.warning("请填写处理说明");return}try{await action.run(`/api/v1/etl-alerts/${selected.alert.id}/${selected.action}`,{reason:reason.trim()});setSelected(null);alerts.reload();message.success("告警处理记录已保存")}catch(e){message.error(errorMessage(e))}}
 return <section aria-label="运行告警"><div hidden={Boolean(selected)}><Typography.Title level={5}>运行告警</Typography.Title><Space size={16} wrap style={{marginBottom:16}}><Select aria-label="告警状态" placeholder="请选择告警状态" style={{minWidth:140}} value={state} options={[{value:"",label:"全部告警"},...Object.entries(alertStates).map(([value,label])=>({value,label}))]} onChange={v=>{setState(v);setPage(1)}}/><Button onClick={alerts.reload}>刷新告警</Button></Space>
  {alerts.error?<Failure error={alerts.error} retry={alerts.reload}/>:<Table<OperationAlert> rowKey="id" dataSource={alerts.data?.items} loading={alerts.loading} scroll={{x:850}} pagination={{...pagePagination,total:alerts.data?.total}} columns={[{title:"产生时间",render:(_,a)=><Stamp value={a.created_at}/>},{title:"运行编号",dataIndex:"run_id",ellipsis:true},{title:"级别",render:(_,a)=><Tag color={a.severity==="error"?"error":"warning"}>{a.severity==="error"?"错误":"警告"}</Tag>},{title:"告警内容",dataIndex:"message"},{title:"状态",render:(_,a)=>alertStates[a.state]},{title:"处理说明",dataIndex:"reason"},{title:"操作",render:(_,a)=><Space size={16}>{a.state==="open"&&<Button onClick={()=>{setReason("");setSelected({alert:a,action:"acknowledge"})}}>确认处理</Button>}{a.state!=="resolved"&&<Button onClick={()=>{setReason("");setSelected({alert:a,action:"resolve"})}}>关闭告警</Button>}</Space>}]}/>}
  </div>
  {selected&&<section aria-label="处理运行告警"><Typography.Title level={5}>处理运行告警</Typography.Title><p>{selected.alert.message}</p><Form layout="vertical"><Form.Item label="处理说明" required><Input.TextArea rows={3} value={reason} maxLength={1000} placeholder="请说明已采取的处理措施或关闭依据" onChange={e=>setReason(e.target.value)}/></Form.Item></Form><DialogActions><Button disabled={action.busy} onClick={()=>setSelected(null)}>返回告警列表</Button><Button type="primary" loading={action.busy} onClick={()=>void submit()}>保存处理记录</Button></DialogActions></section>}
 </section>;
}
