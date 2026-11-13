import {usePagination} from "../hooks/usePagination";
import {PlusOutlined} from "@ant-design/icons";
import {GovernanceWorkspaceEditor} from "../components/GovernanceWorkspaceEditor";
import {App,Alert,Button,Empty,Input,Select,Space,Table,Tag} from "antd";
import {useEffect,useState} from "react";
import type {Page,Resource} from "../api/types";
import type {MetadataResource} from "../api/governance";
import {governanceResourceType} from "../api/governance";
import type {GovernanceWorkspace,GovernanceWorkspaceSource} from "../api/governanceWorkspace";
import {useQuery} from "../hooks/useQuery";
import {useAction} from "../hooks/useAction";
import {useSession} from "../stores/session";
import {useGovernanceWorkspace} from "../components/GovernanceWorkspaceContext";
import {GovernanceDialog} from "../components/GovernanceDialog";
import {Failure,Stamp} from "../components/common";
import {errorMessage} from "../api/client";
import {navigate,usePath} from "../app/navigation";
import "./GovernanceWorkspaces.css";

export function GovernanceWorkspaces(){return <WorkspaceList/>}
function WorkspaceList(){
 const path=usePath();const createRequested=new URLSearchParams(path.split("?")[1]).get("create")==="true";
 const user=useSession(s=>s.user)!;
 const [page,setPage,pagePagination]=usePagination();const [search,setSearch]=useState("");const [state,setState]=useState("");const [editing,setEditing]=useState<GovernanceWorkspace|"new"|null>(null);
 useEffect(()=>{if(createRequested)setEditing("new")},[createRequested]);
 function closeEditor(){setEditing(null);if(createRequested)navigate("/platform/governance/workspaces",true)}
 const list=useQuery<Page<GovernanceWorkspace>>(`/api/v1/governance/workspaces?page=${page}&page_size=${pagePagination.pageSize}&search=${encodeURIComponent(search)}&state=${state}`);
 function saved(row:GovernanceWorkspace){const created=editing==="new";setEditing(null);list.reload();if(created)navigate(`/platform/governance/overview?workspace_id=${row.id}`)}
 return <><div className="governance-spaces-toolbar"><Space size={16} wrap><Input.Search placeholder="搜索治理空间" aria-label="搜索治理空间" allowClear onSearch={v=>{setSearch(v);setPage(1)}}/><Select aria-label="空间状态" placeholder="请选择空间状态" value={state} onChange={v=>{setState(v);setPage(1)}} options={[{value:"",label:"全部空间"},{value:"active",label:"进行中"},{value:"archived",label:"已归档"}]}/></Space><Button type="primary" icon={<PlusOutlined aria-hidden="true"/>} onClick={()=>setEditing("new")}>新建治理空间</Button></div>{list.error?<Failure error={list.error} retry={list.reload}/>:<Table rowKey="id" loading={list.loading} dataSource={list.data?.items??[]} scroll={{x:840}} pagination={{...pagePagination,total:list.data?.total??0}} locale={{emptyText:"暂无治理空间，可新建客户资料整理、商品分析等空间。"}} columns={[
 {title:"空间名称",dataIndex:"name",render:(name:string,row:GovernanceWorkspace)=><Button type="link" onClick={()=>navigate(`/platform/governance/overview?workspace_id=${row.id}`)}>{name}</Button>},
 {title:"业务用途",dataIndex:"description",render:(description:string)=>description||"未填写"},
 {title:"状态",render:(_,row)=><Tag>{row.state==="active"?"进行中":"已归档"}</Tag>},
 {title:"更新时间",render:(_,row)=><Stamp value={row.updated_at}/>},
 {title:"操作",render:(_,row)=><Space><Button type="link" onClick={()=>navigate(`/platform/governance/overview?workspace_id=${row.id}`)}>查看看板</Button>{row.state==="active"&&(row.owner_id===user.user_id||user.roles.includes("admin"))&&<Button type="link" onClick={()=>setEditing(row)}>空间设置</Button>}</Space>}
 ]}/>} {editing&&<GovernanceWorkspaceEditor key={editing==="new"?"new":editing.id} editing={editing} onClose={closeEditor} onSaved={saved}/>}</>;
}
export function GovernanceWorkspaceSources(){
 const {workspaceID,workspace,navigation}=useGovernanceWorkspace();const {message}=App.useApp();const action=useAction();const [page,setPage,pagePagination]=usePagination();const [adding,setAdding]=useState(false);const [search,setSearch]=useState("");const [resourcePage,setResourcePage,resourcePagePagination]=usePagination();const [chosen,setChosen]=useState<MetadataResource[]>([]);const [associating,setAssociating]=useState(false);const [associationError,setAssociationError]=useState("");const [removing,setRemoving]=useState<GovernanceWorkspaceSource|null>(null);
 const list=useQuery<Page<GovernanceWorkspaceSource>>(workspaceID?`/api/v1/governance/workspaces/${workspaceID}/sources?page=${page}&page_size=${pagePagination.pageSize}`:null);const resources=useQuery<Page<MetadataResource>>(adding&&workspaceID?`/api/v1/governance/workspaces/${workspaceID}/source-candidates?page=${resourcePage}&page_size=${resourcePagePagination.pageSize}&search=${encodeURIComponent(search)}`:null);
 async function associate(){
  if(!chosen.length||!workspaceID||associating)return;
  setAssociating(true);setAssociationError("");const failed:MetadataResource[]=[];const errors:string[]=[];let succeeded=0;
  try{for(const item of chosen){
   const resource:Resource={resource_type:item.resource_type??"raw_object",resource_id:item.object_id,contract_version:item.schema_version};
   try{await action.run(`/api/v1/governance/workspaces/${workspaceID}/sources`,{workspace_id:workspaceID,resource});succeeded++}
   catch(e){failed.push(item);errors.push(`${item.display_name||item.qualified_name}：${errorMessage(e)}`)}
  }
  setChosen(failed);if(succeeded){list.reload();message.success(`已关联 ${succeeded} 份来源`)}
  if(failed.length)setAssociationError(`有 ${failed.length} 份来源未关联，已保留勾选，可重试。${errors.join("；")}`);else setAdding(false);
  }finally{setAssociating(false)}
 }
 async function remove(){if(!removing||!workspaceID)return;try{await action.run(`/api/v1/governance/workspaces/${workspaceID}/sources/remove`,{workspace_id:workspaceID,resource:{resource_type:removing.reference.resource_type,resource_id:removing.reference.resource_id,contract_version:removing.reference.contract_version}});setRemoving(null);list.reload()}catch(e){message.error(errorMessage(e))}}
 if(!workspaceID)return <Empty description="请先选择治理空间"/>;
 return <><Alert className="governance-sources-notice" type="info" showIcon title="先选择来源，再查看元数据" description="来源统一从共享交换平台登记的表、文件和 API 资源中选择。关联完成后，可在元数据管理查看结构和变化；治理成果在成果管理中查看。来源可见、关联和结构版本一致均不代表已获加工授权；普通账号实际处理前，仍须由来源责任部门在共享交换“数据加工授权”批准资源版本、字段、用途及期限，形成成果需允许派生。管理员按现有平台权限直接治理，不会自动生成申请记录。"/><div className="governance-spaces-toolbar"><Button onClick={list.reload}>刷新</Button><Button type="primary" disabled={workspace?.state==="archived"} onClick={()=>{setAdding(true);setChosen([]);setAssociationError("")}}>选择共享交换资源</Button></div>{workspace?.is_legacy&&<p className="helper-text">历史任务及其引用继续保留；新增来源请统一选择共享交换资源。</p>}{list.error?<Failure error={list.error} retry={list.reload}/>:<Table rowKey={r=>r.reference.resource_type+":"+r.reference.resource_id+":"+r.reference.contract_version} loading={list.loading} dataSource={list.data?.items} pagination={{...pagePagination,total:list.data?.total}} scroll={{x:600}} columns={[{title:"来源",render:(_,r)=><Button type="link" disabled={!r.resource||!["table_resource","file_resource","api_resource"].includes(r.reference.resource_type)} onClick={()=>navigation(`/platform/governance/metadata?resource_id=${r.reference.resource_id}`)}>{r.resource?.display_name||r.resource?.qualified_name||"来源当前不可见"}</Button>},{title:"类型",render:(_,r)=><Space>{governanceResourceType({resource_type:r.reference.resource_type})}{!["table_resource","file_resource","api_resource"].includes(r.reference.resource_type)&&<Tag>历史直接引用 · 仅保留追溯</Tag>}</Space>},{title:"结构版本",render:(_,r)=>`v${r.reference.contract_version}`},{title:"结构状态",render:(_,r)=><Tag color={r.available?"success":"warning"}>{r.available?"版本一致":"需要核对来源"}</Tag>},{title:"操作",render:(_,r)=><Button disabled={workspace?.state==="archived"} onClick={()=>setRemoving(r)}>取消关联</Button>} ]}/>} {adding&&<GovernanceDialog header="选择共享交换资源" width={1000} busy={associating} onClose={()=>{if(!associating)setAdding(false)}} closeLabel="取消" actions={<Space><span>已选 {chosen.length} 份</span><Button disabled={!chosen.length||associating} onClick={()=>setChosen([])}>清空选择</Button><Button type="primary" loading={associating} disabled={!chosen.length} onClick={associate}>关联所选来源</Button></Space>}><p className="helper-text">这里显示你有治理管理权限的共享交换资源。关联不会授予数据加工权限，普通账号需另获来源责任部门的加工授权后才能执行。找不到时，请先确认来源登记与岗位范围。</p>{associationError&&<Alert className="governance-sources-notice" type="error" showIcon title="部分来源未关联" description={associationError}/>}<Input.Search disabled={associating} placeholder="搜索客户、商品或来源名称" onSearch={v=>{setSearch(v);setResourcePage(1)}}/>{resources.error?<Failure error={resources.error}/>:<Table className="detail-section" scroll={{x:800}} rowKey="object_id" loading={resources.loading} dataSource={resources.data?.items} rowSelection={{type:"checkbox",preserveSelectedRowKeys:true,selectedRowKeys:chosen.map(item=>item.object_id),getCheckboxProps:()=>({disabled:associating}),onChange:(_,rows)=>setChosen(rows)}} pagination={{...resourcePagePagination,total:resources.data?.total}} columns={[{title:"来源与业务介绍",render:(_,r)=><div className="governance-source-summary"><strong>{r.display_name||r.qualified_name}</strong>{r.display_name&&r.display_name!==r.qualified_name&&<span className="governance-source-technical">{r.qualified_name}</span>}<p>{r.description||"共享交换平台尚未填写这份来源的业务介绍。"}</p></div>},{title:"提供部门",width:150,render:(_,r)=>r.provider_name||"未填写"},{title:"类型",width:110,render:(_,r)=>governanceResourceType(r)},{title:"版本",width:70,render:(_,r)=>`v${r.schema_version}`}]}/>}</GovernanceDialog>}{removing&&<GovernanceDialog header="取消来源关联" busy={action.busy} onClose={()=>setRemoving(null)} actions={<Button danger loading={action.busy} onClick={remove}>确认取消关联</Button>}><p>只取消当前空间与这份来源的关联，原始数据继续保留。如果已有数据流引用，需先调整数据流。</p></GovernanceDialog>}</>;
}
