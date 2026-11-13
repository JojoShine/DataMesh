import {usePagination} from "../hooks/usePagination";
import {createContext,useContext,useEffect,useState,type ReactNode} from "react";
import {DatabaseOutlined,DownOutlined,AppstoreOutlined,SettingOutlined,PlusOutlined} from "@ant-design/icons";
import {Pagination,Button,Dropdown,Select,Space,Spin,Tag} from "antd";
import type {Page} from "../api/types";
import type {GovernanceWorkspace} from "../api/governanceWorkspace";
import {useQuery} from "../hooks/useQuery";
import {usePath,navigate} from "../app/navigation";
import {useSession} from "../stores/session";
import {GovernanceWorkspaceEditor} from "./GovernanceWorkspaceEditor";
import {Failure} from "./common";
interface WorkspaceContext {workspace:GovernanceWorkspace|null;workspaceID:string|null;helpOpen:boolean;toggleHelp:()=>void;refreshWorkspace:()=>void;navigation:(path:string)=>void}
const Context=createContext<WorkspaceContext>({workspace:null,workspaceID:null,helpOpen:true,toggleHelp:()=>undefined,refreshWorkspace:()=>undefined,navigation:navigate});
export function useGovernanceWorkspace(){return useContext(Context)}
export function GovernanceHelpScope({children}:{children:ReactNode}){const current=useGovernanceWorkspace();return <Context.Provider value={{...current,helpOpen:true}}>{children}</Context.Provider>}
export function GovernanceWorkspaceBar(){
 const {workspace,workspaceID,helpOpen,toggleHelp,navigation,refreshWorkspace}=useGovernanceWorkspace();const path=usePath();
 const [search,setSearch]=useState("");const [page,setPage,pagePagination]=usePagination();const [editing,setEditing]=useState(false);
 const user=useSession(s=>s.user);const canManage=workspace?.state==="active"&&(workspace.owner_id===user?.user_id||user?.roles.includes("admin"));
 const list=useQuery<Page<GovernanceWorkspace>>(`/api/v1/governance/workspaces?page=${page}&page_size=${pagePagination.pageSize}&search=${encodeURIComponent(search)}`);
 const options=(list.data?.items??[]).map(row=>({value:row.id,label:row.name+(row.state==="archived"?"（已归档）":"")}));
 if(workspace&&!options.some(row=>row.value===workspace.id))options.unshift({value:workspace.id,label:workspace.name});
 function switchSpace(id:string){const current=path.split("?")[0];navigate(`${current.endsWith("/workspaces")?"/platform/governance/overview":current}?workspace_id=${encodeURIComponent(id)}`)}
 return <><section className="governance-space-context" aria-label="当前治理空间"><Space size={12} wrap><label className="governance-space-label" htmlFor="governance-space-selector">当前空间</label><Select id="governance-space-selector" aria-label="切换治理空间" value={workspaceID??undefined} placeholder="选择治理空间" options={options} loading={list.loading} showSearch={{filterOption:false,onSearch:value=>{setSearch(value);setPage(1)}}} onChange={switchSpace} popupRender={menu=><>{menu}<div className="governance-space-selector-pages"><Pagination {...pagePagination} size="small" total={list.data?.total??0} disabled={list.loading}/></div></>}/>{workspace?.state==="archived"&&<Tag>已归档 · 仅查看</Tag>}<Button icon={<DatabaseOutlined aria-hidden="true"/>} disabled={!workspaceID} type={path.split("?")[0].endsWith("/space-sources")?"primary":"text"} aria-current={path.split("?")[0].endsWith("/space-sources")?"page":undefined} onClick={()=>navigation("/platform/governance/space-sources")}>来源数据</Button><Dropdown trigger={["click"]} placement="bottomRight" menu={{items:[...(canManage?[{key:"settings",label:"当前空间设置",icon:<SettingOutlined aria-hidden="true"/>}]:[]),{key:"new",label:"新建治理空间",icon:<PlusOutlined aria-hidden="true"/>},{key:"all",label:"管理全部空间",icon:<AppstoreOutlined aria-hidden="true"/>}],onClick:({key})=>{if(key==="settings")setEditing(true);else navigate(`/platform/governance/workspaces${key==="new"?"?create=true":""}`)}}}><Button type="text" aria-label="空间管理">空间管理<DownOutlined aria-hidden="true"/></Button></Dropdown></Space><Button className="governance-context-help" type="text" aria-expanded={helpOpen} onClick={toggleHelp}>{helpOpen?"收起帮助":"帮助"}</Button>{Boolean(list.error)&&<Failure error={list.error} retry={list.reload}/>}</section>{editing&&workspace&&<GovernanceWorkspaceEditor editing={workspace} onClose={()=>setEditing(false)} onSaved={()=>{setEditing(false);list.reload();refreshWorkspace()}}/>}</>
}
export function GovernanceWorkspaceProvider({children}:{children:ReactNode}){
 const canGovern=useSession(s=>Boolean(s.user?.public_test||s.user?.roles.some(role=>role==="admin"||role==="governor")));
 const path=usePath();const params=new URLSearchParams(path.split("?")[1]);const workspaceID=params.get("workspace_id");
 const detail=useQuery<GovernanceWorkspace>(workspaceID?`/api/v1/governance/workspaces/${encodeURIComponent(workspaceID)}`:null,true);const [helpOpen,setHelpOpen]=useState(false);
 const page=path.split("?")[0].split("/")[3];const definition=params.get("definition_id"),version=params.get("version")??"1",run=params.get("run_id"),dataset=params.get("dataset_id");
 const resolvePath=workspaceID?null:definition&&["dataflows","models","workflows"].includes(page)?`/api/v1/governance/${page}/${encodeURIComponent(definition)}/versions/${encodeURIComponent(version)}`:run?`/api/v1/governance/runs/${encodeURIComponent(run)}`:dataset?`/api/v1/governance/datasets/${encodeURIComponent(dataset)}`:null;
 const choices=useQuery<Page<GovernanceWorkspace>>(canGovern&&!workspaceID&&page!=="workspaces"&&!resolvePath?"/api/v1/governance/workspaces?state=active&page=1":null);
 const target=useQuery<{workspace_id?:string|null}>(resolvePath);
 useEffect(()=>{if(!workspaceID&&target.data?.workspace_id){const url=new URL(path,location.origin);url.searchParams.set("workspace_id",target.data.workspace_id);navigate(url.pathname+url.search,true)}},[workspaceID,target.data,path]);
 useEffect(()=>{if(!workspaceID&&!resolvePath&&choices.data?.items?.[0]){const url=new URL(path,location.origin);url.searchParams.set("workspace_id",choices.data.items[0].id);navigate(url.pathname+url.search,true)}},[workspaceID,resolvePath,choices.data,path]);
 useEffect(()=>{if(workspaceID&&page==="workspaces")navigate(`/platform/governance/overview?workspace_id=${encodeURIComponent(workspaceID)}`,true)},[workspaceID,page]);
 useEffect(()=>setHelpOpen(false),[workspaceID,page]);
 function navigation(target:string){if(workspaceID&&target.startsWith("/platform/governance/")&&!target.startsWith("/platform/governance/workspaces")&&!["/platform/governance/lightweight","/platform/governance/sandbox"].includes(target.split("?")[0])){const url=new URL(target,location.origin);url.searchParams.set("workspace_id",workspaceID);navigate(url.pathname+url.search)}else navigate(target)}
 return <Context.Provider value={{workspace:detail.data,workspaceID,helpOpen,toggleHelp:()=>setHelpOpen(v=>!v),refreshWorkspace:detail.reload,navigation}}>{workspaceID&&detail.error?<Failure error={detail.error} retry={detail.reload}/>:workspaceID&&detail.loading&&!detail.data||resolvePath&&target.loading||choices.loading||(!workspaceID&&Boolean(choices.data?.items?.[0]))?<Spin description="正在打开治理空间"/>:choices.error?<Failure error={choices.error} retry={choices.reload}/>:target.error?<Failure error={target.error} retry={target.reload}/>:children}</Context.Provider>
}
