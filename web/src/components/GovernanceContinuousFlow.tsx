import {usePagination} from "../hooks/usePagination";
import {GovernanceNodeLabel} from "./GovernanceNodeLabel";
import type {GovernanceNodeContext} from "../api/governanceNodeInfo";
import {useGovernanceFlowNodeContext} from "../hooks/useGovernanceRunNodeContext";
import type {GovernanceParameterValues} from "./GovernanceRunParameters";
import {useEffect,useState} from "react";
import {Alert,App,Button,Descriptions,Select,Table,Tag} from "antd";
import {useQuery} from "../hooks/useQuery";
import {useAction} from "../hooks/useAction";
import {errorMessage} from "../api/client";
import type {Page} from "../api/types";
import type {GovernanceRecord,GovernanceFlowRun,GovernanceExecutionPlan,GovernanceStream,GovernanceStreamHealth} from "../api/governance";
import {governanceDataLayerLabel} from "../api/governance";
import {GovernanceDialogActions} from "./GovernanceDialog";
import {Failure,Stamp} from "./common";
import "./GovernanceContinuousFlow.css";
const states:Record<string,string>={starting:"正在启动",running:"持续更新中",pausing:"正在保存恢复点",paused:"已暂停",failed:"已停止",unknown:"等待核对实际作业"};
const healthLabels:Record<string,string>={running:"运行中",paused:"已暂停",stopped:"已停止",unhealthy:"需要处理",unknown:"未知",readable:"已提交位点可读取"};
function evidenceTime(value:string|null|undefined){return value?<Stamp value={value}/>:"未知"}
function healthEvidence(health:GovernanceStreamHealth|undefined){return <><Tag>{healthLabels[health?.state??"unknown"]??"未知"}</Tag> 健康核对时间：{evidenceTime(health?.observed_at)}</>}
function StreamFreshness({stream,context}:{stream:GovernanceStream;context:GovernanceNodeContext}){
 const evidence=stream.freshness;
 return <><Descriptions title="数据时效证据" column={1} items={[
  {key:"source",label:"来源采集",children:healthEvidence(evidence?.source_capture)},
  {key:"compute",label:"Flink 计算",children:healthEvidence(evidence?.flink_compute)},
  {key:"doris",label:"Doris 写入",children:healthEvidence(evidence?.doris_write)},
  {key:"cursor",label:"已核实的落库位点",children:evidence?.doris_output_offset??"未知"},
  {key:"processed",label:"已处理到的来源时间",children:evidenceTime(evidence?.processed_to_at)},
  {key:"latency",label:"端到端数据延迟",children:evidence?.end_to_end_latency_ms==null?"未知":`${evidence.end_to_end_latency_ms} 毫秒`},
 ]}/>{evidence?.sources.map(source=><Descriptions key={source.node_id} title={<GovernanceNodeLabel id={source.node_id} context={context}/>} column={{xs:1,sm:2,md:3}} items={[
  {key:"health",label:"采集状态",children:healthEvidence(source)},
  {key:"latest",label:"最近捕获的来源事件时间",children:evidenceTime(source.latest_event_at)},
  {key:"forwarded",label:"已转入计算的来源时间",children:evidenceTime(source.forwarded_to_at)},
  {key:"backlog",label:"待转入计算的日志位点数",children:source.backlog_offsets??"未知"},
 ]}/>)}<details className="detail-section"><summary>时效核对范围</summary><p className="helper-text">健康核对时间仅表示检查时间。来源时间取自可读取的最近一条日志，快照、删除墓碑或缺少事件时间时显示未知；日志位点数只表示已采集但尚未转入计算的范围，不包含来源端未采集的变化或计算中的积压。当前成果未记录来源时间传递证据，已处理到的来源时间和端到端延迟显示未知。Doris 位点可读取仅证明已有落库记录可查；落库追平不代表来源端到端已同步。</p></details></>
}
export function GovernanceContinuousFlow({record,parameters,onSnapshot}:{record:GovernanceRecord;parameters:GovernanceParameterValues;onSnapshot:(id:string)=>void}){
 const {message}=App.useApp();const action=useAction();const [selected,setSelected]=useState<string>();const [page,setPage,pagePagination]=usePagination();const [plan,setPlan]=useState<GovernanceExecutionPlan>();
 const list=useQuery<Page<GovernanceStream>>(`/api/v1/governance/streams?definition_id=${encodeURIComponent(record.id)}&version=${record.version}`,true);
 const activeID=selected??list.data?.items[0]?.id;
 const detail=useQuery<GovernanceStream>(activeID?`/api/v1/governance/streams/${activeID}`:null,true);const stream=detail.data;
 const fixed=useQuery<GovernanceRecord>(stream&&(stream.flow_id!==record.id||stream.flow_version!==record.version)?`/api/v1/governance/dataflows/${stream.flow_id}/versions/${stream.flow_version}`:null);
 const {context,errors}=useGovernanceFlowNodeContext(stream&&(stream.flow_id!==record.id||stream.flow_version!==record.version)?fixed.data:record);
 const records=useQuery<Page<Record<string,unknown>>>(stream&&["running","paused"].includes(stream.state)?`/api/v1/governance/streams/${stream.id}/records?page=${page}&page_size=${pagePagination.pageSize}`:null,true);
 useEffect(()=>{const timer=window.setInterval(()=>{if(!document.hidden){list.reload();detail.reload();if(stream?.state==="running")records.reload()}},3000);return()=>window.clearInterval(timer)},[activeID,stream?.state]);
 async function start(){try{const base=`/api/v1/governance/dataflows/${record.id}/versions/${record.version}`;const checked=await action.run<GovernanceExecutionPlan>(`${base}/execution-plan?mode=continuous`,{parameters});setPlan(checked);const next=await action.run<GovernanceStream>(`${base}/stream`,{parameters});setSelected(next.id);list.reload()}catch(e){message.error(errorMessage(e))}}
 async function control(kind:"pause"|"resume"|"reconcile"|"snapshot"|"stop"){if(!stream)return;try{if(kind==="snapshot"){const next=await action.run<GovernanceFlowRun>(`/api/v1/governance/streams/${stream.id}/snapshot`,{});onSnapshot(next.id)}else{await action.run<GovernanceStream>(`/api/v1/governance/streams/${stream.id}/${kind}`,{});detail.reload();list.reload()}}catch(e){message.error(errorMessage(e))}}
 const output=record.definition.dataflow?.nodes.find(n=>n.kind==="output");
 const model=useQuery<GovernanceRecord>(output?.model?`/api/v1/governance/models/${output.model.id}/versions/${output.model.version}`:null);
 const resolvedLayer=stream?.execution_plan?.data_layer??plan?.data_layer??(output?.model&&!model.data?undefined:model.data?.definition.model?.data_layer||output?.data_layer||"dwd");
 return <div className="governance-continuous-flow"><Alert showIcon type="info" title="流式处理 · Kafka → Flink → Doris" description="运行实例由本数据流直接启动，无需先建工作流或调度。上游 CDC 负责采集来源变化；Flink 持续处理新增、修改和删除，通过 Kafka 事务结果写入 Doris。这里显示最新处理状态；暂停后可生成一致的成果快照并核验、发布。"/>
 <Descriptions items={[{key:"layer",label:"成果分层",children:resolvedLayer?governanceDataLayerLabel(resolvedLayer):"读取模型分层中"},{key:"engine",label:"实际计算",children:"Flink（持续处理）"},{key:"archive",label:"文件存档",children:"生成成果后，可按需保存至 RustFS"}]}/>
 {Boolean(model.error)&&<Failure error={model.error} retry={model.reload}/>}
 {Boolean(list.error)&&<Failure error={list.error}/>}<Select aria-label="流式运行实例" placeholder="请选择运行实例" value={activeID} options={list.data?.items.map(r=>({value:r.id,label:`第 ${r.flow_version} 版 · ${states[r.state]??r.state} · ${r.id.slice(0,8)}`}))} onChange={id=>{setSelected(id);setPage(1)}} style={{minWidth:240,maxWidth:"100%"}}/>
 {detail.error?<Failure error={detail.error}/>:stream&&<><Descriptions items={[{key:"state",label:"状态",children:<Tag>{stream.desired_state==="failed"&&stream.state!=="failed"?"正在结束任务":states[stream.state]??stream.state}</Tag>},{key:"rows",label:"最新成果行数",children:stream.output_rows},{key:"bad",label:"待处理质量记录",children:stream.rejected_rows},{key:"input",label:"已转入的来源变更",children:stream.input_events},{key:"offset",label:"结果落库位点",children:stream.output_offset},{key:"updated",label:"实例状态更新时间",children:<Stamp value={stream.updated_at}/>} ]}/><StreamFreshness stream={stream} context={context}/>{fixed.error&&<Failure error={fixed.error}/>} {errors.length>0&&<Failure error={errors[0]}/>}{stream.error_code&&<Alert type="warning" showIcon title="流式运行需要处理" description={stream.error_code}/>}</>}
 {records.error?<Failure error={records.error}/>:records.data&&<Table rowKey={(_,index)=>String(index)} scroll={{x:true}} dataSource={records.data.items} columns={Object.keys(records.data.items[0]??{}).map(name=>({title:name,dataIndex:name,render:(value:unknown)=>value==null?"—":String(value)}))} pagination={{...pagePagination,total:records.data.total}}/>}
 <GovernanceDialogActions><Button disabled={action.busy||action.readOnly||record.state!=="published"} onClick={start}>启动持续运行</Button>{stream&&stream.desired_state!=="failed"&&["running","starting"].includes(stream.state)&&<Button disabled={action.busy||action.readOnly} onClick={()=>control("pause")}>暂停并保存恢复点</Button>}{stream?.state==="paused"&&stream.desired_state!=="failed"&&<><Button disabled={action.busy||action.readOnly} onClick={()=>control("resume")}>恢复更新</Button><Button type="primary" disabled={action.busy||action.readOnly} onClick={()=>control("snapshot")}>生成本次成果</Button></>}{stream?.state==="unknown"&&<Button disabled={action.busy||action.readOnly} onClick={()=>control("reconcile")}>核对实际作业</Button>}{stream&&stream.state!=="failed"&&stream.desired_state!=="failed"&&<Button disabled={action.busy||action.readOnly} onClick={()=>control("stop")}>停止持续运行</Button>}</GovernanceDialogActions></div>
}
