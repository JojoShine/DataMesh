import {usePagination} from "../hooks/usePagination";
import {Pagination,Button,Select,Space} from "antd";
import {useState} from "react";
import type {Page} from "../api/types";
import {useQuery} from "../hooks/useQuery";
import {Failure} from "./common";

export function GovernanceOutputSelect({value,onChange,placeholder,id}:{value?:string;onChange?:(value:string)=>void;placeholder:string;id?:string}){
 const [page,setPage,pagePagination]=usePagination();
 const [selected,setSelected]=useState<{value:string;label:string}|null>(null);
 const list=useQuery<Page<{id:string;name:string;state:string}>>(`/api/v1/governance/lifecycle?page=${page}&page_size=${pagePagination.pageSize}`);
 const options=(list.data?.items??[]).map(row=>({value:row.id,label:`${row.name} · ${row.id}`,disabled:row.state==="retired"}));
 if(selected&&selected.value===value&&!options.some(option=>option.value===value))options.unshift({...selected,disabled:false});
 return <Select id={id} showSearch optionFilterProp="label" value={value} placeholder={placeholder} loading={list.loading} options={options} onChange={next=>{setSelected(options.find(option=>option.value===next)??null);onChange?.(next)}} popupRender={menu=><>{menu}{Boolean(list.error)&&<Failure error={list.error} retry={list.reload}/>}<Space size={16} wrap className="governance-select-pager"><Pagination {...pagePagination} size="small" total={list.data?.total??0} disabled={list.loading}/></Space></>}/>;
}
