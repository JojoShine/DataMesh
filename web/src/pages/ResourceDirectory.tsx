import {usePagination} from "../hooks/usePagination";
import {ResourceSecurityContext} from "../components/ResourceSecurityContext";
import { Alert, Button, Descriptions, Dropdown, Input, Radio, Select, Space, Table, Tag, Menu, Tooltip, Typography } from "antd";
import { AppstoreOutlined, DownOutlined, FolderOutlined, PlusOutlined, ReloadOutlined } from "@ant-design/icons";
import { navigate } from "../app/navigation";
import { useState } from "react";
import type { DirectoryResource, Page, ResourceDirectory as Directory } from "../api/types";
import { deliveryMethod, directoryResources, resourceLabel } from "../api/types";
import { query } from "../api/client";
import { useQuery } from "../hooks/useQuery";
import { useSession } from "../stores/session";
import { Failure, Status, Title } from "../components/common";
import { ApplyModal } from "./Catalog";
import { GovernanceDialog } from "../components/GovernanceDialog";
import { GovernanceProvenanceDetails } from "../components/GovernanceProvenanceDetails";

export const sharingNames:Record<string,string>={unconditional:"无条件共享",conditional:"有条件共享",not_shared:"不予共享"};
export const classificationNames:Record<string,string>={base:"基础信息",theme:"主题信息",department:"部门信息"};
function variantKey(variant:DirectoryResource){const r=variant.resource;return `${r.resource_type}:${r.resource_id}:${r.contract_version}`}
function directoryMethods(row:Directory):ReturnType<typeof deliveryMethod>[]{
 const resources=directoryResources(row);
 return resources.length?[...new Set(resources.map(v=>deliveryMethod(v.resource)))]:[row.definition.format==="api"?"invoke":row.definition.format==="file"?"download":"query"];
}
function DirectoryFormats({row}:{row:Directory}){
 const resources=directoryResources(row);
 const methods=directoryMethods(row);
 const formats={
  query:{label:"数据库",shape:<><path d="M5 6v12c0 1.66 3.13 3 7 3s7-1.34 7-3V6" fill="currentColor" fillOpacity=".12"/><ellipse cx="12" cy="6" rx="7" ry="3" fill="currentColor" fillOpacity=".16"/><path d="M5 12c0 1.66 3.13 3 7 3s7-1.34 7-3"/></>},
  download:{label:"文件",shape:<><path d="M13 3H7a2 2 0 0 0-2 2v14a2 2 0 0 0 2 2h10a2 2 0 0 0 2-2V9Z" fill="currentColor" fillOpacity=".12"/><path d="M13 3v6h6M9 13h6M9 17h4"/></>},
  invoke:{label:"接口",shape:<><path d="m7 7-5 5 5 5m10-10 5 5-5 5M14 5l-4 14"/></>},
 };
 return <Space size={8} role="group" aria-label="资源形式">{methods.map(method=>{
  const format=formats[method];const names=resources.filter(v=>deliveryMethod(v.resource)===method).map(v=>v.name||row.definition.name);
  return <Tooltip key={method} trigger={["hover","focus"]} title={`${format.label}：${names.length?[...new Set(names)].join("、"):"待挂载资源"}`}><span className="directory-resource-format" role="img" aria-label={format.label} tabIndex={0}><svg viewBox="0 0 24 24" fill="none" stroke="currentColor" strokeWidth="1.8" strokeLinecap="round" strokeLinejoin="round" aria-hidden="true" focusable="false">{format.shape}</svg></span></Tooltip>;
 })}</Space>;
}
export function DirectoryDetails({row,selectedResource,onSelectResource}:{row:Directory;selectedResource?:DirectoryResource|null;onSelectResource?:(resource:DirectoryResource)=>void}){
 const d=row.definition;
 const variants=directoryResources(row);
 return <Space orientation="vertical" size={24} style={{width:"100%"}}>
  <Space wrap><Status value={row.state}/><Status value={row.revision_state}/><Tag color="blue">{sharingNames[d.sharing_type]}</Tag><Tag>{row.available?"已挂载":"暂不可用"}</Tag></Space>
  <Descriptions title="资源信息" column={1} items={[
   {key:"code",label:"资源编码",children:d.resource_code||"待正式编目"},{key:"desc",label:"资源说明",children:d.description||"—"},{key:"class",label:"目录分类",children:classificationNames[d.classification]+" / "+(d.classification_path||"待补全")},
   {key:"provider",label:"提供单位",children:d.provider_name||row.department_id},{key:"frequency",label:"更新周期",children:d.update_frequency||"—"},{key:"layer",label:"数据层",children:d.data_layer==="raw_object"?"原始数据":"治理成果"},{key:"security",label:"分类分级",children:d.security_classification||"待补全"},
  ]}/>
  <Descriptions title="共享条件" column={1} items={[
   {key:"sharing",label:"共享属性",children:sharingNames[d.sharing_type]},{key:"conditions",label:"使用条件",children:d.sharing_conditions||"—"},{key:"usage",label:"使用限制",children:d.usage_restrictions||"—"},
   ...(d.sharing_type==="not_shared"?[{key:"reason",label:"不予共享理由",children:d.non_sharing_reason},{key:"basis",label:"法律依据",children:d.legal_basis}]:[]),
   {key:"method",label:"共享方式",children:variants.length?[...new Set(variants.map(v=>v.resource.resource_type==="api_resource"?"API 调用":v.resource.resource_type==="file_resource"?"授权下载":"受控查询"))].join("、"):d.sharing_method==="query"?"受控查询":d.sharing_method==="download"?"授权下载":"API 调用"},{key:"open",label:"公开范围",children:d.open_to_public?"摘要经审核可在门户展示":"仅内部发现"},
  ]}/>
  {variants.length>0&&<div><Typography.Title level={5}>{onSelectResource?"选择申请的数据":"目录内的数据"}</Typography.Title>
   <Table<DirectoryResource> rowKey={variantKey} dataSource={variants} pagination={false} tableLayout="fixed" scroll={{x:900}} columns={[
    ...(onSelectResource?[{title:"选择",width:64,render:(_:unknown,v:DirectoryResource)=><Radio aria-label={v.name||resourceLabel(v.resource)} checked={selectedResource?variantKey(selectedResource)===variantKey(v):false} disabled={!v.available} onChange={()=>onSelectResource(v)}/>}]:[]),
    {title:"数据名称",width:180,render:(_,v)=>v.name||resourceLabel(v.resource)},
    {title:"数据层",width:100,render:(_,v)=><Tag color={(v.data_layer??v.resource.resource_type)==="governed_dataset"?"blue":undefined}>{(v.data_layer??v.resource.resource_type)==="governed_dataset"?"治理成果":"原始数据"}</Tag>},
    {title:"交付形式",width:110,render:(_,v)=>v.resource.resource_type==="api_resource"?"API 查询":v.resource.resource_type==="file_resource"?"文件下载":"库表查询"},
    {title:"版本",width:80,render:(_,v)=>`V${v.resource.contract_version}`},
    {title:"可用状态",width:100,render:(_,v)=>v.available?"可申请":"暂不可用"},
    {title:"治理说明",render:(_,v)=>v.governance?<GovernanceProvenanceDetails governance={v.governance} compact/>:"未提供治理说明，按目录范围申请"},
   ]}/>
   {variants.length>1&&<p className="helper-text">库表、治理成果及 API 按所选资源的字段范围分别申请和审批，不互相继承使用授权。</p>}
  </div>}
  {selectedResource&&<ResourceSecurityContext key={variantKey(selectedResource)} resource={selectedResource.resource}/>}
  <div><Typography.Title level={5}>数据项</Typography.Title><Table rowKey="name" pagination={false} dataSource={d.items} scroll={{x:true}} columns={[
   {title:"数据项",dataIndex:"business_name"},{title:"字段",dataIndex:"name"},{title:"类型",dataIndex:"logical_type"},{title:"敏感标记",render:(_,f)=><Tag>{f.sensitive?"交付时脱敏":"普通"}</Tag>},
  ]}/></div>
  {row.reason&&<Alert title="最近处理意见" description={row.reason} type="info" showIcon/>}
 </Space>;
}
export function ResourceDirectory(){
 const [page,setPage,pagePagination]=usePagination(),[search,setSearch]=useState(""),[classification,setClassification]=useState(""),[sharing,setSharing]=useState(""),[layer,setLayer]=useState("");
 const [selected,setSelected]=useState<Directory|null>(null),[selectedResource,setSelectedResource]=useState<DirectoryResource|null>(null),[apply,setApply]=useState<DirectoryResource|null>(null);
 function showDetails(row:Directory){setSelected(row);const variants=directoryResources(row);setSelectedResource(variants.find(v=>v.available)??variants[0]??null)}
 function applyToDirectory(row:Directory){const variants=directoryResources(row);if(variants.length===1)setApply(variants[0]);else showDetails(row)}
 const user=useSession(s=>s.user)!;const canApply=user.roles.includes("consumer")||(user.roles.includes("admin") || user.roles.includes("provider"));
 const access=useQuery<Record<string,boolean>>("/api/v1/resource-access");
 const list=useQuery<Page<Directory>>("/api/v1/resource-directories?"+query({page,search,classification,sharing_type:sharing,data_layer:layer,page_size:pagePagination.pageSize}));
 return <>
  <Title title="资源目录" description="统一发现原始数据与治理成果，按目录公布的范围申请使用。" actions={<><Button icon={<ReloadOutlined/>} onClick={list.reload}>刷新</Button><Tooltip title={access.data?.manage_resource?undefined:"需由管理员分配本部门资源管理职责"}><Button type="primary" icon={<PlusOutlined/>} disabled={!access.data?.manage_resource} onClick={()=>navigate("/platform/exchange/directory?view=manage&action=create")}>新增目录</Button></Tooltip></>}/>
  {access.error&&<Failure error={access.error} retry={access.reload}/>}
  <section className="directory-layout" aria-label="共享资源目录">
   <aside className="directory-categories" aria-label="目录分类">
    <div className="directory-category-heading">目录分类</div>
    <Menu selectedKeys={[classification||"all"]} onClick={({key})=>{setClassification(key==="all"?"":key);setPage(1)}} items={[{key:"all",label:"全部资源",icon:<AppstoreOutlined/>},...Object.entries(classificationNames).map(([key,label])=>({key,label,icon:<FolderOutlined/>}))]}/>
    <p className="directory-category-note">按信息类别浏览已发布资源</p>
   </aside>
   <div className="directory-results">
    <div className="directory-results-heading"><strong>{classificationNames[classification]||"全部资源"}</strong><Typography.Text type="secondary">共 {list.data?.total??0} 项资源</Typography.Text></div>
    <div className="directory-filters">
    <Input.Search allowClear placeholder="搜索资源名称或编码" onSearch={v=>{setSearch(v);setPage(1)}}/>
    <Select placeholder="请选择共享属性" aria-label="共享属性" value={sharing} onChange={v=>{setSharing(v);setPage(1)}} options={[{value:"",label:"全部共享属性"},...Object.entries(sharingNames).map(([value,label])=>({value,label}))]}/>
    <Select placeholder="请选择数据层" aria-label="数据层" value={layer} onChange={v=>{setLayer(v);setPage(1)}} options={[{value:"",label:"全部数据层"},{value:"raw_object",label:"原始数据"},{value:"governed_dataset",label:"治理成果"}]}/>
   </div>
   {list.error?<Failure error={list.error} retry={list.reload}/>:<Table<Directory> rowKey="directory_id" loading={list.loading} dataSource={list.data?.items} scroll={{x:1600}} pagination={{...pagePagination,total:list.data?.total}} columns={[
    {title:"资源名称",width:340,render:(_,r)=><Space size={8}><DirectoryFormats row={r}/><Button type="link" onClick={()=>showDetails(r)}>{r.definition.name}</Button></Space>},
    {title:"资源编码",width:220,render:(_,r)=>r.definition.resource_code||"待编目"},{title:"提供单位",width:160,render:(_,r)=>r.definition.provider_name||r.department_id},
    {title:"数据层",width:120,render:(_,r)=><Space wrap>{[...new Set([r.definition.data_layer,...directoryResources(r).map(v=>v.data_layer??(v.resource.resource_type==="governed_dataset"?"governed_dataset":"raw_object"))])].map(value=><Tag key={value}>{value==="raw_object"?"原始数据":"治理成果"}</Tag>)}</Space>},
    {title:"共享属性",width:120,render:(_,r)=>sharingNames[r.definition.sharing_type]},
    {title:"共享方式",width:250,render:(_,r)=>directoryMethods(r).map(method=>({query:"数据库查询",download:"文件下载",invoke:"接口调用"})[method]).join("、")},
    {title:"可用状态",width:110,render:(_,r)=><Tag color={directoryResources(r).some(v=>v.available)?"success":"default"}>{directoryResources(r).some(v=>v.available)?"已挂载":r.state==="published"?"待挂载或暂不可用":"待发布"}</Tag>},
    {title:"操作",width:352,render:(_,r)=>{
     const actions=[
      ...(canApply?[{key:"apply",label:"申请使用",disabled:!directoryResources(r).some(v=>v.available),onClick:()=>applyToDirectory(r)}]:[]),
      ...(r.permissions.manage_resource?[{key:"manage",label:"维护目录",disabled:false,onClick:()=>navigate(`/platform/exchange/directory?view=manage&directory_id=${encodeURIComponent(r.directory_id)}`)}]:[]),
      ...(r.permissions.operate_resource?[{key:"mount",label:"挂载资源",disabled:false,onClick:()=>navigate(`/platform/exchange/directory?view=manage&action=mount&directory_id=${encodeURIComponent(r.directory_id)}`)}]:[]),
     ];
     const visible=actions.slice(0,3),more=actions.slice(3);
     return visible.length>0?<Space size={16}>{visible.map(action=><Button key={action.key} disabled={action.disabled} onClick={action.onClick}>{action.label}</Button>)}{more.length>0&&<Dropdown trigger={["click"]} menu={{items:more}}><Button>更多 <DownOutlined/></Button></Dropdown>}</Space>:"—";
    }},
   ]}/>}</div>
  </section>
  {selected&&!apply&&<GovernanceDialog header={selected.definition.name} onClose={()=>setSelected(null)} width={1100} actions={canApply?<Button type="primary" disabled={!selectedResource?.available} onClick={()=>selectedResource&&setApply(selectedResource)}>申请使用</Button>:undefined}><DirectoryDetails row={selected} selectedResource={selectedResource} onSelectResource={canApply?setSelectedResource:undefined}/></GovernanceDialog>}
  <ApplyModal key={apply?variantKey(apply):"none"} returnLabel={selected?"返回目录详情":undefined} resource={apply?.resource??null} fields={apply?.fields??[]} onClose={()=>setApply(null)} onDone={list.reload}/>
 </>;
}
