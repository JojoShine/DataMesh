import {usePagination} from "../hooks/usePagination";
import {Alert,App,Button,DatePicker,Descriptions,Form,Input,Select,Space,Table,Tag} from "antd";
import {useId,useState} from "react";
import type {Dayjs} from "dayjs";
import type {Page,Resource} from "../api/types";
import type {GovernanceMember} from "../api/governance";
import {errorMessage,query} from "../api/client";
import {navigate} from "../app/navigation";
import {useAction} from "../hooks/useAction";
import {useQuery} from "../hooks/useQuery";
import {DialogActions,PlatformDialog} from "./PlatformDialog";
import {Failure,Stamp} from "./common";

export const feedbackCategories:Record<string,string>={quality:"数据质量",freshness:"更新不及时",meaning:"字段与口径",access:"访问与交付"};
const states:Record<string,string>={submitted:"待受理",in_progress:"处理中",resolved:"已完成"};
interface Feedback {
 id:string;workspace_id:string;resource_id:string;resource_name:string;contract_version:number;
 category:string;title:string;description:string;fields:string[];state:string;response:string;version:number;
 issue_id:string|null;issue_state:string;can_remediate:boolean;created_at:string;
 events?:{id:number;action:string;response:string;created_at:string}[];
}
export function AssetFeedbackForm({resource,grantID,fields,onDone}:{resource:Resource;grantID?:string;fields:string[];onDone:()=>void}){
 const action=useAction();const {message}=App.useApp();const id=useId();
 async function submit(v:{category:string;title:string;description:string;fields?:string[]}){
  try{await action.run("/api/v1/assets/feedback",{...v,fields:v.fields??[],resource,grant_id:grantID??""});message.success("反馈已提交，可在“我的资产—我的反馈”查看进展");onDone()}catch(e){message.error(errorMessage(e))}
 }
 return <Space orientation="vertical" size={24} style={{width:"100%"}}>
  <Alert showIcon title="说明问题及期望结果，不填写敏感数据原值" description="反馈会保留当前资源版本。提供部门或治理人员受理后，您可查看公开回复与整改进度。"/>
  <Form id={id} layout="vertical" onFinish={submit}>
   <Form.Item name="category" label="问题类型" rules={[{required:true}]}><Select placeholder="请选择问题类型" options={Object.entries(feedbackCategories).map(([value,label])=>({value,label}))}/></Form.Item>
   <Form.Item name="title" label="问题概述" rules={[{required:true,whitespace:true}]}><Input maxLength={200} placeholder="简要说明遇到的问题"/></Form.Item>
   {fields.length>0&&<Form.Item name="fields" label="涉及字段"><Select mode="multiple" allowClear placeholder="可选；仅列出当前共享或授权字段" options={fields.map(value=>({value,label:value}))}/></Form.Item>}
   <Form.Item name="description" label="具体情况与期望结果" rules={[{required:true,whitespace:true}]}><Input.TextArea rows={4} maxLength={4000} showCount placeholder="例如发现时间、字段含义疑问、更新预期或访问错误；不要填写密码、令牌和敏感记录值"/></Form.Item>
  </Form>
  <DialogActions><Button form={id} htmlType="submit" type="primary" loading={action.busy}>提交反馈</Button></DialogActions>
 </Space>;
}
export function AssetFeedbackList({governance=false,resourceID}:{governance?:boolean;resourceID?:string}){
 const [page,setPage,pagePagination]=usePagination();const [state,setState]=useState<string>();const [id,setID]=useState<string|null>(null);
 const prefix=governance?"/api/v1/governance/asset-feedback":"/api/v1/assets/feedback";
 const list=useQuery<Page<Feedback>>(prefix+"?"+query({page,state,resource_id:resourceID,page_size:pagePagination.pageSize}));
 return <><Space size={16} wrap style={{marginBottom:24}}><Select aria-label="反馈状态" style={{minWidth:160}} allowClear placeholder="全部反馈状态" value={state} onChange={v=>{setState(v);setPage(1)}} options={Object.entries(states).map(([value,label])=>({value,label}))}/><Button onClick={list.reload}>刷新反馈</Button></Space>
 {governance&&<Alert showIcon style={{marginBottom:24}} title="受理使用者的问题，必要时关联治理整改" description="本部门的使用反馈集中受理；仅有原运行访问权限的人员可关联整改。回复会对提交人可见，内部整改记录不会对外展示。"/>}
 {list.error?<Failure error={list.error}/>:<Table<Feedback> rowKey="id" loading={list.loading} dataSource={list.data?.items} scroll={{x:760}} pagination={{...pagePagination,total:list.data?.total}} columns={[
 {title:"问题",render:(_,r)=><Button type="link" onClick={()=>setID(r.id)}>{r.title}</Button>},
 {title:"资产",render:(_,r)=>`${r.resource_name} · V${r.contract_version}`},
 {title:"类型",render:(_,r)=>feedbackCategories[r.category]},
 {title:"状态",render:(_,r)=><Tag>{states[r.state]}</Tag>},
 {title:"提交时间",render:(_,r)=><Stamp value={r.created_at}/>},
 ]}/>}
 {id&&<AssetFeedbackDetail id={id} governance={governance} onClose={()=>{setID(null);list.reload()}}/>}
 </>;
}
function AssetFeedbackDetail({id,governance,onClose}:{id:string;governance:boolean;onClose:()=>void}){
 const prefix=governance?"/api/v1/governance/asset-feedback":"/api/v1/assets/feedback";
 const detail=useQuery<Feedback>(`${prefix}/${id}`);const row=detail.data;
 const members=useQuery<{items:GovernanceMember[]}>(governance&&row?.can_remediate&&!row.issue_id?"/api/v1/governance/members":null);
 const action=useAction();const {message}=App.useApp();const replyID=useId();const remedyID=useId();const [remedy,setRemedy]=useState(false);
 async function update(kind:string,data:Record<string,unknown>){if(!row)return;try{await action.run(`${prefix}/${id}/${kind}`,{base_version:row.version,...data});detail.reload();setRemedy(false);message.success("反馈处理记录已更新")}catch(e){message.error(errorMessage(e))}}
 return <PlatformDialog open title="使用反馈" width={880} onCancel={onClose} busy={action.busy} destroyOnHidden>
 {detail.error?<Failure error={detail.error}/>:row&&<Space orientation="vertical" size={24} style={{width:"100%"}}>
 <Descriptions column={1} items={[
 {key:"asset",label:"反馈资产",children:`${row.resource_name} · V${row.contract_version}`},
 {key:"title",label:"问题",children:row.title},
 {key:"category",label:"类型",children:feedbackCategories[row.category]},
 {key:"state",label:"进度",children:states[row.state]},
 {key:"description",label:"具体情况",children:<span style={{whiteSpace:"pre-wrap"}}>{row.description}</span>},
 {key:"fields",label:"涉及字段",children:row.fields.join("、")||"未指定"},
 {key:"reply",label:"最新回复",children:row.response||"等待受理"},
 {key:"issue",label:"治理整改",children:row.issue_id?<Space wrap><span>{{open:"待处理",in_progress:"处理中",retesting:"复测中",closed:"已通过复测并关闭"}[row.issue_state]??row.issue_state}</span>{governance&&<Button type="link" onClick={()=>navigate(`/platform/governance/issues?issue_id=${encodeURIComponent(row.issue_id!)}&workspace_id=${encodeURIComponent(row.workspace_id)}`)}>查看整改单</Button>}</Space>:"尚未关联"},
 ]}/>
 {governance&&row.state!=="resolved"&&<>
 {remedy?<Form id={remedyID} layout="vertical" onFinish={(v:{assignee_id:string;due_at:Dayjs})=>update("remediate",{assignee_id:v.assignee_id,due_at:v.due_at.toISOString()})}>
 <Alert showIcon style={{marginBottom:24}} title="人工确认问题后关联整改" description="同一运行已有未关闭整改时会复用；新建整改不会自动执行数据流。完成真实复测后再回复用户。"/>
 {Boolean(members.error)&&<Failure error={members.error}/>}
 <Form.Item name="assignee_id" label="整改责任人" rules={[{required:true}]}><Select placeholder="选择治理人员" options={members.data?.items.map(m=>({value:m.user_id,label:m.username}))}/></Form.Item>
 <Form.Item name="due_at" label="处理期限" rules={[{required:true}]}><DatePicker showTime style={{width:"100%"}}/></Form.Item>
 <DialogActions><Button onClick={()=>setRemedy(false)}>返回回复</Button><Button form={remedyID} htmlType="submit" type="primary" loading={action.busy}>确认关联整改</Button></DialogActions>
 </Form>:<Form id={replyID} layout="vertical" initialValues={{state:"in_progress"}} onFinish={(v:{state:string;response:string})=>update("reply",v)}>
 <Form.Item name="response" label="公开回复" rules={[{required:true,whitespace:true}]}><Input.TextArea rows={3} maxLength={4000} placeholder="说明处理进度、核验结论或使用建议，不包含内部日志和敏感数据"/></Form.Item>
 <Form.Item name="state" label="处理状态"><Select options={[{value:"in_progress",label:"处理中"},{value:"resolved",label:"处理完成",disabled:Boolean(row.issue_id&&row.issue_state!=="closed")} ]}/></Form.Item>
 <DialogActions>{!row.issue_id&&row.can_remediate&&<Button disabled={action.busy||action.readOnly} onClick={()=>setRemedy(true)}>关联治理整改</Button>}<Button form={replyID} htmlType="submit" type="primary" loading={action.busy} disabled={action.readOnly}>保存回复</Button></DialogActions>
 </Form>}
 </>}
 <Table rowKey="id" dataSource={row.events} pagination={false} scroll={{x:560}} columns={[{title:"时间",render:(_,r)=><Stamp value={r.created_at}/>},{title:"进展",render:(_,r)=>r.action==="remediation"?"关联整改":states[r.action]??r.action},{title:"公开回复",render:(_,r)=>r.response||"已提交反馈"}]}/>
 </Space>}
 </PlatformDialog>;
}
