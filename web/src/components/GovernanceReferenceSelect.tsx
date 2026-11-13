import {usePagination} from "../hooks/usePagination";
import {Pagination,Button, Select, Space} from "antd";
import {useState} from "react";
import type {Page} from "../api/types";
import type {GovernanceKind, GovernanceRecord, GovernanceRef} from "../api/governance";
import {governancePaths} from "../api/governance";
import {useQuery} from "../hooks/useQuery";
import {Failure} from "./common";
export function GovernanceReferenceSelect({kind,value,onChange,departmentId,id,ariaLabel,domainScope,executionMode}: {kind:GovernanceKind;value?:GovernanceRef;onChange?:(value:GovernanceRef|undefined,record?:GovernanceRecord)=>void;departmentId:string;id?:string;ariaLabel?:string;domainScope?:"enterprise";executionMode?:"batch"|"continuous"}) {
 const [search,setSearch]=useState("");const [page,setPage,pagePagination]=usePagination();
 const path="/api/v1/governance/"+governancePaths[kind];
 const list=useQuery<Page<GovernanceRecord>>(`${path}?state=published&page_size=${pagePagination.pageSize}&page=${page}&search=${encodeURIComponent(search)}${domainScope?`&domain_scope=${domainScope}`:""}${executionMode?`&execution_mode=${executionMode}`:""}`);
 const selected=useQuery<GovernanceRecord>(value?`${path}/${value.id}/versions/${value.version}`:null);
 const rows=(list.data?.items??[]).filter(row=>row.department_id===departmentId||(kind==="domain"&&row.definition.domain_scope==="enterprise"));
 if(selected.data&&!rows.some(row=>row.id===selected.data?.id&&row.version===selected.data.version))rows.unshift(selected.data);
 return <Select id={id} aria-label={ariaLabel} allowClear showSearch searchValue={search} onSearch={value=>{setSearch(value);setPage(1)}} filterOption={false} placeholder="请选择已发布版本，可搜索名称" loading={list.loading} value={value?`${value.id}:${value.version}`:undefined}
 options={rows.map(row=>({value:`${row.id}:${row.version}`,disabled:executionMode!==undefined&&(row.execution_mode??"batch")!==executionMode,label:`${kind==="domain"?(row.definition.domain_scope==="enterprise"?"企业统一 · ":row.definition.enterprise_domain?"本部门责任 · ":""):""}${row.definition.name} · v${row.version}${row.state==="retired"?"（已停用）":""}`}))}
 onChange={(key?:string)=>{const row=rows.find(row=>`${row.id}:${row.version}`===key);onChange?.(row?{id:row.id,version:row.version}:undefined,row)}}
 popupRender={menu=><>{menu}{Boolean(list.error)&&<Failure error={list.error} retry={list.reload}/>}<Space className="governance-select-pager"><Pagination {...pagePagination} size="small" total={list.data?.total??0} disabled={list.loading}/></Space></>}/>;
}
