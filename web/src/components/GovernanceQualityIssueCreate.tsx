import {Alert,Button,Descriptions,Form,Select} from "antd";
import type {GovernanceRun,GovernanceMember} from "../api/governance";
import {governanceRunCodes} from "../api/governance";
import {governanceQualityFlow,governanceQualityIssueEligible} from "../api/governanceQuality";
import {useQuery} from "../hooks/useQuery";
import {useSession} from "../stores/session";
import {Failure,Status} from "./common";
import {GovernanceDateTimePicker} from "./GovernanceDateInputs";
import {GovernanceDialog} from "./GovernanceDialog";

export function GovernanceQualityIssueCreate({runID,members,membersError,onReloadMembers,busy,readOnly,onClose,onCreate}:{runID:string;members:GovernanceMember[];membersError?:unknown;onReloadMembers?:()=>void;busy:boolean;readOnly:boolean;onClose:()=>void;onCreate:(values:{run_id:string;assignee_id:string;due_at:string})=>Promise<void>}){
 const user=useSession(s=>s.user)!;const run=useQuery<GovernanceRun>(`/api/v1/governance/runs/${encodeURIComponent(runID)}`);
 const context=useQuery<{eligible:boolean;origin_kind:string;summary:string;existing_issue_id:string}>(run.data?.kind==="profile"?`/api/v1/governance/runs/${encodeURIComponent(runID)}/issue-context`:null);
 const eligible=Boolean(context.data?.eligible||run.data?.kind==="dataflow"&&governanceQualityIssueEligible(run.data));
 return <GovernanceDialog header="登记质量问题" onClose={onClose} closeLabel="返回问题整改" busy={busy} width={850} actions={<Button form="quality-issue-from-run" htmlType="submit" type="primary" disabled={!eligible||readOnly||Boolean(membersError)} loading={busy}>登记问题</Button>}>
 <Alert showIcon type="info" title="原运行已带入，无需重新选择" description="填写责任人和期限即可登记。平台仍会核对质量异常、访问权限和快照是否可用。"/>{run.error?<Failure error={run.error} retry={run.reload}/>:run.data?.kind==="dataflow"?<><Descriptions className="detail-section" column={1} items={[{key:"flow",label:"数据流",children:`${governanceQualityFlow(run.data)?.name??run.data.definition.flow.id} · v${run.data.definition.flow.version}`},{key:"run",label:"原运行",children:run.data.id},{key:"state",label:"运行状态",children:<Status value={run.data.state}/>},{key:"reason",label:"质量异常",children:run.data.error_code?governanceRunCodes[run.data.error_code]??run.data.error_code:run.data.result?`隔离 ${run.data.result.rejected_rows} 行`:"尚无质量结果"}]}/>{!eligible&&<Alert showIcon type="warning" title="此运行没有可登记的质量异常" description="普通执行失败或没有隔离记录的成功运行，不能登记为质量问题。请返回检查记录核对。"/>}</>:run.data?.kind==="profile"?<Alert className="detail-section" showIcon type={eligible?"warning":"info"} title={context.data?.existing_issue_id?"此运行已登记整改":context.data?.summary||(context.loading?"正在核对完整检查结果":"此运行没有可登记的完整检查异常")} description={context.data?.existing_issue_id?`工单编号：${context.data.existing_issue_id}`:undefined}/>:run.data?<Alert className="detail-section" showIcon type="warning" title="此运行没有可登记的质量证据"/>:null}{Boolean(context.error)&&<Failure error={context.error} retry={context.reload}/>}
 {Boolean(membersError)&&<Failure error={membersError} retry={onReloadMembers}/>}<Form id="quality-issue-from-run" className="detail-section" layout="vertical" disabled={busy||readOnly||!eligible||Boolean(membersError)} onFinish={(values:{assignee_id:string;due_at:string})=>eligible&&!readOnly&&!busy&&!membersError?onCreate({...values,run_id:runID}):undefined}><Form.Item name="assignee_id" label="整改责任人" rules={[{required:true}]}><Select placeholder="请选择本部门有效治理人员" options={members.filter(member=>member.department_id===(run.data?.department_id??user.department_id)).map(member=>({value:member.user_id,label:member.username}))}/></Form.Item><Form.Item name="due_at" label="整改期限（本地时间）" rules={[{required:true}]}><GovernanceDateTimePicker placeholder="请选择未来的整改期限"/></Form.Item></Form>
 </GovernanceDialog>;
}
