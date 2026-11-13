import {usePagination} from "../hooks/usePagination";
import {AssetSaveControls} from "./AssetFollowing";
import {AssetEvidence} from "../components/AssetEvidence";
import {AssetFeedbackForm} from "../components/AssetFeedback";
import {Alert, Button, Input, Select, Space, Table, Tabs, Tag} from "antd";
import {useEffect, useRef, useState} from "react";
import type {DirectoryResource, Page, ResourceDirectory} from "../api/types";
import {deliveryMethod, directoryResources} from "../api/types";
import {query} from "../api/client";
import {navigate, usePath} from "../app/navigation";
import {useQuery} from "../hooks/useQuery";
import {useSession} from "../stores/session";
import {Failure, Title} from "../components/common";
import {PlatformDialog} from "../components/PlatformDialog";
import {ApplyModal} from "./Catalog";
import {DirectoryDetails, classificationNames} from "./ResourceDirectory";

const methods = {query:"查询数据", download:"下载文件", invoke:"调用 API"};
export function Assets({trust=false}:{trust?:boolean}) {
 const path=usePath();
 const targetParams=new URLSearchParams(path.split("?")[1]);
 const targetID=targetParams.get("resource_id")??targetParams.get("dataset_id");
 const openedTarget=useRef<string|null>(null);
 const target=useQuery<Page<ResourceDirectory>>(targetID?"/api/v1/assets/directories?"+query({resource_id:targetID}):null);
 const [page,setPage,pagePagination]=usePagination();
 const [search,setSearch]=useState("");
 const routeClassification=targetParams.get("classification");
 const [classification,setClassification]=useState<string|undefined>(routeClassification&&Object.hasOwn(classificationNames,routeClassification)?routeClassification:undefined);
 useEffect(()=>{setClassification(routeClassification&&Object.hasOwn(classificationNames,routeClassification)?routeClassification:undefined);setPage(1)},[routeClassification]);
 const [selected,setSelected]=useState<ResourceDirectory|null>(null);
 const [variant,setVariant]=useState<DirectoryResource|null>(null);
 const [applying,setApplying]=useState(false);
 const [detailTab,setDetailTab]=useState(trust?"evidence":"overview");
 const user=useSession(s=>s.user)!;
 const canApply=user.roles.some(role=>["admin","consumer","provider"].includes(role));
 const list=useQuery<Page<ResourceDirectory>>("/api/v1/assets/directories?"+query({page,search,classification,page_size:pagePagination.pageSize}));
 useEffect(()=>{
  if(!targetID){openedTarget.current=null;return}
  if(openedTarget.current===targetID||!target.data)return;
  const row=target.data.items.find(r=>directoryResources(r).some(v=>v.resource.resource_id===targetID));
  if(row){openedTarget.current=targetID;setSelected(row);setVariant(directoryResources(row).find(v=>v.resource.resource_id===targetID)??null);setApplying(false)}
 },[targetID,target.data]);
 function open(row:ResourceDirectory) {setDetailTab(trust?"evidence":"overview");setSelected(row);setVariant(directoryResources(row).find(v=>v.available)??directoryResources(row)[0]??null);setApplying(false)}
 return <>
  <Title title={trust?"可信资产核验":"资产发现"} description={trust?"按共享版本核对字段口径、责任人、质量检查与更新依据，再决定是否申请使用。":"找到已共享的数据，了解字段、质量和使用条件，申请后按授权查询、下载或调用。"} actions={<Space size={16} wrap><Button onClick={()=>navigate("/platform/assets/applications")}>我的申请</Button><Button onClick={()=>navigate("/platform/assets/grants")}>我的资产</Button></Space>}/>
  {trust&&<Alert showIcon style={{marginBottom:24}} title="依据实际证据判断适用性" description="检查步骤数、模型校验或生成时间不代表资产已认证、业务质量全部通过或源数据实时更新。未提供的证据会明确标注。"/>}
  {target.error&&<Failure error={target.error}/>}
  {targetID&&!target.loading&&target.data&&!target.data.items.some(r=>directoryResources(r).some(v=>v.resource.resource_id===targetID))&&<Alert style={{marginBottom:24}} showIcon title="该成果尚未开放共享，或共享已暂停" description="请联系提供部门确认，也可在下方查找其他可共享资产。"/>}
  <Space className="asset-catalog-filters" size={16} wrap style={{marginBottom:24}}>
   <Input.Search placeholder="搜索资产名称或资源编码" allowClear onSearch={v=>{setSearch(v);setPage(1)}} style={{width:280,maxWidth:"100%"}}/>
   <Select aria-label="资产分类" placeholder="全部分类" allowClear value={classification} style={{minWidth:160}} options={Object.entries(classificationNames).map(([value,label])=>({value,label}))} onChange={v=>{setClassification(v);setPage(1)}}/>
   <Button onClick={list.reload}>刷新</Button>
  </Space>
  {list.error?<Failure error={list.error}/>:<Table<ResourceDirectory> rowKey="directory_id" loading={list.loading} dataSource={list.data?.items} scroll={{x:1000}} pagination={{...pagePagination,total:list.data?.total}} locale={{emptyText:"暂无已共享资产，可联系数据提供部门完成编目、审核和共享启用。"}} columns={[
   {title:"资产名称",render:(_,r)=><Button type="link" onClick={()=>open(r)}>{r.definition.name}</Button>},
   {title:"提供单位",render:(_,r)=>r.definition.provider_name||r.department_id},
   {title:"资产说明",render:(_,r)=>r.definition.description||"—"},
   {title:"交付方式",render:(_,r)=><Space wrap>{[...new Set(directoryResources(r).map(v=>deliveryMethod(v.resource)))].map(m=><Tag key={m}>{methods[m]}</Tag>)}</Space>},
   {title:"更新周期",render:(_,r)=>r.definition.update_frequency||"未说明"},
   {title:"共享状态",render:(_,r)=>directoryResources(r).some(v=>v.available)?<Tag color="blue">可申请</Tag>:<Tag>暂不可用</Tag>},
   {title:"操作",render:(_,r)=><Button onClick={()=>open(r)}>{trust?"查看可信证据":"查看与申请"}</Button>},
  ]}/>}
  <PlatformDialog open={Boolean(selected)&&!applying} title={selected?.definition.name??"资产详情"} width={960} onCancel={()=>{setSelected(null);setVariant(null)}} footer={detailTab!=="feedback"&&<Button type="primary" disabled={!variant?.available||!canApply} onClick={()=>setApplying(true)}>申请使用所选数据</Button>} destroyOnHidden>
   {selected&&<Space orientation="vertical" size={24} style={{width:"100%"}}>
    <Alert showIcon title="申请通过后，在“我的资产”中使用" description="不同数据及交付方式分别授权。治理成果已发布仍须完成共享审核；本页不会展示未共享的内部数据。"/>
    {variant&&<p className="helper-text">当前选择：{variant.name||selected.definition.name} · V{variant.resource.contract_version} · {methods[deliveryMethod(variant.resource)]}</p>}
    <Tabs activeKey={detailTab} onChange={setDetailTab} destroyOnHidden items={[
     {key:"overview",label:"资产与共享条件",children:<DirectoryDetails row={selected} selectedResource={variant} onSelectResource={setVariant}/>},
     ...(variant?[{key:"evidence",label:"字段、质量与更新",children:<AssetEvidence key={`${variant.resource.resource_id}:${variant.resource.contract_version}`} resource={variant.resource}/>},
     ...(canApply?[{key:"feedback",label:"反馈使用问题",children:<AssetFeedbackForm key={`${variant.resource.resource_id}:${variant.resource.contract_version}`} resource={variant.resource} fields={variant.fields.map(f=>f.name)} onDone={()=>{setSelected(null);navigate("/platform/assets/grants?tab=feedback")}}/>}]:[])]:[]),
    ]}/>
    {variant&&detailTab!=="feedback"&&<AssetSaveControls key={`${variant.resource.resource_id}:${variant.resource.contract_version}`} resource={variant.resource}/>}
    {!canApply&&<Alert showIcon type="info" title="当前账号未分配数据使用角色，请联系管理员开通。"/>}
   </Space>}
  </PlatformDialog>
  {applying&&variant&&<ApplyModal assets resource={variant.resource} fields={variant.fields} returnLabel="返回资产详情" onClose={()=>setApplying(false)} onDone={()=>{setSelected(null);navigate("/platform/assets/applications")}}/>}
 </>;
}
