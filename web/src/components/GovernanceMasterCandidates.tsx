import {Alert,App,Button,Form,Input,Select,Space,Table,Tag} from "antd";
import {useState} from "react";
import type {Page} from "../api/types";
import {errorMessage} from "../api/client";
import {governanceFieldLabel} from "../api/governanceFieldLabels";
import {useAction} from "../hooks/useAction";
import {usePagination} from "../hooks/usePagination";
import {useQuery} from "../hooks/useQuery";
import {DialogActions} from "./PlatformDialog";
import {Failure,Stamp} from "./common";

interface SourceRow {source:string;record_id:string;ordinal:number;values:Record<string,unknown>}
interface Candidate {id:string;entity_id:string;run_id:string;payload:SourceRow[];conflicts:string[];confirmed:boolean}
interface Revision {entity_id:string;version:number;candidate_id:string;payload:Record<string,unknown>;reason:string;created_at:string}
interface BatchPreview {preview_token:string;items:{candidate_id:string;entity_id:string;expected_version:number;payload:Record<string,unknown>;sources:SourceRow[]}[]}

export function MasterCandidates({runID}:{runID:string}) {
 const {message}=App.useApp();const action=useAction();const [page,setPage,pagination]=usePagination();
 const [onlyConflicts,setOnlyConflicts]=useState(false);const [confirmed,setConfirmed]=useState("");const [ids,setIDs]=useState<string[]>([]);
 const list=useQuery<Page<Candidate>&{field_labels:Record<string,string>}>(`/api/v1/governance/master-runs/${runID}/candidates?page=${page}&page_size=${pagination.pageSize}&conflicts=${onlyConflicts}${confirmed?`&confirmed=${confirmed}`:""}`);
 const [selected,setSelected]=useState<Candidate|null>(null);const [choices,setChoices]=useState<Record<string,number>>({});const [note,setNote]=useState("");
 const [preview,setPreview]=useState<BatchPreview|null>(null);const [historyPage,setHistoryPage,historyPagination]=usePagination();
 const history=useQuery<Page<Revision>>(selected?`/api/v1/governance/master-candidates/${selected.id}/history?page=${historyPage}&page_size=${historyPagination.pageSize}`:null);
 const label=(name:string)=>governanceFieldLabel({name,business_name:list.data?.field_labels?.[name]});
 const resultText=(payload:Record<string,unknown>)=>Object.entries(payload).map(([key,value])=>`${label(key)}：${JSON.stringify(value)}`).join("；");
 async function confirm() {
  if(!selected||!history.data)return;
  try { await action.run(`/api/v1/governance/master-candidates/${selected.id}/confirm`,{expected_version:history.data.total,choices,reason:note});history.reload();list.reload();setNote("");message.success("已保存实体版本和来源映射"); }
  catch(e){message.error(errorMessage(e))}
 }
 async function batchPreview() {
  try { const result=await action.run<BatchPreview>(`/api/v1/governance/master-runs/${runID}/batch-preview`,{candidate_ids:ids});setPreview(result);setNote(""); }
  catch(e){message.error(errorMessage(e))}
 }
 async function batchConfirm() {
  if(!preview)return;
  try { await action.run(`/api/v1/governance/master-runs/${runID}/batch-confirm`,{candidate_ids:preview.items.map(item=>item.candidate_id),preview_token:preview.preview_token,reason:note});setPreview(null);setIDs([]);setNote("");list.reload();message.success("所选实体已全部确认，来源映射和审计记录已保存"); }
  catch(e){message.error(errorMessage(e));setPreview(null);setIDs([]);list.reload()}
 }
 if(preview)return <>
  <Alert showIcon type="info" title={`请核对本次 ${preview.items.length} 个实体`} description="固定所选候选和当前实体版本；敏感值保持脱敏。仅确认无冲突且没有历史版本的实体，任一项变化将整批拒绝。"/>
  <Table className="detail-section" rowKey="candidate_id" dataSource={preview.items} pagination={false} scroll={{x:800}} columns={[{title:"实体编号",dataIndex:"entity_id"},{title:"确认结果",render:(_,row)=>resultText(row.payload)},{title:"来源依据",render:(_,row)=>row.sources.map(source=>`${source.source} / ${source.record_id}`).join("；")}]}/>
  <Form layout="vertical" className="detail-section"><Form.Item label="批量确认原因" htmlFor="master-batch-reason" required><Input.TextArea id="master-batch-reason" placeholder="请说明已核对的依据及确认原因" rows={2} maxLength={4000} value={note} onChange={event=>setNote(event.target.value)}/></Form.Item></Form>
  <DialogActions><Button disabled={action.busy} onClick={()=>setPreview(null)}>返回候选</Button><Button type="primary" disabled={!note.trim()||Boolean(action.readOnly)} loading={action.busy} onClick={()=>void batchConfirm()}>确认所选实体</Button></DialogActions>
 </>;
 if(selected){const fields=Object.keys(selected.payload[0]?.values??{});return <>
  <DialogActions><Button onClick={()=>setSelected(null)}>返回候选</Button></DialogActions><Tag>{selected.conflicts.length?"存在字段冲突":"可见取值无冲突，待人工确认"}</Tag>
  <Table className="detail-section" rowKey={row=>`${row.source}:${row.ordinal}`} dataSource={selected.payload} pagination={false} scroll={{x:800}} columns={[{title:"来源",dataIndex:"source"},{title:"原始编号",dataIndex:"record_id"},...fields.map(field=>({title:label(field),render:(_:unknown,row:SourceRow)=>JSON.stringify(row.values[field])}))]}/>
  <Form layout="vertical" className="detail-section"><div className="governance-form-grid">{fields.map(field=><Form.Item key={field} label={`${label(field)}${selected.conflicts.includes(field)?" · 冲突字段":""}`} htmlFor={`master-choice-${field}`} required><Select id={`master-choice-${field}`} placeholder="请选择经过核对的来源取值" value={choices[field]} onChange={value=>setChoices({...choices,[field]:value})} options={selected.payload.map((row,index)=>({value:index,label:`${row.source} / ${row.record_id}：${JSON.stringify(row.values[field])}`}))}/></Form.Item>)}</div><Form.Item label="确认原因" htmlFor="master-single-reason" required><Input.TextArea id="master-single-reason" placeholder="请说明取值选择的业务依据" rows={2} value={note} onChange={e=>setNote(e.target.value)} maxLength={4000}/></Form.Item></Form>
  <DialogActions><Button onClick={history.reload}>刷新版本</Button><Button type="primary" disabled={!note.trim()||fields.some(field=>choices[field]===undefined)||history.loading||Boolean(history.error)||Boolean(action.readOnly)} loading={action.busy} onClick={()=>void confirm()}>确认实体</Button></DialogActions>
  {history.error?<Failure error={history.error} retry={history.reload}/>:<Table className="detail-section" rowKey="version" dataSource={history.data?.items} pagination={{...historyPagination,total:history.data?.total}} columns={[{title:"版本",dataIndex:"version"},{title:"确认原因",dataIndex:"reason"},{title:"确认时间",render:(_,row)=><Stamp value={row.created_at}/>},{title:"确认结果",render:(_,row)=>resultText(row.payload)}]}/>}
 </>}
 return <>
  <Space size={16} wrap className="governance-filter-bar"><Select aria-label="候选范围" placeholder="请选择候选范围" value={onlyConflicts?"conflicts":"all"} onChange={value=>{setOnlyConflicts(value==="conflicts");setPage(1);setIDs([])}} options={[{value:"all",label:"全部实体候选"},{value:"conflicts",label:"仅冲突实体"}]}/><Select aria-label="实体确认状态" placeholder="请选择确认状态" value={confirmed} onChange={value=>{setConfirmed(value);setPage(1);setIDs([])}} options={[{value:"",label:"全部确认状态"},{value:"false",label:"尚未确认"},{value:"true",label:"已有确认记录"}]}/><Button onClick={()=>{list.reload();setIDs([])}}>刷新候选</Button><Button disabled={!ids.length||Boolean(action.readOnly)} loading={action.busy} onClick={()=>void batchPreview()}>预览批量确认（{ids.length}）</Button></Space>
  <p className="helper-text">每批最多100个新实体。冲突、跨来源敏感值及已有实体修订请逐条核对。预览后填写原因并确认才会保存。</p>
  {list.error?<Failure error={list.error} retry={list.reload}/>:<Table rowKey="id" rowSelection={{selectedRowKeys:ids,onChange:keys=>setIDs(keys.map(String)),getCheckboxProps:row=>({disabled:row.confirmed||row.conflicts.length>0})}} dataSource={list.data?.items} loading={list.loading} scroll={{x:800}} pagination={{...pagination,onChange:(next,size)=>{pagination.onChange?.(next,size);setIDs([])},total:list.data?.total}} columns={[{title:"实体候选",render:(_,row)=><Button type="link" onClick={()=>{setSelected(row);setChoices({});setNote("");setHistoryPage(1)}}>{row.entity_id.slice(0,12)}</Button>},{title:"来源记录",render:(_,row)=>row.payload.length},{title:"确认状态",render:(_,row)=><Tag>{row.confirmed?"已有确认记录":"尚未确认"}</Tag>},{title:"冲突字段",render:(_,row)=>row.conflicts.map(label).join("、")||"可见取值无冲突"}]}/>}
 </>;
}
