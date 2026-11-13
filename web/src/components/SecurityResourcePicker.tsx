import {usePagination} from "../hooks/usePagination";
import {Button,Input,Space,Table,Tag} from "antd";
import {useState} from "react";
import type {Page,Resource} from "../api/types";
import type {SecurityResource} from "../api/security";
import {resourceLabel} from "../api/types";
import {useQuery} from "../hooks/useQuery";
import {query} from "../api/client";
import {Failure} from "./common";
export function SecurityResourcePicker({value,onChange}:{value:Resource|null;onChange:(row:SecurityResource)=>void}){
 const [page,setPage,pagePagination]=usePagination();const [search,setSearch]=useState("");const list=useQuery<Page<SecurityResource>>("/api/v1/security/resources?"+query({page,search,page_size:pagePagination.pageSize}));
 return <Space orientation="vertical" size={16} style={{width:"100%"}}>{value&&<Tag>已选 {resourceLabel(value)} · {value.resource_id} · V{value.contract_version}</Tag>}<Input.Search placeholder="搜索资源名称或输入资源编号" onSearch={v=>{setSearch(v);setPage(1)}}/>{list.error?<Failure error={list.error} retry={list.reload}/>:<Table<SecurityResource> rowKey={r=>`${r.resource.resource_type}:${r.resource.resource_id}:${r.resource.contract_version}`} size="small" loading={list.loading} dataSource={list.data?.items} scroll={{x:520}} pagination={{...pagePagination,total:list.data?.total}} columns={[{title:"资源",dataIndex:"name"},{title:"载体与版本",render:(_,r)=>`${resourceLabel(r.resource)} · V${r.resource.contract_version}`},{title:"选择",render:(_,r)=><Button onClick={()=>onChange(r)}>选择资源</Button>}]}/>}</Space>;
}
