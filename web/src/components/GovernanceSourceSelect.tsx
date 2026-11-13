import {Pagination,Select,Space} from "antd";
import {useState} from "react";
import type {Page,Resource} from "../api/types";
import type {MetadataResource} from "../api/governance";
import {useQuery} from "../hooks/useQuery";
import {usePagination} from "../hooks/usePagination";
import {Failure} from "./common";
export function GovernanceSourceSelect({value,onChange,onSelected,id}:{value?:Resource;onChange?:(value:Resource)=>void;onSelected?:(source:MetadataResource)=>void;id?:string}){
 const[search,setSearch]=useState("");const[page,setPage,pagination]=usePagination();const[cached,setCached]=useState<MetadataResource|null>(null);
 const selected=useQuery<MetadataResource>(value?`/api/v1/governance/metadata/${encodeURIComponent(value.resource_id)}/contract`:null);
 const list=useQuery<Page<MetadataResource>>(`/api/v1/governance/metadata?page=${page}&page_size=${pagination.pageSize}&search=${encodeURIComponent(search)}`);const rows=[...(list.data?.items??[])];if(selected.data&&!rows.some(r=>r.object_id===selected.data?.object_id))rows.unshift(selected.data);if(cached&&!rows.some(r=>r.object_id===cached.object_id))rows.unshift(cached);
 if(value&&!rows.some(r=>r.object_id===value.resource_id))rows.unshift({object_id:value.resource_id,resource_type:value.resource_type,schema_version:value.contract_version,qualified_name:value.resource_id,fields:[],source_id:""});
 return <Select id={id} aria-label="选择授权数据来源" style={{width:"100%"}} showSearch searchValue={search} filterOption={false} placeholder="选择来源，可搜索表名" value={value?.resource_id} loading={list.loading} onSearch={v=>{setSearch(v);setPage(1)}} options={rows.map(row=>({value:row.object_id,label:row.display_name??row.qualified_name}))} onChange={key=>{const row=rows.find(r=>r.object_id===key);if(!row)return;setCached(row);onChange?.({resource_type:row.resource_type??"raw_object",resource_id:row.object_id,contract_version:row.schema_version});onSelected?.(row)}} popupRender={menu=><>{menu}{Boolean(list.error)&&<Failure error={list.error} retry={list.reload}/>}<Space className="governance-select-pager"><Pagination {...pagination} size="small" total={list.data?.total??0}/></Space></>}/>;
}
