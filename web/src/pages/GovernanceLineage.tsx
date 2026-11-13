import {usePagination} from "../hooks/usePagination";
import {governanceDataLayerLabel} from "../api/governance";
import {GovernanceGuide} from "../components/GovernanceGuide";
import {Button,Input,Select,Space,Table} from "antd";
import {useState} from "react";
import type {Page} from "../api/types";
import type {GovernanceLineageEntry} from "../api/governanceOperations";
import {useQuery} from "../hooks/useQuery";
import {Failure,Title} from "../components/common";
import {GovernanceDialog} from "../components/GovernanceDialog";
import {GovernanceImpact} from "../components/GovernanceImpact";
export function GovernanceLineage(){
 const [selected,setSelected]=useState<{id:string;kind:string}|null>(null);const [root,setRoot]=useState("");const [rootKind,setRootKind]=useState("resource");
 const [resource,setResource]=useState("");const [page,setPage,pagePagination]=usePagination();const list=useQuery<Page<GovernanceLineageEntry>>(`/api/v1/governance/lineage?page=${page}&page_size=${pagePagination.pageSize}&resource_id=${encodeURIComponent(resource)}`);
 return <><Title title="血缘与影响" description="从实际完成的运行追溯字段来源、模型版本及治理成果。查询时重新核对来源授权。" actions={<Button onClick={list.reload}>刷新</Button>}/><GovernanceGuide topic="lineage"/><Space size={16} wrap className="governance-filter-bar"><Select aria-label="影响分析对象类型" value={rootKind} options={[{value:"resource",label:"资源或成果"},{value:"run",label:"实际运行"},{value:"source",label:"数据源"},{value:"definition",label:"治理定义"}]} onChange={setRootKind}/><Input placeholder="输入对象编号" value={root} onChange={e=>setRoot(e.target.value)}/><Button disabled={!root} onClick={()=>setSelected({id:root,kind:rootKind})}>分析影响</Button></Space><Input.Search className="governance-search" placeholder="按来源资源或成果编号筛选" allowClear onSearch={v=>{setResource(v);setPage(1)}}/>{list.error?<Failure error={list.error}/>:<Table rowKey="run_id" dataSource={list.data?.items} loading={list.loading} scroll={{x:800}} pagination={{...pagePagination,total:list.data?.total}}  columns={[{title:"成果分层",render:(_,r)=>governanceDataLayerLabel(r.data_layer)},{title:"执行运行",render:(_,r)=><Button type="link" onClick={()=>setSelected({id:r.run_id,kind:"run"})}>{r.run_id}</Button>},{title:"数据流版本",render:(_,r)=>`${r.flow.id} · v${r.flow.version}`},{title:"模型版本",render:(_,r)=>`${r.model.id} · v${r.model.version}`},{title:"字段数量",render:(_,r)=>Object.keys(r.fields).length},{title:"下游成果",render:(_,r)=>r.outputs.length}]}/>}{selected&&<GovernanceDialog guide="lineage" header={<Title title="血缘与影响分析" description="查看已登记对象及实际运行的下游影响。"/>} onClose={()=>setSelected(null)} width={1200}><GovernanceImpact id={selected.id} kind={selected.kind}/></GovernanceDialog>}</>
}
