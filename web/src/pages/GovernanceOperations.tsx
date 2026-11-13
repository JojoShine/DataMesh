import {GovernanceMasterRunView} from "./GovernanceCapabilities";
import {usePagination} from "../hooks/usePagination";
import {GovernanceGuide} from "../components/GovernanceGuide";
import {GovernanceDialog} from "../components/GovernanceDialog";
import {Button,Descriptions,Select,Space,Table} from "antd";
import {useState} from "react";
import type {Page} from "../api/types";
import type {GovernanceRun} from "../api/governance";
import {governanceRunCodes} from "../api/governance";
import {useQuery} from "../hooks/useQuery";
import {Failure,Stamp,Status,Title} from "../components/common";
import {GovernanceFlowRunView} from "./GovernanceDataflows";
import {GovernanceWorkflowRunView} from "./GovernanceWorkflows";
import {ProfileResult} from "./GovernanceProfiles";
export {GovernanceDashboard as GovernanceOverview} from "./GovernanceDashboard";

export function GovernanceOperations(){
 const [kind,setKind]=useState("");const [state,setState]=useState("");const [page,setPage,pagePagination]=usePagination();const [id,setID]=useState<string|null>(null);
 const list=useQuery<Page<GovernanceRun>>(`/api/v1/governance/runs?page=${page}&page_size=${pagePagination.pageSize}${kind?`&kind=${kind}`:""}${state?`&state=${state}`:""}`,true);const detail=useQuery<GovernanceRun>(id?`/api/v1/governance/runs/${id}`:null);
 const dialog0 = id ? <GovernanceDialog guide="operations" header={<Title title="治理运行详情" description="查看实际状态、处理计数和节点日志。" />} closeLabel="返回运行列表" onClose={()=>{setID(null);list.reload()}} width={1200}>{detail.error?<Failure error={detail.error}/>:(detail.data?.kind==="dataflow"||detail.data?.kind==="dataflow_preview")?<GovernanceFlowRunView id={id}/>:detail.data?.kind==="workflow"?<GovernanceWorkflowRunView id={id}/>:detail.data?.kind==="master"?<GovernanceMasterRunView id={id}/>:detail.data?.kind==="profile"&&<><Descriptions items={[{key:"state",label:"状态",children:<Status value={detail.data.state}/>},{key:"count",label:"已处理",children:detail.data.processed},{key:"error",label:"诊断",children:governanceRunCodes[detail.data.error_code]??detail.data.error_code}]}/><ProfileResult run={detail.data}/></>}</GovernanceDialog> : null;
 return <><Title title="运行运维" description="统一查看剖析、数据流和工作流的真实运行，失败和中断可进入详情处理。" actions={<Button onClick={list.reload}>刷新</Button>}/><GovernanceGuide topic="operations"/><Space size={16} wrap className="governance-filter-bar"><Select aria-label="运行类型" placeholder="请选择运行类型" value={kind} onChange={v=>{setKind(v);setPage(1)}} options={[{value:"",label:"全部类型"},{value:"master",label:"主数据匹配"},{value:"profile",label:"数据剖析"},{value:"dataflow",label:"数据流"},{value:"dataflow_preview",label:"代码样本试运行"},{value:"workflow",label:"工作流"}]}/><Select aria-label="运行状态" placeholder="请选择运行状态" value={state} onChange={v=>{setState(v);setPage(1)}} options={[{value:"",label:"全部状态"},{value:"queued",label:"排队中"},{value:"running",label:"运行中"},{value:"succeeded",label:"已完成"},{value:"failed",label:"失败"},{value:"cancelled",label:"已取消"},{value:"interrupted",label:"已中断"}]}/></Space>{list.error?<Failure error={list.error}/>:<Table rowKey="id" dataSource={list.data?.items} loading={list.loading} scroll={{x:900}} pagination={{...pagePagination,total:list.data?.total}} columns={[{title:"运行",render:(_,r)=><Button type="link" onClick={()=>setID(r.id)}>{r.id}</Button>},{title:"类型",render:(_,r)=>({master:"主数据匹配",profile:"剖析",dataflow:"数据流",dataflow_preview:"代码试运行",workflow:"工作流"})[r.kind]},{title:"状态",render:(_,r)=><Status value={r.state}/>},{title:"已处理",dataIndex:"processed"},{title:"诊断",render:(_,r)=>governanceRunCodes[r.error_code]??r.error_code},{title:"建立时间",render:(_,r)=><Stamp value={r.created_at}/>} ]}/>}{dialog0}</>
}
