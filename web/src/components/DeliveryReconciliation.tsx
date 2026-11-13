import {applicationURL} from "../app/basePath";
import {usePagination} from "../hooks/usePagination";
import {Alert,App,Button,Descriptions,Form,Input,Space,Table,Tag} from "antd";
import {useEffect,useRef,useState} from "react";
import type {DeliveryReceipt,DeliveryReceiptPage,DeliveryReconciliationPage,DeliveryReconciliationReport} from "../api/types";
import type {Run} from "../pages/ETL";
import {errorMessage,query} from "../api/client";
import {useQuery} from "../hooks/useQuery";
import {useAction} from "../hooks/useAction";
import {Failure,Stamp} from "./common";

const states:Record<string,string>={pending:"待核对",checking:"核对中",passed:"核对通过",mismatched:"存在差异",failed:"对账失败"};
const receiptStates:Record<string,string>={pending:"待验收",accepted:"已接收",rejected:"已驳回",not_applicable:"暂不可验收"};
const errors:Record<string,string>={SOURCE_UNAVAILABLE:"目标连接不可用，请检查数据源后重新核对",TARGET_SCHEMA_CHANGED:"目标字段类型、主键或排序规则已变化，旧索引不能继续使用",DELIVERY_INDEX_EXPIRED:"核对索引已过保留期，既有报告保留，但不能重新核对",DELIVERY_INDEX_UNAVAILABLE:"本次运行没有可用的期望索引",DELIVERY_INDEX_INVALID:"核对索引完整性检查失败，请联系管理员",DELIVERY_NOT_COMMITTED:"执行尚未确认提交，暂不能进行提交后核对",FORBIDDEN:"接收账号当前权限已失效，不能继续核对",SOURCE_VERSION_CHANGED:"目标数据源配置已变化，请核查交付范围",DELIVERY_INDEX_KEY_UNAVAILABLE:"核对密钥不可用，请联系管理员",RUN_TIMEOUT:"核对超时，可在检查目标状态后单独重核"};
interface Editing {decision:"accepted"|"rejected";version:number;reportID:string}
export function DeliveryReconciliation({run}:{run:Run}){
 const {message}=App.useApp();const [reportPage,setReportPage,reportPagePagination]=usePagination(),[receiptPage,setReceiptPage,receiptPagePagination]=usePagination();
 const base=`/api/v1/etl-runs/${run.id}`;
 const reports=useQuery<DeliveryReconciliationPage>(base+"/reconciliation?"+query({page:reportPage,page_size:reportPagePagination.pageSize}),true);
 const receipts=useQuery<DeliveryReceiptPage>(base+"/receipts?"+query({page:receiptPage,page_size:receiptPagePagination.pageSize}),true);
 const [editing,setEditing]=useState<Editing|null>(null),[reason,setReason]=useState(""),[delegateReason,setDelegateReason]=useState(""),[error,setError]=useState("");
 const saveAction=useAction(),checkAction=useAction();
 const refresh=useRef(()=>{});refresh.current=()=>{reports.reload();receipts.reload()};
 useEffect(()=>{const timer=setInterval(()=>{if(!document.hidden)refresh.current()},3000);return()=>clearInterval(timer)},[]);
 const latest=reports.data?.latest;const receipt=receipts.data;
 const authorized=!reports.error&&!receipts.error;
 const committed=run.state==="succeeded";
 const canAccept=committed&&authorized&&latest?.state==="passed"&&Boolean(receipt?.can_accept);
 const canReject=committed&&authorized&&Boolean(latest)&&Boolean(receipt?.can_reject);
 function begin(decision:Editing["decision"]){if(!latest||!receipt)return;setEditing({decision,version:receipt.version,reportID:latest.id});setError("")}
 async function recheck(){try{await checkAction.run(base+"/reconciliation");reports.reload();message.success("已提交交付对账，请等待核对结果")}catch(e){message.error(errorMessage(e));reports.reload()}}
 async function save(){
  if(!editing)return;
  if(!reason.trim()||(receipt?.delegate&&!delegateReason.trim())){setError("请填写验收说明；管理员代办还需填写代办依据");return}
  try{
   await saveAction.run(base+"/receipts",{decision:editing.decision,reason:reason.trim(),expected_version:editing.version,reconciliation_id:editing.reportID,...(receipt?.delegate?{on_behalf_reason:delegateReason.trim()}:{})});
   setEditing(null);setReason("");setDelegateReason("");setError("");receipts.reload();message.success("验收回执已追加保存，历史记录保留")
  }catch(e){setError(errorMessage(e))}
 }
 return <section className="delivery-reconciliation" aria-label="交付对账与验收">
  <Descriptions size="small" column={{xs:1,sm:3}} items={[{key:"execution",label:"执行结果",children:committed?"已提交":run.state==="unknown"?"提交结果待核实":run.state==="failed"||run.state==="cancelled"?"未提交":"执行中"},{key:"reconciliation",label:"技术对账",children:<Tag color={latest?.state==="passed"?"success":latest?.state==="failed"||latest?.state==="mismatched"?"error":"processing"}>{latest?states[latest.state]:"未核对"}</Tag>},{key:"receipt",label:"业务验收",children:receipt?receiptStates[receipt.state]:"待查询"}]}/>
  {!committed&&<Alert showIcon type="info" title={run.state==="unknown"?"先核实目标事务是否提交，再进行技术对账和验收":"目标尚未确认提交，验收暂不可用"}/>}
  {Boolean(reports.error)&&<Failure error={reports.error} retry={reports.reload}/>}
  {Boolean(receipts.error)&&<Failure error={receipts.error} retry={receipts.reload}/>}
  <Space wrap size={16}>
   <Button onClick={()=>refresh.current()}>刷新对账</Button>
   <Button loading={checkAction.busy} disabled={!committed||!authorized||!reports.data?.can_retry||Boolean(editing)} onClick={()=>void recheck()}>重核本次交付</Button>
   <Button href={applicationURL(base+"/reconciliation?download=1")} disabled={!latest||!authorized}>下载对账报告</Button>
  </Space>
  {latest?<>
   <Descriptions size="small" column={{xs:1,sm:2}} items={[{key:"version",label:"报告版本",children:`V${latest.version}`},{key:"time",label:"核对时间",children:latest.checked_at?<Stamp value={latest.checked_at}/>:"等待完成"},{key:"processed",label:"处理行数",children:latest.processed_rows},{key:"keys",label:"最终交付主键数",children:latest.key_count},{key:"checked",label:"已读回核对行数",children:latest.checked_rows},{key:"mismatch",label:"差异数量",children:latest.mismatch_count},{key:"snapshot",label:"输入快照",children:latest.input_snapshot_id||"—"}]}/>
   {latest.error_code&&<Alert type="warning" showIcon title={errors[latest.error_code]??"对账未完成，请联系管理员按运行编号核查"}/>}
   {latest.state==="mismatched"&&<Alert type="warning" showIcon title="目标当前数据与本次交付期望不一致，不能确认接收"/>}
   <p className="helper-text">核对本次最终主键集合与映射字段。处理行数包含同一主键的多次更新；重新核对只读取目标，保留原交换执行结果。</p>
  </>:!reports.loading&&<p className="helper-text">历史运行尚未建立交付对账记录，不会补写为已通过。</p>}
  <div className="detail-section"><h3>业务验收</h3>
   {error&&<Alert type="error" showIcon title={error}/>}
   {!editing?<Space wrap size={16}><Button type="primary" disabled={!canAccept} onClick={()=>begin("accepted")}>{receipt?.delegate?"代办接收":"确认接收"}</Button><Button danger disabled={!canReject} onClick={()=>begin("rejected")}>{receipt?.delegate?"代办驳回":"驳回交付"}</Button></Space>:<section aria-label="填写验收回执">
    <p>{editing.decision==="accepted"?"确认接收本次交付":"记录本次交付的驳回原因"}；引用已查看的对账报告及验收版本。</p>
    <Form layout="vertical" disabled={saveAction.busy}>
     <Form.Item label="验收说明" required><Input.TextArea aria-label="验收说明" placeholder="请说明核验结论或驳回原因" rows={3} value={reason} maxLength={1000} onChange={e=>setReason(e.target.value)}/></Form.Item>
     {receipt?.delegate&&<Form.Item label="代办依据" required><Input.TextArea aria-label="代办依据" placeholder="请说明代办原因及授权依据" rows={2} value={delegateReason} maxLength={1000} onChange={e=>setDelegateReason(e.target.value)}/></Form.Item>}
    </Form>
    <Space size={16}><Button disabled={saveAction.busy} onClick={()=>{setEditing(null);setError("");refresh.current()}}>返回并刷新</Button><Button type="primary" loading={saveAction.busy} onClick={()=>void save()}>保存验收回执</Button></Space>
   </section>}
   {!editing&&<Table<DeliveryReceipt> scroll={{x:800}} size="small" rowKey="id" dataSource={receipt?.items??[]} pagination={{...receiptPagePagination,total:receipt?.total}} columns={[{title:"时间",render:(_,r)=><Stamp value={r.created_at}/>},{title:"验收结果",render:(_,r)=>receiptStates[r.decision]},{title:"说明",dataIndex:"reason"},{title:"实际操作人",dataIndex:"actor_id",ellipsis:true},{title:"接收账号",dataIndex:"recipient_id",ellipsis:true},{title:"代办依据",dataIndex:"on_behalf_reason"}]}/>}
  </div>
  <div className="detail-section"><h3>对账历史</h3><Table<DeliveryReconciliationReport> scroll={{x:700}} size="small" rowKey="id" dataSource={reports.data?.history??[]} pagination={{...reportPagePagination,total:reports.data?.total}} columns={[{title:"核对次数",render:(_,r)=>`第 ${r.version} 次`},{title:"时间",render:(_,r)=>r.checked_at?<Stamp value={r.checked_at}/>:"—"},{title:"结果",render:(_,r)=>states[r.state]},{title:"交付主键数",dataIndex:"key_count"},{title:"差异数量",dataIndex:"mismatch_count"}]}/></div>
 </section>
}
