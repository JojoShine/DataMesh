import {usePagination} from "../hooks/usePagination";
import {App,Alert,Button,Select,Space,Table,Tabs,Tag} from "antd";
import {BellOutlined,StarOutlined} from "@ant-design/icons";
import {useEffect,useRef,useState} from "react";
import type {Page,Resource} from "../api/types";
import {resourceLabel} from "../api/types";
import {query,errorMessage} from "../api/client";
import {navigate} from "../app/navigation";
import {useQuery} from "../hooks/useQuery";
import {useAction} from "../hooks/useAction";
import {DialogActions} from "../components/PlatformDialog";
import {Failure,Stamp,Title} from "../components/common";
interface Saved extends Resource {id:string;resource_name:string;directory_id:string;bookmarked:boolean;following:boolean;available:boolean;version:number;checked_at:string}
interface Notice {product_id?:string;id:string;category:string;resource_name:string;resource_id:string;contract_version:number;message:string;read_at:string|null;created_at:string}
const noticeLabels:Record<string,string>={product_contract_violated:"产品契约违约",sharing_changed:"共享条件变化",sharing_unavailable:"共享暂不可用",sharing_restored:"共享恢复",grant_expiring:"授权即将到期",grant_expired:"授权已到期"};
function discover(id:string){navigate("/platform/assets/catalog?"+query({resource_id:id}))}
export function AssetSaveControls({resource}:{resource:Resource}){
 const state=useQuery<Saved>("/api/v1/assets/saved-state?"+query({...resource}));const action=useAction();const {message}=App.useApp();const row=state.data;
 async function change(kind:"bookmarked"|"following"){
  if(!row)return;
  try{await action.run("/api/v1/assets/saved",{resource,base_version:row.version,bookmarked:row.bookmarked,following:row.following,[kind]:!row[kind]});state.reload();message.success(kind==="bookmarked"?(row.bookmarked?"已取消收藏":"已收藏，可在“收藏与关注”找到"):(row.following?"已取消关注":"已关注，进入“我的提醒”核对共享变化"))}catch(e){message.error(errorMessage(e));state.reload()}
 }
 return <>{state.error?<Failure error={state.error} retry={state.reload}/>:null}<DialogActions><Button icon={<StarOutlined aria-hidden="true"/>} disabled={!row||state.loading||action.busy} onClick={()=>change("bookmarked")}>{row?.bookmarked?"取消收藏":"收藏资产"}</Button><Button icon={<BellOutlined aria-hidden="true"/>} disabled={!row||state.loading||action.busy} onClick={()=>change("following")}>{row?.following?"取消关注":"关注变化"}</Button></DialogActions></>;
}
export function AssetSaved(){
 const [scope,setScope]=useState("bookmarks");const [page,setPage,pagePagination]=usePagination();const list=useQuery<Page<Saved>>("/api/v1/assets/saved?"+query({scope,page,page_size:pagePagination.pageSize}));const action=useAction();const {message}=App.useApp();
 async function remove(r:Saved){try{await action.run("/api/v1/assets/saved",{resource:{resource_type:r.resource_type,resource_id:r.resource_id,contract_version:r.contract_version},base_version:r.version,bookmarked:scope==="bookmarks"?false:r.bookmarked,following:scope==="following"?false:r.following});if(list.data?.items.length===1&&page>1)setPage(page-1);else list.reload()}catch(e){message.error(errorMessage(e));list.reload()}}
 async function refresh(){try{await action.run("/api/v1/assets/notices/refresh");list.reload();message.success("已核对当前共享状态")}catch(e){message.error(errorMessage(e))}}
 return <><Title title="收藏与关注" description="收藏常用资产便于再次查找；关注共享变化后，在我的提醒核对状态。收藏和关注不会授予数据访问权限。" actions={<Space size={16} wrap><Button disabled={action.busy} onClick={()=>navigate("/platform/assets/catalog")}>发现资产</Button><Button loading={action.busy} onClick={refresh}>核对共享状态</Button></Space>}/>
 <Tabs activeKey={scope} onChange={v=>{setScope(v);setPage(1)}} items={[{key:"bookmarks",label:"我的收藏"},{key:"following",label:"我的关注"}]}/>
 {list.error?<Failure error={list.error} retry={list.reload}/>:<Table<Saved> rowKey="id" loading={list.loading} dataSource={list.data?.items} scroll={{x:900}} locale={{emptyText:scope==="bookmarks"?"还没有收藏，可从资产详情收藏常用数据":"还没有关注，可从资产详情关注共享变化"}} pagination={{...pagePagination,total:list.data?.total}} columns={[
 {title:"资产",render:(_,r)=><Button type="link" onClick={()=>discover(r.resource_id)}>{r.resource_name}</Button>},
 {title:"形式与版本",render:(_,r)=>`${resourceLabel(r)} · V${r.contract_version}`},
 {title:"最近核对状态",render:(_,r)=><Tag color={r.available?"blue":undefined}>{r.available?"可共享":"暂不可用或版本变化"}</Tag>},
 {title:"核对时间",render:(_,r)=><Stamp value={r.checked_at}/>},
 {title:"操作",render:(_,r)=><Space size={16} wrap><Button onClick={()=>discover(r.resource_id)}>{r.available?"查看与申请":"重新发现"}</Button><Button disabled={action.busy} onClick={()=>remove(r)}>{scope==="bookmarks"?"取消收藏":"取消关注"}</Button></Space>},
 ]}/>}
 <Alert showIcon style={{marginTop:24}} title="状态以最近一次核对为准" description="点击“核对共享状态”检查变化。暂停或下架后保留您曾收藏的名称，不展示未共享的新版本内容；取消关注后不再生成该资产的共享变化提醒，历史提醒保留。"/>
 </>;
}
export function AssetNotices(){
 const [page,setPage,pagePagination]=usePagination();const [unread,setUnread]=useState(false);const [checked,setChecked]=useState<string>();const initial=useRef(false);
 const list=useQuery<Page<Notice>&{unread:number}>("/api/v1/assets/notices?"+query({page,unread:String(unread),page_size:pagePagination.pageSize}));const action=useAction();const {message}=App.useApp();const [refreshError,setRefreshError]=useState<unknown>(null);
 async function refresh(){setRefreshError(null);try{const result=await action.run<{checked_at:string}>("/api/v1/assets/notices/refresh");setChecked(result.checked_at);list.reload()}catch(e){setRefreshError(e)}}
 useEffect(()=>{if(!initial.current){initial.current=true;void refresh()}},[]);
 async function read(r:Notice){try{await action.run(`/api/v1/assets/notices/${r.id}/read`);if(unread&&list.data?.items.length===1&&page>1)setPage(page-1);else list.reload()}catch(e){message.error(errorMessage(e))}}
 return <><Title title="我的提醒" description="查看关注资产的共享变化、本人授权到期情况，以及主动核验发现的产品契约违约。" actions={<Button loading={action.busy} onClick={refresh}>核对最新状态</Button>}/>
 <Alert style={{marginBottom:24}} showIcon title="进入页面或主动刷新时核对，不是后台实时推送" description="本页刷新核对共享变化与授权期限；产品契约需进入“契约核验”主动检查。提醒不能替代完整变更审计，标记已读不会改变原授权。"/>
 {Boolean(refreshError)&&<Failure error={refreshError} retry={()=>void refresh()}/>}
 <Space size={16} wrap style={{marginBottom:24}}><Select aria-label="提醒筛选" value={unread} style={{minWidth:160}} onChange={v=>{setUnread(v);setPage(1)}} options={[{value:false,label:"全部提醒"},{value:true,label:"仅未读"}]}/><span>未读 {list.data?.unread??0} 条</span>{checked&&<span className="helper-text">最近核对：<Stamp value={checked}/></span>}</Space>
 {list.error?<Failure error={list.error} retry={list.reload}/>:<Table<Notice> rowKey="id" loading={list.loading||action.busy} dataSource={list.data?.items} scroll={{x:1000}} locale={{emptyText:"暂无提醒"}} pagination={{...pagePagination,total:list.data?.total}} columns={[
 {title:"提醒",render:(_,r)=><Space wrap><Tag color={r.read_at?undefined:"blue"}>{r.read_at?"已读":"未读"}</Tag>{noticeLabels[r.category]}</Space>},
 {title:"资产",render:(_,r)=>r.product_id?r.resource_name:`${r.resource_name} · V${r.contract_version}`},
 {title:"说明",dataIndex:"message"},
 {title:"发现时间",render:(_,r)=><Stamp value={r.created_at}/>},
 {title:"操作",render:(_,r)=><Space size={16} wrap>{r.product_id?<Button onClick={()=>navigate("/platform/assets/contracts?"+query({product_id:r.product_id}))}>查看契约核验</Button>:<Button onClick={()=>discover(r.resource_id)}>核对并申请</Button>}{r.category.startsWith("grant_")&&<Button onClick={()=>navigate("/platform/assets/grants")}>查看授权</Button>}{!r.read_at&&<Button disabled={action.busy} onClick={()=>read(r)}>标记已读</Button>}</Space>},
 ]}/>}
 </>;
}
