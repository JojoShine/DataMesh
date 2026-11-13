import {usePagination} from "../hooks/usePagination";
import {GovernanceFieldHelp} from "./GovernanceFieldHelp";
import {GovernanceDialogActions} from "./GovernanceDialog";
import {GovernanceGuide} from "./GovernanceGuide";
import {Alert,App,Button,Descriptions,Input,Space,Table} from "antd";
import {useState} from "react";
import type {Page} from "../api/types";
import {useQuery} from "../hooks/useQuery";
import {useAction} from "../hooks/useAction";
import {errorMessage} from "../api/client";
import {Failure} from "./common";

interface PublicationApproval{parent_run_id:string;task_id:string;dataset_id:string;name:string;actor_id:string;result:{checked_rows:number;rejected_rows:number;contract_version:number}}
export function GovernancePublicationApprovals(){
 const {message}=App.useApp();const action=useAction();const [page,setPage,pagePagination]=usePagination();const [selected,setSelected]=useState<PublicationApproval|null>(null);const [note,setNote]=useState("");const list=useQuery<Page<PublicationApproval>>(`/api/v1/governance/publication-approvals?page=${page}&page_size=${pagePagination.pageSize}`);
 async function decide(decision:"approve"|"reject"){if(!selected)return;try{await action.run(`/api/v1/governance/publication-approvals/${selected.parent_run_id}/${selected.task_id}/${decision}`,{dataset_id:selected.dataset_id,note});setSelected(null);setNote("");list.reload();message.success(decision==="approve"?"成果已核验发布，工作流将继续":"已记录驳回意见")}catch(e){message.error(errorMessage(e))}}
 if(selected)return <><GovernanceDialogActions><Button onClick={()=>{setSelected(null);setNote("")}}>返回待审批列表</Button></GovernanceDialogActions><Descriptions className="detail-section" items={[{key:"name",label:"成果",children:selected.name},{key:"dataset",label:"成果编号",children:selected.dataset_id},{key:"version",label:"契约版本",children:selected.result.contract_version},{key:"checked",label:"检查行数",children:selected.result.checked_rows},{key:"bad",label:"隔离行数",children:selected.result.rejected_rows},{key:"actor",label:"申请人",children:selected.actor_id}]}/><Alert type="info" showIcon title="审批会重新核验真实证据" description="当前展示的是已提交成果的检查计数；实际发布还需通过有效来源、质量、结果有效期和共享依据核验。"/><div className="governance-control-section"><label htmlFor="governance-publication-note">发布依据或驳回原因<GovernanceFieldHelp label="发布依据或驳回原因"/></label></div><Input.TextArea id="governance-publication-note" className="governance-control-section" placeholder="请说明发布依据或驳回原因，不填写敏感记录值" value={note} onChange={e=>setNote(e.target.value)} maxLength={2000} rows={3}/><Space size={16} wrap className="detail-section"><GovernanceDialogActions><Button type="primary" disabled={!note.trim()} loading={action.busy} onClick={()=>decide("approve")}>核验并批准发布</Button></GovernanceDialogActions><GovernanceDialogActions><Button danger disabled={!note.trim()||action.busy} onClick={()=>decide("reject")}>驳回</Button></GovernanceDialogActions></Space></>;
 return <><GovernanceGuide topic="approval"/><Alert showIcon type="info" title="工作流成果发布审批" description="只展示当前账号有维护职责的待审批成果；申请人不能自行审批，管理员按原有职责处理。"/><GovernanceDialogActions><Button onClick={list.reload}>刷新待审批</Button></GovernanceDialogActions>{list.error?<Failure error={list.error}/>:<Table rowKey={r=>`${r.parent_run_id}:${r.task_id}`} dataSource={list.data?.items} loading={list.loading} scroll={{x:650}} pagination={{...pagePagination,total:list.data?.total}} columns={[{title:"成果",render:(_,r)=><Button type="link" onClick={()=>setSelected(r)}>{r.name}</Button>},{title:"任务",dataIndex:"task_id"},{title:"申请人",dataIndex:"actor_id"},{title:"隔离行数",render:(_,r)=>r.result.rejected_rows}]}/>}</>;
}
